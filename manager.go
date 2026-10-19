package main

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"sync"
	"time"
)

// Manager 维护所有隧道，负责分配槽位与端口。
type Manager struct {
	proxycheck   *proxycheckStore
	verification *verificationStore
	probes       map[int]*Tunnel // isolated batch probes; never client exits
	stateMu      sync.Mutex
	selection    *selectionStore
	risk         *riskStore
	mu           sync.RWMutex
	tunnels      map[int]*Tunnel
	nodes        []Node
	fetched      time.Time
	workDir      string
	maxSlots     int
	jobs         JobStore
	quality      *qualityStore
}

func NewManager(maxSlots int, workDir string) *Manager {
	return &Manager{
		proxycheck:   newProxycheckStore(workDir),
		verification: newVerificationStore(workDir),
		probes:       map[int]*Tunnel{},
		tunnels:      map[int]*Tunnel{},
		workDir:      workDir,
		maxSlots:     maxSlots,
		quality:      newQualityStore(workDir),
		selection:    newSelectionStore(workDir),
		risk:         newRiskStore(workDir),
	}
}

// RefreshNodes 重新拉取节点列表。
func (m *Manager) RefreshNodes() (int, error) {
	nodes, err := fetchNodes(60 * time.Second)
	if err != nil {
		log.Printf("官方节点源暂不可用，尝试补充源和近期目录")
	}
	for _, source := range getWebSettings().AdditionalSources {
		extra, ferr := fetchAdditionalNodes(source)
		if ferr != nil {
			log.Printf("补充节点源暂不可用")
			continue
		}
		nodes = append(nodes, extra...)
	}
	m.mu.Lock()
	m.nodes = mergeCatalog(m.workDir, nodes, time.Now())
	m.fetched = time.Now()
	count := len(m.nodes)
	m.mu.Unlock()
	if count == 0 && err != nil {
		return 0, err
	}
	return count, nil
}

func (m *Manager) Nodes() ([]Node, time.Time) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Node, len(m.nodes))
	copy(out, m.nodes)
	return out, m.fetched
}

func (m *Manager) Tunnels() []*Tunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Tunnel, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

// freeSlot 找一个未占用的槽位。槽位同时决定端口与网段。
func (m *Manager) freeSlot() (int, error) {
	for i := 1; i <= m.maxSlots; i++ {
		if _, used := m.tunnels[i]; !used && m.probes[i] == nil {
			return i, nil
		}
	}
	return 0, fmt.Errorf("槽位已满（上限 %d）", m.maxSlots)
}

// Start 为指定节点开一条隧道，返回分配到的本地端口。
func (m *Manager) Start(node Node) (*Tunnel, error) { return m.StartSelected(node, SelectionPolicy{}) }
func (m *Manager) StartSelected(node Node, policy SelectionPolicy) (*Tunnel, error) {
	policy, err := normalizePolicy(policy, node)
	if err != nil {
		return nil, err
	}
	if m.nodeBlocked(node) {
		return nil, fmt.Errorf("节点已排除或排除列表不可用")
	}

	m.mu.Lock()
	for _, probe := range m.probes {
		if probe.Node.HostName == node.HostName {
			m.mu.Unlock()
			return nil, fmt.Errorf("该节点正在批量核验，请稍后再试")
		}
	}
	for _, other := range m.tunnels {
		if other.Node.HostName == node.HostName {
			m.mu.Unlock()
			return nil, fmt.Errorf("该节点已被其他出口使用")
		}
	}
	slot, err := m.freeSlot()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	// 端口随机取，避免固定规律撞上机器上的其他服务
	taken := map[int]bool{}
	for _, other := range m.tunnels {
		taken[other.Port] = true
	}
	port, err := freeRandomPort(taken)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	cred, err := newSocksCred()
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	t := &Tunnel{
		stopCh: make(chan struct{}),
		Slot:   slot,
		Port:   port,
		Node:   node,
		Status: "starting",
		Since:  time.Now(),
		Cred:   cred,
		Policy: policy,
	}
	m.tunnels[slot] = t
	m.mu.Unlock()

	go func() {
		t.opMu.Lock()
		defer t.opMu.Unlock()
		m.bringUpPersist(t, true, !policy.ProbeOnly && (policy.Mode != "auto" || riskFilterEnabled()))
	}()
	return t, nil
}

