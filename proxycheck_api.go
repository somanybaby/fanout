package main

import (
	"encoding/json"
	"net/http"
)

func apiProxycheck(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var in struct {
				Enabled *bool   `json:"enabled"`
				APIKey  *string `json:"api_key"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&in) != nil {
				writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
			if err := m.proxycheck.configure(in.Enabled, in.APIKey); err != nil {
				writeJSON(w, 409, map[string]string{"error": err.Error()})
				return
			}
		} else if r.Method != http.MethodGet {
			writeJSON(w, 405, map[string]string{"error": "使用 GET 或 POST"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, m.proxycheck.status())
	}
}
