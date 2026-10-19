package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func apiVerification(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := false
		if r.Method == http.MethodPost {
			var in struct {
				Action string `json:"action"`
				Hourly bool   `json:"hourly"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&in) != nil {
				writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
			var err error
			switch in.Action {
			case "start":
				started, err = m.enqueueVerification(time.Now(), false)
			case "schedule":
				err = m.verification.configureHourly(in.Hourly, time.Now())
			case "cancel":
				err = m.verification.cancel()
			default:
				writeJSON(w, 400, map[string]string{"error": "请选择开始、停止或定时设置"})
				return
			}
			if err != nil {
				writeJSON(w, 409, map[string]string{"error": err.Error()})
				return
			}
		} else if r.Method != http.MethodGet {
			writeJSON(w, 405, map[string]string{"error": "使用 GET 或 POST"})
			return
		}
		state := m.verification.snapshot()
		writeJSON(w, 200, map[string]any{"state": state, "started": started, "scope": "connectivity_and_exit_identity", "notice": "全部国家候选串行核验，日本优先；上一轮未结束不叠加。不批量查询 IPPure；连接通过不等于风险分合格。不改变正在使用的节点。"})
	}
}
