package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Settings 是可以在界面里改、并落盘持久化的运行时配置。
// 命令行 flag 仍是初始默认值：首次启动用 flag 建档，之后以文件为准，
// 界面改动即时生效并覆盖。
type Settings struct {
	// PublicIP 是母机公网 IPv4，用于分享链接与 SOCKS5 地址；空表示自动探测。
	PublicIP string `json:"public_ip"`
	// AutoReconnect 决定出口掉线后是否自动换节点重连；关掉就停在原地不动。
	AutoReconnect bool `json:"auto_reconnect"`
}

var (
	settingsMu   sync.RWMutex
	settingsCur  Settings
	settingsPath string
)

func settingsFilePath(dir string) string { return filepath.Join(dir, "settings.json") }

// loadSettings 读盘并生效。文件不存在时用传入的默认值建档。
// initialIP 来自 -ip / FANOUT_PUBLIC_IP，仅在没有存档时作为初值。
func loadSettings(dir, initialIP string) error {
	settingsPath = settingsFilePath(dir)

	s := Settings{PublicIP: strings.TrimSpace(initialIP), AutoReconnect: true}
	blob, err := os.ReadFile(settingsPath)
	switch {
	case os.IsNotExist(err):
		// 首次启动：用默认值建档
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(blob, &s); err != nil {
			return err
		}
	}

	applySettings(s)
	if os.IsNotExist(err) {
		return saveSettings()
	}
	return nil
}

// applySettings 把配置写进内存并同步到依赖它的子系统。
func applySettings(s Settings) {
	settingsMu.Lock()
	settingsCur = s
	settingsMu.Unlock()
	setPublicIPOverride(s.PublicIP)
}

// getSettings 返回当前配置的副本。
func getSettings() Settings {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return settingsCur
}

// autoReconnectEnabled 供健康检查判断是否要自动换节点。
func autoReconnectEnabled() bool {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return settingsCur.AutoReconnect
}

// updateSettings 校验后落盘并即时生效。
func updateSettings(s Settings) error {
	s.PublicIP = strings.TrimSpace(s.PublicIP)
	if s.PublicIP != "" {
		ip := net.ParseIP(s.PublicIP)
		if ip == nil || ip.To4() == nil {
			return errInvalidPublicIP
		}
	}
	applySettings(s)
	return saveSettings()
}

func saveSettings() error {
	settingsMu.RLock()
	blob, err := json.MarshalIndent(settingsCur, "", "  ")
	settingsMu.RUnlock()
	if err != nil {
		return err
	}
	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, blob, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, settingsPath)
}

var errInvalidPublicIP = &settingsError{"母机公网地址必须是合法的 IPv4"}

type settingsError struct{ msg string }

func (e *settingsError) Error() string { return e.msg }
