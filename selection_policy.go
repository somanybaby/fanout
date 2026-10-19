package main

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

type SelectionPolicy struct {
	Region        string `json:"region,omitempty"`
	RegionCode    string `json:"region_code,omitempty"`
	RotateMinutes int    `json:"rotate_minutes"`
	ProbeOnly     bool   `json:"-"`
	Mode          string `json:"mode"`
	Country       string `json:"country"`
	Host          string `json:"host,omitempty"`
	IP            string `json:"ip,omitempty"`
	Preferred     Node   `json:"-"`
}

func normalizePolicy(p SelectionPolicy, n Node) (SelectionPolicy, error) {
	if p.Mode == "" {
		p.Mode = "auto"
	}
	if p.Country == "" {
		p.Country = n.CountryCode
	}
	p.Country = strings.ToUpper(p.Country)
	if !validRotation(p.RotateMinutes) || (p.Mode == "strict" && p.RotateMinutes > 0) {
		return p, fmt.Errorf("轮换间隔无效，严格锁定时请关闭轮换")
	}
	if p.Mode != "auto" && p.Mode != "preferred" && p.Mode != "strict" {
		return p, fmt.Errorf("请选择自动、首选 IP 或严格锁定模式")
	}
	if p.Mode != "auto" {
		if len(p.Country) != 2 || p.Country != strings.ToUpper(n.CountryCode) {
			return p, fmt.Errorf("节点必须具有明确且一致的国家信息")
		}
		a := net.ParseIP(p.IP)
		if a == nil || a.To4() == nil {
			return p, fmt.Errorf("首选或锁定模式需要预期出口 IPv4")
		}
		p.IP = a.String()
		p.Host = n.HostName
		p.Preferred = n
	} else {
		p.Host = ""
		p.IP = ""
		p.Preferred = Node{}
	}
	return p, nil
}
func (t *Tunnel) selectionPolicy() SelectionPolicy { t.mu.Lock(); defer t.mu.Unlock(); return t.Policy }
func (t *Tunnel) setPolicy(p SelectionPolicy)      { t.mu.Lock(); t.Policy = p; t.mu.Unlock() }
func (m *Manager) nodeBlocked(n Node) bool {
	if m.selection == nil {
		return false
	}
	if m.selection.loadErr != nil {
		return true
	}
	if m.selection.blocked(n.IP) || (m.risk != nil && m.risk.denied(n.IP)) {
		return true
	}
	q := m.quality.snapshot(n.HostName)
	return q.Identity.IP != "" && (m.selection.blocked(q.Identity.IP) || (m.risk != nil && m.risk.denied(q.Identity.IP)))
}
func (m *Manager) checkExitPolicy(t *Tunnel, ip, country string) error {
	p := t.selectionPolicy()
	wanted := p.Country
	if wanted == "" {
		wanted = t.Node.CountryCode
	}
	if wanted != "" && !strings.EqualFold(wanted, country) {
		return fmt.Errorf("实际出口国家 %s 与选择的 %s 不符", country, wanted)
	}
	if m.selection != nil && (m.selection.loadErr != nil || m.selection.blocked(ip)) {
		return fmt.Errorf("此出口 IP 已排除或排除列表不可用")
	}
	if (p.Mode == "strict" || (p.Mode == "preferred" && t.Node.HostName == p.Host)) && ip != p.IP {
		return fmt.Errorf("实际出口 IP 与选定的 %s 不符", p.IP)
	}
	return nil
}

