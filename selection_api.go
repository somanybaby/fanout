package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CandidateView struct {
	Host            string         `json:"host"`
	IP              string         `json:"ip"`
	Country         string         `json:"country"`
	CountryName     string         `json:"country_name"`
	AdvertisedSpeed float64        `json:"advertised_speed_mbps"`
	Identity        ExitIdentity   `json:"identity"`
	Risk            RiskAssessment `json:"risk"`
	Quality         NodeQuality    `json:"quality"`
	Favorite        bool           `json:"favorite"`
	Blocked         bool           `json:"blocked"`
	ActiveSlot      int            `json:"active_slot"`
	StableSeconds   int64          `json:"stable_seconds"`
}

func (m *Manager) candidateViews() []CandidateView {
	nodes, _ := m.Nodes()
	rows := make([]CandidateView, 0, len(nodes))
	active := map[string]*Tunnel{}
	for _, t := range m.Tunnels() {
		active[t.Node.HostName] = t
	}
	for host, t := range active {
		found := false
		for _, n := range nodes {
			if n.HostName == host {
				found = true
				break
			}
		}
		if !found {
			nodes = append(nodes, t.Node)
		}
	}
	for _, n := range nodes {
		if residentialOnly() && !n.Residential && active[n.HostName] == nil {
			continue
		}
		q := m.quality.snapshot(n.HostName)
		mark := m.selection.mark(n.IP)
		observed := m.selection.mark(q.Identity.IP)
		row := CandidateView{Host: n.HostName, IP: n.IP, Country: n.CountryCode, CountryName: countryLabel(n.CountryCode, n.Country), AdvertisedSpeed: n.SpeedMbps, Identity: q.Identity, Risk: m.risk.snapshot(q.Identity.IP), Quality: q, Favorite: mark.Favorite || observed.Favorite, Blocked: mark.Blocked || observed.Blocked}
		if t := active[n.HostName]; t != nil {
			row.ActiveSlot = t.Slot
			if t.Status == "up" {
				row.StableSeconds = int64(time.Since(t.Since).Seconds())
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Favorite != b.Favorite {
			return a.Favorite
		}
		if (a.Country == "JP") != (b.Country == "JP") {
			return a.Country == "JP"
		}
		return a.Host < b.Host
	})
	return rows
}
func apiCandidates(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writeJSON(w, 405, map[string]string{"error": "使用 GET"})
			return
		}
		country := strings.ToUpper(r.URL.Query().Get("country"))
		rows := []CandidateView{}
		for _, row := range m.candidateViews() {
			if country == "" || row.Country == country {
				rows = append(rows, row)
			}
		}
		_, at := m.Nodes()
		writeJSON(w, 200, map[string]any{"nodes": rows, "fetched": at, "measurement_scope": "server_to_exit_https", "notice": "公布 IP 是 VPN 连接地址；实际出口需连接后核验。历史测量不代表当前在线或手机延迟。"})
	}
}
func apiIPMarks(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			writeJSON(w, 200, m.selection.snapshot().Marks)
			return
		}
		if r.Method != "POST" {
			writeJSON(w, 405, map[string]string{"error": "使用 GET 或 POST"})
			return
		}
		var in struct {
			IP       string `json:"ip"`
			Favorite bool   `json:"favorite"`
			Blocked  bool   `json:"blocked"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
			return
		}
		// Excluding a working exit would be disruptive. Require choosing another IP
		// or stopping that exit first; the UI explains this before any mutation.
		if in.Blocked {
			for _, t := range m.Tunnels() {
				if t.Status == "up" && (t.ExitIP == in.IP || t.Node.IP == in.IP) {
					writeJSON(w, 409, map[string]string{"error": "此 IP 正在使用，请先选择其他 IP 或停止该出口"})
					return
				}
			}
		}
		if err := m.selection.setMark(in.IP, IPMark{Favorite: in.Favorite, Blocked: in.Blocked}); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}
func apiSelectionEvents(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writeJSON(w, 405, map[string]string{"error": "使用 GET"})
			return
		}
		slot, _ := strconv.Atoi(r.URL.Query().Get("slot"))
		events := []SelectionEvent{}
		all := m.selection.snapshot().Events
		for i := len(all) - 1; i >= 0; i-- {
			if slot == 0 || all[i].Slot == slot {
				events = append(events, all[i])
			}
		}
		writeJSON(w, 200, events)
	}
}
func apiSelectIP(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeJSON(w, 405, map[string]string{"error": "使用 POST"})
			return
		}
		var in struct {
			Slot          int    `json:"slot"`
			Host          string `json:"host"`
			Mode          string `json:"mode"`
			IP            string `json:"ip"`
			TemplateID    int    `json:"template_id"`
			RotateMinutes int    `json:"rotate_minutes"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in) != nil {
			writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
			return
		}
		if in.TemplateID < 0 {
			writeJSON(w, 400, map[string]string{"error": "模板无效"})
			return
		}
		if in.Slot > 0 {
			if err := m.Select(in.Slot, in.Host, in.Mode, in.IP); err != nil {
				writeJSON(w, 409, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		var node *Node
		nodes, _ := m.Nodes()
		for _, n := range nodes {
			if n.HostName == in.Host {
				copy := n
				node = &copy
				break
			}
		}
		if node == nil {
			writeJSON(w, 404, map[string]string{"error": "节点已不在目录中，请刷新"})
			return
		}
		if residentialOnly() && !node.Residential {
			writeJSON(w, 409, map[string]string{"error": "当前只允许家宽候选"})
			return
		}
		t, err := m.StartSelected(*node, SelectionPolicy{Mode: in.Mode, IP: in.IP, RotateMinutes: in.RotateMinutes})
		if err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		// Template 0 performs an isolated trial exit without changing 3x-ui inbounds.
		if in.TemplateID > 0 {
			go func() {
				m.waitUp(t)
				if t.Status != "up" {
					return
				}
				panel, err := openPanel()
				if err == nil {
					_, err = panel.CloneToTunnels(in.TemplateID, []string{t.Node.HostName}, m.Tunnels())
				}
				if err != nil {
					m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "binding_failed", Reason: "新出口已连接，但创建节点链接失败，请在面板重试", Country: t.selectionPolicy().Country})
				}
				invalidateInbounds()
			}()
		}
		writeJSON(w, 200, map[string]any{"ok": true, "slot": t.Slot})
	}
}

