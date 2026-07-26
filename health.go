package main

import (
	"log"
	"os/exec"
	"time"
)

const (
	healthInterval = 60 * time.Second
	healthFailures = 2 // 连续失败几次才判定掉线，避免网络抖动误杀
)

// WatchHealth 周期检查每条隧道是否还能出网，掉线的自动换节点重连。
// VPN Gate 是志愿者节点，运行中掉线很常见。
func (m *Manager) WatchHealth() {
	fails := map[int]int{}

	for range time.Tick(healthInterval) {
		for _, t := range m.Tunnels() {
			if t.Status != "up" {
				continue
			}
			if tunnelAlive(t.nsName()) {
				fails[t.Slot] = 0
				continue
			}

			fails[t.Slot]++
			if fails[t.Slot] < healthFailures {
				log.Printf("隧道 %d (%s) 探测失败 %d 次", t.Slot, t.Node.HostName, fails[t.Slot])
				continue
			}

			log.Printf("隧道 %d (%s) 已掉线，正在换节点重连", t.Slot, t.Node.HostName)
			fails[t.Slot] = 0
			m.reconnect(t)
		}
	}
}

// tunnelAlive 在 netns 内做一次轻量探测。
func tunnelAlive(ns string) bool {
	cmd := exec.Command("ip", "netns", "exec", ns,
		"curl", "-s", "--max-time", "10", "-o", "/dev/null", "http://api.ipify.org")
	return cmd.Run() == nil
}

// reconnect 就地把一条隧道换到别的节点上，保持槽位与端口不变，
// 这样已经分发出去的客户端配置仍然可用。
func (m *Manager) reconnect(t *Tunnel) {
	oldHost := t.Node.HostName
	t.Status = "starting"
	t.Err = "正在换节点重连"
	t.ExitIP = ""

	if t.ovpn != nil && t.ovpn.Process != nil {
		_ = t.ovpn.Process.Kill()
		t.ovpn = nil
	}
	t.teardownNetns()

	go func() {
		m.bringUp(t)
		// 出站 tag 跟着节点名走，换了节点就要把原来指向它的入站重新绑过去，
		// 否则面板里的路由会指向一个已经不存在的出站。
		if t.Status == "up" && t.Node.HostName != oldHost {
			if err := m.rebind(oldHost, t); err != nil {
				log.Printf("重连后同步 3x-ui 绑定失败: %v", err)
			}
		}
	}()
}
