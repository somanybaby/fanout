package main

import (
	"net"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// dialerInNetns 返回一个在指定 netns 内建立出站连接的 dial 函数。
// 每次拨号都要切一次 netns，因为 socket 的归属在创建时确定。
func dialerInNetns(nsName string) func(network, addr string) (net.Conn, error) {
	return func(network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		ch := make(chan result, 1)

		go func() {
			runtime.LockOSThread()

			origin, err := os.Open("/proc/self/ns/net")
			if err != nil {
				ch <- result{nil, err}
				return
			}
			defer origin.Close()

			target, err := os.Open("/var/run/netns/" + nsName)
			if err != nil {
				ch <- result{nil, err}
				return
			}
			defer target.Close()

			if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
				ch <- result{nil, err}
				return
			}

			conn, dialErr := net.Dial(network, addr)

			if err := unix.Setns(int(origin.Fd()), unix.CLONE_NEWNET); err != nil {
				if conn != nil {
					conn.Close()
				}
				ch <- result{nil, err}
				return
			}

			runtime.UnlockOSThread()
			ch <- result{conn, dialErr}
		}()

		r := <-ch
		return r.conn, r.err
	}
}
