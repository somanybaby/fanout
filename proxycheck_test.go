package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func proxycheckFixtureJSON(ip, score string) []byte {
	return []byte(fmt.Sprintf(`{"status":"ok","%s":{"risk":%s,"network":{"type":"Residential"},"location":{"isocode":"JP"},"detections":{"anonymous":true,"proxy":false,"vpn":true,"hosting":false,"tor":false,"confidence":95}}}`, ip, score))
}

func configureProxycheckTest(t *testing.T, s *proxycheckStore) string {
	t.Helper()
	enabled, key := true, "test-private-key-never-return-in-api"
	if err := s.configure(&enabled, &key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestProxycheckV3ParseAndIndependentScore(t *testing.T) {
	for _, score := range []string{"0", "10", "20", "50", "100"} {
		r, err := parseProxycheck(proxycheckFixtureJSON("1.1.1.1", score), "1.1.1.1", time.Now())
		if err != nil || r.Status != "ok" || r.Score == nil || r.VPN == nil || !*r.VPN || *r.Proxy || r.NetworkType != "Residential" {
			t.Fatalf("valid v3 result rejected or confused with IPPure threshold: %+v %v", r, err)
		}
	}
	for _, score := range []string{"null", "-1", "101"} {
		r, err := parseProxycheck(proxycheckFixtureJSON("1.1.1.1", score), "1.1.1.1", time.Now())
		if err == nil || r.Score != nil {
			t.Fatal("invalid score became a usable result")
		}
	}
	valid := string(proxycheckFixtureJSON("1.1.1.1", "4"))
	for _, blob := range []string{
		strings.Replace(valid, `"risk":4,`, "", 1),
		strings.Replace(valid, `"vpn":true,`, "", 1),
		strings.Replace(valid, `"status":"ok"`, `"status":"denied"`, 1),
		strings.Replace(valid, `"1.1.1.1":`, `"9.9.9.9":`, 1),
	} {
		if r, err := parseProxycheck([]byte(blob), "1.1.1.1", time.Now()); err == nil || r.Score != nil {
			t.Fatal("partial or wrong-IP result accepted")
		}
	}
}

func TestProxycheckCacheQuotaReserveAndPrivatePersistence(t *testing.T) {
	s := newProxycheckStore(t.TempDir())
	key := configureProxycheckTest(t, s)
	calls := 0
	s.fetch = func(ctx context.Context, ip, gotKey string) ([]byte, error) {
		calls++
		if gotKey != key {
			t.Fatal("wrong private key")
		}
		return proxycheckFixtureJSON(ip, "50"), nil
	}
	if _, err := s.assess(context.Background(), "1.1.1.1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.assess(context.Background(), "1.1.1.1", false); err != nil || calls != 1 {
		t.Fatal("hourly cache bypassed")
	}
	s.mu.Lock()
	s.data.Requests = make([]time.Time, proxycheckCandidateLimit)
	for i := range s.data.Requests {
		s.data.Requests[i] = time.Now()
	}
	s.data.LastRequest = time.Now().Add(-11 * time.Second)
	s.mu.Unlock()
	if _, err := s.assess(context.Background(), "2.2.2.2", false); err == nil || calls != 1 {
		t.Fatal("candidate used active-exit reserve")
	}
	if _, err := s.assess(context.Background(), "2.2.2.2", true); err != nil || calls != 2 {
		t.Fatal("active exit could not use its reserve")
	}
	loaded := newProxycheckStore(strings.TrimSuffix(s.path, "/proxycheck.json"))
	if loaded.key != key || loaded.snapshot("2.2.2.2").Score == nil {
		t.Fatal("key or result not restored")
	}
	for _, path := range []string{s.path, s.keyPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private state permissions")
		}
	}
	s.mu.Lock()
	for len(s.data.Requests) < proxycheckDailyLimit {
		s.data.Requests = append(s.data.Requests, time.Now())
	}
	s.data.LastRequest = time.Now().Add(-11 * time.Second)
	s.mu.Unlock()
	if _, err := s.assess(context.Background(), "3.3.3.3", true); err == nil || calls != 2 {
		t.Fatal("free daily cap exceeded")
	}
}

func TestProxycheckTargetsOnlyActualJapanAndActiveExits(t *testing.T) {
	m, current := selectionFixture(t)
	m.quality.identify("tokyo", ExitIdentity{IP: "9.9.9.9", Country: "JP", CheckedAt: time.Now()})
	m.quality.identify("osaka", ExitIdentity{IP: "8.8.8.8", Country: "US", CheckedAt: time.Now()})
	m.quality.identify("foreign", ExitIdentity{IP: "4.4.4.4", Country: "KR", CheckedAt: time.Now()})
	targets := m.proxycheckTargets()
	if len(targets) != 2 || !targets[0].active || targets[0].ip != current.ExitIP || targets[1].ip != "9.9.9.9" {
		t.Fatalf("queried advertised, foreign or wrong-country IP: %+v", targets)
	}
	configureProxycheckTest(t, m.proxycheck)
	current.egressReady.Store(true)
	m.proxycheck.fetch = func(ctx context.Context, ip, key string) ([]byte, error) {
		return proxycheckFixtureJSON(ip, "100"), nil
	}
	m.proxycheckSweep(context.Background())
	if current.ExitIP != "1.1.1.1" || !current.egressReady.Load() || current.Status != "up" || current.riskMustRotate.Load() {
		t.Fatal("observation score changed or blocked the working exit")
	}
}

func TestProxycheckMissingKeyFailureAndAPISecretRedaction(t *testing.T) {
	m, _ := selectionFixture(t)
	m.proxycheck.data.Enabled = true
	m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) { t.Fatal("query without key"); return nil, nil }
	m.proxycheckSweep(context.Background())
	key := configureProxycheckTest(t, m.proxycheck)
	m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
		return nil, fmt.Errorf("network error containing %s", key)
	}
	r, err := m.proxycheck.assess(context.Background(), "1.1.1.1", true)
	if err == nil || r.Score != nil || r.Status != "unknown" {
		t.Fatal("failed query became zero risk")
	}
	for _, path := range []string{"/api/ip/proxycheck", "/api/candidates"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		if path == "/api/candidates" {
			apiCandidates(m)(w, req)
		} else {
			apiProxycheck(m)(w, req)
		}
		if w.Code != 200 || strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "never-export") {
			t.Fatal("API exposed a secret")
		}
	}
	public := "public-123456-123456-123456"
	if m.proxycheck.configure(nil, &public) == nil {
		t.Fatal("public key accepted for server queries")
	}
	if m.proxycheck.key != key {
		t.Fatal("invalid replacement destroyed the existing key")
	}
}

func TestProxycheckHonorsProviderCooldownAndRollingBudget(t *testing.T) {
	s := newProxycheckStore(t.TempDir())
	configureProxycheckTest(t, s)
	until := time.Now().Add(20 * time.Minute)
	calls := 0
	s.fetch = func(context.Context, string, string) ([]byte, error) {
		calls++
		return nil, &riskWaitError{Until: until}
	}
	_, _ = s.assess(context.Background(), "1.1.1.1", true)
	s.mu.Lock()
	s.data.LastRequest = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if _, err := s.assess(context.Background(), "2.2.2.2", true); err == nil || calls != 1 {
		t.Fatal("provider Retry-After bypassed")
	}
	s.mu.Lock()
	s.data.Requests = []time.Time{time.Now().Add(-25 * time.Hour), time.Now()}
	s.mu.Unlock()
	if s.status()["used_24h"].(int) != 1 {
		t.Fatal("rolling 24h accounting incorrect")
	}
}
