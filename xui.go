package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// XUI 对接本机的 3x-ui 面板。
// 面板端口与 webBasePath 都是安装时随机生成的，这里从 x-ui 命令行读出来，
// API token 同样由 `x-ui setting -getApiToken` 提供（没有时它会自动生成一个）。
type XUI struct {
	Port     int    `json:"port"`
	BasePath string `json:"base_path"`
	Scheme   string `json:"scheme"`
	token    string
	client   *http.Client
}

// base 返回面板的本地访问前缀。
func (x *XUI) base() string {
	return fmt.Sprintf("%s://127.0.0.1:%d%s", x.Scheme, x.Port, x.BasePath)
}

const xuiBinary = "/usr/local/x-ui/x-ui"

// 每次调用 `x-ui setting -getApiToken` 面板都会新生成一个 token，
// 所以进程内缓存复用，避免把面板的 token 列表撑爆。
var (
	cachedToken   string
	cachedTokenMu sync.Mutex
)

var (
	reXUIPort  = regexp.MustCompile(`(?m)^port:\s*(\d+)`)
	reXUIBase  = regexp.MustCompile(`(?m)^webBasePath:\s*(\S+)`)
	// 只认 "apiToken: xxx" 这一行，避免匹配到提示文字里的长单词
	reXUIToken = regexp.MustCompile(`(?m)^apiToken:\s*([A-Za-z0-9]+)`)
	// 面板开了 TLS 时 setting -show 会打印 "Panel is secure with SSL"
	reXUISSL = regexp.MustCompile(`(?i)panel is secure with ssl`)
	// 兜底：证书路径非空也说明启用了 TLS
	reXUICert = regexp.MustCompile(`(?m)^cert:\s*\S+`)
)

// DetectXUI 探测本机 3x-ui。未安装或未运行时返回错误。
func DetectXUI() (*XUI, error) {
	if err := exec.Command("systemctl", "is-active", "--quiet", "x-ui").Run(); err != nil {
		return nil, fmt.Errorf("本机未安装或未运行 3x-ui")
	}

	out, err := exec.Command(xuiBinary, "setting", "-show").Output()
	if err != nil {
		return nil, fmt.Errorf("读取面板设置失败: %w", err)
	}
	text := string(out)

	scheme := "http"
	if reXUISSL.MatchString(text) {
		scheme = "https"
	} else if out, err := exec.Command(xuiBinary, "setting", "-getCert").Output(); err == nil {
		if reXUICert.MatchString(string(out)) {
			scheme = "https"
		}
	}

	pm := reXUIPort.FindStringSubmatch(text)
	bm := reXUIBase.FindStringSubmatch(text)
	if pm == nil || bm == nil {
		return nil, fmt.Errorf("无法从面板设置中解析端口或路径")
	}
	var port int
	fmt.Sscanf(pm[1], "%d", &port)

	cachedTokenMu.Lock()
	defer cachedTokenMu.Unlock()
	if cachedToken != "" {
		return &XUI{
			Port:     port,
			BasePath: strings.TrimSuffix(bm[1], "/"),
			Scheme:   scheme,
			token:    cachedToken,
			client:   localClient(),
		}, nil
	}

	// 没有 token 时这条命令会自动生成一个
	tokOut, err := exec.Command(xuiBinary, "setting", "-getApiToken").Output()
	if err != nil {
		return nil, fmt.Errorf("获取 API token 失败: %w", err)
	}
	tm := reXUIToken.FindStringSubmatch(string(tokOut))
	if tm == nil {
		return nil, fmt.Errorf("未能取得 API token")
	}
	token := tm[1]

	cachedToken = token
	return &XUI{
		Port:     port,
		BasePath: strings.TrimSuffix(bm[1], "/"),
		Scheme:   scheme,
		token:    token,
		client:   localClient(),
	}, nil
}

// localClient 用于访问本机面板。
//
// 面板启用 TLS 时证书通常签给公网 IP 或域名，而我们走的是 127.0.0.1，
// 校验必然失败。这是同一台机器上的进程间调用，不经过网络，
// 没有中间人风险，所以跳过证书校验。
func localClient() *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //#nosec G402 -- 仅用于 127.0.0.1
		},
	}
}