var trialLimit = make(chan struct{}, 1)

func apiTrialIP(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeJSON(w, 405, map[string]string{"error": "使用 POST"})
			return
		}
		var in struct {
			Host string `json:"host"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
			writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
			return
		}
		select {
		case trialLimit <- struct{}{}:
		default:
			writeJSON(w, 429, map[string]string{"error": "已有节点正在试连"})
			return
		}
		nodes, _ := m.Nodes()
		var picked *Node
		for _, n := range nodes {
			if n.HostName == in.Host {
				v := n
				picked = &v
				break
			}
		}
		if picked == nil || (residentialOnly() && !picked.Residential) {
			<-trialLimit
			writeJSON(w, 404, map[string]string{"error": "节点不在可用目录中"})
			return
		}
		if m.nodeInUse(picked.HostName, 0) {
			<-trialLimit
			writeJSON(w, 409, map[string]string{"error": "此节点正在使用，请用该出口的检查按钮"})
			return
		}
		t, err := m.StartSelected(*picked, SelectionPolicy{Mode: "auto", ProbeOnly: true})
		if err != nil {
			<-trialLimit
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		go func() {
			defer func() { <-trialLimit }()
			m.waitUp(t)
			reason := "试连结束，未通过出口筛选"
			if t.Status == "up" {
				reason = "试连完成，已记录实际出口和 HTTPS 查询耗时"
			}
			m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "trial", Reason: reason, Country: t.selectionPolicy().Country, NewIP: t.ExitIP})
			m.mu.RLock()
			same := m.tunnels[t.Slot] == t
			m.mu.RUnlock()
			if same {
				_ = m.Stop(t.Slot)
			}
		}()
		writeJSON(w, 200, map[string]any{"slot": t.Slot, "ok": true, "temporary": true})
	}
}
