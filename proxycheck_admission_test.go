package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func admissionFixture(t *testing.T) (*Manager, *Tunnel) {
	t.Helper()
	m, tn := selectionFixture(t)
	webSettingsMu.Lock()
	old := webSettingsCur.RiskFilterEnabled
	webSettingsCur.RiskFilterEnabled = true
	webSettingsMu.Unlock()
	t.Cleanup(func() { webSettingsMu.Lock(); webSettingsCur.RiskFilterEnabled = old; webSettingsMu.Unlock() })
	configureProxycheckTest(t, m.proxycheck)
	enabled := true
	if err := m.proxycheck.configure(nil, nil, &enabled); err != nil {
		t.Fatal(err)
	}
	return m, tn
}

func TestProxycheckAdmissionOrderAndFallbackBoundaries(t *testing.T) {
	for _, tc := range []struct {
		score    string
		low      bool
		wantPure bool
	}{{"0", false, true}, {"1", false, false}, {"1", true, true}, {"25", true, true}, {"26", true, false}, {"100", true, false}, {"null", true, false}} {
		t.Run(fmt.Sprintf("%s-low-%v", tc.score, tc.low), func(t *testing.T) {
			m, tn := admissionFixture(t)
			tn.allowLowProxycheck = tc.low
			order := []string{}
			m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
				order = append(order, "pc")
				return proxycheckFixtureJSON(tn.ExitIP, tc.score), nil
			}
			m.risk.fetch = func(*Tunnel) ([]byte, error) {
				order = append(order, "ippure")
				return []byte(`{"ip":"1.1.1.1","fraudScore":4,"countryCode":"JP"}`), nil
			}
			err := m.verifyCandidateRisks(tn, tn, tn.ExitIP, "JP")
			want := []string{"pc"}
			if tc.wantPure {
				want = append(want, "ippure")
			}
			if !reflect.DeepEqual(order, want) || (err == nil) != tc.wantPure {
				t.Fatalf("wrong sequence or gate: %v %v", order, err)
			}
		})
	}
}

func TestProxycheckAdmissionDefersLowWithoutSpendingIPPure(t *testing.T) {
	m, tn := admissionFixture(t)
	pcCalls, pureCalls := 0, 0
	m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
		pcCalls++
		return proxycheckFixtureJSON(tn.ExitIP, "10"), nil
	}
	m.risk.fetch = func(*Tunnel) ([]byte, error) {
		pureCalls++
		return []byte(`{"ip":"1.1.1.1","fraudScore":4,"countryCode":"JP"}`), nil
	}
	if m.verifyCandidateRisks(tn, tn, tn.ExitIP, "JP") == nil || pureCalls != 0 {
		t.Fatal("low score was admitted during zero-only phase")
	}
	tn.allowLowProxycheck = true
	if m.verifyCandidateRisks(tn, tn, tn.ExitIP, "JP") != nil || pcCalls != 1 || pureCalls != 1 {
		t.Fatal("low fallback did not reuse fresh PC result then query IPPure")
	}
}

func TestProxycheckAdmissionStillRequiresIPPureAtMostTwenty(t *testing.T) {
	for _, score := range []float64{10, 20, 20.01} {
		t.Run(fmt.Sprint(score), func(t *testing.T) {
			m, tn := admissionFixture(t)
			m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
				return proxycheckFixtureJSON(tn.ExitIP, "0"), nil
			}
			m.risk.fetch = func(*Tunnel) ([]byte, error) {
				return []byte(fmt.Sprintf(`{"ip":"1.1.1.1","fraudScore":%v,"countryCode":"JP"}`, score)), nil
			}
			if accepted := m.verifyCandidateRisks(tn, tn, tn.ExitIP, "JP") == nil; accepted != (score <= 20) {
				t.Fatal("ProxyCheck zero must still respect the inclusive IPPure limit of 20")
			}
		})
	}
}

func TestProxycheckAdmissionVerifiedHandoffRechecksLowScore(t *testing.T) {
	m, tn := admissionFixture(t)
	saved := tn.Node
	tn.restoreFirst = &saved
	tn.handoffVerified = true
	m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
		return proxycheckFixtureJSON(tn.ExitIP, "10"), nil
	}
	if m.verifyProxycheckAdmission(tn, tn.ExitIP) != nil {
		t.Fatal("already-preflighted low fallback was deferred again during handoff")
	}
	tn.Node = m.nodes[2]
	if m.verifyProxycheckAdmission(tn, tn.ExitIP) == nil {
		t.Fatal("handoff permission leaked to a different VPN host")
	}
}

