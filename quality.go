package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type NodeQuality struct {
	Successes           int       `json:"successes"`
	Failures            int       `json:"failures"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LatencyMS           float64   `json:"latency_ms"`
	LastSuccess         time.Time `json:"last_success"`
	CooldownUntil       time.Time `json:"cooldown_until"`
}

type qualityStore struct {
	mu       sync.Mutex
	Nodes    map[string]NodeQuality
	path     string
	lastSave time.Time
}

func newQualityStore(dir string) *qualityStore {
	q := &qualityStore{Nodes: map[string]NodeQuality{}}
	if dir != "" {
		q.path = filepath.Join(dir, "quality.json")
		blob, _ := os.ReadFile(q.path)
		_ = json.Unmarshal(blob, &q.Nodes)
		if q.Nodes == nil {
			q.Nodes = map[string]NodeQuality{}
		}
	}
	return q
}

func (q *qualityStore) record(host string, success bool, latency int64) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	n := q.Nodes[host]
	if success {
		n.Successes++
		n.ConsecutiveFailures = 0
		n.LastSuccess = time.Now()
		n.CooldownUntil = time.Time{}
		if latency > 0 {
			if n.LatencyMS == 0 {
				n.LatencyMS = float64(latency)
			} else {
				n.LatencyMS = .8*n.LatencyMS + .2*float64(latency)
			}
		}
	} else {
		n.Failures++
		n.ConsecutiveFailures++
		delay := time.Duration(n.ConsecutiveFailures) * time.Minute
		if delay > 10*time.Minute {
			delay = 10 * time.Minute
		}
		n.CooldownUntil = time.Now().Add(delay)
	}
	q.Nodes[host] = n
	if q.path != "" && (!success || time.Since(q.lastSave) > time.Minute) {
		blob, _ := json.MarshalIndent(q.Nodes, "", "  ")
		tmp := q.path + ".tmp"
		if os.WriteFile(tmp, blob, 0600) == nil && os.Rename(tmp, q.path) == nil {
			q.lastSave = time.Now()
		}
	}
}

func (q *qualityStore) rank(nodes []Node) []Node {
	out := append([]Node(nil), nodes...)
	if q == nil {
		return out
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	score := func(n Node) float64 {
		v := q.Nodes[n.HostName]
		if v.CooldownUntil.After(now) {
			return -10000 - float64(v.ConsecutiveFailures)
		}
		if v.Successes == 0 {
			return 0
		}
		ratio := float64(v.Successes) / float64(v.Successes+v.Failures)
		return 1000*ratio - v.LatencyMS/20
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := score(out[i]), score(out[j])
		if a != b {
			return a > b
		}
		return out[i].SpeedMbps > out[j].SpeedMbps
	})
	return out
}
