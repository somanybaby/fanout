package main

import (
	"net"
)

// Probe through an ephemeral host-loopback listener while the public listener
// stays closed to application egress. A wrong strict IP never becomes usable.
func (t *Tunnel) privateProbe() (*Tunnel, func(), error) {
	var ln net.Listener
	err := inMainNetns(func() error { var e error; ln, e = net.Listen("tcp4", "127.0.0.1:0"); return e })
	if err != nil {
		return nil, nil, err
	}
	cred := t.credential()
	probe := &Tunnel{Port: ln.Addr().(*net.TCPAddr).Port, Cred: cred}
	dial := dialerInNetns(t.nsName())
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			go serveSocks(c, &cred, dial)
		}
	}()
	return probe, func() { ln.Close() }, nil
}
