package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerificationHourlyDoesNotOverlapAndKeepsCountries(t *testing.T) {
	m, tn := selectionFixture(t)
	tn.egressReady.Store(true)
	if err := m.selection.setMark("3.3.3.3", IPMark{Blocked: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := m.verification.configureHourly(true, now); err != nil {
		t.Fatal(err)
	}
	started, err := m.enqueueVerification(now, true)
	if !started || err != nil {
		t.Fatalf("hourly start: %v %v", started, err)
	}
	first := m.verification.snapshot()
	if first.Total != 3 || first.Queue[2] != "foreign" || first.NextRun.Sub(now) != time.Hour {
		t.Fatalf("priority, exclusion or country lost: %+v", first)
	}
	if started, err := m.enqueueVerification(now.Add(2*time.Hour), true); started || err != nil {
		t.Fatal("hourly round overlapped")
	}
	if started, err := m.enqueueVerification(now, false); started || err != nil {
		t.Fatal("manual click duplicated an active queue")
	}
	if len(m.verification.snapshot().Queue) != 3 || tn.ExitIP != "1.1.1.1" || !tn.egressReady.Load() {
		t.Fatal("queue or working exit changed")
	}
	loaded := newVerificationStore(m.workDir).snapshot()
	if !loaded.AutoHourly || len(loaded.Queue) != 3 {
		t.Fatal("hourly preference or pending work did not persist")
	}
}

func TestVerificationResultsAndAPINeverExposeOrSelectCredentials(t *testing.T) {
	m, tn := selectionFixture(t)
	_, _ = m.enqueueVerification(time.Now(), false)
	m.verification.probe = func(ctx context.Context, n Node) VerificationResult {
		return VerificationResult{Status: "pass", IP: n.IP, Detail: "连接已核验，风险待查询"}
	}
	m.verificationStep(context.Background(), time.Now())
	s := m.verification.snapshot()
	if s.Completed != 1 || s.Passed != 1 || s.Results["current"].Status != "pass" {
		t.Fatalf("missing results: %+v", s)
	}
	for _, handler := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			apiVerification(m)(w, httptest.NewRequest("GET", "/api/ip/verification", nil))
		},
		func(w *httptest.ResponseRecorder) {
			apiCandidates(m)(w, httptest.NewRequest("GET", "/api/candidates", nil))
		},
	} {
		w := httptest.NewRecorder()
		handler(w)
		if w.Code != 200 || strings.Contains(w.Body.String(), "never-export") || strings.Contains(w.Body.String(), "remote 1.1.1.1") {
			t.Fatal("verification API exposed private connection data")
		}
	}
	if tn.ExitIP != "1.1.1.1" || tn.selectionPolicy().Mode != "auto" {
		t.Fatal("verification selected a new exit")
	}
	info, _ := os.Stat(filepath.Join(m.workDir, "verification.json"))
	if info == nil || info.Mode().Perm() != 0600 {
		t.Fatal("verification persistence must be private")
	}
}

func TestVerificationCancelCannotOverwriteNextRound(t *testing.T) {
	m, _ := selectionFixture(t)
	_, _ = m.enqueueVerification(time.Now(), false)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	m.verification.probe = func(ctx context.Context, n Node) VerificationResult {
		close(entered)
		<-ctx.Done()
		<-release
		return VerificationResult{Status: "failed", Detail: "obsolete result"}
	}
	go func() { defer close(done); m.verificationStep(context.Background(), time.Now()) }()
	<-entered
	if err := m.verification.cancel(); err != nil {
		t.Fatal(err)
	}
	if started, err := m.enqueueVerification(time.Now(), false); !started || err != nil {
		t.Fatal("could not queue next round")
	}
	close(release)
	<-done
	s := m.verification.snapshot()
	if s.Completed != 0 || s.Total != 4 || len(s.Queue) != 4 {
		t.Fatal("cancelled round overwrote a new job")
	}
}

func TestVerificationLeasesReserveRecoveryAndNeverCreateClientExit(t *testing.T) {
	m, current := selectionFixture(t)
	probe, why := m.reserveVerification(m.nodes[1])
	if probe == nil || why != "" || len(m.Tunnels()) != 1 || probe.listener != nil || probe.egressReady.Load() {
		t.Fatal("batch probe became a client exit")
	}
	if slot, _ := m.freeSlot(); slot == probe.Slot {
		t.Fatal("reserved namespace could be reused")
	}
	if _, err := m.StartSelected(m.nodes[1], SelectionPolicy{}); err == nil {
		t.Fatal("manual selection raced with the isolated probe")
	}
	m.mu.Lock()
	delete(m.probes, probe.Slot) // No real namespace was created in this test.
	m.mu.Unlock()
	m.maxSlots = 2
	if p, why := m.reserveVerification(m.nodes[1]); p != nil || why != "capacity" {
		t.Fatal("batch probe consumed the last recovery slot")
	}
	m.risk.fetch = func(*Tunnel) ([]byte, error) { t.Fatal("batch must not query IPPure"); return nil, nil }
	r := m.probeVerification(context.Background(), current.Node)
	if r.Status != "skipped" || len(m.Tunnels()) != 1 || len(m.probes) != 0 {
		t.Fatal("working exit was reconnected instead of skipped")
	}
}

func TestVerificationInterruptedWorkAndBadState(t *testing.T) {
	dir := t.TempDir()
	state := verificationData{AutoHourly: true, Current: "interrupted", Queue: []string{"pending"}}
	b, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "verification.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	s := newVerificationStore(dir).snapshot()
	if s.Current != "" || len(s.Queue) != 2 || s.Queue[0] != "interrupted" {
		t.Fatal("restart lost interrupted work")
	}
	_ = os.WriteFile(filepath.Join(dir, "verification.json"), []byte("broken"), 0600)
	bad := newVerificationStore(dir)
	if bad.snapshot().AutoHourly || bad.configureHourly(true, time.Now()) == nil {
		t.Fatal("corrupt queue was silently overwritten")
	}
}
