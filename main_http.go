package main

import (
	"context"
	"net"
	"net/http"
	"time"
)

// VPN dialing can leave unrelated runtime threads in a tunnel namespace.
// Source/update/control HTTP must always open sockets in the host namespace.
func mainHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var c net.Conn
		err := inMainNetns(func() error { var e error; c, e = (&net.Dialer{}).DialContext(ctx, network, addr); return e })
		return c, err
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}
