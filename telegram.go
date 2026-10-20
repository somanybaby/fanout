package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type telegramNotice struct {
	ID       string    `json:"id"`
	Text     string    `json:"text"`
	Attempts int       `json:"attempts"`
	NextTry  time.Time `json:"next_try"`
}

type telegramData struct {
	Enabled    bool              `json:"enabled"`
	Token      string            `json:"bot_token"` // Private file only; never returned by the API.
	ChatID     string            `json:"chat_id"`
	LastEvents map[string]string `json:"last_events"`
	Pending    []telegramNotice  `json:"pending"`
	LastSentAt time.Time         `json:"last_sent_at"`
	LastError  string            `json:"last_error"`
}

type telegramStore struct {
	mu      sync.Mutex
	sendMu  sync.Mutex
	data    telegramData
	path    string
	loadErr error
	wake    chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	send    func(context.Context, string, string, string) (time.Duration, error)
}

func newTelegramStore(dir string) *telegramStore {
	s := &telegramStore{wake: make(chan struct{}, 1), send: sendTelegram}
	if dir != "" {
		s.path = filepath.Join(dir, "telegram.json")
		b, err := os.ReadFile(s.path)
		if err == nil {
			s.loadErr = json.Unmarshal(b, &s.data)
		} else if !os.IsNotExist(err) {
			s.loadErr = err
		}
	}
	if s.data.LastEvents == nil {
		s.data.LastEvents = map[string]string{}
	}
	return s
}

func (s *telegramStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	return atomicPrivateJSON(s.path, s.data)
}

func (s *telegramStore) status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.data.LastError
	if s.loadErr != nil {
		err = "Telegram 配置不可读，通知已暂停"
	}
	return map[string]any{"enabled": s.data.Enabled, "token_configured": s.data.Token != "", "chat_id": s.data.ChatID, "pending": len(s.data.Pending), "last_sent_at": s.data.LastSentAt, "last_error": err, "timezone": "UTC+8"}
}