// post 调用面板 API。v3.5.0 里 Bearer token 只对 /panel/api/ 前缀生效。
func (x *XUI) post(path string, form url.Values) ([]byte, error) {
	return x.call(http.MethodPost, path, form)
}

// get 调用面板的只读 API。inbounds/list 是 GET。
func (x *XUI) get(path string) ([]byte, error) {
	return x.call(http.MethodGet, path, nil)
}

func (x *XUI) call(method, path string, form url.Values) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/%s", x.base(), strings.TrimPrefix(path, "/"))

	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+x.token)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := x.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用 %s 失败: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("调用 %s 返回 HTTP %d", path, resp.StatusCode)
	}

	var envelope struct {
		Success bool            `json:"success"`
		Msg     string          `json:"msg"`
		Obj     json.RawMessage `json:"obj"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("解析 %s 响应失败: %w", path, err)
	}
	if !envelope.Success {
		return nil, fmt.Errorf("面板返回失败: %s", envelope.Msg)
	}
	return envelope.Obj, nil
}

// xrayConfig 是 /panel/api/xray/ 返回的结构。
// 注意 obj 本身是一个 JSON 字符串，要二次解析。
type xrayConfig struct {
	OutboundTestURL string          `json:"outboundTestUrl"`
	XraySetting     json.RawMessage `json:"xraySetting"`
}

// loadXray 读取当前 Xray 配置模板。
func (x *XUI) loadXray() (map[string]any, string, error) {
	obj, err := x.post("panel/api/xray/", nil)
	if err != nil {
		return nil, "", err
	}

	// obj 是被再次编码成字符串的 JSON
	var inner string
	if err := json.Unmarshal(obj, &inner); err != nil {
		return nil, "", fmt.Errorf("解析 Xray 配置外层失败: %w", err)
	}
	var cfg xrayConfig
	if err := json.Unmarshal([]byte(inner), &cfg); err != nil {
		return nil, "", fmt.Errorf("解析 Xray 配置失败: %w", err)
	}

	var setting map[string]any
	if err := json.Unmarshal(cfg.XraySetting, &setting); err != nil {
		return nil, "", fmt.Errorf("解析 xraySetting 失败: %w", err)
	}
	return setting, cfg.OutboundTestURL, nil
}

// saveXray 写回 Xray 配置模板并让面板重启 Xray。
func (x *XUI) saveXray(setting map[string]any, testURL string) error {
	blob, err := json.Marshal(setting)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("xraySetting", string(blob))
	if testURL != "" {
		form.Set("outboundTestUrl", testURL)
	}
	if _, err := x.post("panel/api/xray/update", form); err != nil {
		return err
	}

	// 只写模板不够：面板要重载 Xray 才会用新的 outbounds 与 routing 生成运行配置，
	// 否则路由改动看起来保存成功了，实际流量还按旧规则走。
	if _, err := x.post("panel/api/server/restartXrayService", nil); err != nil {
		return fmt.Errorf("配置已保存但重载 Xray 失败: %w", err)
	}
	return nil
}


// Inbound 是面板里已有的一个入站。
type Inbound struct {
	ID       int    `json:"id"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Remark   string `json:"remark"`
	Enable   bool   `json:"enable"`
	Tag      string `json:"tag"`      // Xray 里的 inboundTag
	BoundTo  string `json:"bound_to"` // 已绑定的节点主机名，空表示未绑定
	BoundUp  bool   `json:"bound_up"` // 绑定的节点当前是否有运行中的隧道
}

// Inbounds 列出面板里已有的入站。
func (x *XUI) Inbounds(live map[string]bool) ([]Inbound, error) {
	obj, err := x.get("panel/api/inbounds/list")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID       int    `json:"id"`
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
		Remark   string `json:"remark"`
		Enable   bool   `json:"enable"`
		Stream   json.RawMessage `json:"streamSettings"`
	}
	if err := json.Unmarshal(obj, &raw); err != nil {
		return nil, fmt.Errorf("解析入站列表失败: %w", err)
	}

	bound, err := x.boundInbounds()
	if err != nil {
		return nil, err
	}

	out := make([]Inbound, 0, len(raw))
	for _, r := range raw {
		tag := inboundTag(r.Port, r.Stream)
		out = append(out, Inbound{
			ID: r.ID, Port: r.Port, Protocol: r.Protocol,
			Remark: r.Remark, Enable: r.Enable,
			Tag: tag, BoundTo: bound[tag], BoundUp: live[bound[tag]],
		})
	}
	return out, nil
}

