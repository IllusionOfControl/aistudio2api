package proxydial

import (
	"bufio"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// TestRelayAuthenticatedUpstream verifies relay forwards domain targets via authenticated CONNECT upstream and fails on invalid credentials
func TestRelayAuthenticatedUpstream(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	targets := make(chan string, 4)
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:secret"))
	go func() {
		for {
			conn, err := upstream.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				request, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				if request.Header.Get("Proxy-Authorization") != expected {
					_, _ = conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\n\r\n"))
					return
				}
				targets <- request.Host
				target, err := net.Dial("tcp", echo.Addr().String())
				if err != nil {
					return
				}
				defer target.Close()
				_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
				go func() { _, _ = io.Copy(target, conn) }()
				_, _ = io.Copy(conn, target)
			}()
		}
	}()

	for _, password := range []string{"secret", "wrong"} {
		relay, err := StartRelay(&url.URL{Scheme: "http", User: url.UserPassword("user", password), Host: upstream.Addr().String()})
		if err != nil {
			t.Fatal(err)
		}
		client, err := proxy.SOCKS5("tcp", relay.Addr().String(), nil, &net.Dialer{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		conn, err := client.Dial("tcp", "aistudio.google.com:443")
		if password == "wrong" {
			if err == nil {
				_ = conn.Close()
				t.Fatal("connection established despite invalid credentials")
			}
			_ = relay.Close()
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if target := <-targets; target != "aistudio.google.com:443" {
			t.Fatalf("upstream received target %q", target)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		reply := make([]byte, 4)
		if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "ping" {
			t.Fatalf("echo %q, err = %v", reply, err)
		}
		_ = conn.Close()
		_ = relay.Close()
	}
}
