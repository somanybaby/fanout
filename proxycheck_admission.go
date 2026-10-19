package main

import (
	"context"
	"fmt"
	"time"
)

type proxycheckLowScore struct{}

func (*proxycheckLowScore) Error() string {
	return "ProxyCheck 为 1～25 分，留作 0 分候选之后的备用"
}

type proxycheckAdmissionWait struct{ Until time.Time }

func (*proxycheckAdmissionWait) Error() string {
	return "等待有效的 ProxyCheck 评分或查询额度，暂不查询 IPPure"
}

func (s *proxycheckStore) admissionEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.AdmissionEnabled
}

// Avoid opening candidate VPNs when no fresh score can be obtained yet.
func (s *proxycheckStore) admissionQueryWait() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.loadErr != nil || !s.data.Enabled || s.key == "" {
		return now.Add(time.Minute)
	}
	s.pruneRequestsLocked(now)
	until := s.data.LastRequest.Add(10 * time.Second)
	if s.data.BlockedUntil.After(until) {
		until = s.data.BlockedUntil
	}
	if len(s.data.Requests) >= proxycheckCandidateLimit && len(s.data.Requests) > 0 {
		reset := s.data.Requests[0].Add(24 * time.Hour)
		if reset.After(until) {
			until = reset
		}
	}
	if until.After(now) {
		return until
	}
	return time.Time{}
}

// 0: fresh zero; 1: unknown/stale; 2: fresh low fallback; 3: fresh rejected.
func proxycheckRank(r ProxycheckResult, ip string, now time.Time) int {
	if r.IP != ip || r.Status != "ok" || r.Score == nil || *r.Score < 0 || *r.Score > 100 || !now.Before(r.ExpiresAt) {
		return 1
	}
	if *r.Score == 0 {
		return 0
	}
	if *r.Score > 0 && *r.Score <= 25 {
		return 2
	}
	return 3
}

func (m *Manager) nodeProxycheckRank(n Node) int {
	ip := m.quality.snapshot(n.HostName).Identity.IP
	if ip == "" {
		return 1
	}
	return proxycheckRank(m.proxycheck.snapshot(ip), ip, time.Now())
}

func (m *Manager) verifyCandidateRisks(t, probe *Tunnel, ip, country string) error {
	if m.proxycheck.admissionEnabled() && !riskFilterEnabled() {
		return fmt.Errorf("ProxyCheck 顺序筛选需同时开启 IPPure 风险筛选")
	}
	if err := m.verifyProxycheckAdmission(t, ip); err != nil {
		return err
	}
	return m.verifyRisk(t, probe, ip, country)
}

func (m *Manager) verifyProxycheckAdmission(t *Tunnel, ip string) error {
	s := m.proxycheck
	if !s.admissionEnabled() {
		return nil
	}
	// Only resuming the exact previously verified exit gets this exception.
	// New IPs, even on the same VPN host, must pass both new admission checks.
	if t.resumeSavedExit && t.restoreFirst != nil && t.Node.HostName == t.restoreFirst.HostName && t.savedExitIP() == ip && ip != "" {
		return nil
	}
	r, err := s.assess(context.Background(), ip, false, true)
	if err != nil || proxycheckRank(r, ip, time.Now()) == 1 {
		s.mu.Lock()
		until := s.data.LastRequest.Add(10 * time.Second)
		if s.data.BlockedUntil.After(until) {
			until = s.data.BlockedUntil
		}
		s.mu.Unlock()
		if until.Before(time.Now().Add(time.Minute)) {
			until = time.Now().Add(time.Minute)
		}
		return &proxycheckAdmissionWait{Until: until}
	}
	if *r.Score > 25 {
		return fmt.Errorf("ProxyCheck 风险分 %.0f 高于备用上限 25，不查询 IPPure", *r.Score)
	}
	p := t.selectionPolicy()
	verifiedHandoff := t.handoffVerified && t.restoreFirst != nil && t.Node.HostName == t.restoreFirst.HostName
	if *r.Score > 0 && (p.RequirePCZero || (p.Mode == "auto" && !t.allowLowProxycheck && !verifiedHandoff)) {
		return &proxycheckLowScore{}
	}
	return nil
}
