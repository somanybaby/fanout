package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// VPN Gate publishes a changing sample. Retain recent official samples so a
// less common country does not vanish on every refresh. Availability is still
// established by connection probes; a cached entry is not a live guarantee.
type catalogEntry struct {
	Node   Node      `json:"node"`
	Config string    `json:"config"`
	Seen   time.Time `json:"seen"`
}

const catalogTTL = 6 * time.Hour

func mergeCatalog(dir string, fresh []Node, now time.Time) []Node {
	entries := map[string]catalogEntry{}
	path := filepath.Join(dir, "catalog.json")
	if dir != "" {
		blob, _ := os.ReadFile(path)
		_ = json.Unmarshal(blob, &entries)
		if entries == nil {
			entries = map[string]catalogEntry{}
		}
	}
	for _, n := range fresh {
		entries[n.HostName] = catalogEntry{Node: n, Config: n.Config, Seen: now}
	}
	out := make([]Node, 0, len(entries))
	for host, e := range entries {
		if now.Sub(e.Seen) > catalogTTL || e.Node.HostName == "" || e.Config == "" {
			delete(entries, host)
			continue
		}
		e.Node.Config = e.Config
		out = append(out, e.Node)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpeedMbps != out[j].SpeedMbps {
			return out[i].SpeedMbps > out[j].SpeedMbps
		}
		return out[i].HostName < out[j].HostName
	})
	if dir != "" {
		blob, _ := json.Marshal(entries)
		tmp := path + ".tmp"
		if os.WriteFile(tmp, blob, 0600) == nil {
			_ = os.Rename(tmp, path)
		}
	}
	return out
}

func fetchAdditionalNodes(source string) ([]Node, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("补充节点源必须是 HTTPS CSV 地址")
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) > 5 {
			return fmt.Errorf("节点源重定向不安全")
		}
		return nil
	}}
	r, err := client.Get(source)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("节点源 HTTP %d", r.StatusCode)
	}
	blob, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return parseNodeCSV(string(blob))
}
