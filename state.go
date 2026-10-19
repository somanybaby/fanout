package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// persistedTunnel 是隧道在磁盘上的形态。
// 只存重建所需的信息，运行态（netns、进程、监听）重启后重新建立。
type persistedTunnel struct {
	LastExitIP      string          `json:"last_exit_ip,omitempty"`
	Policy          SelectionPolicy `json:"selection"`
	PreferredNode   *Node           `json:"preferred_node,omitempty"`
	PreferredConfig string          `json:"preferred_config,omitempty"`
	Slot            int             `json:"slot"`
	Port            int             `json:"port"`
	HostName        string          `json:"hostname"`
	CountryCode     string          `json:"country_code"`
	Country         string          `json:"country"`
	Config          string          `json:"config"`
	// SOCKS5 凭据要存盘：用户已经把它分发给客户端了，重启后变掉等于全断
	SocksUser string `json:"socks_user,omitempty"`
	SocksPass string `json:"socks_pass,omitempty"`
	// PrevHost 非空表示上次换节点只做了一半：隧道已经指向新节点，
	// 但入站还绑在这个旧节点上。恢复时照着它把入站接回来。
	PrevHost string `json:"prev_host,omitempty"`
}

type persistedState struct {
	Tunnels []persistedTunnel `json:"tunnels"`
}

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

// saveState 把当前隧道写入磁盘，供重启后恢复。
func (m *Manager) saveState() error {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	var st persistedState
	for _, t := range m.Tunnels() {
		// 只跳过用户主动停掉的。starting/failed 的隧道也要存：
		// 它们正在重连或等着重试，漏存会让重启后凭空少几个出口。
		if t.Status == "stopped" || t.selectionPolicy().ProbeOnly {
			continue
		}
		policy := t.selectionPolicy()
		var preferred *Node
		if policy.Mode == "strict" || policy.Mode == "preferred" {
			n := policy.Preferred
			preferred = &n
		}
		st.Tunnels = append(st.Tunnels, persistedTunnel{
			LastExitIP: t.savedExitIP(),
			Policy:     policy, PreferredNode: preferred, PreferredConfig: policy.Preferred.Config,
			Slot:        t.Slot,
			Port:        t.Port,
			HostName:    t.Node.HostName,
			CountryCode: t.Node.CountryCode,
			Country:     t.Node.Country,
			Config:      t.Node.Config,
			SocksUser:   t.Cred.User,
			SocksPass:   t.Cred.Pass,
			PrevHost:    t.prevHostOf(),
		})
	}

	return atomicPrivateJSON(statePath(m.workDir), st)
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
			// 节点已从 VPN Gate 列表消失，用存盘的信息重建
			node = Node{
				HostName:    p.HostName,
				CountryCode: p.CountryCode,
				Country:     p.Country,
			}
		}
		node.Config = p.Config
		// 从旧版本升上来的状态文件没有凭据字段，补一套新的
		cred := SocksCred{User: p.SocksUser, Pass: p.SocksPass}
		if cred.User == "" || cred.Pass == "" {
			gen, err := newSocksCred()
			if err != nil {
				return 0, fmt.Errorf("生成 SOCKS5 凭据失败: %w", err)
			}
			cred = gen
		}
		policy := p.Policy
		if policy.Mode == "" {
			policy.Mode = "auto"
		}
		if policy.Country == "" {
			policy.Country = p.CountryCode
		}
		if p.PreferredNode != nil {
			policy.Preferred = *p.PreferredNode
			policy.Preferred.Config = p.PreferredConfig
		}
		if policy.Mode != "auto" {
			preferred := policy.Preferred
			if preferred.HostName == "" {
				preferred = node
				policy.Preferred = node
			}
			if _, err := normalizePolicy(policy, preferred); err != nil {
				return 0, fmt.Errorf("已保存的 IP 选择规则无效: %w", err)
			}
		}
		t := &Tunnel{
			lastVerifiedIP: p.LastExitIP,
			stopCh:         make(chan struct{}),
			Policy:         policy,
			Slot:           p.Slot,
			Port:           p.Port,
			Node:           node,
			Status:         "starting",
			Cred:           cred,
		}
		t.setPrevHost(p.PrevHost)
		m.mu.Lock()
		m.tunnels[p.Slot] = t
		m.mu.Unlock()
		go m.restoreTunnel(t)
	}
	return len(st.Tunnels), nil
}

// restoreTunnel 拉起一条恢复出来的隧道，顺手把上次没做完的换节点收尾。
func (m *Manager) restoreTunnel(t *Tunnel) {
	t.opMu.Lock()
	defer t.opMu.Unlock()
	saved := t.Node
	t.restoreFirst = &saved
	t.resumeSavedExit = true
	defer func() { t.restoreFirst = nil; t.resumeSavedExit = false }()
	prev := t.prevHostOf()
	if prev == "" {
		prev = t.Node.HostName
	}
	t.setPrevHost(prev)
	m.bringUpPersist(t, true, true)
	if t.Status != "up" {
		return
	}
	// 上次换节点改完隧道就中断了，入站还指着旧节点。不接回来的话它会一直
	// 显示成"未绑定出口"，流量悄悄走直连——用户看不出哪里不对。
	if err := m.rebind(prev, t); err != nil {
		log.Printf("恢复时把入站接回出口 %d 失败: %v", t.Slot, err)
		return
	}
	log.Printf("出口 %d 上次换节点没收尾，已把原来绑着 %s 的入站接到 %s",
		t.Slot, prev, t.Node.HostName)
	t.setPrevHost("")
	if err := m.saveState(); err != nil {
		log.Printf("保存状态失败: %v", err)
	}
}
