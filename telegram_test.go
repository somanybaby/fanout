package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const telegramTestToken = "123456:abcdefghijklmnopqrstuvwxyz_12345"

func telegramFixture(t *testing.T) (*Manager, *Tunnel) {
	t.Helper()
	m, tn := selectionFixture(t)
	tn.egressReady.Store(true)
	tn.ExitIP = "2.2.2.2"
	tn.Since = time.Date(2026, 10, 20, 13, 30, 0, 0, time.UTC)
	tn.Policy.RotateMinutes = 1440
	tn.Identity = ExitIdentity{IP: tn.ExitIP, Country: "JP", Region: "Tokyo"}
	score := 2.0
	tn.Risk = RiskAssessment{IP: tn.ExitIP, Score: &score}
	token, chat, enabled := telegramTestToken, "123456789", true
	if err := m.telegram.configure(&enabled, &token, &chat); err != nil {
		t.Fatal(err)
	}
	return m, tn
}

func TestTelegramChangeMessageQueueAndSecretRedaction(t *testing.T) {
	m, tn := telegramFixture(t)
	calls := 0
	m.telegram.send = func(context.Context, string, string, string) (time.Duration, error) { calls++; return 0, nil }
	m.notifyIPChange(tn, "1.1.1.1", "定时轮换")
	m.notifyIPChange(tn, "1.1.1.1", "定时轮换")
	if calls != 0 || len(m.telegram.data.Pending) != 1 {
		t.Fatal("handoff sent synchronously or duplicated the event")
	}
	text := m.telegram.data.Pending[0].Text
	for _, expected := range []string{"原 IP：1.1.1.1", "新 IP（当前）：2.2.2.2", "IPPure 风险分：2 / 100", "2026-10-21 21:30:00（UTC+8）", "风险升高或连接故障可能提前"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("notification missing %q", expected)
		}
	}
	loaded := newTelegramStore(m.workDir)
	if loaded.loadErr != nil || len(loaded.data.Pending) != 1 || loaded.data.Token != telegramTestToken {
		t.Fatal("pending notification or configuration did not survive restart")
	}
	info, _ := os.Stat(loaded.path)
	if info == nil || info.Mode().Perm() != 0600 {
		t.Fatal("Telegram secret configuration is not private")
	}
	w := httptest.NewRecorder()
	apiTelegram(m)(w, httptest.NewRequest("GET", "/api/telegram", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), telegramTestToken) || strings.Contains(w.Body.String(), "bot_token") {
		t.Fatal("Telegram API exposed the bot token")
	}
	m.telegram.deliverOnce(context.Background())
	if calls != 1 || len(m.telegram.data.Pending) != 0 || m.telegram.data.LastSentAt.IsZero() {
		t.Fatal("queued notification did not deliver once")
	}
}

func TestTelegramOnlyNotifiesRealReadyChanges(t *testing.T) {
	m, tn := telegramFixture(t)
	for _, old := range []string{"", tn.ExitIP, "not-an-IP"} {
		m.notifyIPChange(tn, old, "test")
	}
	tn.Policy.ProbeOnly = true
	m.notifyIPChange(tn, "1.1.1.1", "trial")
	tn.Policy.ProbeOnly = false
	tn.Status = "starting"
	m.notifyIPChange(tn, "1.1.1.1", "not bound")
	tn.Status = "up"
	tn.egressReady.Store(false)
	m.notifyIPChange(tn, "1.1.1.1", "binding failed")
	tn.egressReady.Store(true)
	if len(m.telegram.data.Pending) != 0 {
		t.Fatal("trial, restart with same IP or failed handoff generated a notification")
	}
	tn.Policy.Mode = "strict"
	tn.Risk.IP = "9.9.9.9"
	text := telegramExitText(tn, "1.1.1.1", "manual", false)
	if !strings.Contains(text, "严格锁定，不定时更换 IP") || !strings.Contains(text, "未知（未取得此 IP 的有效评分）") {
		t.Fatal("message invented a score or scheduled time")
	}
}

func TestTelegramFailureRetriesBoundedlyWithoutAffectingExit(t *testing.T) {
	m, tn := telegramFixture(t)
	calls := 0
	m.telegram.send = func(context.Context, string, string, string) (time.Duration, error) {
		calls++
		return 2 * time.Minute, fmt.Errorf("secret URL /bot%s", telegramTestToken)
	}
	m.notifyIPChange(tn, "1.1.1.1", "test")
	m.telegram.deliverOnce(context.Background())
	m.telegram.deliverOnce(context.Background())
	if calls != 1 || time.Until(m.telegram.data.Pending[0].NextTry) < 119*time.Second {
		t.Fatal("notification ignored retry delay")
	}
	for i := 0; i < 2; i++ {
		m.telegram.data.Pending[0].NextTry = time.Time{}
		m.telegram.deliverOnce(context.Background())
	}
	b, _ := json.Marshal(m.telegram.status())
	if calls != 3 || len(m.telegram.data.Pending) != 0 || strings.Contains(string(b), telegramTestToken) || tn.Status != "up" || tn.ExitIP != "2.2.2.2" || !tn.egressReady.Load() {
		t.Fatal("failed notification leaked credentials, retried without bound or changed the exit")
	}
}

func TestTelegramTestMessageAndCredentialUpdates(t *testing.T) {
	m, tn := telegramFixture(t)
	m.telegram.send = func(_ context.Context, token, chat, text string) (time.Duration, error) {
		if token != telegramTestToken || chat != "123456789" || !strings.Contains(text, "测试通知（未更换 IP）") || !strings.Contains(text, "当前 IP：2.2.2.2") {
			t.Fatal("test message used wrong credentials or pretended an IP change")
		}
		return 0, nil
	}
	w := httptest.NewRecorder()
	apiTelegramTest(m)(w, httptest.NewRequest("POST", "/api/telegram/test", nil))
	if w.Code != 200 || tn.ExitIP != "2.2.2.2" || strings.Contains(w.Body.String(), telegramTestToken) {
		t.Fatal("test failed or modified the exit")
	}
	empty := ""
	if err := m.telegram.configure(nil, &empty, nil); err != nil || m.telegram.data.Token != telegramTestToken {
		t.Fatal("blank token cleared the saved token")
	}
	bad := "bad-token"
	if err := m.telegram.configure(nil, &bad, nil); err == nil || m.telegram.data.Token != telegramTestToken {
		t.Fatal("invalid token replacement changed the working configuration")
	}
	m.notifyIPChange(tn, "1.1.1.1", "test")
	disabled := false
	if err := m.telegram.configure(&disabled, nil, nil); err != nil || len(m.telegram.data.Pending) != 0 {
		t.Fatal("disabling notifications left old messages queued")
	}
}