// bringUp 把一条隧道拉起来。
//
// notify 决定成功后是否立刻重建后端配置。换节点重连时要传 false：
// 那条路径随后会调 rebind/resync 把入站改绑到新节点，在那之前重建配置
// 会因为入站还指着旧节点名而把路由规则丢掉。
func (m *Manager) bringUp(t *Tunnel, notify bool) {
	m.bringUpPersist(t, notify, false)
}

// 自动重连的退避区间：一轮候选全挂后等一会儿再刷新节点列表重来，
// 别把死节点列表打爆，也别让恢复拖太久。
const (
	reconnectBackoffMin = 5 * time.Second
	reconnectBackoffMax = 60 * time.Second
)

// bringUpPersist 把一条隧道拉起来。
//
// persist=false（手动新建）：走一轮候选，全失败就标 failed，让用户能立刻看到并重试。
// persist=true（自动重连 / 重启恢复）：一轮全失败不放弃，退避后刷新节点列表再来一轮，
// 一直循环到连上或这条隧道被用户停掉。VPN Gate 死节点多，"当前都不可用"往往只是
// 这一批候选恰好都挂了，过一会儿就有新节点，不该让出口永久躺死。
func (m *Manager) bringUpPersist(t *Tunnel, notify bool, persist bool) {
	backoff := reconnectBackoffMin
	for {
		if m.tryCandidates(t, notify) {
			return
		}
		// 隧道已被用户停掉或从管理器移除，别再重试
		if !persist || !m.tunnelActive(t) {
			if persist {
				return
			}
			t.Status = "failed"
			if serr := m.saveState(); serr != nil {
				log.Printf("保存状态失败: %v", serr)
			}
			return
		}

		t.Status = "starting"
		wait := backoff
		if left := time.Until(t.retryAfter); left > wait {
			wait = left
			if wait > time.Minute {
				wait = time.Minute
			}
		}
		t.Err = fmt.Sprintf("暂无合格出口，%.0f 秒后重试", wait.Seconds())
		log.Printf("隧道 %d 一轮候选均失败，%.0f 秒后刷新节点重试", t.Slot, backoff.Seconds())
		select {
		case <-time.After(wait):
		case <-t.stopCh:
			return
		}
		if !m.tunnelActive(t) {
			return
		}
		if _, err := m.RefreshNodes(); err != nil {
			log.Printf("重试前刷新节点列表失败: %v", err)
		}
		if backoff < reconnectBackoffMax {
			backoff *= 2
			if backoff > reconnectBackoffMax {
				backoff = reconnectBackoffMax
			}
		}
	}
}

// tryCandidates 走一轮候选节点，成功返回 true。失败不改 Status（留给调用方决定）。
func (m *Manager) tryCandidates(t *Tunnel, notify bool) bool {
	candidates := m.candidatesFor(t)
	deferred := []Node{}
	for phase := 0; phase < 2; phase++ {
		if phase == 1 {
			candidates = deferred
		}
		t.allowRegionFallback = phase == 1
		for i, node := range candidates {
			if !m.tunnelActive(t) {
				return false
			}
			if m.nodeInUse(node.HostName, t.Slot) {
				continue
			}
			t.Node = node
			t.Status = "starting"
			t.Err = fmt.Sprintf("核验第 %d 个候选出口", i+1)
			err := m.tryNode(t)
			if err == nil {
				t.Status = "up"
				t.Err = ""
				t.Since = time.Now()
				m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "connected", Reason: func() string {
					if phase == 1 {
						return "暂无同地区合格候选，接入同国家备用出口"
					}
					return "出口核验通过"
				}(), Country: t.selectionPolicy().Country, NewIP: t.ExitIP})
				if serr := m.saveState(); serr != nil {
					log.Printf("保存状态失败: %v", serr)
				}
				if notify {
					m.notifyPanel()
				}
				t.allowRegionFallback = false
				t.skipHostOnce = ""
				t.retryAfter = time.Time{}
				return true
			}
			t.Err = err.Error()
			var outside *otherRegionError
			var waiting *riskWaitError
			switch {
			case errors.As(err, &outside):
				deferred = append(deferred, node)
			case errors.As(err, &waiting):
				t.retryAfter = waiting.Until
			default:
				m.quality.record(node.HostName, false, 0)
				m.selection.addEvent(SelectionEvent{Slot: t.Slot, Kind: "attempt_failed", Reason: err.Error(), Country: t.selectionPolicy().Country})
			}
			if t.ovpn != nil && t.ovpn.Process != nil {
				_ = t.ovpn.Process.Kill()
				t.ovpn = nil
			}
			t.teardownNetns()
			if waiting != nil {
				break
			}
		}
	}
	t.allowRegionFallback = false
	return false
}