// inboundTag 复原 3x-ui 给入站生成的 Xray tag，格式是 in-<端口>-<网络>。
// streamSettings 在不同接口下有时是 JSON 对象、有时是被编码过的字符串。
func inboundTag(port int, streamSettings json.RawMessage) string {
	network := "tcp"
	if len(streamSettings) > 0 {
		raw := streamSettings
		var asString string
		if json.Unmarshal(raw, &asString) == nil {
			raw = json.RawMessage(asString)
		}
		var st struct {
			Network string `json:"network"`
		}
		if json.Unmarshal(raw, &st) == nil && st.Network != "" {
			network = st.Network
		}
	}
	return fmt.Sprintf("in-%d-%s", port, network)
}

// 出站与路由规则统一带这个前缀，便于识别与清理，不碰用户手工加的条目。
const xuiTagPrefix = "fanout-"

// tunnelTag 用节点主机名而非槽位号做标识：槽位在 fanout 重启后会重新分配，
// 用它做 tag 会让已有的入站绑定悄悄串到别的节点上。
func tunnelTag(t *Tunnel) string {
	return xuiTagPrefix + sanitizeTag(t.Node.HostName)
}

// sanitizeTag 把主机名收敛成安全的 tag 片段。
func sanitizeTag(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

// boundInbounds 返回 inboundTag -> 隧道槽位 的当前绑定关系。
func (x *XUI) boundInbounds() (map[string]string, error) {
	setting, _, err := x.loadXray()
	if err != nil {
		return nil, err
	}
	bound := map[string]string{}
	routing, _ := setting["routing"].(map[string]any)
	if routing == nil {
		return bound, nil
	}
	rules, _ := routing["rules"].([]any)
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := m["outboundTag"].(string)
		if !strings.HasPrefix(tag, xuiTagPrefix) {
			continue
		}
		host := strings.TrimPrefix(tag, xuiTagPrefix)
		// 早期版本用槽位号做 tag，这类规则在重启后必然指向错误的节点，直接忽略
		if isAllDigits(host) {
			continue
		}
		for _, it := range toStringSlice(m["inboundTag"]) {
			bound[it] = host
		}
	}
	return bound, nil
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Bind 把某个入站的流量导向指定隧道。slot 传 0 表示解绑，恢复直连。
//
// 只动 fanout- 前缀的出站与规则，用户手工配置的条目原样保留。
func (x *XUI) Bind(inboundTag string, hostname string, tunnels []*Tunnel) error {
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

	live := map[string]bool{}
	for _, t := range tunnels {
		if t.Status == "up" {
			live[sanitizeTag(t.Node.HostName)] = true
		}
	}
	current, err := x.Inbounds(live)
	if err != nil {
		return err
	}
	knownTags := map[string]bool{}
	for _, ib := range current {
		knownTags[ib.Tag] = true
	}

	setting, testURL, err := x.loadXray()
	if err != nil {
		return err
	}

	x.syncOutbounds(setting, tunnels)

	routing, _ := setting["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{}
	}
	rules, _ := routing["rules"].([]any)

	// 先摘掉这个入站现有的 fanout 绑定，再按需要重新加一条
	cleaned := make([]any, 0, len(rules)+1)
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			cleaned = append(cleaned, r)
			continue
		}
		outTag, _ := m["outboundTag"].(string)
		if !strings.HasPrefix(outTag, xuiTagPrefix) {
			cleaned = append(cleaned, r)
			continue
		}
		// 顺便丢掉不再存在的入站标签（如换过端口后残留的旧规则）
		remain := []any{}
		for _, it := range toStringSlice(m["inboundTag"]) {
			if it != inboundTag && knownTags[it] {
				remain = append(remain, it)
			}
		}
		if len(remain) > 0 {
			m["inboundTag"] = remain
			cleaned = append(cleaned, m)
		}
	}

	if target != nil {
		cleaned = append(cleaned, map[string]any{
			"type":        "field",
			"inboundTag":  []any{inboundTag},
			"outboundTag": tunnelTag(target),
		})
	}

	routing["rules"] = cleaned
	setting["routing"] = routing
	return x.saveXray(setting, testURL)
}

