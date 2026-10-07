package main

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

type vpnEndpoint struct {
	Host     string
	Port     string
	Protocol string
}

// Guard only namespaces created by this instance. A missing tunnel must never
// turn the namespace's bootstrap route into an application egress route.
func vpnEndpoints(config string) ([]vpnEndpoint, error) {
	proto := "udp"
	for _, line := range strings.Split(config, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) >= 2 && f[0] == "proto" {
			proto = f[1]
		}
	}
	var out []vpnEndpoint
	for _, line := range strings.Split(config, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 2 || f[0] != "remote" {
			continue
		}
		port, transport := "1194", proto
		if len(f) > 2 {
			port = f[2]
		}
		if len(f) > 3 {
			transport = f[3]
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid VPN remote port")
		}
		switch transport {
		case "tcp", "tcp-client", "tcp4", "tcp4-client":
			transport = "tcp"
		case "udp", "udp4":
			transport = "udp"
		default:
			return nil, fmt.Errorf("unsupported VPN transport %q", transport)
		}
		host := strings.Trim(f[1], "\"'")
		if host == "" || strings.HasPrefix(host, "-") {
			return nil, fmt.Errorf("invalid VPN remote host")
		}
		out = append(out, vpnEndpoint{host, port, transport})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("VPN config has no remote endpoint; refusing unguarded tunnel")
	}
	return out, nil
}

func (t *Tunnel) installEgressGuard() error {
	endpoints, err := vpnEndpoints(t.Node.Config)
	if err != nil {
		return err
	}
	var resolved []vpnEndpoint
	for _, e := range endpoints {
		var ips []net.IP
		err := inMainNetns(func() error { var lookupErr error; ips, lookupErr = net.LookupIP(e.Host); return lookupErr })
		if err != nil {
			return fmt.Errorf("resolve VPN endpoint: %w", err)
		}
		for _, ip := range ips {
			if v4 := ip.To4(); v4 != nil {
				resolved = append(resolved, vpnEndpoint{v4.String(), e.Port, e.Protocol})
			}
		}
	}
	if len(resolved) == 0 {
		return fmt.Errorf("VPN endpoint has no IPv4 address")
	}
	ns := t.nsName()
	if err := cmdRun(exec.Command("ip", "netns", "exec", ns, "sysctl", "-q", "-w", "net.ipv6.conf.all.disable_ipv6=1", "net.ipv6.conf.default.disable_ipv6=1")); err != nil {
		return fmt.Errorf("无法关闭家宽命名空间 IPv6，拒绝未防护隧道: %w", err)
	}
	apply := func(args ...string) error {
		return cmdRun(exec.Command("ip", append([]string{"netns", "exec", ns, "iptables", "-w", "5"}, args...)...))
	}
	// Set policy first: an interrupted setup leaves the namespace blocked.
	if err := apply("-P", "OUTPUT", "DROP"); err != nil {
		return err
	}
	if err := apply("-A", "OUTPUT", "-o", "lo", "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := apply("-A", "OUTPUT", "-o", "tun+", "-j", "ACCEPT"); err != nil {
		return err
	}
	for _, e := range resolved {
		if err := apply("-A", "OUTPUT", "-d", e.Host, "-p", e.Protocol, "--dport", e.Port, "-j", "ACCEPT"); err != nil {
			return err
		}
	}
	for _, proto := range []string{"udp", "tcp"} {
		if err := apply("-A", "OUTPUT", "-d", "8.8.8.8", "-p", proto, "--dport", "53", "-j", "ACCEPT"); err != nil {
			return err
		}
	}
	return nil
}