// tunnelActive 判断这条隧道是否还归管理器所有且未被用户停掉。
// 用指针比对：Stop 会从 map 里删除并把 Status 置 stopped，
// 重连循环据此退出，避免对着一条已经不存在的隧道空转。
func (m *Manager) tunnelActive(t *Tunnel) bool {
	if t.cancelled.Load() || t.Status == "stopped" {
		return false
	}
	m.mu.RLock()
	cur, ok := m.tunnels[t.Slot]
	m.mu.RUnlock()
	return ok && cur == t
}

// tryNode 尝试用当前节点把隧道拉起来。
func (m *Manager) tryNode(t *Tunnel) error {
	if t.cancelled.Load() {
		return fmt.Errorf("出口已停止")
	}
	if err := t.setupNetns(); err != nil {
		return err
	}
	if err := t.installEgressGuard(); err != nil {
		return fmt.Errorf("出口保护失败: %w", err)
	}
	if err := t.startOpenVPN(m.workDir); err != nil {
		return err
	}
	if t.cancelled.Load() {
		return fmt.Errorf("出口已停止")
	}
	probeTunnel, closeProbe, err := t.privateProbe()
	if err != nil {
		return err
	}
	defer closeProbe()
	probe, ip := probeSOCKSExit(probeTunnel)
	if !probe.OK {
		return fmt.Errorf("认证 SOCKS 出口检查失败")
	}
	identity, err := lookupExitIdentity(probeTunnel, ip)
	if err != nil {
		return err
	}
	m.quality.identify(t.Node.HostName, identity)
	if err := m.checkExitPolicy(t, ip, identity.Country); err != nil {
		return err
	}
	if err := m.verifyRisk(t, probeTunnel, ip, identity.Country); err != nil {
		return err
	}
	if riskFilterEnabled() {
		r := t.Risk
		if r.Region != "" {
			identity.Region = r.Region
			identity.RegionCode = r.RegionCode
			identity.City = r.City
			m.quality.identify(t.Node.HostName, identity)
		}
	}
	if err := m.checkRegionPreference(t, identity); err != nil {
		return err
	}
	m.rememberRegion(t, identity)
	if residentialOnly() && identity.Residential == "hosting" {
		return fmt.Errorf("实际出口为已知机房节点")
	}
	t.mu.Lock()
	t.Identity = identity
	t.mu.Unlock()
	t.ExitIP = ip
	if t.cancelled.Load() {
		return fmt.Errorf("出口已停止")
	}
	if t.listener == nil {
		if err := t.serve(); err != nil {
			return err
		}
	}
	t.riskMustRotate.Store(false)
	t.mu.Lock()
	t.nextRiskCheck = time.Time{}
	t.mu.Unlock()
	t.egressReady.Store(true)
	m.quality.identify(t.Node.HostName, identity)
	m.quality.record(t.Node.HostName, true, probe.LatencyMS)
	return nil
}

// candidatesFor 以这条隧道当前的节点打头，后面跟上同地区的其他节点作为备选。
//
// 打头的一定是 t.Node：自动重连的目标是把这条出口恢复原样，先试原节点。
// 备选会避开用户手动换掉过的节点——那些是他明确不想要的 IP，
// 让重连悄悄换回去等于撤销了他的操作。
func (m *Manager) candidatesFor(t *Tunnel) []Node { return m.policyCandidates(t) }

