package main

import (
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const vpngateAPI = "https://www.vpngate.net/api/iphone/"

// Node 是一个 VPN Gate 节点。
type Node struct {
	HostName    string  `json:"hostname"`
	IP          string  `json:"ip"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Ping        int     `json:"ping"`
	SpeedMbps   float64 `json:"speed_mbps"`
	Sessions    int     `json:"sessions"`
	Config      string  `json:"-"` // 解码后的 .ovpn 内容
}

// fetchNodes 拉取并解析 VPN Gate 的节点列表。
// 返回的列表已按速度降序排列。
func fetchNodes(timeout time.Duration) ([]Node, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(vpngateAPI)
	if err != nil {
		return nil, fmt.Errorf("拉取节点列表失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取节点列表失败: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取节点列表失败: %w", err)
	}
	return parseNodeCSV(string(raw))
}

// parseNodeCSV 解析 VPN Gate 的 CSV。首行是 "*vpn_servers"，
// 第二行是以 '#' 开头的表头，末行是 "*"。
func parseNodeCSV(body string) ([]Node, error) {
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		kept = append(kept, strings.TrimPrefix(line, "#"))
	}
	if len(kept) < 2 {
		return nil, fmt.Errorf("节点列表格式异常: 有效行不足")
	}

	r := csv.NewReader(strings.NewReader(strings.Join(kept, "\n")))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("解析节点 CSV 失败: %w", err)
	}

	header := records[0]
	idx := map[string]int{}
	for i, name := range header {
		idx[strings.TrimSpace(name)] = i
	}
	need := []string{"HostName", "IP", "CountryLong", "CountryShort", "Ping", "Speed", "OpenVPN_ConfigData_Base64"}
	for _, k := range need {
		if _, ok := idx[k]; !ok {
			return nil, fmt.Errorf("节点列表缺少字段 %s", k)
		}
	}

	var nodes []Node
	for _, rec := range records[1:] {
		get := func(k string) string {
			i := idx[k]
			if i >= len(rec) {
				return ""
			}
			return rec[i]
		}
		cfgB64 := get("OpenVPN_ConfigData_Base64")
		if cfgB64 == "" || get("HostName") == "" {
			continue
		}
		cfg, err := base64.StdEncoding.DecodeString(cfgB64)
		if err != nil {
			continue
		}
		ping, _ := strconv.Atoi(get("Ping"))
		speed, _ := strconv.ParseFloat(get("Speed"), 64)
		sessions, _ := strconv.Atoi(get("NumVpnSessions"))
		nodes = append(nodes, Node{
			HostName:    get("HostName"),
			IP:          get("IP"),
			Country:     get("CountryLong"),
			CountryCode: get("CountryShort"),
			Ping:        ping,
			SpeedMbps:   speed / 1e6,
			Sessions:    sessions,
			Config:      string(cfg),
		})
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("节点列表为空")
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].SpeedMbps > nodes[j].SpeedMbps })
	return nodes, nil
}
