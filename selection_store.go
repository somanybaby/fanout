package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func atomicPrivateJSON(path string, v any) error {
	blob, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".fanout-write-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(blob)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}

type IPMark struct {
	Favorite bool `json:"favorite"`
	Blocked  bool `json:"blocked"`
}
type SelectionEvent struct {
	Time    time.Time `json:"time"`
	Slot    int       `json:"slot"`
	Kind    string    `json:"kind"`
	Reason  string    `json:"reason"`
	Country string    `json:"country"`
	OldIP   string    `json:"old_ip,omitempty"`
	NewIP   string    `json:"new_ip,omitempty"`
}
type selectionData struct {
	Marks  map[string]IPMark `json:"marks"`
	Events []SelectionEvent  `json:"events"`
}
type selectionStore struct {
	mu      sync.Mutex
	data    selectionData
	path    string
	loadErr error
}

func newSelectionStore(dir string) *selectionStore {
	s := &selectionStore{data: selectionData{Marks: map[string]IPMark{}}}
	if dir != "" {
		s.path = filepath.Join(dir, "selections.json")
		b, err := os.ReadFile(s.path)
		if err == nil {
			s.loadErr = json.Unmarshal(b, &s.data)
		} else if !os.IsNotExist(err) {
			s.loadErr = err
		}
	}
	if s.data.Marks == nil {
		s.data.Marks = map[string]IPMark{}
	}
	if len(s.data.Events) > 200 {
		s.data.Events = s.data.Events[len(s.data.Events)-200:]
	}
	return s
}
func (s *selectionStore) mark(ip string) IPMark {
	if s == nil {
		return IPMark{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Marks[ip]
}
func (s *selectionStore) setMark(ip string, mark IPMark) error {
	a := net.ParseIP(ip)
	if a == nil || a.To4() == nil {
		return fmt.Errorf("请输入合法的 IPv4 地址")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("收藏和排除文件损坏，请先恢复备份")
	}
	old, exists := s.data.Marks[ip]
	s.data.Marks[ip] = mark
	if s.path != "" {
		if err := atomicPrivateJSON(s.path, s.data); err != nil {
			if exists {
				s.data.Marks[ip] = old
			} else {
				delete(s.data.Marks, ip)
			}
			return err
		}
	}
	return nil
}
func (s *selectionStore) blocked(ip string) bool { return s.mark(ip).Blocked }
func (s *selectionStore) addEvent(e SelectionEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Time = time.Now().UTC()
	s.data.Events = append(s.data.Events, e)
	if len(s.data.Events) > 200 {
		s.data.Events = s.data.Events[len(s.data.Events)-200:]
	}
	if s.path != "" && s.loadErr == nil {
		_ = atomicPrivateJSON(s.path, s.data)
	}
}
func (s *selectionStore) snapshot() selectionData {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := selectionData{Marks: map[string]IPMark{}, Events: append([]SelectionEvent{}, s.data.Events...)}
	for ip, v := range s.data.Marks {
		d.Marks[ip] = v
	}
	return d
}
