package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const riskScoreLimit = 10
const riskCacheTTL = 30 * time.Minute

type RiskAssessment struct {
	IP         string    `json:"ip"`
	Score      *float64  `json:"score"`
	Country    string    `json:"country"`
	Region     string    `json:"region"`
	RegionCode string    `json:"region_code"`
	City       string    `json:"city"`
	Status     string    `json:"status"`
	CheckedAt  time.Time `json:"checked_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type riskData struct {
	BlockedUntil time.Time                 `json:"blocked_until"`
	Entries      map[string]RiskAssessment `json:"entries"`
	LastRequest  time.Time                 `json:"last_request"`
	Window       time.Time                 `json:"window"`
	Requests     int                       `json:"requests"`
}
type riskStore struct {
	mu      sync.Mutex
	data    riskData
	path    string
	loadErr error
	fetch   func(*Tunnel) ([]byte, error)
}
type riskWaitError struct{ Until time.Time }

func (e *riskWaitError) Error() string { return "IPPure 查询限频中，保持阻断，稍后重试" }
func riskFilterEnabled() bool          { return getWebSettings().RiskFilterEnabled }
func newRiskStore(dir string) *riskStore {
	s := &riskStore{data: riskData{Entries: map[string]RiskAssessment{}}}
	if dir != "" {
		s.path = filepath.Join(dir, "risk-cache.json")
		b, e := os.ReadFile(s.path)
		if e == nil {
			s.loadErr = json.Unmarshal(b, &s.data)
		} else if !os.IsNotExist(e) {
			s.loadErr = e
		}
	}
	if s.data.Entries == nil {
		s.data.Entries = map[string]RiskAssessment{}
	}
	s.fetch = func(t *Tunnel) ([]byte, error) {
		client := proxyHTTPClient(t.Port, t.credential(), healthTimeout)
		resp, err := client.Get("https://my.ippure.com/v1/info")
		if err != nil {
			return nil, fmt.Errorf("IPPure 暂不可用")
		}
		defer resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			return nil, &riskWaitError{Until: riskRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("IPPure 暂不可用或被限频")
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	return s
}
func parseRisk(blob []byte, expected string, now time.Time) (RiskAssessment, error) {
	var raw struct {
		IP         string   `json:"ip"`
		Score      *float64 `json:"fraudScore"`
		Country    string   `json:"countryCode"`
		Region     string   `json:"region"`
		RegionCode string   `json:"regionCode"`
		City       string   `json:"city"`
	}
	r := RiskAssessment{IP: expected, Status: "unknown", CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if json.Unmarshal(blob, &raw) != nil || raw.IP != expected || raw.Score == nil || math.IsNaN(*raw.Score) || *raw.Score < 0 || *raw.Score > 100 || len(raw.Country) != 2 {
		return r, fmt.Errorf("IPPure 未返回与实际出口一致的有效风险分")
	}
	r.Score = raw.Score
	r.Country = raw.Country
	r.Region = raw.Region
	r.RegionCode = raw.RegionCode
	r.City = raw.City
	r.ExpiresAt = now.Add(riskCacheTTL)
	r.Status = "reject"
	if *raw.Score < riskScoreLimit {
		r.Status = "pass"
	}
	return r, nil
}
func (s *riskStore) snapshot(ip string) RiskAssessment {
	if s == nil {
		return RiskAssessment{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Entries[ip]
}
func (s *riskStore) denied(ip string) bool {
	if !riskFilterEnabled() || s == nil || ip == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data.Entries[ip]
	return s.loadErr != nil || (ok && time.Now().Before(v.ExpiresAt) && v.Status != "pass")
}
func (s *riskStore) assess(t *Tunnel, ip string) (RiskAssessment, error) {
	s.mu.Lock()
	now := time.Now().UTC()
	if s.loadErr != nil {
		s.mu.Unlock()
		return RiskAssessment{}, fmt.Errorf("IPPure 缓存损坏，停止接入候选")
	}
	if r, ok := s.data.Entries[ip]; ok && now.Before(r.ExpiresAt) {
		s.mu.Unlock()
		if r.Status == "unknown" {
			return r, fmt.Errorf("IPPure 暂无有效评分，稍后重试")
		}
		return r, nil
	}
	if now.Sub(s.data.Window) >= time.Hour {
		s.data.Window = now
		s.data.Requests = 0
	}
	until := s.data.LastRequest.Add(time.Minute)
	if s.data.Requests >= 12 {
		until = s.data.Window.Add(time.Hour)
	}
	if s.data.BlockedUntil.After(until) {
		until = s.data.BlockedUntil
	}
	if now.Before(until) {
		s.mu.Unlock()
		return RiskAssessment{}, &riskWaitError{Until: until}
	}
	s.data.LastRequest = now
	s.data.Requests++
	if s.path != "" {
		if err := atomicPrivateJSON(s.path, s.data); err != nil {
			s.mu.Unlock()
			return RiskAssessment{}, err
		}
	}
	fetch := s.fetch
	s.mu.Unlock()
	b, err := fetch(t)
	var r RiskAssessment
	if err == nil {
		r, err = parseRisk(b, ip, now)
	} else {
		r = RiskAssessment{IP: ip, Status: "unknown", CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	}
	s.mu.Lock()
	if wait, ok := err.(*riskWaitError); ok {
		s.data.BlockedUntil = wait.Until
	}
	for key, v := range s.data.Entries {
		if now.Sub(v.CheckedAt) > 24*time.Hour {
			delete(s.data.Entries, key)
		}
	}
	s.data.Entries[ip] = r
	if s.path != "" {
		_ = atomicPrivateJSON(s.path, s.data)
	}
	s.mu.Unlock()
	return r, err
}

func riskRetryAfter(raw string, now time.Time) time.Time {
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if at, err := http.ParseTime(raw); err == nil && at.After(now) {
		return at
	}
	return now.Add(10 * time.Minute)
}
func (m *Manager) verifyRisk(t, probe *Tunnel, ip, country string) error {
	if !riskFilterEnabled() {
		return nil
	}
	r, err := m.risk.assess(probe, ip)
	if err != nil {
		return err
	}
	if r.Status != "pass" || r.Score == nil || *r.Score >= riskScoreLimit {
		return fmt.Errorf("IPPure 风险分不低于 10，拒绝接入")
	}
	if r.Country != country {
		return fmt.Errorf("IPPure 与出口国家核验结果不一致，拒绝接入")
	}
	t.mu.Lock()
	t.Risk = r
	t.mu.Unlock()
	return nil
}
func apiRiskSettings(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var in struct {
				Enabled bool `json:"enabled"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in) != nil {
				writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
			webSettingsMu.Lock()
			old := webSettingsCur.RiskFilterEnabled
			webSettingsCur.RiskFilterEnabled = in.Enabled
			webSettingsMu.Unlock()
			if err := saveWebSettings(); err != nil {
				webSettingsMu.Lock()
				webSettingsCur.RiskFilterEnabled = old
				webSettingsMu.Unlock()
				writeJSON(w, 500, map[string]string{"error": "无法保存风险筛选设置"})
				return
			}
		} else if r.Method != "GET" {
			writeJSON(w, 405, map[string]string{"error": "使用 GET 或 POST"})
			return
		}
		writeJSON(w, 200, map[string]any{"enabled": riskFilterEnabled(), "score_below": riskScoreLimit, "cache_minutes": 30, "requests_per_hour": 12, "notice": "仅筛选新连接；现有出口不立即切换。查询失败不放行。不保证服务解锁。"})
	}
}
