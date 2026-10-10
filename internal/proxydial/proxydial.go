package proxydial

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

// connectTimeout is the maximum wait time for proxy handshake and CONNECT response
const connectTimeout = 30 * time.Second

// connectDialer dials through an HTTP or HTTPS proxy CONNECT tunnel
type connectDialer struct {
	proxyURL    *url.URL
	proxyTarget string
	direct      *net.Dialer
}

// Dialer returns a dialer establishing TCP connections through proxy, supporting HTTP/HTTPS CONNECT and SOCKS5 with authentication
func Dialer(proxyURL *url.URL) (proxy.ContextDialer, error) {
	direct := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	proxyTarget := proxyURL.Host
	if proxyURL.Port() == "" {
		switch strings.ToLower(proxyURL.Scheme) {
		case "http":
			proxyTarget = net.JoinHostPort(proxyURL.Hostname(), "80")
		case "https":
			proxyTarget = net.JoinHostPort(proxyURL.Hostname(), "443")
		case "socks5":
			return nil, fmt.Errorf("SOCKS5 proxy URL missing port")
		}
	}
	if strings.EqualFold(proxyURL.Scheme, "socks5") {
		var auth *proxy.Auth
		if proxyURL.User != nil {
			password, _ := proxyURL.User.Password()
			auth = &proxy.Auth{User: proxyURL.User.Username(), Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", proxyTarget, auth, direct)
		if err != nil {
			return nil, fmt.Errorf("create SOCKS5 proxy connection: %w", err)
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS5 proxy does not support context cancellation")
		}
		return socksDialer{contextDialer}, nil
	}
	return &connectDialer{
		proxyURL:    proxyURL,
		proxyTarget: proxyTarget,
		direct:      direct,
	}, nil
}

// socksDialer sets the same maximum timeout for SOCKS5 handshake as CONNECT
type socksDialer struct {
	proxy.ContextDialer
}

func (dialer socksDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return dialer.ContextDialer.DialContext(ctx, network, address)
}

func (dialer *connectDialer) Dial(network, address string) (net.Conn, error) {
	return dialer.DialContext(context.Background(), network, address)
}

func (dialer *connectDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var connection net.Conn
	var err error
	if strings.EqualFold(dialer.proxyURL.Scheme, "https") {
		tlsDialer := tls.Dialer{
			NetDialer: dialer.direct,
			Config: &tls.Config{
				ServerName: dialer.proxyURL.Hostname(),
				NextProtos: []string{"http/1.1"},
			},
		}
		connection, err = tlsDialer.DialContext(ctx, network, dialer.proxyTarget)
	} else {
		connection, err = dialer.direct.DialContext(ctx, network, dialer.proxyTarget)
	}
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = connection.Close()
		}
	}()
	deadline := time.Now().Add(connectTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stopCancel()
	request := (&http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: address},
		Host:   address,
		Header: make(http.Header),
	}).WithContext(ctx)
	if dialer.proxyURL.User != nil && dialer.proxyURL.User.Username() != "" {
		password, _ := dialer.proxyURL.User.Password()
		credentials := dialer.proxyURL.User.Username() + ":" + password
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err := request.Write(connection); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("proxy CONNECT returned %s", response.Status)
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	success = true
	return connection, nil
}
