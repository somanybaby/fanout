package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type ProxycheckResult struct {
	IP          string    `json:"ip"`
	Score       *float64  `json:"score"`
	Anonymous   *bool     `json:"anonymous"`
	Proxy       *bool     `json:"proxy"`
	VPN         *bool     `json:"vpn"`
	Hosting     *bool     `json:"hosting"`
	Tor         *bool     `json:"tor"`
	Confidence  *float64  `json:"confidence"`
	NetworkType string    `json:"network_type"`
	Country     string    `json:"country"`
	Status      string    `json:"status"`
	CheckedAt   time.Time `json:"checked_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type proxycheckData struct {
	AdmissionEnabled bool                        `json:"admission_enabled"`
	ObservedExits    map[string]string           `json:"observed_exits"`
	Enabled          bool                        `json:"enabled"`
	Entries          map[string]ProxycheckResult `json:"entries"`
	Requests         []time.Time                 `json:"requests"`
	LastRequest      time.Time                   `json:"last_request"`
	BlockedUntil     time.Time                   `json:"blocked_until"`
	LastError        string                      `json:"last_error"`
}

type proxycheckStore struct {
	mu      sync.Mutex
	queryMu sync.Mutex
	data    proxycheckData
	path    string
	keyPath string
	key     string // Never serialized into status, candidates, events or logs.
	loadErr error
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	fetch   func(context.Context, string, string) ([]byte, error)
}

const proxycheckDailyLimit = 1000
const proxycheckCandidateLimit = 900 // Keep 100 requests for active exits.

func newProxycheckStore(dir string) *proxycheckStore {
	s := &proxycheckStore{data: proxycheckData{Entries: map[string]ProxycheckResult{}}, wake: make(chan struct{}, 1), fetch: fetchProxycheck}
	if dir != "" {
		s.path, s.keyPath = filepath.Join(dir, "proxycheck.json"), filepath.Join(dir, "proxycheck-key")
		b, err := os.ReadFile(s.path)
		if err == nil {
			s.loadErr = json.Unmarshal(b, &s.data)
		} else if !os.IsNotExist(err) {
			s.loadErr = err
		}
		b, err = os.ReadFile(s.keyPath)
		if err == nil {
			s.key = strings.TrimSpace(string(b))
		} else if !os.IsNotExist(err) {
			s.loadErr = err
		}
	}
	if s.data.Entries == nil {
		s.data.Entries = map[string]ProxycheckResult{}
	}
	// Older configurations must never restore ProxyCheck admission checks.
	s.data.AdmissionEnabled = false
	if s.data.ObservedExits == nil {
		s.data.ObservedExits = map[string]string{}
	}
	return s
}

func (s *proxycheckStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	return atomicPrivateJSON(s.path, s.data)
}

func (s *proxycheckStore) snapshot(ip string) ProxycheckResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Entries[ip]
}

func (s *proxycheckStore) pruneRequestsLocked(now time.Time) {
	kept := []time.Time{}
	for _, at := range s.data.Requests {
		if at.After(now.Add(-24 * time.Hour)) {
			kept = append(kept, at)
		}
	}
	s.data.Requests = kept
}

func (s *proxycheckStore) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRequestsLocked(time.Now())
	message := s.data.LastError
	if s.loadErr != nil {
		message = "ProxyCheck 配置或缓存不可读，查询已暂停"
	} else if s.key == "" {
		message = "待填写 Private API Key，尚未开始查询"
	}
	return map[string]any{"enabled": s.data.Enabled, "key_configured": s.key != "", "used_24h": len(s.data.Requests), "limit_24h": proxycheckDailyLimit, "blocked_until": s.data.BlockedUntil, "last_error": message, "scope": "changed_exits_only", "trigger": "on_ip_change", "hourly_queries": false, "observation_only": true, "admission_enabled": false}
}

var proxycheckKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,256}$`)