// Stop 停掉一条隧道并释放槽位。
func (m *Manager) Stop(slot int) error {
	invalidateInbounds()
	m.mu.RLock()
	t := m.tunnels[slot]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("槽位 %d 没有运行中的隧道", slot)
	}
	t.cancelled.Store(true)
	t.egressReady.Store(false)
	t.stopOnce.Do(func() {
		if t.stopCh != nil {
			close(t.stopCh)
		}
	})
	// Do not release the slot until the previous worker and namespace have
	// finished. Otherwise a late cleanup could destroy the next user's tunnel.
	t.opMu.Lock()
	defer t.opMu.Unlock()
	m.mu.Lock()
	if m.tunnels[slot] != t {
		m.mu.Unlock()
		return nil
	}
	t.stop()
	delete(m.tunnels, slot)
	m.mu.Unlock()
	if err := m.saveState(); err != nil {
		log.Printf("保存状态失败: %v", err)
	}
	m.notifyPanel()
	return nil
}

// Swap 把一条隧道换到同地区的另一个节点上，端口与已分发的客户端配置保持不变。
//
// 与健康检查的自动重连不同：那边优先重连原节点（目标是恢复），
// 这里用户是嫌当前出口 IP 不好用，必须真的换一个。
//
// 换过的节点会记进历史一并避开。只排除"当前这个"是不够的：
// A 换成 B 之后 A 就空出来了，而候选是按速度排的，A 往往又排在最前面，
// 于是再点一次就换回了 A。历史让每次点击都真的换一个没用过的。
func (m *Manager) Swap(slot int) error {
	m.mu.RLock()
	t := m.tunnels[slot]
	m.mu.RUnlock()
	if t == nil {
		return fmt.Errorf("出口不存在")
	}
	if !t.opMu.TryLock() {
		return fmt.Errorf("出口正在操作中")
	}
	if t.Status == "starting" || t.Status == "stopped" {
		t.opMu.Unlock()
		return fmt.Errorf("出口正在连接或已停止")
	}
	if t.selectionPolicy().Mode == "strict" {
		t.opMu.Unlock()
		return fmt.Errorf("严格锁定模式下不能自动换 IP，请先修改模式或手动选用 IP")
	}
	node, err := m.pickSwapTarget(t)
	if err != nil {
		t.opMu.Unlock()
		return err
	}
	p := t.selectionPolicy()
	if p.Mode == "preferred" {
		p.Mode = "auto"
		p.Host = ""
		p.IP = ""
		p.Preferred = Node{}
	}
	m.prepareSwitch(t, node, p, "手动换一个")
	return nil
}

// pickSwapTarget 给"换节点"挑下一个目标，并把该跳过的节点记进历史。
//
// 记两个：换下来的那个，以及刚挑中的这个。挑中的也记是因为真机上踩到过——
// 它连不上时会被候选列表换成别人，但它自己没进历史，
// 于是下次点换节点又从它开始试一遍，白等一轮握手超时。
func (m *Manager) pickSwapTarget(t *Tunnel) (Node, error) {
	avoid := t.swapAvoid()
	picks, err := m.pickNodes(t.Node.CountryCode, 1, avoid)
	if err != nil && len(avoid) > 1 {
		// 这个地区的节点都换过一轮了。清掉历史重新开始，
		// 总比告诉用户"没得换了"好——转一圈之后原来那些节点未必还是当初的状态。
		t.forgetSwaps()
		picks, err = m.pickNodes(t.Node.CountryCode, 1, t.swapAvoid())
	}
	if err != nil {
		return Node{}, err
	}
	t.rememberSwap(t.Node.HostName)
	t.rememberSwap(picks[0].HostName)
	return picks[0], nil
}

// StopAll 停掉所有隧道并清空状态文件。
func (m *Manager) StopAll() {
	for _, t := range m.Tunnels() {
		_ = m.Stop(t.Slot)
	}
}

// SetCred 改一条出口的 SOCKS5 凭据。cred 两个字段都为空表示随机重置。
//
// 改完要通知后端：本机 Xray 的 socks 出站里带着这套凭据，
// 不同步的话面板侧的节点会立刻连不上自己的出口。
func (m *Manager) SetCred(slot int, cred SocksCred) (SocksCred, error) {
	m.mu.RLock()
	t, ok := m.tunnels[slot]
	m.mu.RUnlock()
	if !ok {
		return SocksCred{}, fmt.Errorf("槽位 %d 没有运行中的隧道", slot)
	}

	if cred.User == "" && cred.Pass == "" {
		gen, err := newSocksCred()
		if err != nil {
			return SocksCred{}, err
		}
		cred = gen
	}
	if err := validateCred(cred); err != nil {
		return SocksCred{}, err
	}

	t.setCredential(cred)
	if err := m.saveState(); err != nil {
		log.Printf("保存状态失败: %v", err)
	}
	m.syncCred(t)
	return cred, nil
}

