package main

import (
	"strings"
	"testing"
)

func TestNativeInboundTagMatchesXUIFormat(t *testing.T) {
	// tag 格式必须和 3x-ui 一致，否则两种后端的绑定语义会对不上
	cases := []struct {
		ib   nativeInbound
		want string
	}{
		{nativeInbound{Port: 443, Network: "tcp"}, "in-443-tcp"},
		{nativeInbound{Port: 8080, Network: "ws"}, "in-8080-ws"},
		{nativeInbound{Port: 1234}, "in-1234-tcp"}, // 缺省按 tcp
	}
	for _, c := range cases {
		if got := c.ib.tag(); got != c.want {
			t.Errorf("tag() = %q, want %q", got, c.want)
		}
	}
}

func TestBuildXrayConfigBindsOnlyLiveTunnels(t *testing.T) {
	up := &Tunnel{Port: 1080, Status: "up", Node: Node{HostName: "jp1"}}
	down := &Tunnel{Port: 1081, Status: "failed", Node: Node{HostName: "jp2"}}
	inbounds := []*nativeInbound{
		{ID: 1, Port: 100, Protocol: "vless", Enable: true, BoundTo: "jp1"},
		{ID: 2, Port: 200, Protocol: "vless", Enable: true, BoundTo: "jp2"},
		{ID: 3, Port: 300, Protocol: "vless", Enable: true},
	}

	cfg := buildXrayConfig(inbounds, []*Tunnel{up, down})

	outs := map[string]bool{}
	for _, o := range cfg["outbounds"].([]any) {
		outs[o.(map[string]any)["tag"].(string)] = true
	}
	if !outs["fanout-jp1"] {
		t.Error("已连通的隧道应当有对应出站")
	}
	if outs["fanout-jp2"] {
		t.Error("未连通的隧道不该生成出站")
	}

	rules := cfg["routing"].(map[string]any)["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("只有绑到连通隧道的入站才该有规则，实际 %d 条", len(rules))
	}
	if got := rules[0].(map[string]any)["outboundTag"]; got != "fanout-jp1" {
		t.Errorf("outboundTag = %v, want fanout-jp1", got)
	}
}

func TestBuildXrayConfigForcesIPv4OnDirect(t *testing.T) {
	// 隧道内没有 IPv6，direct 走 IPv6 会暴露母机真实地址
	cfg := buildXrayConfig(nil, nil)
	for _, o := range cfg["outbounds"].([]any) {
		m := o.(map[string]any)
		if m["tag"] != "direct" {
			continue
		}
		s := m["settings"].(map[string]any)
		if s["domainStrategy"] != "UseIPv4" {
			t.Errorf("direct 出站应强制 IPv4，实际 %v", s["domainStrategy"])
		}
		return
	}
	t.Fatal("没有找到 direct 出站")
}

func TestShareLinkPerProtocol(t *testing.T) {
	c := nativeClient{ID: "uuid-1", Password: "pw-1", Email: "e", Enable: true}

	vless := shareLink(&nativeInbound{Port: 100, Protocol: "vless", Remark: "r"}, c, "1.2.3.4")
	if !strings.HasPrefix(vless, "vless://uuid-1@1.2.3.4:100?") {
		t.Errorf("vless 链接格式不对: %s", vless)
	}
	if !strings.Contains(vless, "encryption=none") {
		t.Errorf("vless 需要 encryption=none: %s", vless)
	}

	tro := shareLink(&nativeInbound{Port: 200, Protocol: "trojan", Network: "ws", Path: "/p"}, c, "h")
	if !strings.HasPrefix(tro, "trojan://pw-1@h:200?") {
		t.Errorf("trojan 应当用密码而不是 UUID: %s", tro)
	}
	if !strings.Contains(tro, "path=%2Fp") {
		t.Errorf("ws 链接要带 path: %s", tro)
	}
}

func TestCloneRemark(t *testing.T) {
	if got := cloneRemark("线路A", "JP-244"); got != "线路A-JP-244" {
		t.Errorf("cloneRemark = %q", got)
	}
	if got := cloneRemark("", "JP-244"); got != "JP-244" {
		t.Errorf("空备注时应直接用标签，实际 %q", got)
	}
}
