package proxydial

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Relay provides unauthenticated SOCKS5 listener on loopback and forwards connections through upstream proxy
type Relay struct {
	listener net.Listener
	dialer   proxy.ContextDialer
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closed   bool
}

// StartRelay starts local SOCKS5 relay for upstream proxy
func StartRelay(proxyURL *url.URL) (*Relay, error) {
	dialer, err := Dialer(proxyURL)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("create local proxy relay: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	relay := &Relay{listener: listener, dialer: dialer, ctx: ctx, cancel: cancel, conns: make(map[net.Conn]struct{})}
	go relay.serve()
	return relay, nil
}

// Addr returns local address that relay listens on
func (relay *Relay) Addr() *net.TCPAddr {
	return relay.listener.Addr().(*net.TCPAddr)
}

// Close stops listening and closes all forwarded connections; idempotent
func (relay *Relay) Close() error {
	relay.mu.Lock()
	if relay.closed {
		relay.mu.Unlock()
		return nil
	}
	relay.closed = true
	conns := make([]net.Conn, 0, len(relay.conns))
	for conn := range relay.conns {
		conns = append(conns, conn)
	}
	relay.mu.Unlock()
	relay.cancel()
	err := relay.listener.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	return err
}

func (relay *Relay) serve() {
	for {
		conn, err := relay.listener.Accept()
		if err != nil {
			return
		}
		if !relay.track(conn) {
			_ = conn.Close()
			continue
		}
		go relay.handle(conn)
	}
}

// track registers an active connection, returning false if relay is closed
func (relay *Relay) track(conn net.Conn) bool {
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.closed {
		return false
	}
	relay.conns[conn] = struct{}{}
	return true
}

func (relay *Relay) untrack(conn net.Conn) {
	relay.mu.Lock()
	delete(relay.conns, conn)
	relay.mu.Unlock()
	_ = conn.Close()
}

// handle completes SOCKS5 handshake, connects to target via upstream proxy, and relays bidirectionally
func (relay *Relay) handle(client net.Conn) {
	defer relay.untrack(client)
	if err := client.SetDeadline(time.Now().Add(connectTimeout)); err != nil {
		return
	}
	target, err := readSocksConnect(client)
	if err != nil {
		return
	}
	upstream, err := relay.dialer.DialContext(relay.ctx, "tcp", target)
	if err != nil {
		_, _ = client.Write([]byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	if !relay.track(upstream) {
		_ = upstream.Close()
		return
	}
	defer relay.untrack(upstream)
	if _, err := client.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	if err := client.SetDeadline(time.Time{}); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go pipe(upstream, client, done)
	go pipe(client, upstream, done)
	<-done
	<-done
}

// pipe relays unidirectionally until source ends, closing write direction on success or both on error
func pipe(destination, source net.Conn, done chan<- struct{}) {
	_, err := io.Copy(destination, source)
	writer, halfClose := destination.(interface{ CloseWrite() error })
	if err != nil || !halfClose || writer.CloseWrite() != nil {
		_ = destination.Close()
		_ = source.Close()
	}
	done <- struct{}{}
}

// readSocksConnect reads unauthenticated SOCKS5 handshake and CONNECT request, returning unresolved target address
func readSocksConnect(client net.Conn) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil {
		return "", err
	}
	if header[0] != 5 {
		return "", fmt.Errorf("SOCKS version %d is not supported", header[0])
	}
	methods := make([]byte, header[1])
	if _, err := io.ReadFull(client, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, method := range methods {
		noAuth = noAuth || method == 0
	}
	if !noAuth {
		_, _ = client.Write([]byte{5, 0xff})
		return "", errors.New("client does not support unauthenticated SOCKS5")
	}
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(client, request); err != nil {
		return "", err
	}
	if request[0] != 5 || request[1] != 1 {
		_, _ = client.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("SOCKS5 command %d is not supported", request[1])
	}
	var host string
	switch request[3] {
	case 1, 4:
		address := make([]byte, 4)
		if request[3] == 4 {
			address = make([]byte, 16)
		}
		if _, err := io.ReadFull(client, address); err != nil {
			return "", err
		}
		host = net.IP(address).String()
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(client, length); err != nil {
			return "", err
		}
		domain := make([]byte, length[0])
		if _, err := io.ReadFull(client, domain); err != nil {
			return "", err
		}
		host = string(domain)
	default:
		_, _ = client.Write([]byte{5, 8, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", fmt.Errorf("SOCKS5 address type %d is not supported", request[3])
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(client, port); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), nil
}
