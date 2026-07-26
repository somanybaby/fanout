package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// persistedTunnel 是隧道在磁盘上的形态。
// 只存重建所需的信息，运行态（netns、进程、监听）重启后重新建立。
type persistedTunnel struct {
	Slot     int    `json:"slot"`
	Port     int    `json:"port"`
	HostName string `json:"hostname"`
	Config   string `json:"config"`
}

type persistedState struct {
	Tunnels []persistedTunnel `json:"tunnels"`
}

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

// saveState 把当前隧道写入磁盘，供重启后恢复。
func (m *Manager) saveState() error {
	var st persistedState
	for _, t := range m.Tunnels() {
		if t.Status != "up" {
			continue
		}
		st.Tunnels = append(st.Tunnels, persistedTunnel{
			Slot:     t.Slot,
			Port:     t.Port,
			HostName: t.Node.HostName,
			Config:   t.Node.Config,
		})
	}

	blob, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := statePath(m.workDir) + ".tmp"
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(m.workDir))
}

// restoreState 读回上次的隧道并逐条拉起。
// 节点配置一并存了盘，所以即使 VPN Gate 列表里该节点已消失也能重建。
func (m *Manager) restoreState() (int, error) {
	blob, err := os.ReadFile(statePath(m.workDir))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	var st persistedState
	if err := json.Unmarshal(blob, &st); err != nil {
		return 0, fmt.Errorf("解析状态文件失败: %w", err)
	}

	// 从当前节点列表补回地区、延迟等元数据；节点已下线时退回存盘的最小信息
	known := map[string]Node{}
	for _, n := range m.nodes {
		known[n.HostName] = n
	}

	for _, p := range st.Tunnels {
		node, ok := known[p.HostName]
		if !ok {
			node = Node{HostName: p.HostName}
		}
		node.Config = p.Config
		t := &Tunnel{
			Slot:   p.Slot,
			Port:   p.Port,
			Node:   node,
			Status: "starting",
		}
		m.mu.Lock()
		m.tunnels[p.Slot] = t
		m.mu.Unlock()
		go m.bringUp(t)
	}
	return len(st.Tunnels), nil
}
