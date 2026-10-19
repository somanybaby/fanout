package main

import (
	"fmt"
	"log"
	"syscall"
	"time"
)

const (
	healthInterval       = 10 * time.Second
	healthFailures       = 3 // HTTP 探测连续失败后预检接替节点，避免短时抖动拆掉原隧道
	healthTimeout        = 6 * time.Second
	healthRecoveryReason = "连续三次健康检查失败"
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
			if m.tunnelHealthy(t) {
				fails[t.Slot] = 0
				m.maybeCheckRisk(t)
				m.maybeRotate(t)
				continue
			}

			fails[t.Slot]++
			if fails[t.Slot] < healthFailures {
				log.Printf("隧道 %d (%s) 探测失败 %d 次", t.Slot, t.Node.HostName, fails[t.Slot])
				continue
			}

			log.Printf("隧道 %d (%s) 持续探测失败，检查恢复方案", t.Slot, t.Node.HostName)
			fails[t.Slot] = 0
			m.reconnect(t, t.Node.HostName)
		}
	}
}

// tunnelHealthy 判断隧道是否还真的走在 VPN 上。
//
// 只看"能不能出网"是不够的：netns 通过 veth 走母机 NAT，
// openvpn 死掉后照样能出网，只是出口变回了母机 IP。
// 所以要比对出口 IP 是否仍是建立隧道时拿到的那个。
func (m *Manager) tunnelHealthy(t *Tunnel) bool {
	probe := t
	if !t.egressReady.Load() {
		// The public gate may have closed after an IP change. Probe privately so
		// the original verified IP can recover without reopening a different IP.
		private, closeProbe, err := t.privateProbe()
		if err != nil {
			return false
		}
		defer closeProbe()
		probe = private
	}
	socks, got := probeSOCKSExit(probe)
	web := ProbeResult{Detail: "SOCKS 出口验证未通过"}
	if socks.OK && got == t.ExitIP {
		web = probeWeb(probe)
	} else {
		socks.OK = false
		socks.Detail = "出口失效或发生变化"
	}
	healthy := socks.OK && web.OK
	if m.selection != nil && m.selection.blocked(got) {
		healthy = false
		socks.OK = false
		socks.Detail = "当前出口 IP 已排除"
	}
	if !healthy && ((got != "" && got != t.ExitIP) || (m.selection != nil && m.selection.blocked(got))) {
		t.egressReady.Store(false)
	}
	if healthy && !(t.selectionPolicy().Mode == "strict" && t.riskMustRotate.Load()) {
		t.egressReady.Store(true)
	}
	m.quality.record(t.Node.HostName, healthy, socks.LatencyMS)
	t.mu.Lock()
	t.LastDiagnostics = TunnelDiagnostics{CheckedAt: time.Now(), SOCKS: socks, HTTPS: web, Identity: t.Identity, Scope: "server_local"}
	t.mu.Unlock()
	return healthy
}

// reconnect 就地把一条隧道换到别的节点上，保持槽位与端口不变，
// 这样已经分发出去的客户端配置仍然可用。
//
// oldHost 必须是本次重连前那条隧道真正绑着的节点名。调用方若已经
// 改过 t.Node（比如手动换节点），就要把改之前的名字传进来，
// 否则 rebind 找不到旧绑定，入站会掉成孤儿。
func (m *Manager) reconnect(t *Tunnel, oldHost string) {
	if !t.opMu.TryLock() {
		return
	}
	if t.Status != "up" {
		t.opMu.Unlock()
		return
	}
	if time.Since(t.lastRecoveryAttempt) < time.Minute {
		t.opMu.Unlock()
		return
	}
	t.lastRecoveryAttempt = time.Now()
	// HTTP failure alone does not prove the VPN process is dead. Keep it and
	// its routes intact until a replacement passes the normal admission gates.
	if t.ovpn != nil && t.ovpn.Process != nil && t.ovpn.Process.Signal(syscall.Signal(0)) == nil {
		p := t.selectionPolicy()
		if p.Mode == "strict" {
			m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "recovery_wait", Reason: "严格锁定：保留原隧道等待恢复，不更换其他 IP", Country: p.Country, OldIP: t.ExitIP})
			t.opMu.Unlock()
			return
		}
		node, err := m.pickSwapTarget(t)
		if err != nil {
			m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "recovery_wait", Reason: "暂无同国接替候选，保留原隧道等待恢复", Country: p.Country, OldIP: t.ExitIP})
			t.opMu.Unlock()
			return
		}
		m.prepareSwitch(t, node, p, healthRecoveryReason)
		return
	}
	m.launchReconnect(t, oldHost, t.ExitIP, healthRecoveryReason)
}

