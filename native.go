package main

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// Native 是 fanout 自己跑 Xray 的后端，用在本机没装 3x-ui 的场合。
//
// 入站数据存在 native.json，Xray 的运行配置每次改动后整份重新生成。
// 全量重写比增量改省心：配置是纯函数产物，不会出现改了一半的中间态。
type Native struct {
	mu    sync.Mutex
	dir   string
	store *nativeStore
	proc  *xrayProc
}

func openNative(workDir string) (*Native, error) {
	if workDir == "" {
		return nil, fmt.Errorf("自建模式缺少工作目录")
	}
	bin, err := findXray(workDir)
	if err != nil {
		return nil, err
	}
	store, err := loadNativeStore(workDir)
	if err != nil {
		return nil, err
	}
	n := &Native{
		dir:   workDir,
		store: store,
		proc:  &xrayProc{bin: bin, dir: workDir},
	}
	// 上次进程被强杀时遗留的 Xray 还占着入站端口，先收掉
	n.proc.reapOrphan()
	return n, nil
}

func (n *Native) Kind() string { return "native" }

func (n *Native) Describe() string {
	return fmt.Sprintf("fanout 自建 Xray（%s）", n.proc.bin)
}

// apply 重新生成配置并重启 Xray，然后落盘。
// 调用方必须已持有 n.mu。
func (n *Native) apply(tunnels []*Tunnel) error {
	cfg := buildXrayConfig(n.store.sorted(), tunnels)
	path, err := writeXrayConfig(n.dir, cfg)
	if err != nil {
		return err
	}
	if err := verifyXrayConfig(n.proc.bin, path); err != nil {
		return err
	}
	// 没有入站时不必留着进程占资源
	if len(cfg["inbounds"].([]any)) == 0 {
		n.proc.stop()
		return n.store.save(n.dir)
	}
	if err := n.proc.restart(path); err != nil {
		return err
	}
	return n.store.save(n.dir)
}

// OnTunnelsChanged 在隧道集合变化后重建配置。自建模式下出站直接由隧道列表
// 推导，所以隧道一变就要重新生成，否则新出口没有对应的 socks 出站。
func (n *Native) OnTunnelsChanged(tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.apply(tunnels)
}

// Close 停掉自己拉起的 Xray。
func (n *Native) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.proc.stop()
}

func (n *Native) Inbounds(live map[string]bool) ([]Inbound, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	list := n.store.sorted()
	out := make([]Inbound, 0, len(list))
	for _, ib := range list {
		out = append(out, Inbound{
			ID: ib.ID, Port: ib.Port, Protocol: ib.Protocol,
			Remark: ib.Remark, Enable: ib.Enable, Tag: ib.tag(),
			BoundTo: ib.BoundTo, BoundUp: live[ib.BoundTo],
		})
	}
	return out, nil
}

func (n *Native) InboundDetail(id int, publicHost string) (*InboundDetail, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	ib := n.store.byID(id)
	if ib == nil {
		return nil, fmt.Errorf("入站 %d 不存在", id)
	}
	detail := &InboundDetail{
		Inbound: Inbound{
			ID: ib.ID, Port: ib.Port, Protocol: ib.Protocol,
			Remark: ib.Remark, Enable: ib.Enable, Tag: ib.tag(),
			BoundTo: ib.BoundTo,
		},
		Listen:  "0.0.0.0",
		Network: ib.netOrTCP(),
		TLS:     "none",
	}
	for _, c := range ib.Clients {
		id := c.ID
		if ib.Protocol == "trojan" {
			id = c.Password
		}
		detail.Clients = append(detail.Clients, ClientInfo{Email: c.Email, ID: id, Enable: c.Enable})
		detail.Links = append(detail.Links, shareLink(ib, c, publicHost))
	}
	return detail, nil
}

func (n *Native) InboundLinks(ids []int, publicHost string) ([]string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	var out []string
	for _, id := range ids {
		ib := n.store.byID(id)
		if ib == nil {
			continue
		}
		for _, c := range ib.Clients {
			out = append(out, shareLink(ib, c, publicHost))
		}
	}
	return out, nil
}

func (n *Native) Bind(inboundTag string, hostname string, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	var target *Tunnel
	if hostname != "" {
		for _, t := range tunnels {
			if t.Node.HostName == hostname {
				target = t
				break
			}
		}
		if target == nil {
			return fmt.Errorf("节点 %s 没有运行中的隧道", hostname)
		}
		if target.Status != "up" {
			return fmt.Errorf("节点 %s 的隧道还没连通（当前 %s）", hostname, target.Status)
		}
	}

	var found *nativeInbound
	for _, ib := range n.store.Inbounds {
		if ib.tag() == inboundTag {
			found = ib
			break
		}
	}
	if found == nil {
		return fmt.Errorf("入站 %s 不存在", inboundTag)
	}

	if target == nil {
		found.BoundTo = ""
	} else {
		found.BoundTo = sanitizeTag(target.Node.HostName)
	}
	return n.apply(tunnels)
}

func (n *Native) Rebind(oldHost string, target *Tunnel, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	oldTag := sanitizeTag(oldHost)
	newTag := sanitizeTag(target.Node.HostName)
	newLabel := exitLabel(target)
	for _, ib := range n.store.Inbounds {
		if ib.BoundTo != oldTag {
			continue
		}
		ib.BoundTo = newTag
		// 备注里带着旧出口的地区和 IP 尾段，换了节点要跟着改
		ib.Remark = renameExitSuffix(ib.Remark, newLabel)
	}
	return n.apply(tunnels)
}

func (n *Native) ResyncOutbound(t *Tunnel, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.apply(tunnels)
}

