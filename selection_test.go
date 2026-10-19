package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func selectionFixture(t *testing.T) (*Manager, *Tunnel) {
	t.Helper()
	withResidentialOnly(t, boolPtr(true))
	m := NewManager(4, t.TempDir())
	m.nodes = []Node{{HostName: "current", IP: "1.1.1.1", CountryCode: "JP", Config: "remote 1.1.1.1 1194", Residential: true}, {HostName: "tokyo", IP: "2.2.2.2", CountryCode: "JP", Config: "remote 2.2.2.2 1194", Residential: true}, {HostName: "osaka", IP: "3.3.3.3", CountryCode: "JP", Config: "remote 3.3.3.3 1194", Residential: true}, {HostName: "foreign", IP: "4.4.4.4", CountryCode: "KR", Config: "remote 4.4.4.4 1194", Residential: true}}
	tn := &Tunnel{Slot: 1, Port: 32123, Node: m.nodes[0], Status: "up", ExitIP: "1.1.1.1", Policy: SelectionPolicy{Mode: "auto", Country: "JP", Region: "Tokyo", RegionCode: "13"}, Cred: SocksCred{User: "user-test", Pass: "never-export"}, Since: time.Now()}
	m.tunnels[1] = tn
	return m, tn
}
func TestSelectionStrictNeverFallsBackOrCrossesCountry(t *testing.T) {
	m, tn := selectionFixture(t)
	p, err := normalizePolicy(SelectionPolicy{Mode: "strict", IP: "2.2.2.2"}, m.nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	tn.setPolicy(p)
	got := m.candidatesFor(tn)
	if len(got) != 1 || got[0].HostName != "tokyo" {
		t.Fatalf("strict candidates: %+v", got)
	}
	tn.Node = m.nodes[1]
	if m.checkExitPolicy(tn, "9.9.9.9", "JP") == nil {
		t.Fatal("wrong actual IP accepted")
	}
	if m.checkExitPolicy(tn, "2.2.2.2", "KR") == nil {
		t.Fatal("cross-country exit accepted")
	}
	if m.checkExitPolicy(tn, "2.2.2.2", "JP") != nil {
		t.Fatal("correct locked exit rejected")
	}
	if err := m.selection.setMark("2.2.2.2", IPMark{Blocked: true}); err != nil {
		t.Fatal(err)
	}
	if len(m.candidatesFor(tn)) != 0 {
		t.Fatal("excluded strict target caused a fallback")
	}
}
func TestSelectionPreferredRetainsAnchorAndFallsBackSameCountry(t *testing.T) {
	m, tn := selectionFixture(t)
	p, err := normalizePolicy(SelectionPolicy{Mode: "preferred", IP: "2.2.2.2", Region: "Tokyo", RegionCode: "13"}, m.nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	tn.setPolicy(p)
	got := m.candidatesFor(tn)
	if got[0].HostName != "tokyo" {
		t.Fatal("preferred target lost")
	}
	for _, n := range got {
		if n.CountryCode != "JP" {
			t.Fatal("foreign fallback")
		}
	}
	tn.Node = m.nodes[2]
	if m.checkExitPolicy(tn, "3.3.3.3", "JP") != nil {
		t.Fatal("same-country fallback rejected")
	}
	m.rememberRegion(tn, ExitIdentity{Region: "Osaka", RegionCode: "27"})
	if tn.selectionPolicy().Region != "Tokyo" {
		t.Fatal("fallback replaced the original region anchor")
	}
	if m.checkRegionPreference(tn, ExitIdentity{Region: "Osaka", RegionCode: "27"}) == nil {
		t.Fatal("other region accepted in first phase")
	}
	tn.allowRegionFallback = true
	if m.checkRegionPreference(tn, ExitIdentity{Region: "Osaka", RegionCode: "27"}) != nil {
		t.Fatal("same-country second phase rejected")
	}
}
func TestSelectionKnownSameRegionAheadOfOtherRegions(t *testing.T) {
	m, tn := selectionFixture(t)
	tn.Node = m.nodes[2]
	m.quality.identify("tokyo", ExitIdentity{Region: "Tokyo", RegionCode: "13"})
	m.quality.identify("osaka", ExitIdentity{Region: "Osaka", RegionCode: "27"})
	got := m.candidatesFor(tn)
	if got[0].HostName != "tokyo" {
		t.Fatalf("known same region was not first: %+v", got)
	}
	if !sameRegion("JP", "JP-13", "Tokyo", "13", "Tokyo Prefecture") {
		t.Fatal("region code normalization failed")
	}
	if sameRegion("JP", "13", "Tokyo", "27", "Osaka") {
		t.Fatal("different prefectures matched")
	}
}
func TestSelectionPolicyOnlyEditPreservesClientAndDoesNotReconnect(t *testing.T) {
	m, tn := selectionFixture(t)
	before := tn.Cred
	if err := m.Select(1, "current", "strict", "1.1.1.1"); err != nil {
		t.Fatal(err)
	}
	if tn.Status != "up" || tn.Port != 32123 || tn.Cred != before {
		t.Fatal("policy-only edit changed working client")
	}
	if tn.selectionPolicy().Mode != "strict" || tn.selectionPolicy().RotateMinutes != 0 {
		t.Fatal("strict policy not retained")
	}
	if m.SetRotation(1, 60) == nil {
		t.Fatal("strict lock allowed periodic rotation")
	}
	if m.Select(1, "foreign", "preferred", "4.4.4.4") == nil {
		t.Fatal("manual existing exit crossed countries")
	}
	if tn.selectionPolicy().Mode != "strict" {
		t.Fatal("failed edit mutated prior policy")
	}
}
func TestSelectionPrivateStatePersistsWithoutAPICredentials(t *testing.T) {
	m, tn := selectionFixture(t)
	p, _ := normalizePolicy(SelectionPolicy{Mode: "preferred", IP: "2.2.2.2", RotateMinutes: 60, Region: "Tokyo"}, m.nodes[1])
	tn.setPolicy(p)
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(statePath(m.workDir))
	var st persistedState
	if json.Unmarshal(b, &st) != nil {
		t.Fatal("invalid state")
	}
	saved := st.Tunnels[0]
	if saved.Policy.Mode != "preferred" || saved.Policy.RotateMinutes != 60 || saved.PreferredConfig != m.nodes[1].Config || saved.SocksPass != tn.Cred.Pass {
		t.Fatal("policy/client snapshot lost")
	}
	request := httptest.NewRequest("GET", "/api/candidates", nil)
	w := httptest.NewRecorder()
	apiCandidates(m)(w, request)
	if strings.Contains(w.Body.String(), "never-export") || strings.Contains(w.Body.String(), "remote 1.1.1.1") {
		t.Fatal("candidate API exposed connection secrets")
	}
	tn.Policy.ProbeOnly = true
	if err := m.saveState(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(statePath(m.workDir))
	json.Unmarshal(b, &st)
	if len(st.Tunnels) != 0 {
		t.Fatal("trial tunnel restored at next boot")
	}
}
func TestSelectionMarksAndEventsSurviveReload(t *testing.T) {
	dir := t.TempDir()
	s := newSelectionStore(dir)
	if err := s.setMark("1.2.3.4", IPMark{Favorite: true, Blocked: true}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		s.addEvent(SelectionEvent{Slot: 1, Reason: "safe reason", Kind: "test"})
	}
	next := newSelectionStore(dir)
	if !next.mark("1.2.3.4").Blocked || !next.mark("1.2.3.4").Favorite || len(next.snapshot().Events) != 200 {
		t.Fatal("marks or bounded events lost")
	}
	if err := s.setMark("not-an-ip", IPMark{}); err == nil {
		t.Fatal("invalid mark accepted")
	}
	os.WriteFile(filepath.Join(dir, "selections.json"), []byte("{broken"), 0600)
	broken := newSelectionStore(dir)
	if broken.loadErr == nil || broken.setMark("1.2.3.4", IPMark{}) == nil {
		t.Fatal("corrupt exclusion file silently overwritten")
	}
}
func TestSelectionParallelStateWritesAreAtomic(t *testing.T) {
	m, _ := selectionFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- m.saveState() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(statePath(m.workDir))
	if !json.Valid(b) {
		t.Fatal("partial state committed")
	}
}
func TestSelectionRejectsActiveIPExclusion(t *testing.T) {
	m, _ := selectionFixture(t)
	r := httptest.NewRequest("POST", "/api/ip/marks", strings.NewReader(`{"ip":"1.1.1.1","blocked":true}`))
	w := httptest.NewRecorder()
	apiIPMarks(m)(w, r)
	if w.Code != 409 || m.selection.blocked("1.1.1.1") {
		t.Fatal("active IP excluded unexpectedly")
	}
}

func TestSelectionPreflightNoSpareSlotKeepsWorkingExit(t *testing.T) {
	m, tn := selectionFixture(t)
	m.maxSlots = 1
	tn.egressReady.Store(true)
	before := tn.Cred
	tn.opMu.Lock()
	m.prepareSwitch(tn, m.nodes[1], tn.selectionPolicy(), "定时轮换测试")
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
	if !finished {
		t.Fatal("preflight did not finish")
	}
	if tn.Status != "up" || tn.ExitIP != "1.1.1.1" || tn.Cred != before || !tn.egressReady.Load() {
		t.Fatal("failed preflight interrupted working exit")
	}
}
