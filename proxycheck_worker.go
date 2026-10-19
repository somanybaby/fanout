package main

import (
	"context"
	"strconv"
	"time"
)

type proxycheckTarget struct {
	ip     string
	active bool
	slot   string
}

// Only actual active exits whose IP changed are observed. Candidate lists,
// health checks, trials and hourly reviews never generate ProxyCheck requests.
func (m *Manager) proxycheckTargets() []proxycheckTarget {
	targets := []proxycheckTarget{}
	for _, t := range m.Tunnels() {
		if t.Status != "up" || t.selectionPolicy().ProbeOnly || t.ExitIP == "" {
			continue
		}
		slot := strconv.Itoa(t.Slot)
		m.proxycheck.mu.Lock()
		previous := m.proxycheck.data.ObservedExits[slot]
		m.proxycheck.mu.Unlock()
		if previous != t.ExitIP {
			targets = append(targets, proxycheckTarget{ip: t.ExitIP, active: true, slot: slot})
		}
	}
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
		// Persist before querying to avoid periodic retry storms after a failure or
		// restart. Replacing the key or re-enabling reference queries is explicit retry.
		s.mu.Lock()
		if s.data.ObservedExits[target.slot] == target.ip {
			s.mu.Unlock()
			continue
		}
		now := time.Now()
		r := s.data.Entries[target.ip]
		cached := (r.Status == "ok" && now.Before(r.CheckedAt.Add(time.Hour))) || (r.Status != "ok" && now.Before(r.ExpiresAt))
		if !cached {
			s.pruneRequestsLocked(now)
			if len(s.data.Requests) >= proxycheckDailyLimit || now.Before(s.data.BlockedUntil) || now.Sub(s.data.LastRequest) < 10*time.Second {
				s.mu.Unlock()
				return // Keep this IP change pending until a single query is permitted.
			}
		}
		s.data.ObservedExits[target.slot] = target.ip
		err := s.saveLocked()
		s.mu.Unlock()
		if err != nil {
			return
		}
		_, _ = s.assess(ctx, target.ip, true)
		return
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