// syncOutbounds 让 fanout- 出站与当前已连通的隧道保持一致。
func (x *XUI) syncOutbounds(setting map[string]any, tunnels []*Tunnel) {
	outbounds, _ := setting["outbounds"].([]any)
	kept := make([]any, 0, len(outbounds))
	for _, ob := range outbounds {
		m, ok := ob.(map[string]any)
		if !ok {
			kept = append(kept, ob)
			continue
		}
		tag, _ := m["tag"].(string)
		if !strings.HasPrefix(tag, xuiTagPrefix) {
			kept = append(kept, ob)
		}
	}
	for _, t := range tunnels {
		if t.Status != "up" {
			continue
		}
		kept = append(kept, map[string]any{
			"tag":      tunnelTag(t),
			"protocol": "socks",
			"settings": map[string]any{
				"servers": []any{map[string]any{
					"address": "127.0.0.1",
					"port":    t.Port,
				}},
			},
		})
	}
	setting["outbounds"] = kept
}

// CloneToTunnels 以某个入站为模板，为每条指定隧道复制一个入站并绑定到对应出口。
//
// 复制时必须换掉端口、备注，以及客户端的 id/email —— 这些在面板里要求唯一。
// 返回新建入站的端口列表。
func (x *XUI) CloneToTunnels(templateID int, hosts []string, tunnels []*Tunnel) ([]int, error) {
	raw, err := x.rawInbound(templateID)
	if err != nil {
		return nil, err
	}

	byHost := map[string]*Tunnel{}
	for _, t := range tunnels {
		byHost[t.Node.HostName] = t
	}

	used, err := x.usedPorts()
	if err != nil {
		return nil, err
	}

	emails, err := clientEmails(raw)
	if err != nil {
		return nil, err
	}

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

		clone, err := cloneInboundPayload(raw, port, t)
		if err != nil {
			return created, err
		}
		newID, err := x.addInbound(clone)
		if err != nil {
			return created, fmt.Errorf("复制到端口 %d 失败: %w", port, err)
		}
		if len(emails) > 0 {
			if err := x.attachClients(emails, newID); err != nil {
				return created, err
			}
		}
		created = append(created, port)

		if err := x.Bind(inboundTagOf(port, raw), t.Node.HostName, tunnels); err != nil {
			return created, fmt.Errorf("端口 %d 绑定失败: %w", port, err)
		}
	}
	return created, nil
}

// rawInbound 取回某个入站的原始 JSON，用作复制模板。
func (x *XUI) rawInbound(id int) (map[string]any, error) {
	obj, err := x.get("panel/api/inbounds/list")
	if err != nil {
		return nil, err
	}
	var list []map[string]any
	if err := json.Unmarshal(obj, &list); err != nil {
		return nil, fmt.Errorf("解析入站列表失败: %w", err)
	}
	for _, m := range list {
		if int(toFloat(m["id"])) == id {
			return m, nil
		}
	}
	return nil, fmt.Errorf("入站 %d 不存在", id)
}

func toFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

// usedPorts 收集面板里已占用的入站端口。
func (x *XUI) usedPorts() (map[int]bool, error) {
	list, err := x.Inbounds(nil)
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, ib := range list {
		used[ib.Port] = true
	}
	return used, nil
}

func inboundTagOf(port int, template map[string]any) string {
	stream, _ := json.Marshal(template["streamSettings"])
	return inboundTag(port, stream)
}

