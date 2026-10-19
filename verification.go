package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Connectivity results deliberately do not mean that IPPure approved an IP.
type VerificationResult struct {
	Status    string    `json:"status"`
	Detail    string    `json:"detail"`
	IP        string    `json:"ip,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
}

type verificationData struct {
	AutoHourly bool                          `json:"auto_hourly"`
	NextRun    time.Time                     `json:"next_run"`
	StartedAt  time.Time                     `json:"started_at"`
	FinishedAt time.Time                     `json:"finished_at"`
	Queue      []string                      `json:"queue"`
	Current    string                        `json:"current"`
	Status     string                        `json:"status"`
	Total      int                           `json:"total"`
	Completed  int                           `json:"completed"`
	Passed     int                           `json:"passed"`
	Failed     int                           `json:"failed"`
	Skipped    int                           `json:"skipped"`
	Message    string                        `json:"message"`
	Results    map[string]VerificationResult `json:"results"`
}

type verificationStore struct {
	mu           sync.Mutex
	data         verificationData
	path         string
	loadErr      error
	generation   uint64
	nextAttempt  time.Time
	activeCancel context.CancelFunc
	workerCancel context.CancelFunc
	done         chan struct{}
	// Injectable for regression tests; production always uses isolated netns.
	probe func(context.Context, Node) VerificationResult
}

func newVerificationStore(dir string) *verificationStore {
	s := &verificationStore{data: verificationData{Status: "idle", Results: map[string]VerificationResult{}}}
	if dir != "" {
		s.path = filepath.Join(dir, "verification.json")
		b, err := os.ReadFile(s.path)
		if err == nil {
			s.loadErr = json.Unmarshal(b, &s.data)
		} else if !os.IsNotExist(err) {
			s.loadErr = err
		}
	}
	if s.data.Results == nil {
		s.data.Results = map[string]VerificationResult{}
	}
	if s.data.Current != "" {
		s.data.Queue = append([]string{s.data.Current}, s.data.Queue...)
		s.data.Current = ""
		s.data.Status = "queued"
	}
	if s.loadErr != nil {
		s.data.AutoHourly = false
		s.data.Status = "error"
		s.data.Message = "核验队列文件不可读，请检查备份；现有出口继续工作"
	}
	return s
}

// Caller holds mu. Queue entries contain hostnames, never VPN configs or keys.
func (s *verificationStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	return atomicPrivateJSON(s.path, s.data)
}

func (s *verificationStore) snapshot() verificationData {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.data
	out.Queue = append([]string{}, s.data.Queue...)
	out.Results = make(map[string]VerificationResult, len(s.data.Results))
	for k, v := range s.data.Results {
		out.Results[k] = v
	}
	return out
}

func (s *verificationStore) result(host string) VerificationResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Results[host]
}

func (m *Manager) verificationHosts() []string {
	nodes, _ := m.Nodes()
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if (a.CountryCode == "JP") != (b.CountryCode == "JP") {
			return a.CountryCode == "JP"
		}
		af := m.selection.mark(a.IP).Favorite
		bf := m.selection.mark(b.IP).Favorite
		if af != bf {
			return af
		}
		return a.HostName < b.HostName
	})
	seen := map[string]bool{}
	hosts := []string{}
	for _, n := range nodes {
		q := m.quality.snapshot(n.HostName)
		if n.HostName == "" || seen[n.HostName] || (residentialOnly() && !n.Residential) || m.selection.blocked(n.IP) || m.selection.blocked(q.Identity.IP) {
			continue
		}
		seen[n.HostName] = true
		hosts = append(hosts, n.HostName)
	}
	return hosts
}

func (m *Manager) enqueueVerification(now time.Time, hourly bool) (bool, error) {
	hosts := m.verificationHosts()
	s := m.verification
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return false, fmt.Errorf("核验队列不可读，未启动任务")
	}
	if hourly {
		if !s.data.AutoHourly || now.Before(s.data.NextRun) {
			return false, nil
		}
		s.data.NextRun = now.Add(time.Hour)
	}
	if s.data.Current != "" || len(s.data.Queue) > 0 {
		return false, s.saveLocked() // No duplicate or overlapping rounds.
	}
	previous := s.data
	s.generation++
	s.data.Queue = hosts
	s.data.Current = ""
	s.data.Total, s.data.Completed = len(hosts), 0
	s.data.Passed, s.data.Failed, s.data.Skipped = 0, 0, 0
	s.data.StartedAt, s.data.FinishedAt = now, time.Time{}
	s.data.Status, s.data.Message = "queued", "串行核验全部国家候选，日本优先；只查连接与出口信息，不批量查询 IPPure"
	if len(hosts) == 0 {
		s.data.Status, s.data.FinishedAt = "finished", now
	}
	// Bound history without discarding the current catalog's results.
	if len(s.data.Results) > 2000 {
		keep := make(map[string]VerificationResult, len(hosts))
		for _, h := range hosts {
			if r, ok := s.data.Results[h]; ok {
				keep[h] = r
			}
		}
		s.data.Results = keep
	}
	if err := s.saveLocked(); err != nil {
		s.data = previous
		return false, err
	}
	return true, nil
}

func (s *verificationStore) configureHourly(enabled bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("核验队列不可读")
	}
	old := s.data
	if enabled && !s.data.AutoHourly {
		s.data.NextRun = now // Start the first round after enabling.
	}
	s.data.AutoHourly = enabled
	if err := s.saveLocked(); err != nil {
		s.data = old
		return err
	}
	return nil
}

func (s *verificationStore) cancel() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("核验队列不可读")
	}
	s.generation++
	if s.activeCancel != nil {
		s.activeCancel()
	}
	s.data.Queue, s.data.Current = []string{}, ""
	s.data.Status, s.data.Message = "cancelled", "本轮已停止；每小时自动核验开关独立控制下一轮"
	s.data.FinishedAt = time.Now()
	return s.saveLocked()
}

func (m *Manager) startVerificationWorker() {
	s := m.verification
	s.mu.Lock()
	if s.workerCancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.workerCancel, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_, _ = m.enqueueVerification(now, true)
				m.verificationStep(ctx, now)
			}
		}
	}()
}

func (s *verificationStore) stop() {
	s.mu.Lock()
	cancel, done := s.workerCancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (m *Manager) verificationStep(ctx context.Context, now time.Time) {
	s := m.verification
	s.mu.Lock()
	if s.loadErr != nil || len(s.data.Queue) == 0 || s.data.Current != "" || now.Before(s.nextAttempt) || ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	host := s.data.Queue[0]
	s.data.Queue = s.data.Queue[1:]
	s.data.Current, s.data.Status = host, "running"
	gen := s.generation
	probeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	s.activeCancel = cancel
	if err := s.saveLocked(); err != nil {
		cancel()
		s.data.Queue = append([]string{host}, s.data.Queue...)
		s.data.Current, s.data.Status, s.data.Message = "", "error", "无法保存进度，暂停核验"
		s.nextAttempt = now.Add(time.Minute)
		s.mu.Unlock()
		return
	}
	probe := s.probe
	s.mu.Unlock()
	nodes, _ := m.Nodes()
	r := VerificationResult{Status: "skipped", Detail: "节点已离开目录"}
	for _, n := range nodes {
		if n.HostName == host {
			if probe == nil {
				r = m.probeVerification(probeCtx, n)
			} else {
				r = probe(probeCtx, n)
			}
			break
		}
	}
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.generation {
		return // Cancelled round cannot overwrite a newly queued round.
	}
	s.activeCancel = nil
	s.data.Current = ""
	s.nextAttempt = time.Now().Add(10 * time.Second)
	if r.Status == "waiting" || ctx.Err() != nil {
		s.data.Queue = append([]string{host}, s.data.Queue...)
		s.data.Status, s.data.Message = "waiting", r.Detail
		_ = s.saveLocked()
		return
	}
	r.CheckedAt = time.Now().UTC()
	s.data.Results[host] = r
	s.data.Completed++
	s.data.Message = r.Detail
	switch r.Status {
	case "pass":
		s.data.Passed++
	case "failed":
		s.data.Failed++
	default:
		s.data.Skipped++
	}
	s.data.Status = "queued"
	if len(s.data.Queue) == 0 {
		s.data.Status, s.data.FinishedAt = "finished", time.Now()
	}
	_ = s.saveLocked()
}
