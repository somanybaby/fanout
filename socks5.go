package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// SOCKS5 最小实现：只支持无认证 + CONNECT。
// 域名在本进程内解析，隧道里只跑 TCP，避免依赖隧道内的 UDP/DNS。

const (
	socksVer5     = 0x05
	authNone      = 0x00
	cmdConnect    = 0x01
	atypIPv4      = 0x01
	atypDomain    = 0x03
	atypIPv6      = 0x04
	repSuccess    = 0x00
	repGenFail    = 0x01
	repHostUnre   = 0x04
	repCmdNotSupp = 0x07
)

// serveSocks 处理一条 SOCKS5 连接。dial 决定流量从哪条链路出去。
func serveSocks(client net.Conn, dial func(network, addr string) (net.Conn, error)) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))

	if err := socksHandshake(client); err != nil {
		return
	}

	addr, err := socksReadRequest(client)
	if err != nil {
		if errors.Is(err, errCmdNotSupported) {
			socksReply(client, repCmdNotSupp)
		} else {
			socksReply(client, repGenFail)
		}
		return
	}

	remote, err := dial("tcp", addr)
	if err != nil {
		socksReply(client, repHostUnre)
		return
	}
	defer remote.Close()

	if err := socksReply(client, repSuccess); err != nil {
		return
	}

	// 转发阶段不设整体超时，交给两端自然关闭
	_ = client.SetDeadline(time.Time{})
	_ = remote.SetDeadline(time.Time{})
	relay(client, remote)
}

func socksHandshake(c net.Conn) error {
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return err
	}
	if head[0] != socksVer5 {
		return errors.New("不是 socks5")
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return err
	}
	_, err := c.Write([]byte{socksVer5, authNone})
	return err
}

var errCmdNotSupported = errors.New("仅支持 CONNECT")

func socksReadRequest(c net.Conn) (string, error) {
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return "", err
	}
	if head[1] != cmdConnect {
		return "", errCmdNotSupported
	}

	var host string
	switch head[3] {
	case atypIPv4:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case atypIPv6:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case atypDomain:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", fmt.Errorf("不支持的地址类型 %d", head[3])
	}

	pb := make([]byte, 2)
	if _, err := io.ReadFull(c, pb); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))), nil
}

func socksReply(c net.Conn, code byte) error {
	_, err := c.Write([]byte{socksVer5, code, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}