// ReconcileOutbounds 在启动恢复隧道后跑一次，把后端出站对齐到当前隧道（含 SOCKS5 凭据）。
//
// 只为 3x-ui 模式而生：它的 OnTunnelsChanged 是空操作，重启不会重写面板出站，
// 而从旧版本升上来时面板里持久化的 socks 出站没有认证字段，端口一旦要认证就连不上。
// 自建模式恢复时每条隧道 up 都会重建配置，本就自洽，这里跳过免得多重启一次 Xray。
func (m *Manager) ReconcileOutbounds() {
	p, err := openPanel()
	if err != nil || p.Kind() != "3x-ui" {
		return
	}

	// 等隧道尽量都起完再重写一次，避免只覆盖到先 up 的那几条
	deadline := time.Now().Add(90 * time.Second)
	for {
		tunnels := m.Tunnels()
		if len(tunnels) == 0 {
			return
		}
		var up *Tunnel
		settled := true
		for _, t := range tunnels {
			if t.Status == "up" && up == nil {
				up = t
			}
			if t.Status == "starting" {
				settled = false
			}
		}
		if (settled || time.Now().After(deadline)) && up != nil {
			if err := m.resync(up); err != nil {
				log.Printf("启动对账面板出站失败: %v", err)
			}
			return
		}
		if settled || time.Now().After(deadline) {
			return // 全 failed，没有可写的出站
		}
		time.Sleep(2 * time.Second)
	}
}

// syncCred 把新凭据写进后端的 socks 出站。
//
// 两种后端的做法不同：自建模式整份重建配置，3x-ui 模式只改出站那一段。
// 都走 ResyncOutbound，接口语义正好是"重写这条隧道对应的出站"。
func (m *Manager) syncCred(t *Tunnel) {
	if err := m.resync(t); err != nil {
		log.Printf("同步 SOCKS5 凭据到节点链接后端失败: %v", err)
	}
}

// Shutdown 停掉运行态但保留状态文件，让下次启动能恢复同样的隧道。
func (m *Manager) Shutdown() {
	m.proxycheck.stop()
	m.verification.stop()
	for _, t := range m.Tunnels() {
		t.stop()
	}
}

// prepareHost 打开转发开关。netns 出网依赖它。
func prepareHost() error {
	if err := cmdRun(exec.Command("sysctl", "-qw", "net.ipv4.ip_forward=1")); err != nil {
		return fmt.Errorf("开启 ip_forward 失败: %w", err)
	}
	return nil
}

// nodeInUse 判断某节点是否已被别的隧道占用。
func (m *Manager) nodeInUse(host string, exceptSlot int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for slot, t := range m.tunnels {
		if slot != exceptSlot && t.Node.HostName == host {
			return true
		}
	}
	return false
}

// rebind 在隧道换节点后，把原先指向旧节点的 3x-ui 入站改绑到新节点。
// 面板不可用时静默跳过，健康检查本身不应因此失败。
func (m *Manager) rebind(oldHost string, t *Tunnel) error {
	x, err := openPanel()
	if err != nil {
		return nil
	}
	return x.Rebind(oldHost, t, m.Tunnels())
}

// resync 在节点没换但重连过之后，把 3x-ui 的出站配置刷新一遍。
// 面板不可用时静默跳过，健康检查本身不应因此失败。
func (m *Manager) resync(t *Tunnel) error {
	x, err := openPanel()
	if err != nil {
		return nil
	}
	return x.ResyncOutbound(t, m.Tunnels())
}

// notifyPanel 告诉后端隧道集合变了。
//
// 自建模式下出站是由隧道列表现算出来的，不通知的话新开的出口在 Xray 里
// 没有对应的 socks 出站，绑定会指向一个不存在的 tag。接管 3x-ui 时是空操作。
// 后端不可用不该让开关出口失败，所以只记日志。
func (m *Manager) notifyPanel() {
	p, err := openPanel()
	if err != nil {
		return
	}
	if err := p.OnTunnelsChanged(m.Tunnels()); err != nil {
		log.Printf("同步节点链接后端失败: %v", err)
	}
}
