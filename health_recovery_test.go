package main

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestHealthRecoveryPreservesLiveTunnelWhenIPPureUnavailable(t *testing.T) {
	for _, mode := range []string{"auto", "strict"} {
		t.Run(mode, func(t *testing.T) {
			m, tn := selectionRiskFixture(t)
			tn.Policy.Mode = mode
			tn.egressReady.Store(true)
			tn.lastVerifiedIP = tn.ExitIP
			process := exec.Command("sleep", "30")
			if err := process.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
			tn.ovpn = process
			m.proxycheck.data.BlockedUntil = time.Now().Add(time.Hour)
			m.risk.data.LastRequest = time.Now()
			m.risk.data.Window = time.Now()
			m.proxycheck.fetch = func(_ context.Context, _, _ string) ([]byte, error) {
				t.Error("health recovery unexpectedly queried reference-only ProxyCheck")
				return nil, nil
			}
			beforeNode, beforeIP, beforeCred := tn.Node.HostName, tn.ExitIP, tn.Cred
			m.reconnect(tn, beforeNode)
			deadline := time.Now().Add(2 * time.Second)
			for !tn.opMu.TryLock() {
				if time.Now().After(deadline) {
					t.Fatal("recovery opened a candidate VPN despite IPPure cooldown")
				}
				time.Sleep(time.Millisecond)
			}
			tn.opMu.Unlock()
			if tn.Status != "up" || tn.ExitIP != beforeIP || tn.Node.HostName != beforeNode || tn.Cred != beforeCred || !tn.egressReady.Load() || tn.ovpn != process {
				t.Fatal("HTTP probe failure destroyed the current exit before replacement preflight passed")
			}
			if err := process.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatal("the current VPN process was killed")
			}
			if len(m.Tunnels()) != 1 {
				t.Fatal("cooldown spawned a futile candidate VPN")
			}
			attempt := tn.lastRecoveryAttempt
			m.reconnect(tn, beforeNode)
			if tn.lastRecoveryAttempt != attempt {
				t.Fatal("recovery retried before its one-minute backoff")
			}
		})
	}
}