func (s *proxycheckStore) configure(enabled *bool, key *string, admission ...*bool) error {
	s.queryMu.Lock()
	defer s.queryMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("ProxyCheck 配置不可读，未覆盖现有文件")
	}
	if len(admission) > 0 && admission[0] != nil && *admission[0] {
		return fmt.Errorf("ProxyCheck 仅作参考，不参与接入、排序或换 IP 判断；请使用 IPPure")
	}
	if key != nil && strings.TrimSpace(*key) != "" {
		value := strings.TrimSpace(*key)
		if !proxycheckKeyPattern.MatchString(value) || strings.HasPrefix(strings.ToLower(value), "public-") {
			return fmt.Errorf("请填写有效的 Private API Key，不能使用 Public/CORS Key")
		}
		if s.keyPath != "" {
			f, err := os.CreateTemp(filepath.Dir(s.keyPath), ".proxycheck-key-")
			if err != nil {
				return fmt.Errorf("无法保存 API Key")
			}
			name := f.Name()
			defer os.Remove(name)
			if err = f.Chmod(0600); err == nil {
				_, err = f.WriteString(value + "\n")
			}
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil || closeErr != nil || os.Rename(name, s.keyPath) != nil {
				return fmt.Errorf("无法保存 API Key")
			}
		}
		s.key = value
		s.data.BlockedUntil, s.data.LastError = time.Time{}, ""
		s.data.ObservedExits = map[string]string{} // Explicit key replacement may recheck current exits once.
	}
	old, oldAdmission := s.data.Enabled, s.data.AdmissionEnabled
	s.data.AdmissionEnabled = false
	if enabled != nil {
		if *enabled && !s.data.Enabled {
			s.data.ObservedExits = map[string]string{}
		}
		s.data.Enabled = *enabled
	}
	if err := s.saveLocked(); err != nil {
		s.data.Enabled = old
		s.data.AdmissionEnabled = oldAdmission
		return fmt.Errorf("无法保存 ProxyCheck 设置")
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func fetchProxycheck(ctx context.Context, ip, key string) ([]byte, error) {
	address := "https://proxycheck.io/v3/" + ip + "?" + url.Values{"key": {key}, "ver": {"24-June-2026"}, "p": {"0"}, "tag": {"0"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("ProxyCheck 请求失败")
	}
	client := mainHTTPClient(12 * time.Second)
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ProxyCheck 暂不可用") // URL errors can contain the key.
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		return nil, &riskWaitError{Until: riskRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, &riskWaitError{Until: time.Now().Add(time.Hour)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ProxyCheck 查询未成功")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("ProxyCheck 响应读取失败")
	}
	return b, nil
}

func parseProxycheck(blob []byte, ip string, now time.Time) (ProxycheckResult, error) {
	r := ProxycheckResult{IP: ip, Status: "unknown", CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(blob, &envelope) != nil {
		return r, fmt.Errorf("ProxyCheck 响应无效")
	}
	var status string
	_ = json.Unmarshal(envelope["status"], &status)
	var raw struct {
		Score   *float64 `json:"risk"`
		Network struct {
			Type string `json:"type"`
		} `json:"network"`
		Location struct {
			Country string `json:"isocode"`
		} `json:"location"`
		Detections struct {
			Risk       *float64 `json:"risk"`
			Anonymous  *bool    `json:"anonymous"`
			Proxy      *bool    `json:"proxy"`
			VPN        *bool    `json:"vpn"`
			Hosting    *bool    `json:"hosting"`
			Tor        *bool    `json:"tor"`
			Confidence *float64 `json:"confidence"`
		} `json:"detections"`
	}
	if json.Unmarshal(envelope[ip], &raw) != nil {
		return r, fmt.Errorf("ProxyCheck 未返回查询 IP 的有效数据")
	}
	// The live v3 API nests risk in detections; some published schemas put it
	// at the IP's top level. Accept either location, reject conflicting scores.
	if raw.Score != nil && raw.Detections.Risk != nil && *raw.Score != *raw.Detections.Risk {
		return r, fmt.Errorf("ProxyCheck 返回的风险分不一致")
	}
	if raw.Score == nil {
		raw.Score = raw.Detections.Risk
	}
	if (status != "ok" && status != "warning") || raw.Score == nil || math.IsNaN(*raw.Score) || *raw.Score < 0 || *raw.Score > 100 || raw.Detections.Anonymous == nil || raw.Detections.Proxy == nil || raw.Detections.VPN == nil || raw.Detections.Hosting == nil || raw.Detections.Tor == nil {
		return r, fmt.Errorf("ProxyCheck 未返回与查询 IP 一致的有效评分和标记")
	}
	r.Score, r.NetworkType, r.Country = raw.Score, raw.Network.Type, raw.Location.Country
	r.Anonymous, r.Proxy, r.VPN, r.Hosting, r.Tor, r.Confidence = raw.Detections.Anonymous, raw.Detections.Proxy, raw.Detections.VPN, raw.Detections.Hosting, raw.Detections.Tor, raw.Detections.Confidence
	r.Status, r.ExpiresAt = "ok", now.Add(time.Hour)
	if *r.Anonymous || *r.Proxy || *r.VPN || *r.Tor {
		r.ExpiresAt = now.Add(15 * time.Minute)
	}
	return r, nil
}

func (s *proxycheckStore) assess(ctx context.Context, ip string, active bool, fresh ...bool) (ProxycheckResult, error) {
	s.queryMu.Lock()
	defer s.queryMu.Unlock()
	a := net.ParseIP(ip)
	if a == nil || a.To4() == nil || !a.IsGlobalUnicast() || a.IsPrivate() {
		return ProxycheckResult{}, fmt.Errorf("仅查询已核验的公网 IPv4")
	}
	s.mu.Lock()
	now := time.Now().UTC()
	s.pruneRequestsLocked(now)
	r := s.data.Entries[ip]
	if s.loadErr != nil || !s.data.Enabled || s.key == "" {
		s.mu.Unlock()
		return r, fmt.Errorf("ProxyCheck 未启用或尚未配置有效 Key")
	}
	// Scheduled reviews happen hourly even when an anonymous result expires sooner.
	requireFresh := len(fresh) > 0 && fresh[0]
	if (r.Status == "ok" && now.Before(r.CheckedAt.Add(time.Hour)) && (!requireFresh || now.Before(r.ExpiresAt))) || (r.Status != "ok" && now.Before(r.ExpiresAt)) {
		s.mu.Unlock()
		return r, nil
	}
	limit := proxycheckCandidateLimit
	if active {
		limit = proxycheckDailyLimit
	}
	if len(s.data.Requests) >= limit || now.Before(s.data.BlockedUntil) || now.Sub(s.data.LastRequest) < 10*time.Second {
		s.mu.Unlock()
		return r, fmt.Errorf("ProxyCheck 等待查询额度或冷却；原出口继续工作")
	}
	s.data.LastRequest = now
	s.data.Requests = append(s.data.Requests, now)
	key, fetch := s.key, s.fetch
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return r, fmt.Errorf("无法保存查询计数，暂不请求 ProxyCheck")
	}
	s.mu.Unlock()
	b, err := fetch(ctx, ip, key)
	r = ProxycheckResult{IP: ip, Status: "unknown", CheckedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err == nil {
		r, err = parseProxycheck(b, ip, now)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.data.LastError = "ProxyCheck 查询失败，稍后重试；可检查 Key、额度或网络"
		if wait, ok := err.(*riskWaitError); ok {
			s.data.BlockedUntil = wait.Until
		} else {
			s.data.BlockedUntil = now.Add(time.Minute)
		}
	} else {
		s.data.LastError = ""
	}
	s.data.Entries[ip] = r
	for k, v := range s.data.Entries {
		if now.Sub(v.CheckedAt) > 7*24*time.Hour {
			delete(s.data.Entries, k)
		}
	}
	_ = s.saveLocked()
	return r, err
}
