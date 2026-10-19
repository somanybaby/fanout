package main

import (
	"context"
	"sort"
	"time"
)

type proxycheckTarget struct {
	ip     string
	active bool
}

func (m *Manager) proxycheckTargets() []proxycheckTarget {
	targets := []proxycheckTarget{}
	seen := map[string]bool{}
	for _, t := range m.Tunnels() {
		if t.Status == "up" && !t.selectionPolicy().ProbeOnly && t.ExitIP != "" && !seen[t.ExitIP] {
			seen[t.ExitIP] = true
			targets = append(targets, proxycheckTarget{t.ExitIP, true})
		}
	}
	nodes, _ := m.Nodes()
	for _, n := range nodes {
		if n.CountryCode != "JP" || (residentialOnly() && !n.Residential) {
			continue
		}
		identity := m.quality.snapshot(n.HostName).Identity
		ip := identity.IP
		if identity.Country != "JP" || ip == "" || seen[ip] || time.Since(identity.CheckedAt) > 6*time.Hour || m.selection.blocked(n.IP) || m.selection.blocked(ip) {
			continue
		}
		seen[ip] = true
		targets = append(targets, proxycheckTarget{ip, false})
	}
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].active != targets[j].active {
			return targets[i].active
		}
		return m.proxycheck.snapshot(targets[i].ip).CheckedAt.Before(m.proxycheck.snapshot(targets[j].ip).CheckedAt)
	})
	return targets
}

func (m *Manager) proxycheckSweep(ctx context.Context) {
	s := m.proxycheck
	s.mu.Lock()
	ready := s.loadErr == nil && s.data.Enabled && s.key != ""
	s.mu.Unlock()
	if !ready || ctx.Err() != nil {
		return
	}
	for _, target := range m.proxycheckTargets() {
		r := s.snapshot(target.ip)
		due := r.ExpiresAt
		if r.Status == "ok" {
			due = r.CheckedAt.Add(time.Hour)
		}
		if !time.Now().Before(due) {
			_, _ = s.assess(ctx, target.ip, target.active)
			return // One serialized request per turn, active exits first.
		}
	}
}

func (m *Manager) startProxycheckWorker() {
	s := m.proxycheck
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		m.proxycheckSweep(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.proxycheckSweep(ctx)
			case <-s.wake:
				m.proxycheckSweep(ctx)
			}
		}
	}()
}

func (s *proxycheckStore) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}
