package main

import "testing"

func TestVPNEndpointGuardParsing(t *testing.T) {
	for _, tc := range []struct {
		config         string
		valid          bool
		protocol, port string
	}{
		{"proto tcp-client\nremote 1.2.3.4 1321\n", true, "tcp", "1321"},
		{"# remote bad 12\nremote vpn.example 1194 udp\n", true, "udp", "1194"},
		{"remote 1.2.3.4\n", true, "udp", "1194"},
		{"remote 1.2.3.4 0\n", false, "", ""},
		{"remote 1.2.3.4 99999\n", false, "", ""},
		{"proto udp6\nremote ::1 1194\n", false, "", ""},
		{"client\ndev tun\n", false, "", ""},
	} {
		got, err := vpnEndpoints(tc.config)
		if (err == nil) != tc.valid {
			t.Fatalf("%q: %v", tc.config, err)
		}
		if tc.valid && (got[0].Protocol != tc.protocol || got[0].Port != tc.port) {
			t.Fatalf("unexpected endpoint: %+v", got)
		}
	}
}
