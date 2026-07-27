package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// nativeClient 是一个可连接的客户端凭据。
// 复制入站时同一个 client 会挂到所有出口上，用户换出口只需要改端口。
type nativeClient struct {
	Email    string `json:"email"`
	ID       string `json:"id"`       // vless/vmess 用 UUID
	Password string `json:"password"` // trojan 用密码
	Enable   bool   `json:"enable"`
}

// nativeInbound 是自建模式下的一个入站。
//
// 字段刻意贴着 3x-ui 的入站语义，这样两种后端在界面上表现一致。
type nativeInbound struct {
	ID       int            `json:"id"`
	Port     int            `json:"port"`
	Protocol string         `json:"protocol"` // vless | vmess | trojan
	Network  string         `json:"network"`  // tcp | ws
	Path     string         `json:"path"`     // ws 路径
	Remark   string         `json:"remark"`
	Enable   bool           `json:"enable"`
	Clients  []nativeClient `json:"clients"`
	// BoundTo 是绑定的节点主机名经 sanitizeTag 后的形式，空表示直连
	BoundTo string `json:"bound_to"`
}

// tag 复原这个入站在 Xray 里的 inboundTag，格式与 3x-ui 保持一致。
func (n *nativeInbound) tag() string {
	return fmt.Sprintf("in-%d-%s", n.Port, n.netOrTCP())
}

func (n *nativeInbound) netOrTCP() string {
	if n.Network == "" {
		return "tcp"
	}
	return n.Network
}

// nativeStore 是自建模式的持久状态。
type nativeStore struct {
	NextID   int              `json:"next_id"`
	Inbounds []*nativeInbound `json:"inbounds"`
}

func nativeStatePath(dir string) string { return filepath.Join(dir, "native.json") }

func loadNativeStore(dir string) (*nativeStore, error) {
	blob, err := os.ReadFile(nativeStatePath(dir))
	if os.IsNotExist(err) {
		return &nativeStore{NextID: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var st nativeStore
	if err := json.Unmarshal(blob, &st); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", nativeStatePath(dir), err)
	}
	if st.NextID < 1 {
		st.NextID = 1
	}
	return &st, nil
}

func (s *nativeStore) save(dir string) error {
	blob, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := nativeStatePath(dir) + ".tmp"
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, nativeStatePath(dir))
}

func (s *nativeStore) byID(id int) *nativeInbound {
	for _, ib := range s.Inbounds {
		if ib.ID == id {
			return ib
		}
	}
	return nil
}

func (s *nativeStore) usedPorts() map[int]bool {
	used := map[int]bool{}
	for _, ib := range s.Inbounds {
		used[ib.Port] = true
	}
	return used
}

// sorted 返回按端口排序的入站，让界面顺序稳定。
func (s *nativeStore) sorted() []*nativeInbound {
	out := make([]*nativeInbound, len(s.Inbounds))
	copy(out, s.Inbounds)
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// newUUID 生成 Xray 认的 UUID v4。
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// 随机源不可用时退回一个仍然唯一的形式，避免建站直接失败
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", os.Getpid())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}

// randomHex 生成 n 字节的随机十六进制串，用作 trojan 密码与 ws 路径。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprint(os.Getpid())))
	}
	return hex.EncodeToString(b)
}