var telegramTokenPattern = regexp.MustCompile(`^[0-9]{5,20}:[A-Za-z0-9_-]{20,128}$`)
var telegramChatPattern = regexp.MustCompile(`^(-?[1-9][0-9]{0,19}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

func (s *telegramStore) configure(enabled *bool, token, chatID *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return fmt.Errorf("Telegram 配置不可读，未覆盖现有文件")
	}
	next := s.data
	if token != nil && strings.TrimSpace(*token) != "" {
		next.Token = strings.TrimSpace(*token)
		if !telegramTokenPattern.MatchString(next.Token) {
			return fmt.Errorf("Bot Token 格式不正确")
		}
	}
	if chatID != nil {
		next.ChatID = strings.TrimSpace(*chatID)
		if next.ChatID != "" && !telegramChatPattern.MatchString(next.ChatID) {
			return fmt.Errorf("请填写数字 Chat ID 或 @频道用户名")
		}
	}
	if enabled != nil {
		next.Enabled = *enabled
	}
	if next.Enabled && (next.Token == "" || next.ChatID == "") {
		return fmt.Errorf("启用前请先保存 Bot Token 和 Chat ID")
	}
	if !next.Enabled || next.ChatID != s.data.ChatID || next.Token != s.data.Token {
		next.Pending = nil // Do not deliver earlier events to a new recipient/bot.
	}
	next.LastError = ""
	if s.path != "" {
		if err := atomicPrivateJSON(s.path, next); err != nil {
			return fmt.Errorf("无法保存 Telegram 设置")
		}
	}
	s.data = next
	s.signal()
	return nil
}

func (s *telegramStore) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

var telegramTimeZone = time.FixedZone("UTC+8", 8*60*60)

func telegramTime(t time.Time) string {
	return t.In(telegramTimeZone).Format("2006-01-02 15:04:05") + "（UTC+8）"
}

func telegramExitText(t *Tunnel, oldIP, reason string, test bool) string {
	p := t.selectionPolicy()
	t.mu.Lock()
	risk, identity := t.Risk, t.Identity
	t.mu.Unlock()
	score := "未知（未取得此 IP 的有效评分）"
	if risk.IP == t.ExitIP && risk.Score != nil && !math.IsNaN(*risk.Score) && *risk.Score >= 0 && *risk.Score <= 100 {
		score = fmt.Sprintf("%g / 100", *risk.Score)
	}
	next := "未启用定时轮换"
	if p.Mode == "strict" {
		next = "严格锁定，不定时更换 IP"
	} else if p.RotateMinutes > 0 && !t.Since.IsZero() {
		next = telegramTime(t.Since.Add(time.Duration(p.RotateMinutes) * time.Minute))
	}
	title := "Fanout 出口 IP 已更换"
	change := "原 IP：" + oldIP + "\n新 IP（当前）：" + t.ExitIP
	if test {
		title = "Fanout Telegram 测试通知（未更换 IP）"
		change = "当前 IP：" + t.ExitIP
	}
	if len([]rune(reason)) > 160 {
		reason = string([]rune(reason)[:160])
	}
	region := identity.Region
	if len([]rune(region)) > 64 {
		region = string([]rune(region)[:64])
	}
	return fmt.Sprintf("%s\n出口：%d（%s / %s）\n%s\n新出口 IPPure 风险分：%s\n原因：%s\n通知时间：%s\n下次计划轮换：%s\n说明：风险升高或连接故障可能提前；无合格候选时可能延后。", title, t.Slot, p.Country, region, change, score, reason, telegramTime(time.Now()), next)
}

// Called only after validation and successful inbound binding. No HTTP request
// or score lookup runs on the connection handoff path.
func (m *Manager) notifyIPChange(t *Tunnel, oldIP, reason string) {
	if m.telegram == nil {
		return
	}
	if t.Status != "up" || !t.egressReady.Load() || t.selectionPolicy().ProbeOnly || oldIP == "" || oldIP == t.ExitIP || net.ParseIP(oldIP) == nil || net.ParseIP(t.ExitIP) == nil {
		return
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s", t.Slot, oldIP, t.ExitIP, t.Since.UTC().Format(time.RFC3339Nano)))))
	text := telegramExitText(t, oldIP, reason, false)
	s := m.telegram
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil || !s.data.Enabled || s.data.Token == "" || s.data.ChatID == "" || s.data.LastEvents[fmt.Sprint(t.Slot)] == id {
		return
	}
	if len(s.data.Pending) >= 100 {
		s.data.LastError = "Telegram 待发送队列已满，本次通知未入队"
		return
	}
	s.data.Pending = append(s.data.Pending, telegramNotice{ID: id, Text: text})
	s.data.LastEvents[fmt.Sprint(t.Slot)] = id
	if err := s.saveLocked(); err != nil {
		s.data.LastError = "无法保存 Telegram 待发送通知"
		return
	}
	s.signal()
}

func sendTelegram(ctx context.Context, token, chatID, text string) (time.Duration, error) {
	body, _ := json.Marshal(map[string]any{"chat_id": chatID, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("Telegram 请求失败")
	}
	req.Header.Set("Content-Type", "application/json")
	client := mainHTTPClient(10 * time.Second)
	defer client.CloseIdleConnections()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("Telegram 网络请求失败")
	} // Never return URL errors containing the token.
	defer resp.Body.Close()
	var result struct {
		OK         bool `json:"ok"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return 0, fmt.Errorf("Telegram 响应无效")
	}
	if resp.StatusCode == 429 && result.Parameters.RetryAfter > 0 {
		seconds := result.Parameters.RetryAfter
		if seconds > 86400 {
			seconds = 86400
		}
		wait := time.Duration(seconds) * time.Second
		return wait, fmt.Errorf("Telegram 发送限频")
	}
	if resp.StatusCode != 200 || !result.OK {
		return 0, fmt.Errorf("Telegram 未接受通知，请检查 Token、Chat ID 或先向机器人发送 /start")
	}
	return 0, nil
}

