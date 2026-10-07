package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type ProbeResult struct {
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms"`
}

type ExitIdentity struct {
	IP          string    `json:"ip"`
	Country     string    `json:"country"`
	Operator    string    `json:"operator"`
	ASN         string    `json:"asn"`
	Residential string    `json:"residential"`
	CheckedAt   time.Time `json:"checked_at"`
}

type TunnelDiagnostics struct {
	CheckedAt time.Time    `json:"checked_at"`
	SOCKS     ProbeResult  `json:"socks"`
	HTTPS     ProbeResult  `json:"https"`
	Node      ProbeResult  `json:"node"`
	Identity  ExitIdentity `json:"identity"`
	Scope     string       `json:"scope"`
}

// Authentication is written to the socket, never to process arguments or logs.
func dialSOCKS(ctx context.Context, port int, cred SocksCred, target string) (net.Conn, error) {
	host, ps, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	p, err := strconv.Atoi(ps)
	if err != nil || p < 1 || p > 65535 || len(host) > 255 {
		return nil, fmt.Errorf("invalid SOCKS destination")
	}
	var c net.Conn
	err = inMainNetns(func() error {
		var dialErr error
		c, dialErr = (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		return dialErr
	})
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			c.Close()
		}
	}()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(healthTimeout)
	}
	c.SetDeadline(deadline)
	method := byte(0)
	if cred.User != "" {
		method = 2
	}
	if _, err = c.Write([]byte{5, 1, method}); err != nil {
		return nil, err
	}
	var response [2]byte
	if _, err = io.ReadFull(c, response[:]); err != nil {
		return nil, err
	}
	if response[0] != 5 || response[1] != method {
		return nil, fmt.Errorf("SOCKS authentication method rejected")
	}
	if method == 2 {
		if len(cred.User) > 255 || len(cred.Pass) > 255 {
			return nil, fmt.Errorf("invalid SOCKS credential length")
		}
		auth := append([]byte{1, byte(len(cred.User))}, []byte(cred.User)...)
		auth = append(auth, byte(len(cred.Pass)))
		auth = append(auth, []byte(cred.Pass)...)
		if _, err = c.Write(auth); err != nil {
			return nil, err
		}
		if _, err = io.ReadFull(c, response[:]); err != nil {
			return nil, err
		}
		if response[0] != 1 || response[1] != 0 {
			return nil, fmt.Errorf("SOCKS credentials rejected")
		}
	}
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	req = binary.BigEndian.AppendUint16(req, uint16(p))
	if _, err = c.Write(req); err != nil {
		return nil, err
	}
	var head [4]byte
	if _, err = io.ReadFull(c, head[:]); err != nil {
		return nil, err
	}
	if head[0] != 5 || head[1] != 0 {
		return nil, fmt.Errorf("SOCKS connect failed (code %d)", head[1])
	}
	size := 0
	switch head[3] {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		var length [1]byte
		if _, err = io.ReadFull(c, length[:]); err != nil {
			return nil, err
		}
		size = int(length[0])
	default:
		return nil, fmt.Errorf("invalid SOCKS response address")
	}
	if _, err = io.CopyN(io.Discard, c, int64(size+2)); err != nil {
		return nil, err
	}
	c.SetDeadline(time.Time{})
	failed = false
	return c, nil
}

func proxyHTTPClient(port int, cred SocksCred, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: timeout,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialSOCKS(ctx, port, cred, addr)
		},
	}}
}