// cloneInboundPayload 按模板构造一个新入站的提交体。
func cloneInboundPayload(tpl map[string]any, port int, t *Tunnel) (map[string]any, error) {
	settings, err := asObject(tpl["settings"])
	if err != nil {
		return nil, fmt.Errorf("解析模板 settings 失败: %w", err)
	}

	base := strings.TrimSpace(fmt.Sprint(tpl["remark"]))
	label := exitLabel(t)
	if base != "" {
		label = base + "-" + label
	}

	// 客户端不重新生成：建成空入站后用 attach 把模板的客户端挂过来，
	// 这样同一套 UUID 能走所有出口，客户端那边只改端口即可。
	settings["clients"] = []any{}

	stream, err := asObject(tpl["streamSettings"])
	if err != nil {
		return nil, fmt.Errorf("解析模板 streamSettings 失败: %w", err)
	}
	sniff, err := asObject(tpl["sniffing"])
	if err != nil {
		sniff = map[string]any{"enabled": true, "destOverride": []any{"http", "tls"}}
	}

	return map[string]any{
		"enable":         true,
		"remark":         label,
		"listen":         fmt.Sprint(orEmpty(tpl["listen"])),
		"port":           port,
		"protocol":       fmt.Sprint(tpl["protocol"]),
		"expiryTime":     0,
		"total":          0,
		"settings":       mustJSON(settings),
		"streamSettings": mustJSON(stream),
		"sniffing":       mustJSON(sniff),
		"allocate":       mustJSON(map[string]any{}),
	}, nil
}

// exitLabel 给复制出来的入站起个好认的名字：地区 + 出口 IP 末段。
// 同一地区可能有多条隧道，带上末段才能区分。
func exitLabel(t *Tunnel) string {
	region := t.Node.CountryCode
	if region == "" {
		region = t.Node.Country
	}

	suffix := t.Node.HostName
	if t.ExitIP != "" {
		if i := strings.LastIndex(t.ExitIP, "."); i >= 0 {
			suffix = t.ExitIP[i+1:]
		} else {
			suffix = t.ExitIP
		}
	}

	if region == "" {
		return suffix
	}
	return region + "-" + suffix
}

// asObject 兼容字段是对象或是被编码成字符串的两种情况。
func asObject(v any) (map[string]any, error) {
	switch t := v.(type) {
	case map[string]any:
		return t, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, fmt.Errorf("无法解析为对象")
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// addInbound 通过面板 API 新建一个入站，返回新入站的 id。这个端点收 JSON 体。
func (x *XUI) addInbound(payload map[string]any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	endpoint := x.base() + "/panel/api/inbounds/add"
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+x.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := x.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var envelope struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		Obj     struct {
			ID int `json:"id"`
		} `json:"obj"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return 0, fmt.Errorf("解析响应失败: %s", strings.TrimSpace(string(raw)))
	}
	if !envelope.Success {
		return 0, fmt.Errorf("%s", envelope.Msg)
	}
	return envelope.Obj.ID, nil
}

// attachClients 把模板入站上的客户端挂到新建的入站，实现一套凭据走多个出口。
func (x *XUI) attachClients(emails []string, inboundID int) error {
	for _, email := range emails {
		body, err := json.Marshal(map[string]any{"inboundIds": []int{inboundID}})
		if err != nil {
			return err
		}
		endpoint := fmt.Sprintf("%s/panel/api/clients/%s/attach", x.base(), url.PathEscape(email))
		req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+x.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := x.client.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var envelope struct {
			Success bool   `json:"success"`
			Msg     string `json:"msg"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return fmt.Errorf("解析 attach 响应失败: %s", strings.TrimSpace(string(raw)))
		}
		if !envelope.Success {
			return fmt.Errorf("挂载客户端 %s 失败: %s", email, envelope.Msg)
		}
	}
	return nil
}

// clientEmails 取出模板入站上所有客户端的 email，用于 attach。
func clientEmails(tpl map[string]any) ([]string, error) {
	settings, err := asObject(tpl["settings"])
	if err != nil {
		return nil, fmt.Errorf("解析模板 settings 失败: %w", err)
	}
	clients, _ := settings["clients"].([]any)
	out := []string{}
	for _, c := range clients {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if email, _ := cm["email"].(string); email != "" {
			out = append(out, email)
		}
	}
	return out, nil
}

