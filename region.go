package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func regionCode(country, code string) string {
	v := strings.ToUpper(strings.TrimSpace(code))
	v = strings.TrimPrefix(v, strings.ToUpper(country)+"-")
	if n, e := strconv.Atoi(v); e == nil {
		return strconv.Itoa(n)
	}
	return v
}
func regionName(name string) string {
	v := strings.ToLower(strings.TrimSpace(name))
	for _, suffix := range []string{" prefecture", " metropolis", " province", " region", "都", "府", "県", "省"} {
		v = strings.TrimSuffix(v, suffix)
	}
	return strings.TrimSpace(v)
}
func sameRegion(country, code, name, otherCode, otherName string) bool {
	a, b := regionCode(country, code), regionCode(country, otherCode)
	if a != "" && b != "" {
		return a == b
	}
	a, b = regionName(name), regionName(otherName)
	return a != "" && a == b
}

func apiRotation(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeJSON(w, 405, map[string]string{"error": "使用 POST"})
			return
		}
		slot, err := strconv.Atoi(r.URL.Query().Get("slot"))
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "出口无效"})
			return
		}
		minutes, err := strconv.Atoi(r.URL.Query().Get("minutes"))
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": "间隔无效"})
			return
		}
		if err := m.SetRotation(slot, minutes); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

type otherRegionError struct{}

func (*otherRegionError) Error() string {
	return "此候选位于同国其他地区，留作本轮备用"
}
func (m *Manager) rememberRegion(t *Tunnel, identity ExitIdentity) {
	p := t.selectionPolicy()
	if p.Region == "" && p.RegionCode == "" {
		p.Region = identity.Region
		p.RegionCode = identity.RegionCode
		t.setPolicy(p)
	}
}
func (m *Manager) checkRegionPreference(t *Tunnel, identity ExitIdentity) error {
	p := t.selectionPolicy()
	if p.Mode == "strict" || p.ProbeOnly || t.allowRegionFallback || (p.Region == "" && p.RegionCode == "") {
		return nil
	}
	if !sameRegion(p.Country, p.RegionCode, p.Region, identity.RegionCode, identity.Region) {
		return &otherRegionError{}
	}
	return nil
}
func validRotation(minutes int) bool {
	return minutes == 0 || minutes == 60 || minutes == 360 || minutes == 720 || minutes == 1440 || minutes == 2880
}
func (m *Manager) SetRotation(slot, minutes int) error {
	if !validRotation(minutes) {
		return fmt.Errorf("轮换间隔请选择关闭、1、6、12、24 或 48 小时")
	}
	m.mu.RLock()
	t := m.tunnels[slot]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("出口不存在")
	}
	if !t.opMu.TryLock() {
		return fmt.Errorf("出口正在操作中")
	}
	defer t.opMu.Unlock()
	p := t.selectionPolicy()
	if p.Mode == "strict" && minutes > 0 {
		return fmt.Errorf("严格锁定 IP 时不能定时轮换")
	}
	old := p
	p.RotateMinutes = minutes
	t.setPolicy(p)
	if err := m.saveState(); err != nil {
		t.setPolicy(old)
		return err
	}
	return nil
}
