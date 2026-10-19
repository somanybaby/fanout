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
	// Preserve the saved working exit at restart even when its region is only
	// a fallback. Country, exclusions, actual IP and risk are checked earlier.
	if t.restoreFirst != nil && t.Node.HostName == t.restoreFirst.HostName {
		return nil
	}
	if !sameRegion(p.Country, p.RegionCode, p.Region, identity.RegionCode, identity.Region) {
		return &otherRegionError{}
	}
	return nil
}

func (m *Manager) SetRegionPreference(slot int, preference string) error {
	m.mu.RLock()
	t := m.tunnels[slot]
	m.mu.RUnlock()
	if t == nil || !t.opMu.TryLock() {
		return fmt.Errorf("出口不存在或正在操作中")
	}
	defer t.opMu.Unlock()
	p := t.selectionPolicy()
	if p.Mode != "auto" || p.ProbeOnly {
		return fmt.Errorf("请先选择自动模式，再设置地区偏好")
	}
	old := p
	switch preference {
	case "tokyo":
		if p.Country != "JP" {
			return fmt.Errorf("东京优先只适用于日本出口")
		}
		p.Region, p.RegionCode = "Tokyo", "13"
	case "current":
		t.mu.Lock()
		p.Region, p.RegionCode = t.Identity.Region, t.Identity.RegionCode
		t.mu.Unlock()
		if p.Region == "" && p.RegionCode == "" {
			return fmt.Errorf("当前出口尚无已核验的地区")
		}
	default:
		return fmt.Errorf("地区偏好无效")
	}
	t.setPolicy(p)
	if err := m.saveState(); err != nil {
		t.setPolicy(old)
		return err
	}
	m.selection.addEvent(SelectionEvent{Slot: slot, Kind: "region_preference", Reason: "修改优先地区为 " + p.Region + "；当前连接继续使用", Country: p.Country})
	return nil
}

func apiRegionPreference(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "使用 POST"})
			return
		}
		slot, _ := strconv.Atoi(r.URL.Query().Get("slot"))
		if err := m.SetRegionPreference(slot, r.URL.Query().Get("preference")); err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
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
