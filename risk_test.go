package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRiskScoreBoundaryAndMissingValues(t *testing.T) {
	for _, tc := range []struct {
		score  string
		status string
		valid  bool
	}{{"0", "pass", true}, {"9.99", "pass", true}, {"10", "pass", true}, {"19.99", "pass", true}, {"20", "pass", true}, {"20.01", "reject", true}, {"99", "reject", true}, {"null", "unknown", false}, {"-1", "unknown", false}, {"101", "unknown", false}} {
		r, err := parseRisk([]byte(fmt.Sprintf(`{"ip":"1.2.3.4","fraudScore":%s,"countryCode":"JP"}`, tc.score)), "1.2.3.4", time.Now())
		if (err == nil) != tc.valid || r.Status != tc.status {
			t.Fatalf("score %s: %+v %v", tc.score, r, err)
		}
	}
	for _, blob := range []string{`{"ip":"1.2.3.4","countryCode":"JP"}`, `{"ip":"9.9.9.9","fraudScore":0,"countryCode":"JP"}`, `{"ip":"1.2.3.4","fraudScore":0}`} {
		if _, err := parseRisk([]byte(blob), "1.2.3.4", time.Now()); err == nil {
			t.Fatal("missing/mismatched response accepted")
		}
	}
}

func TestRiskRunningThresholdAndHourlyReview(t *testing.T) {
	webSettingsMu.Lock()
	old := webSettingsCur.RiskFilterEnabled
	webSettingsCur.RiskFilterEnabled = true
	webSettingsMu.Unlock()
	t.Cleanup(func() {
		webSettingsMu.Lock()
		webSettingsCur.RiskFilterEnabled = old
		webSettingsMu.Unlock()
	})
	for _, score := range []float64{4, 10, 19, 20, 20.01, 99} {
		t.Run(fmt.Sprint(score), func(t *testing.T) {
			m, tn := selectionFixture(t)
			tn.egressReady.Store(true)
			calls := 0
			m.risk.fetch = func(*Tunnel) ([]byte, error) {
				calls++
				return []byte(fmt.Sprintf(`{"ip":"1.1.1.1","fraudScore":%v,"countryCode":"JP"}`, score)), nil
			}
			tn.opMu.Lock()
			m.checkRunningRisk(tn)
			tn.opMu.Unlock()
			if tn.riskMustRotate.Load() != (score > 20) || !tn.egressReady.Load() || tn.Status != "up" {
				t.Fatal("running threshold blocked or misclassified an existing working exit")
			}
			m.maybeCheckRisk(tn)
			if calls != 1 || time.Until(tn.nextRiskCheck) < 59*time.Minute {
				t.Fatal("hourly schedule repeated a fresh lookup")
			}
			if accepted := m.verifyRisk(tn, tn, "1.1.1.1", "JP") == nil; accepted != (score <= 20) {
				t.Fatal("new-candidate admission must include 20 and reject higher scores")
			}
		})
	}
	if runningRiskHigh(RiskAssessment{}) || !validRotation(1440) || !validRotation(2880) {
		t.Fatal("unknown score or daily rotation policy incorrect")
	}
	m, tn := selectionFixture(t)
	tn.egressReady.Store(true)
	tn.Risk = RiskAssessment{Score: func() *float64 { v := 4.0; return &v }(), CheckedAt: time.Now().Add(-time.Hour)}
	m.risk.fetch = func(*Tunnel) ([]byte, error) { return nil, fmt.Errorf("provider unavailable") }
	tn.opMu.Lock()
	m.checkRunningRisk(tn)
	tn.opMu.Unlock()
	if !tn.egressReady.Load() || tn.riskMustRotate.Load() || *tn.Risk.Score != 4 {
		t.Fatal("lookup failure changed the working exit or fabricated a score")
	}
}

func TestRiskCacheReclassifiesOldThresholdWithoutRefetch(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	entries := map[string]RiskAssessment{}
	for i, score := range []float64{10, 20, 20.01} {
		ip := fmt.Sprintf("1.2.3.%d", i+1)
		entries[ip] = RiskAssessment{IP: ip, Score: &score, Country: "JP", Status: "reject", CheckedAt: now, ExpiresAt: now.Add(riskCacheTTL)}
	}
	unknown := 10.0
	entries["1.2.3.4"] = RiskAssessment{IP: "1.2.3.4", Score: &unknown, Country: "JP", Status: "unknown", CheckedAt: now, ExpiresAt: now.Add(riskCacheTTL)}
	b, err := json.Marshal(riskData{Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "risk-cache.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	s := newRiskStore(dir)
	s.fetch = func(*Tunnel) ([]byte, error) {
		t.Fatal("fresh cache triggered a new provider request")
		return nil, nil
	}
	for ip, old := range entries {
		r, err := s.assess(&Tunnel{}, ip)
		if old.Status == "unknown" {
			if err == nil || r.Status != "unknown" {
				t.Fatal("unknown cache was promoted to a valid score")
			}
			continue
		}
		if err != nil || (r.Status == "pass") != (*old.Score <= 20) || !r.CheckedAt.Equal(now) || !r.ExpiresAt.Equal(old.ExpiresAt) {
			t.Fatalf("cached score was not reclassified without changing its age: %+v %v", r, err)
		}
	}
}
func TestRiskCacheAndGlobalRateLimit(t *testing.T) {
	s := newRiskStore(t.TempDir())
	calls := 0
	s.fetch = func(*Tunnel) ([]byte, error) {
		calls++
		return []byte(`{"ip":"1.2.3.4","fraudScore":4,"countryCode":"JP"}`), nil
	}
	if r, err := s.assess(&Tunnel{}, "1.2.3.4"); err != nil || r.Status != "pass" {
		t.Fatal(err)
	}
	if _, err := s.assess(&Tunnel{}, "1.2.3.4"); err != nil || calls != 1 {
		t.Fatal("cache bypassed")
	}
	if _, err := s.assess(&Tunnel{}, "5.6.7.8"); err == nil || calls != 1 {
		t.Fatal("global minimum request interval bypassed")
	}
	loaded := newRiskStore(s.path[:len(s.path)-len("risk-cache.json")])
	if loaded.snapshot("1.2.3.4").Status != "pass" {
		t.Fatal("risk cache not persisted")
	}
}
func TestRiskFailureNeverBecomesZeroScore(t *testing.T) {
	s := newRiskStore(t.TempDir())
	s.fetch = func(*Tunnel) ([]byte, error) { return nil, fmt.Errorf("offline") }
	r, err := s.assess(&Tunnel{}, "1.2.3.4")
	if err == nil || r.Status != "unknown" || r.Score != nil {
		t.Fatal("API failure accepted as score zero")
	}
	b, _ := json.Marshal(r)
	if string(b) == "" {
		t.Fatal("missing assessment")
	}
}

func TestRiskRespectsProviderRetryAfter(t *testing.T) {
	now := time.Now()
	if riskRetryAfter("600", now).Sub(now) != 10*time.Minute {
		t.Fatal("Retry-After seconds ignored")
	}
	s := newRiskStore(t.TempDir())
	calls := 0
	s.fetch = func(*Tunnel) ([]byte, error) { calls++; return nil, &riskWaitError{Until: now.Add(20 * time.Minute)} }
	_, _ = s.assess(&Tunnel{}, "1.2.3.4")
	s.data.LastRequest = now.Add(-2 * time.Minute)
	if _, err := s.assess(&Tunnel{}, "5.6.7.8"); err == nil || calls != 1 {
		t.Fatal("provider cooldown bypassed")
	}
}
