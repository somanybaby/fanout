package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func protectedInbound(id int) bool {
	for _, p := range getWebSettings().ProtectedInboundIDs {
		if p == id {
			return true
		}
	}
	return false
}

// Keep management API and blocking rules ahead of house egress rules. Existing
// non-fanout rules keep their relative order and contents.
func prioritizeFanoutRules(rules []any) []any {
	var api, managed, other, guards []any
	tags := map[string]bool{}
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		ob, _ := m["outboundTag"].(string)
		if strings.HasPrefix(ob, xuiTagPrefix) {
			for _, tag := range anyStrings(m["inboundTag"]) {
				tags[tag] = true
			}
		}
	}
	for _, r := range rules {
		m, ok := r.(map[string]any)
		if !ok {
			other = append(other, r)
			continue
		}
		ob, _ := m["outboundTag"].(string)
		switch {
		case ob == "api":
			api = append(api, r)
		case strings.HasPrefix(ob, xuiTagPrefix):
			managed = append(managed, r)
		default:
			other = append(other, r)
		}
		if ob != "block" && ob != "blocked" {
			continue
		}
		allowed := anyStrings(m["inboundTag"])
		selected := []string{}
		for tag := range tags {
			if len(allowed) == 0 || containsString(allowed, tag) {
				selected = append(selected, tag)
			}
		}
		sort.Strings(selected)
		if len(selected) == 0 {
			continue
		}
		scoped := map[string]any{}
		for k, v := range m {
			scoped[k] = v
		}
		scoped["inboundTag"] = toAnySliceSafety(selected)
		duplicate := false
		for _, g := range guards {
			if equalJSON(g, scoped) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			guards = append(guards, scoped)
		}
	}
	// Only copies scoped to house inbounds move forward. Unrelated original
	// rules remain in exactly the same order, preserving direct-node policy.
	kept := []any{}
	for _, r := range other {
		isGuard := false
		for _, g := range guards {
			if equalJSON(r, g) {
				isGuard = true
				break
			}
		}
		if !isGuard {
			kept = append(kept, r)
		}
	}
	return append(append(append(api, guards...), managed...), kept...)
}

func anyStrings(v any) []string {
	if s, ok := v.([]string); ok {
		return s
	}
	var out []string
	if a, ok := v.([]any); ok {
		for _, v := range a {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
func containsString(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func runtimeMatchesTemplate(setting map[string]any) bool {
	blob, err := os.ReadFile("/usr/local/x-ui/bin/config.json")
	if err != nil {
		return false
	}
	var actual map[string]any
	if json.Unmarshal(blob, &actual) != nil {
		return false
	}
	return templateRuntimeMatches(actual, setting)
}

func templateRuntimeMatches(actual, setting map[string]any) bool {
	// Runtime JSON numbers decode as float64; constructed SOCKS ports are int.
	// Compare canonical JSON values, so equivalent ports do not trigger rollback.
	return equalJSON(actual["routing"], setting["routing"]) && equalJSON(actual["outbounds"], setting["outbounds"])
}

// 3x-ui 3.7 applies a successful template update to the in-memory core, but
// does not persist that hot snapshot to config.json. Confirm its documented
// synchronous API contract and unchanged core PID instead of trusting a stale
// file. Unknown panel versions keep the conservative file-confirmation path.
func knownSynchronousHotAPI() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := cmdOutput(exec.CommandContext(ctx, xuiBinary, "-v"))
	return err == nil && strings.TrimSpace(string(out)) == "3.7.0"
}

func coreCommand(args []byte) bool {
	for _, p := range bytes.Split(args, []byte{0}) {
		if string(p) == "bin/config.json" || string(p) == "/usr/local/x-ui/bin/config.json" {
			return true
		}
	}
	return false
}

func corePIDs() []string {
	dirs, _ := os.ReadDir("/proc")
	var pids []string
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		args, err := os.ReadFile(filepath.Join("/proc", d.Name(), "cmdline"))
		if err == nil && coreCommand(args) {
			pids = append(pids, d.Name())
		}
	}
	sort.Strings(pids)
	return pids
}

func onlyHotFieldsChanged(old, next map[string]any) bool {
	a := map[string]any{}
	b := map[string]any{}
	for k, v := range old {
		if k != "routing" && k != "outbounds" {
			a[k] = v
		}
	}
	for k, v := range next {
		if k != "routing" && k != "outbounds" {
			b[k] = v
		}
	}
	return equalJSON(a, b)
}

func (x *XUI) validateTemplate(setting map[string]any) error {
	blob, err := os.ReadFile("/usr/local/x-ui/bin/config.json")
	if err != nil {
		return fmt.Errorf("无法读取 3x-ui 运行配置，拒绝盲目重载")
	}
	var runtime map[string]any
	if json.Unmarshal(blob, &runtime) != nil {
		return fmt.Errorf("运行配置解析失败")
	}
	for _, key := range []string{"outbounds", "routing", "dns"} {
		if v, ok := setting[key]; ok {
			runtime[key] = v
		}
	}
	bin, err := findXray(x.workDir)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(x.workDir, "config-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "runtime.json")
	blob, _ = json.Marshal(runtime)
	if err := os.WriteFile(path, blob, 0600); err != nil {
		return err
	}
	if _, err := cmdOutput(exec.Command(bin, "run", "-test", "-c", path)); err != nil {
		return fmt.Errorf("候选配置校验失败，原配置未修改")
	}
	return nil
}

func (x *XUI) sharedProtectedClient(email string) (bool, error) {
	for _, id := range getWebSettings().ProtectedInboundIDs {
		raw, err := x.rawInbound(id)
		if err != nil {
			return false, err
		}
		emails, err := clientEmails(raw)
		if err != nil {
			return false, err
		}
		for _, v := range emails {
			if v == email {
				return true, nil
			}
		}
	}
	return false, nil
}

func (x *XUI) backupTemplate(setting map[string]any) error {
	if x.workDir == "" {
		return nil
	}
	dir := filepath.Join(x.workDir, "config-backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	blob, _ := json.MarshalIndent(setting, "", "  ")
	return os.WriteFile(filepath.Join(dir, time.Now().UTC().Format("20060102T150405.000000000Z")+".json"), blob, 0600)
}

func toAnySliceSafety(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