// Caller holds opMu. Ownership is transferred to the recovery goroutine.
func (m *Manager) launchReconnect(t *Tunnel, oldHost, oldIP, reason string) {
	t.egressReady.Store(false)
	m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "switch_requested", Reason: reason, Country: t.selectionPolicy().Country, OldIP: oldIP})
	t.Status = "starting"
	t.Err = "正在换节点重连"
	t.ExitIP = ""

	// 动手之前先把"原来绑的是谁"落盘。
	//
	// 换节点是两步：连上新节点、再把入站改绑过来。两步之间崩溃或重启的话，
	// 存盘的隧道已经是新节点、入站却还指着旧节点，那个入站就成了孤儿。
	// 这条线索留在盘上，重启恢复时能照着把它接回来。
	if pending := t.prevHostOf(); pending != "" {
		oldHost = pending
	}
	t.setPrevHost(oldHost)
	if err := m.saveState(); err != nil {
		log.Printf("保存状态失败: %v", err)
	}

	if t.ovpn != nil && t.ovpn.Process != nil {
		_ = t.ovpn.Process.Kill()
		t.ovpn = nil
	}
	t.teardownNetns()

	go func() {
		defer t.opMu.Unlock()
		chosen := t.Node
		t.restoreFirst = &chosen
		// A dead process may resume only its exact previously verified host/IP.
		// Changed IPs and preflighted handoffs still pass both admission checks.
		t.resumeSavedExit = reason == healthRecoveryReason && chosen.HostName == oldHost && oldIP != "" && t.savedExitIP() == oldIP
		defer func() { t.restoreFirst = nil; t.handoffVerified = false; t.resumeSavedExit = false }()
		// 通知延后到 rebind/resync 之后：那两步会把入站改绑到新节点，
		// 提前重建配置会因为入站还指着旧节点名而丢掉路由规则
		m.bringUpPersist(t, false, true)
		if t.Status != "up" {
			return
		}
		// 出站 tag 跟着节点名走，换了节点就要把原来指向它的入站重新绑过去，
		// 否则面板里的路由会指向一个已经不存在的出站。
		if t.Node.HostName != oldHost {
			if err := m.rebind(oldHost, t); err != nil {
				t.egressReady.Store(false)
				m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "binding_failed", Reason: "面板绑定同步失败，客户端出口保持阻断", Country: t.selectionPolicy().Country})
				log.Printf("重连后同步 3x-ui 绑定失败: %v", err)
				return
			}
		} else if err := m.resync(t); err != nil {
			// 节点名没变也要重写一次出站：出口 IP 可能变了，
			// 而且上一轮换节点时留下的绑定需要重新指回来。
			t.egressReady.Store(false)
			log.Printf("重连后重写 3x-ui 出站失败: %v", err)
			return
		}
		m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "switched", Reason: reason, Country: t.selectionPolicy().Country, OldIP: oldIP, NewIP: t.ExitIP})
		// 入站已经跟过来了，标记可以清了
		t.setPrevHost("")
		if err := m.saveState(); err != nil {
			log.Printf("保存状态失败: %v", err)
		}
	}()
}

func (m *Manager) maybeRotate(t *Tunnel) {
	p := t.selectionPolicy()
	if p.Mode == "strict" || p.ProbeOnly {
		return
	}
	if !t.opMu.TryLock() {
		return
	}
	t.mu.Lock()
	highRisk := riskFilterEnabled() && (t.riskMustRotate.Load() || runningRiskHigh(t.Risk))
	t.mu.Unlock()
	scheduled := p.RotateMinutes > 0 && time.Since(t.Since) >= time.Duration(p.RotateMinutes)*time.Minute
	if t.Status != "up" || (!highRisk && !scheduled) || time.Since(t.lastRotationAttempt) < time.Minute {
		t.opMu.Unlock()
		return
	}
	t.lastRotationAttempt = time.Now()
	node, err := m.pickSwapTarget(t)
	if err != nil {
		m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "rotation_wait", Reason: "暂无其他同国合格候选，保留当前出口", Country: p.Country})
		t.opMu.Unlock()
		return
	}
	reason := "定时轮换：同地区优先、同国备用"
	if highRisk {
		reason = "当前 IP 风险分高于 20，提前轮换：同地区优先、同国备用"
	}
	m.prepareSwitch(t, node, p, reason)
}

func runningRiskHigh(r RiskAssessment) bool { return r.Score != nil && *r.Score > 20 }

// Current exits use an hourly review and a separate >20 replacement threshold.
// Candidate admission accepts scores <=20. A failed lookup preserves the exit.
func (m *Manager) maybeCheckRisk(t *Tunnel) {
	if !riskFilterEnabled() || t.selectionPolicy().ProbeOnly || !t.opMu.TryLock() {
		return
	}
	t.mu.Lock()
	due := t.nextRiskCheck
	if due.IsZero() {
		due = t.Risk.CheckedAt.Add(time.Hour)
	}
	if t.Status != "up" || time.Now().Before(due) {
		t.mu.Unlock()
		t.opMu.Unlock()
		return
	}
	t.nextRiskCheck = time.Now().Add(10 * time.Minute)
	t.mu.Unlock()
	go func() {
		defer t.opMu.Unlock()
		m.checkRunningRisk(t)
	}()
}

// Caller holds opMu, keeping the checked IP stable during the lookup.
func (m *Manager) checkRunningRisk(t *Tunnel) {
	r, err := m.risk.assess(t, t.ExitIP)
	p := t.selectionPolicy()
	if err != nil || r.Country != p.Country {
		m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "risk_check_failed", Reason: "当前 IP 风险查询失败或国家不一致，保留出口并稍后重试", Country: p.Country, OldIP: t.ExitIP})
		return
	}
	t.mu.Lock()
	t.Risk = r
	t.nextRiskCheck = r.CheckedAt.Add(time.Hour)
	t.mu.Unlock()
	high := runningRiskHigh(r)
	t.riskMustRotate.Store(high)
	reason := fmt.Sprintf("每小时风险复查：%.2f；未高于 20，继续使用", *r.Score)
	if high {
		reason = fmt.Sprintf("每小时风险复查：%.2f，高于 20，等待同地区/同国且不高于 20 的接替 IP", *r.Score)
		if p.Mode == "strict" {
			t.egressReady.Store(false)
			reason += "；严格锁定仅暂停并重试原 IP"
		}
	}
	m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "risk_checked", Reason: reason, Country: p.Country, OldIP: t.ExitIP})
}