func TestProxycheckAdmissionResumesOnlyExactSavedIP(t *testing.T) {
	m, tn := admissionFixture(t)
	saved := tn.Node
	tn.restoreFirst = &saved
	tn.resumeSavedExit = true
	tn.lastVerifiedIP = tn.ExitIP
	m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) {
		return proxycheckFixtureJSON("1.1.1.1", "100"), nil
	}
	if err := m.verifyProxycheckAdmission(tn, tn.ExitIP); err != nil {
		t.Fatal("saved working exit was blocked during upgrade")
	}
	tn.resumeSavedExit = false
	if m.verifyProxycheckAdmission(tn, tn.ExitIP) == nil {
		t.Fatal("handoff incorrectly inherited restart exception")
	}
	tn.resumeSavedExit = true
	if m.verifyProxycheckAdmission(tn, "2.2.2.2") == nil {
		t.Fatal("same VPN host with a new IP bypassed admission")
	}
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	blob, _ := os.ReadFile(statePath(m.workDir))
	var state persistedState
	_ = json.Unmarshal(blob, &state)
	if len(state.Tunnels) != 1 || state.Tunnels[0].LastExitIP != tn.ExitIP {
		t.Fatal("last verified exit IP was not persisted")
	}
}

func TestProxycheckAdmissionZeroOtherRegionBeforeTokyoLow(t *testing.T) {
	m, tn := admissionFixture(t)
	for _, v := range []struct {
		index               int
		region, code, score string
	}{{0, "Tokyo", "13", "100"}, {1, "Tokyo", "13", "10"}, {2, "Osaka", "27", "0"}} {
		n := m.nodes[v.index]
		m.quality.identify(n.HostName, ExitIdentity{IP: n.IP, Country: "JP", Region: v.region, RegionCode: v.code, CheckedAt: time.Now()})
		r, _ := parseProxycheck(proxycheckFixtureJSON(n.IP, v.score), n.IP, time.Now())
		m.proxycheck.data.Entries[n.IP] = r
	}
	c := m.policyCandidates(tn)
	if len(c) != 2 || c[0].HostName != "osaka" || c[1].HostName != "tokyo" {
		t.Fatalf("zero-first ranking or high-score exclusion failed: %+v", c)
	}
}

func TestProxycheckAdmissionExpiredZeroAndQueryErrorNeverPass(t *testing.T) {
	m, tn := admissionFixture(t)
	r, _ := parseProxycheck(proxycheckFixtureJSON(tn.ExitIP, "0"), tn.ExitIP, time.Now().Add(-20*time.Minute))
	m.proxycheck.data.Entries[tn.ExitIP] = r
	m.proxycheck.fetch = func(context.Context, string, string) ([]byte, error) { return nil, fmt.Errorf("unavailable") }
	m.risk.fetch = func(*Tunnel) ([]byte, error) {
		t.Fatal("IPPure queried after stale or failed ProxyCheck result")
		return nil, nil
	}
	if m.verifyCandidateRisks(tn, tn, tn.ExitIP, "JP") == nil {
		t.Fatal("expired zero treated as fresh")
	}
}

func TestProxycheckAdmissionRejectedPoolKeepsServing(t *testing.T) {
	m, tn := admissionFixture(t)
	tn.egressReady.Store(true)
	for _, n := range m.nodes {
		m.quality.identify(n.HostName, ExitIdentity{IP: n.IP, Country: n.CountryCode})
		r, _ := parseProxycheck(proxycheckFixtureJSON(n.IP, "100"), n.IP, time.Now())
		m.proxycheck.data.Entries[n.IP] = r
	}
	tn.opMu.Lock()
	m.prepareSwitch(tn, m.nodes[1], tn.selectionPolicy(), "test")
	deadline := time.Now().Add(2 * time.Second)
	finished := false
	for time.Now().Before(deadline) {
		if tn.opMu.TryLock() {
			tn.opMu.Unlock()
			finished = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !finished || !tn.egressReady.Load() || tn.Status != "up" || tn.ExitIP != "1.1.1.1" {
		t.Fatal("rejected replacements interrupted the working exit")
	}
}