func getProbe(client *http.Client, address string, want int) (ProbeResult, []byte) {
	start := time.Now()
	r, err := client.Get(address)
	if err != nil {
		return ProbeResult{Detail: "连接失败或超时", LatencyMS: time.Since(start).Milliseconds()}, nil
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return ProbeResult{OK: err == nil && r.StatusCode == want, Detail: fmt.Sprintf("HTTP %d", r.StatusCode), LatencyMS: time.Since(start).Milliseconds()}, body
}

func probeSOCKSExit(t *Tunnel) (ProbeResult, string) {
	client := proxyHTTPClient(t.Port, t.credential(), healthTimeout)
	for _, address := range []string{"https://api.ipify.org", "https://checkip.amazonaws.com"} {
		p, body := getProbe(client, address, 200)
		ip := net.ParseIP(stringTrim(body))
		if p.OK && ip != nil && ip.To4() != nil {
			if ip.String() == hostPublicIP() {
				return ProbeResult{Detail: "出口退回母机，已拒绝", LatencyMS: p.LatencyMS}, ""
			}
			p.Detail = "SOCKS 认证和 HTTPS 出口查询成功"
			return p, ip.String()
		}
	}
	return ProbeResult{Detail: "SOCKS 出口查询失败"}, ""
}

func stringTrim(b []byte) string { return string(bytesTrim(b)) }
func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\n' || b[0] == '\r' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

func probeWeb(t *Tunnel) ProbeResult {
	client := proxyHTTPClient(t.Port, t.credential(), healthTimeout)
	for _, address := range []string{"https://www.google.com/generate_204", "https://www.gstatic.com/generate_204"} {
		p, _ := getProbe(client, address, 204)
		if p.OK {
			return p
		}
	}
	return ProbeResult{Detail: "两个 HTTPS 测试地址均不可用"}
}

func lookupExitIdentity(t *Tunnel, ip string) (ExitIdentity, error) {
	info := ExitIdentity{IP: ip, Residential: "unknown", CheckedAt: time.Now()}
	p, body := getProbe(proxyHTTPClient(t.Port, t.credential(), healthTimeout), "https://ipwho.is/"+ip, 200)
	var raw struct {
		Success    bool   `json:"success"`
		IP         string `json:"ip"`
		Country    string `json:"country_code"`
		Connection struct {
			ASN int    `json:"asn"`
			Org string `json:"org"`
			ISP string `json:"isp"`
		} `json:"connection"`
	}
	if !p.OK || json.Unmarshal(body, &raw) != nil || !raw.Success || raw.IP != ip || len(raw.Country) != 2 {
		return info, fmt.Errorf("无法核验实际出口国家")
	}
	info.Country = raw.Country
	info.Operator = raw.Connection.ISP
	if info.Operator == "" {
		info.Operator = raw.Connection.Org
	}
	if raw.Connection.ASN > 0 {
		info.ASN = fmt.Sprintf("AS%d", raw.Connection.ASN)
	}
	if isResidential(t.Node.HostName, ip) {
		info.Residential = "candidate"
	} else {
		info.Residential = "hosting"
	}
	return info, nil
}

// Parse only supported links; never change the user's inbound to make a probe fit.
func diagnosticClientConfig(link string, socksPort int) (map[string]any, error) {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "vless" || u.User == nil {
		return nil, fmt.Errorf("此协议暂不支持本机握手检查")
	}
	q := u.Query()
	if q.Get("security") != "reality" || (q.Get("type") != "" && q.Get("type") != "tcp") {
		return nil, fmt.Errorf("仅检查 VLESS TCP Reality 入站")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return nil, err
	}
	stream := map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"fingerprint": "chrome", "serverName": q.Get("sni"), "publicKey": q.Get("pbk"), "shortId": q.Get("sid")}}
	return map[string]any{
		"log":       map[string]any{"loglevel": "none"},
		"inbounds":  []any{map[string]any{"listen": "127.0.0.1", "port": socksPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}},
		"outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]any{"id": u.User.Username(), "encryption": "none", "flow": q.Get("flow")}}}}}, "streamSettings": stream}},
	}, nil
}

func (m *Manager) probeNode(t *Tunnel) ProbeResult {
	p, err := openPanel()
	if err != nil {
		return ProbeResult{Detail: "节点管理后端不可用"}
	}
	ibs, err := p.Inbounds(nil)
	if err != nil {
		return ProbeResult{Detail: "无法读取绑定入站"}
	}
	var link string
	for _, ib := range ibs {
		if ib.BoundTo != sanitizeTag(t.Node.HostName) || !ib.Enable {
			continue
		}
		detail, err := p.InboundDetail(ib.ID, hostPublicIP())
		if err != nil {
			continue
		}
		for _, l := range detail.Links {
			if _, err := diagnosticClientConfig(l, 1); err == nil {
				link = l
				break
			}
		}
		if link != "" {
			break
		}
	}
	if link == "" {
		return ProbeResult{Detail: "没有可检查的 VLESS TCP Reality 节点"}
	}
	bin, err := findXray(m.workDir)
	if err != nil {
		return ProbeResult{Detail: "找不到节点测试核心"}
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ProbeResult{Detail: "无法分配本机测试端口"}
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg, _ := diagnosticClientConfig(link, port)
	dir, err := os.MkdirTemp(m.workDir, "diagnostic-")
	if err != nil {
		return ProbeResult{Detail: "无法创建测试目录"}
	}
	defer os.RemoveAll(dir)
	blob, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "client.json")
	if err := os.WriteFile(path, blob, 0600); err != nil {
		return ProbeResult{Detail: "无法保存测试配置"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "-c", path)
	if err := cmdStart(cmd); err != nil {
		return ProbeResult{Detail: "无法启动节点测试"}
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	for i := 0; i < 20; i++ {
		c, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	pr, body := getProbe(proxyHTTPClient(port, SocksCred{}, healthTimeout), "https://api.ipify.org", 200)
	if pr.OK && stringTrim(body) == t.ExitIP {
		pr.Detail = "本机 Reality 握手与家宽出口一致；外部接入需另测"
		return pr
	}
	return ProbeResult{Detail: "本机节点握手失败或出口不一致", LatencyMS: pr.LatencyMS}
}

var diagnosticLimit = make(chan struct{}, 1)

func apiDiagnostics(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "使用 POST 运行检查"})
			return
		}
		select {
		case diagnosticLimit <- struct{}{}:
			defer func() { <-diagnosticLimit }()
		default:
			writeJSON(w, 429, map[string]string{"error": "已有检查运行，请稍后重试"})
			return
		}
		slot, _ := strconv.Atoi(r.URL.Query().Get("slot"))
		m.mu.RLock()
		t := m.tunnels[slot]
		m.mu.RUnlock()
		if t == nil || t.Status != "up" {
			writeJSON(w, 409, map[string]string{"error": "出口尚未连通"})
			return
		}
		report := TunnelDiagnostics{CheckedAt: time.Now(), Scope: "server_local"}
		report.SOCKS, _ = probeSOCKSExit(t)
		report.HTTPS = probeWeb(t)
		report.Node = m.probeNode(t)
		t.mu.Lock()
		report.Identity = t.Identity
		t.LastDiagnostics = report
		t.mu.Unlock()
		writeJSON(w, 200, report)
	}
}