// Keep country, preferred IP and exclusions stable throughout retry cycles.
func (m *Manager) policyCandidates(t *Tunnel) []Node {
	const limit = 6
	p := t.selectionPolicy()
	first := t.Node
	region := p.Country
	if region == "" {
		region = first.CountryCode
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if region == "" {
		for _, n := range m.nodes {
			if n.HostName == first.HostName {
				region = n.CountryCode
				break
			}
		}
	}
	used := map[string]bool{}
	for slot, other := range m.tunnels {
		if slot != t.Slot {
			used[other.Node.HostName] = true
		}
	}
	candidates := []Node{}
	seen := map[string]bool{}
	avoid := t.swapAvoid()
	add := func(n Node) {
		if n.HostName == "" || n.HostName == t.skipHostOnce || seen[n.HostName] || used[n.HostName] || m.nodeBlocked(n) {
			return
		}
		if region != "" && !strings.EqualFold(n.CountryCode, region) {
			return
		}
		seen[n.HostName] = true
		candidates = append(candidates, n)
	}
	if p.ProbeOnly {
		add(first)
		return candidates
	}
	if t.restoreFirst != nil && p.Mode != "strict" {
		add(*t.restoreFirst)
	}
	if p.Mode == "strict" || p.Mode == "preferred" {
		preferred := p.Preferred
		for _, n := range m.nodes {
			if n.HostName == p.Host {
				preferred = n
				break
			}
		}
		if preferred.HostName == "" && first.HostName == p.Host {
			preferred = first
		}
		add(preferred)
		if p.Mode == "strict" {
			return candidates
		}
	}
	firstInfo := m.quality.snapshot(first.HostName).Identity
	anchored := p.Region != "" || p.RegionCode != ""
	if !anchored {
		add(first)
	}
	pool := m.nodePoolLocked()
	foundFirst := false
	for _, n := range pool {
		if n.HostName == first.HostName {
			foundFirst = true
			break
		}
	}
	if !foundFirst {
		pool = append([]Node{first}, pool...)
	}
	// Known same-region candidates precede unknown locations, then known other
	// regions. Unknown candidates are privately verified before region fallback.
	sort.SliceStable(pool, func(i, j int) bool {
		rank := func(n Node) int {
			v := m.quality.snapshot(n.HostName).Identity
			if sameRegion(region, p.RegionCode, p.Region, v.RegionCode, v.Region) {
				return 0
			}
			if v.Region == "" && v.RegionCode == "" {
				return 1
			}
			return 2
		}
		return rank(pool[i]) < rank(pool[j])
	})
	for _, n := range pool {
		if len(candidates) >= limit {
			break
		}
		if region == "" || !strings.EqualFold(n.CountryCode, region) || (avoid[n.HostName] && n.HostName != first.HostName) {
			continue
		}
		add(n)
	}
	if anchored && (firstInfo.Region != "" || firstInfo.RegionCode != "") {
		add(first)
	}
	// Reserve a bounded fallback even when same/unknown-region candidates fill
	// the primary budget. Admission still waits until the second region phase.
	if anchored {
		fallbackCount := 0
		for _, n := range pool {
			info := m.quality.snapshot(n.HostName).Identity
			if info.Region == "" && info.RegionCode == "" {
				continue
			}
			if !strings.EqualFold(n.CountryCode, region) || sameRegion(region, p.RegionCode, p.Region, info.RegionCode, info.Region) {
				continue
			}
			if fallbackCount >= 2 {
				break
			}
			before := len(candidates)
			add(n)
			if len(candidates) > before {
				fallbackCount++
			}
		}
	}
	return candidates
}

func (m *Manager) Select(slot int, host, mode, ip string) error {
	m.mu.RLock()
	t := m.tunnels[slot]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("出口不存在")
	}
	if !t.opMu.TryLock() {
		return fmt.Errorf("出口正在操作中，请稍后重试")
	}
	if t.Status == "starting" || t.Status == "stopped" {
		t.opMu.Unlock()
		return fmt.Errorf("出口正在连接或已停止")
	}
	oldPolicy := t.selectionPolicy()
	oldHost, oldIP := t.Node.HostName, t.ExitIP
	node := t.Node
	if host != "" && host != node.HostName {
		found := false
		nodes, _ := m.Nodes()
		for _, n := range nodes {
			if n.HostName == host {
				node = n
				found = true
				break
			}
		}
		if !found {
			t.opMu.Unlock()
			return fmt.Errorf("节点已不在目录中，请刷新后重试")
		}
	}
	country := oldPolicy.Country
	if country == "" {
		country = t.Node.CountryCode
	}
	if country == "" || !strings.EqualFold(country, node.CountryCode) {
		t.opMu.Unlock()
		return fmt.Errorf("现有出口只允许选择同国家节点；其他国家请新建出口")
	}
	if m.nodeBlocked(node) || m.nodeInUse(node.HostName, slot) {
		t.opMu.Unlock()
		return fmt.Errorf("节点已排除或被其他出口使用")
	}
	if ip == "" && node.HostName == oldHost {
		ip = oldIP
	}
	p, err := normalizePolicy(SelectionPolicy{Mode: mode, Country: country, IP: ip, RotateMinutes: func() int {
		if mode == "strict" {
			return 0
		}
		return oldPolicy.RotateMinutes
	}()}, node)
	if err != nil {
		t.opMu.Unlock()
		return err
	}
	if node.HostName == oldHost {
		p.Region = oldPolicy.Region
		p.RegionCode = oldPolicy.RegionCode
	}
	// Policy-only changes on a verified exit need no network restart.
	if node.HostName == oldHost && t.Status == "up" && (p.Mode == "auto" || p.IP == oldIP) {
		t.setPolicy(p)
		if err := m.saveState(); err != nil {
			t.setPolicy(oldPolicy)
			t.opMu.Unlock()
			return err
		}
		m.selection.addEvent(SelectionEvent{Slot: slot, Kind: "policy", Reason: "修改出口选择模式", Country: country, OldIP: oldIP, NewIP: oldIP})
		t.opMu.Unlock()
		return nil
	}
	if t.Status == "up" {
		m.prepareSwitch(t, node, p, "手动选用 IP")
		return nil
	}
	t.setPolicy(p)
	t.Node = node
	m.launchReconnect(t, oldHost, oldIP, "手动选用 IP") // releases opMu asynchronously
	return nil
}
