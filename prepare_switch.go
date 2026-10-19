package main

import (
	"time"
)

// Preflight in a spare slot while the working exit keeps serving clients.
// The verified candidate is revalidated during final handoff; failed probes
// never replace the working exit, and trial state is never restored at boot.
// Caller transfers ownership of t.opMu to this method.
func (m *Manager) prepareSwitch(t *Tunnel, chosen Node, policy SelectionPolicy, reason string) {
	oldHost, oldIP := t.Node.HostName, t.ExitIP
	m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "preparing", Reason: reason + "：先试连核验，当前出口继续使用", Country: policy.Country, OldIP: oldIP})
	go func() {
		transferred := false
		defer func() {
			if !transferred {
				t.opMu.Unlock()
			}
		}()
		shadow := &Tunnel{Slot: t.Slot, Node: chosen, Policy: policy, skipHostOnce: oldHost}
		candidates := m.policyCandidates(shadow)
		var fallback *Node
		for _, n := range candidates {
			if !m.tunnelActive(t) {
				return
			}
			if until := m.risk.queryWait(n.IP, m.quality.snapshot(n.HostName).Identity.IP); !until.IsZero() {
				m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "prepare_wait", Reason: "IPPure 查询冷却中，保留当前出口，稍后重试", Country: policy.Country, OldIP: oldIP})
				break
			}
			p := policy
			p.ProbeOnly = true
			p.RotateMinutes = 0
			probe, err := m.StartSelected(n, p)
			if err != nil {
				m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "prepare_wait", Reason: "候选不可用或没有空闲位置，保留当前出口", Country: policy.Country, OldIP: oldIP})
				break
			}
			deadline := time.Now().Add(55 * time.Second)
			for time.Now().Before(deadline) && probe.Status == "starting" && m.tunnelActive(t) {
				time.Sleep(time.Second)
			}
			up := probe.Status == "up"
			node, identity := probe.Node, probe.Identity
			same := policy.Region == "" && policy.RegionCode == "" || sameRegion(policy.Country, policy.RegionCode, policy.Region, identity.RegionCode, identity.Region)
			m.mu.RLock()
			owned := m.tunnels[probe.Slot] == probe
			m.mu.RUnlock()
			if owned {
				_ = m.Stop(probe.Slot)
			}
			if !m.tunnelActive(t) {
				return
			}
			if up && (same || policy.Mode == "strict") {
				t.setPolicy(policy)
				t.Node = node
				t.skipHostOnce = oldHost
				transferred = true
				m.launchReconnect(t, oldHost, oldIP, reason)
				return
			}
			if up && fallback == nil {
				copy := node
				fallback = &copy
			}
		}
		if fallback != nil && m.tunnelActive(t) {
			t.setPolicy(policy)
			t.Node = *fallback
			t.skipHostOnce = oldHost
			transferred = true
			m.launchReconnect(t, oldHost, oldIP, reason+"：本轮无同地区合格节点，使用同国备用")
			return
		}
		m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "prepare_failed", Reason: "本轮没有核验通过的候选，保留当前出口", Country: policy.Country, OldIP: oldIP})
	}()
}
