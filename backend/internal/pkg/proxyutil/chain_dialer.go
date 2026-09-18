package proxyutil

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	xproxy "golang.org/x/net/proxy"
)

type ChainHop struct {
	Protocol string
	Host     string
	Port     int
	Username string
	Password string
}

type ForwardDialer struct {
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d *ForwardDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialContext(ctx, network, addr)
}

func (d *ForwardDialer) Dial(network, addr string) (net.Conn, error) {
	return d.dialContext(context.Background(), network, addr)
}

func NewChainDialer(hops []ChainHop) (*ForwardDialer, error) {
	base := &net.Dialer{
		Timeout:   socks5DialTimeout,
		KeepAlive: socks5DialKeepAlive,
	}
	var current *ForwardDialer
	current = &ForwardDialer{dialContext: base.DialContext}
	for i := len(hops) - 1; i >= 0; i-- {
		hop := hops[i]
		if strings.TrimSpace(hop.Host) == "" || hop.Port <= 0 {
			return nil, fmt.Errorf("invalid chain proxy hop")
		}
		next, err := newDialThrough(current, hop)
		if err != nil {
			return nil, err
		}
		current = next
	}
	return current, nil
}

func newDialThrough(forward *ForwardDialer, hop ChainHop) (*ForwardDialer, error) {
	scheme := strings.ToLower(strings.TrimSpace(hop.Protocol))
	switch scheme {
	case "http", "https":
		return &ForwardDialer{dialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialHTTPChainHop(ctx, forward, hop, scheme, network, addr)
		}}, nil
	case "socks5", "socks5h":
		return &ForwardDialer{dialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			hopURL := &url.URL{
				// Match proxyurl.Parse semantics: plain socks5 is upgraded to
				// socks5h so the upstream relay resolves the target hostname.
				Scheme: "socks5h",
				Host:   net.JoinHostPort(hop.Host, strconv.Itoa(hop.Port)),
			}
			if hop.Username != "" {
				hopURL.User = url.UserPassword(hop.Username, hop.Password)
			}
			dialer, err := xproxy.FromURL(hopURL, forward)
			if err != nil {
				return nil, fmt.Errorf("create chained socks5 dialer: %w", err)
			}
			if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
				return contextDialer.DialContext(ctx, network, addr)
			}
			return dialer.Dial(network, addr)
		}}, nil
	default:
		return nil, fmt.Errorf("unsupported chain proxy scheme: %s", scheme)
	}
}

func dialHTTPChainHop(
	ctx context.Context,
	forward *ForwardDialer,
	hop ChainHop,
	scheme string,
	network string,
	target string,
) (net.Conn, error) {
	hopAddr := net.JoinHostPort(hop.Host, strconv.Itoa(hop.Port))
	conn, err := forward.DialContext(ctx, network, hopAddr)
	if err != nil {
		return nil, fmt.Errorf("connect chain proxy %s: %w", hopAddr, err)
	}
	if scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: hop.Host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("chain proxy TLS handshake: %w", err)
		}
		conn = tlsConn
	}
	req := &http.Request{
		Method: "CONNECT",
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if hop.Username != "" {
		auth := base64.StdEncoding.EncodeToString([]byte(hop.Username + ":" + hop.Password))
		req.Header.Set("Proxy-Authorization", "Basic "+auth)
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write chain CONNECT: %w", err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read chain CONNECT response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("chain proxy CONNECT failed: %s", resp.Status)
	}
	if reader.Buffered() > 0 {
		conn = &bufferedConn{Conn: conn, reader: reader}
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
