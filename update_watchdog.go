package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func scheduleUpdateWatchdog(backup, expected string) error {
	if !hasCmd("systemd-run") || !dirExists("/run/systemd/system") {
		return fmt.Errorf("自动回滚需要 systemd；此系统请使用手动备份升级")
	}
	dir := strings.TrimSuffix(webSettingsPath, "/settings.json")
	unit := "fanout-update-guard-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	// Run the known working previous executable, even if the new executable fails.
	cmd := exec.Command("systemd-run", "--quiet", "--unit", unit, "--on-active=60s", backup, "-update-watchdog", expected, "-dir", dir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("无法建立自动回滚任务，原程序未修改")
	}
	return nil
}

func updateWatchdog(expected, dir string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if !strings.HasSuffix(self, ".rollback") {
		return fmt.Errorf("回滚监测必须由备份程序运行")
	}
	target := strings.TrimSuffix(self, ".rollback")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	out, verErr := exec.CommandContext(ctx, target, "-version").Output()
	cancel()
	cfg, loadErr := loadWebSettings(dir, 8899, false)
	healthy := verErr == nil && strings.TrimSpace(string(out)) == "fanout "+expected && loadErr == nil
	if healthy {
		host := cfg.ListenAddr
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		client := &http.Client{Timeout: 5 * time.Second}
		base, _ := os.ReadFile(filepath.Join(dir, "basepath"))
		prefix := strings.Trim(strings.TrimSpace(string(base)), "/")
		resp, err := client.Get("http://" + host + ":" + strconv.Itoa(cfg.Port) + "/" + prefix + "/")
		healthy = err == nil && resp.StatusCode == http.StatusOK
		if resp != nil {
			resp.Body.Close()
		}
	}
	if healthy {
		fmt.Println("fanout: 更新后服务验证通过")
		return nil
	}
	staged := target + ".restore"
	if err := copyFileMode(self, staged, 0755); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}
	fmt.Println("fanout: 新版服务验证失败，已恢复上一版程序")
	return exec.Command("systemctl", "restart", "fanout").Run()
}