// InboundDetail 是某个入站的完整信息，用于在 fanout 里直接查看而不必跳到面板。
type InboundDetail struct {
	Inbound
	Clients []ClientInfo `json:"clients"`
	Links   []string     `json:"links"`
	Listen  string       `json:"listen"`
	Network string       `json:"network"`
	TLS     string       `json:"tls"`
}

type ClientInfo struct {
	Email  string `json:"email"`
	ID     string `json:"id"`
	Enable bool   `json:"enable"`
}

// InboundDetail 取一个入站的详情，含客户端与分享链接。
// 分享链接里面板会写 localhost，这里换成实际可连的地址。
func (x *XUI) InboundDetail(id int, publicHost string) (*InboundDetail, error) {
	raw, err := x.rawInbound(id)
	if err != nil {
		return nil, err
	}

	settings, err := asObject(raw["settings"])
	if err != nil {
		return nil, fmt.Errorf("解析 settings 失败: %w", err)
	}
	stream, _ := asObject(raw["streamSettings"])

	bound, err := x.boundInbounds()
	if err != nil {
		return nil, err
	}

	streamJSON, _ := json.Marshal(raw["streamSettings"])
	port := int(toFloat(raw["port"]))
	tag := inboundTag(port, streamJSON)

	detail := &InboundDetail{
		Inbound: Inbound{
			ID:       id,
			Port:     port,
			Protocol: fmt.Sprint(raw["protocol"]),
			Remark:   fmt.Sprint(raw["remark"]),
			Enable:   raw["enable"] == true,
			Tag:      tag,
			BoundTo:  bound[tag],
		},
		Listen: fmt.Sprint(orEmpty(raw["listen"])),
	}
	if stream != nil {
		detail.Network = fmt.Sprint(orEmpty(stream["network"]))
		detail.TLS = fmt.Sprint(orEmpty(stream["security"]))
	}

	clients, _ := settings["clients"].([]any)
	for _, c := range clients {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		info := ClientInfo{
			Email:  fmt.Sprint(orEmpty(cm["email"])),
			ID:     fmt.Sprint(orEmpty(cm["id"])),
			Enable: cm["enable"] != false,
		}
		detail.Clients = append(detail.Clients, info)

		if links, err := x.clientLinks(info.Email); err == nil {
			for _, l := range links {
				if strings.Contains(l, fmt.Sprintf(":%d?", port)) || strings.Contains(l, fmt.Sprintf(":%d#", port)) {
					detail.Links = append(detail.Links, strings.Replace(l, "@localhost:", "@"+publicHost+":", 1))
				}
			}
		}
	}
	return detail, nil
}

// clientLinks 取某个客户端在所有入站上的分享链接。
func (x *XUI) clientLinks(email string) ([]string, error) {
	obj, err := x.get("panel/api/clients/links/" + url.PathEscape(email))
	if err != nil {
		return nil, err
	}
	var links []string
	if err := json.Unmarshal(obj, &links); err != nil {
		return nil, err
	}
	return links, nil
}

// InboundLinks 批量取多个入站的分享链接，用于一次性导出。
func (x *XUI) InboundLinks(ids []int, publicHost string) ([]string, error) {
	var out []string
	for _, id := range ids {
		detail, err := x.InboundDetail(id, publicHost)
		if err != nil {
			return out, err
		}
		out = append(out, detail.Links...)
	}
	return out, nil
}

// Rebind 把原本绑到 oldHost 的入站改绑到新节点上。
// 隧道换节点后出站 tag 会变，需要同步路由规则。
func (x *XUI) Rebind(oldHost string, target *Tunnel, tunnels []*Tunnel) error {
	list, err := x.Inbounds(nil)
	if err != nil {
		return err
	}
	oldTag := sanitizeTag(oldHost)
	for _, ib := range list {
		if ib.BoundTo != oldTag {
			continue
		}
		if err := x.Bind(ib.Tag, target.Node.HostName, tunnels); err != nil {
			return err
		}
	}
	return nil
}

// isAllDigits 判断 tag 后缀是不是纯数字（早期用槽位号命名出站留下的遗留格式）。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