// CloneToTunnels 以某个入站为模板，为每条指定隧道复制一个入站并绑好出口。
//
// 客户端凭据整套沿用模板：同一个 UUID 能走所有出口，用户只改端口。
func (n *Native) CloneToTunnels(templateID int, hosts []string, tunnels []*Tunnel) ([]int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	tpl := n.store.byID(templateID)
	if tpl == nil {
		return nil, fmt.Errorf("模板入站 %d 不存在", templateID)
	}

	byHost := map[string]*Tunnel{}
	for _, t := range tunnels {
		byHost[t.Node.HostName] = t
	}

	used := n.store.usedPorts()
	created := []int{}
	for _, host := range hosts {
		t := byHost[host]
		if t == nil || t.Status != "up" {
			continue
		}
		port, err := freeRandomPort(used)
		if err != nil {
			return created, err
		}
		used[port] = true

		clone := &nativeInbound{
			ID:       n.store.NextID,
			Port:     port,
			Protocol: tpl.Protocol,
			Network:  tpl.Network,
			Path:     tpl.Path,
			Remark:   cloneRemark(tpl.Remark, exitLabel(t)),
			Enable:   true,
			Clients:  append([]nativeClient(nil), tpl.Clients...),
			BoundTo:  sanitizeTag(t.Node.HostName),
		}
		n.store.NextID++
		n.store.Inbounds = append(n.store.Inbounds, clone)
		created = append(created, port)
	}

	if len(created) == 0 {
		return created, fmt.Errorf("没有可用的隧道")
	}
	if err := n.apply(tunnels); err != nil {
		return created, err
	}
	return created, nil
}

func (n *Native) DeleteInbounds(ids []int, tunnels []*Tunnel) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	drop := map[int]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	kept := make([]*nativeInbound, 0, len(n.store.Inbounds))
	for _, ib := range n.store.Inbounds {
		if !drop[ib.ID] {
			kept = append(kept, ib)
		}
	}
	n.store.Inbounds = kept
	return n.apply(tunnels)
}

// NewInboundSpec 是自建模式下新建入站的参数。
type NewInboundSpec struct {
	Protocol string
	Network  string
	Port     int
	Remark   string
	Path     string
}

// nativeProtocols 是自建模式支持的协议，与前端下拉保持一致。
var nativeProtocols = map[string]bool{"vless": true, "vmess": true, "trojan": true}

// CreateInbound 新建一个入站，端口留空时随机分配。
func (n *Native) CreateInbound(spec NewInboundSpec, tunnels []*Tunnel) (*nativeInbound, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	proto := strings.ToLower(strings.TrimSpace(spec.Protocol))
	if proto == "" {
		proto = "vless"
	}
	if !nativeProtocols[proto] {
		return nil, fmt.Errorf("不支持的协议 %q", spec.Protocol)
	}
	network := strings.ToLower(strings.TrimSpace(spec.Network))
	if network == "" {
		network = "tcp"
	}
	if network != "tcp" && network != "ws" {
		return nil, fmt.Errorf("不支持的传输方式 %q", spec.Network)
	}

	used := n.store.usedPorts()
	port := spec.Port
	if port == 0 {
		p, err := freeRandomPort(used)
		if err != nil {
			return nil, err
		}
		port = p
	} else if used[port] {
		return nil, fmt.Errorf("端口 %d 已被别的入站占用", port)
	}

	path := spec.Path
	if network == "ws" && path == "" {
		path = "/" + randomHex(6)
	}

	remark := strings.TrimSpace(spec.Remark)
	if remark == "" {
		remark = fmt.Sprintf("%s-%d", proto, port)
	}

	ib := &nativeInbound{
		ID:       n.store.NextID,
		Port:     port,
		Protocol: proto,
		Network:  network,
		Path:     path,
		Remark:   remark,
		Enable:   true,
		Clients: []nativeClient{{
			Email:    fmt.Sprintf("%s-%d", proto, port),
			ID:       newUUID(),
			Password: randomHex(8),
			Enable:   true,
		}},
	}
	n.store.NextID++
	n.store.Inbounds = append(n.store.Inbounds, ib)

	if err := n.apply(tunnels); err != nil {
		// 起不来就别把坏入站留在库里
		n.store.Inbounds = n.store.Inbounds[:len(n.store.Inbounds)-1]
		n.store.NextID--
		_ = n.apply(tunnels)
		return nil, err
	}
	return ib, nil
}

// cloneRemark 给复制出来的入站起名，与 3x-ui 模式同一套规则。
func cloneRemark(base, label string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return label
	}
	return base + "-" + label
}

// shareLink 生成客户端可直接导入的分享链接。
func shareLink(ib *nativeInbound, c nativeClient, host string) string {
	q := url.Values{}
	q.Set("type", ib.netOrTCP())
	q.Set("security", "none")
	if ib.netOrTCP() == "ws" {
		q.Set("path", ib.Path)
	}
	frag := url.PathEscape(ib.Remark)

	switch ib.Protocol {
	case "trojan":
		return fmt.Sprintf("trojan://%s@%s:%d?%s#%s", c.Password, host, ib.Port, q.Encode(), frag)
	case "vmess":
		// vmess 的 base64 形式各家客户端解析不一，用通用的 URI 形式
		q.Set("encryption", "auto")
		return fmt.Sprintf("vmess://%s@%s:%d?%s#%s", c.ID, host, ib.Port, q.Encode(), frag)
	default:
		q.Set("encryption", "none")
		return fmt.Sprintf("vless://%s@%s:%d?%s#%s", c.ID, host, ib.Port, q.Encode(), frag)
	}
}