func (s *telegramStore) deliverOnce(ctx context.Context) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	if s.loadErr != nil || !s.data.Enabled || len(s.data.Pending) == 0 || time.Now().Before(s.data.Pending[0].NextTry) {
		s.mu.Unlock()
		return
	}
	n := s.data.Pending[0]
	token, chat, send := s.data.Token, s.data.ChatID, s.send
	s.mu.Unlock()
	wait, err := send(ctx, token, chat, n.Text)
	if ctx.Err() != nil {
		return
	} // Shutdown keeps the pending attempt for restart.
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.Pending) == 0 || s.data.Pending[0].ID != n.ID || s.data.Token != token || s.data.ChatID != chat {
		return
	}
	if err == nil {
		s.data.Pending = s.data.Pending[1:]
		s.data.LastSentAt = time.Now().UTC()
		s.data.LastError = ""
	} else {
		s.data.LastError = "Telegram 通知发送失败，请检查网络、Token、Chat ID 或先向机器人发送 /start"
		n.Attempts++
		if n.Attempts >= 3 {
			s.data.Pending = s.data.Pending[1:]
		} else {
			backoff := 15 * time.Second
			if n.Attempts == 2 {
				backoff = time.Minute
			}
			if wait > backoff {
				backoff = wait
			}
			n.NextTry = time.Now().Add(backoff)
			s.data.Pending[0] = n
		}
	}
	if s.saveLocked() != nil {
		s.data.LastError = "无法保存 Telegram 发送结果"
	}
}

func (s *telegramStore) start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	done := s.done
	s.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.deliverOnce(ctx)
			case <-s.wake:
				s.deliverOnce(ctx)
			}
		}
	}()
}

func (s *telegramStore) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func apiTelegram(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var in struct {
				Enabled *bool   `json:"enabled"`
				Token   *string `json:"bot_token"`
				ChatID  *string `json:"chat_id"`
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			d.DisallowUnknownFields()
			if d.Decode(&in) != nil {
				writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
				return
			}
			if err := m.telegram.configure(in.Enabled, in.Token, in.ChatID); err != nil {
				writeJSON(w, 409, map[string]string{"error": err.Error()})
				return
			}
		} else if r.Method != http.MethodGet {
			writeJSON(w, 405, map[string]string{"error": "使用 GET 或 POST"})
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, m.telegram.status())
	}
}

func apiTelegramTest(m *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "使用 POST"})
			return
		}
		s := m.telegram
		s.sendMu.Lock()
		defer s.sendMu.Unlock()
		s.mu.Lock()
		token, chat, send, loadErr := s.data.Token, s.data.ChatID, s.send, s.loadErr
		s.mu.Unlock()
		if loadErr != nil || token == "" || chat == "" {
			writeJSON(w, 409, map[string]string{"error": "请先保存 Bot Token 和 Chat ID"})
			return
		}
		text := "Fanout Telegram 测试通知（未更换 IP）\n当前无可读取的稳定出口信息，可能正在连接或切换。"
		for _, t := range m.Tunnels() {
			if t.opMu.TryLock() {
				if t.Status == "up" && !t.selectionPolicy().ProbeOnly {
					text = telegramExitText(t, "", "测试发送", true)
					t.opMu.Unlock()
					break
				}
				t.opMu.Unlock()
			}
		}
		_, err := send(r.Context(), token, chat, text)
		s.mu.Lock()
		if s.data.Token != token || s.data.ChatID != chat {
			s.mu.Unlock()
			writeJSON(w, 409, map[string]string{"error": "发送期间 Telegram 配置已更新，请重新测试"})
			return
		}
		if err == nil {
			s.data.LastSentAt = time.Now().UTC()
			s.data.LastError = ""
		} else {
			s.data.LastError = "Telegram 测试发送失败，请检查 Token、Chat ID、网络或先发送 /start"
		}
		_ = s.saveLocked()
		s.mu.Unlock()
		if err != nil {
			writeJSON(w, 502, map[string]string{"error": "Telegram 测试发送失败，请检查 Token、Chat ID、网络或先发送 /start"})
			return
		}
		writeJSON(w, 200, m.telegram.status())
	}
}
