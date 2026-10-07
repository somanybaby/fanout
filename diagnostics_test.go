package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestDiagnosticSOCKSAuthenticationAndTraffic(t *testing.T) {
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cred := SocksCred{User: "diagnostic-test", Pass: "local-test-value"}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSocks(c, &cred, net.Dial)
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := dialSOCKS(ctx, port, cred, echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte("check")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "check" {
		t.Fatal("authenticated application traffic failed")
	}
	if bad, err := dialSOCKS(ctx, port, SocksCred{User: cred.User, Pass: "wrong"}, echo.Addr().String()); err == nil {
		bad.Close()
		t.Fatal("bad credentials accepted")
	}
}
