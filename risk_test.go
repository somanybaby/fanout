package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestRiskScoreBoundaryAndMissingValues(t *testing.T) {
	for _, tc := range []struct {
		score  string
		status string
		valid  bool
	}{{"0", "pass", true}, {"9.99", "pass", true}, {"10", "reject", true}, {"99", "reject", true}, {"null", "unknown", false}, {"-1", "unknown", false}, {"101", "unknown", false}} {
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
