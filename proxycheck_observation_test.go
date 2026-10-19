package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func selectionRiskFixture(t *testing.T) (*Manager, *Tunnel) {
	t.Helper()
	m, tn := selectionFixture(t)
	webSettingsMu.Lock()
	old := webSettingsCur.RiskFilterEnabled
	webSettingsCur.RiskFilterEnabled = true
	webSettingsMu.Unlock()
	t.Cleanup(func() { webSettingsMu.Lock(); webSettingsCur.RiskFilterEnabled = old; webSettingsMu.Unlock() })
	configureProxycheckTest(t, m.proxycheck)
	return m, tn
}

func TestProxycheckReferenceCannotBlockIPPureAdmission(t *testing.T) {
	for _, score := range []float64{20, 20.01} {
		m, tn := selectionRiskFixture(t)
		m.proxycheck.data.BlockedUntil = time.Now().Add(time.Hour)
		r, _ := parseProxycheck(proxycheckFixtureJSON(tn.ExitIP, "100"), tn.ExitIP, time.Now())
		m.proxycheck.data.Entries[tn.ExitIP] = r
		m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
			t.Fatal("admission queried ProxyCheck")
			return nil, nil
		}
		m.risk.fetch = func(*Tunnel) ([]byte, error) {
			return []byte(fmt.Sprintf(`{"ip":"1.1.1.1","fraudScore":%v,"countryCode":"JP"}`, score)), nil
		}
		if admitted := m.verifyRisk(tn, tn, tn.ExitIP, "JP") == nil; admitted != (score <= 20) {
			t.Fatal("ProxyCheck score or availability changed IPPure admission")
		}
	}
}

func TestProxycheckReferenceDoesNotRankOrExcludeCandidates(t *testing.T) {
	m, tn := selectionRiskFixture(t)
	for _, v := range []struct {
		index               int
		region, code, score string
	}{{0, "Tokyo", "13", "100"}, {1, "Tokyo", "13", "100"}, {2, "Osaka", "27", "0"}} {
		n := m.nodes[v.index]
		m.quality.identify(n.HostName, ExitIdentity{IP: n.IP, Country: "JP", Region: v.region, RegionCode: v.code, CheckedAt: time.Now()})
		r, _ := parseProxycheck(proxycheckFixtureJSON(n.IP, v.score), n.IP, time.Now())
		m.proxycheck.data.Entries[n.IP] = r
	}
	positions := map[string]int{}
	for i, n := range m.policyCandidates(tn) {
		positions[n.HostName] = i
	}
	if _, ok := positions["current"]; !ok {
		t.Fatal("reference score excluded the current candidate")
	}
	if tokyo, ok := positions["tokyo"]; !ok || tokyo >= positions["osaka"] {
		t.Fatal("ProxyCheck zero displaced the same-region preference")
	}
}

func TestProxycheckOnlyQueriesChangedIPAndPersistsObservation(t *testing.T) {
	m, tn := selectionRiskFixture(t)
	calls := 0
	m.proxycheck.fetch = func(_ context.Context, ip, _ string) ([]byte, error) {
		calls++
		return proxycheckFixtureJSON(ip, "100"), nil
	}
	m.proxycheckSweep(context.Background())
	if calls != 1 {
		t.Fatal("initial active exit was not observed once")
	}
	m.proxycheck.data.LastRequest = time.Now().Add(-2 * time.Hour)
	r := m.proxycheck.data.Entries[tn.ExitIP]
	r.CheckedAt = time.Now().Add(-2 * time.Hour)
	r.ExpiresAt = time.Now().Add(-time.Hour)
	m.proxycheck.data.Entries[tn.ExitIP] = r
	for i := 0; i < 5; i++ {
		m.proxycheckSweep(context.Background())
	}
	if calls != 1 {
		t.Fatal("unchanged exit or expired reference caused periodic queries")
	}
	m.proxycheck.mu.Lock()
	err := m.proxycheck.saveLocked()
	m.proxycheck.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	loaded := NewManager(4, m.workDir)
	loaded.tunnels[1] = tn
	loaded.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
		t.Fatal("restart queried an unchanged IP")
		return nil, nil
	}
	loaded.proxycheckSweep(context.Background())
	tn.ExitIP = "2.2.2.2"
	m.proxycheckSweep(context.Background())
	if calls != 2 {
		t.Fatal("actual IP change did not trigger exactly one observation")
	}
	if err := m.proxycheck.configure(nil, nil, boolPtr(true)); err == nil {
		t.Fatal("legacy client re-enabled ProxyCheck admission")
	}
	s := m.proxycheck.status()
	if s["admission_enabled"] != false || s["observation_only"] != true || s["hourly_queries"] != false || s["trigger"] != "on_ip_change" {
		t.Fatal("reference-only policy was misreported")
	}
}

func TestProxycheckFailedObservationDoesNotRetryPeriodically(t *testing.T) {
	m, _ := selectionRiskFixture(t)
	calls := 0
	m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) { calls++; return nil, fmt.Errorf("unavailable") }
	m.proxycheckSweep(context.Background())
	m.proxycheck.data.BlockedUntil = time.Time{}
	m.proxycheck.data.LastRequest = time.Now().Add(-2 * time.Hour)
	r := m.proxycheck.data.Entries["1.1.1.1"]
	r.ExpiresAt = time.Now().Add(-time.Hour)
	m.proxycheck.data.Entries["1.1.1.1"] = r
	for i := 0; i < 3; i++ {
		m.proxycheckSweep(context.Background())
	}
	if calls != 1 {
		t.Fatal("failed reference query retried without an IP change")
	}
}
