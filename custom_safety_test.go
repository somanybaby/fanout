package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSyncOutboundsPreservesDirectAndBlocksStopped(t *testing.T) {
	direct := map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIPv6"}}
	before, _ := json.Marshal(direct)
	setting := map[string]any{"outbounds": []any{direct, map[string]any{"tag": "fanout-old", "protocol": "socks"}}, "routing": map[string]any{"rules": []any{map[string]any{"inboundTag": []any{"house"}, "outboundTag": "fanout-old"}}}}
	(&XUI{}).syncOutbounds(setting, nil)
	after, _ := json.Marshal(direct)
	if string(before) != string(after) {
		t.Fatal("unrelated direct settings were changed")
	}
	outs := setting["outbounds"].([]any)
	if len(outs) != 2 || outs[1].(map[string]any)["protocol"] != "blackhole" {
		t.Fatal("stopped binding must remain blocked")
	}
}

func TestFanoutPriorityPreservesAPIBlocksAndDirectRules(t *testing.T) {
	api := map[string]any{"inboundTag": []any{"api"}, "outboundTag": "api"}
	block := map[string]any{"ip": []any{"geoip:private"}, "outboundTag": "blocked"}
	google := map[string]any{"domain": []any{"domain:google.com"}, "outboundTag": "warp"}
	house := map[string]any{"inboundTag": []any{"house"}, "outboundTag": "fanout-jp"}
	out := prioritizeFanoutRules([]any{api, google, block, house})
	if !reflect.DeepEqual(prioritizeFanoutRules(out), out) {
		t.Fatal("priority rewrite is not idempotent")
	}
	if !reflect.DeepEqual(out, []any{api, map[string]any{"ip": []any{"geoip:private"}, "outboundTag": "blocked", "inboundTag": []any{"house"}}, house, google, block}) {
		t.Fatalf("unsafe rule order: %+v", out)
	}
}

func TestCatalogKeepsAdditionalCountriesAndExpiresOldEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	jp := Node{HostName: "jp", CountryCode: "JP", Config: "remote 1.2.3.4 1194"}
	de := Node{HostName: "de", CountryCode: "DE", Config: "remote 2.3.4.5 1194"}
	mergeCatalog(dir, []Node{jp}, now)
	merged := mergeCatalog(dir, []Node{de}, now.Add(time.Hour))
	if len(merged) != 2 {
		t.Fatal("refresh discarded another country")
	}
	merged = mergeCatalog(dir, nil, now.Add(8*time.Hour))
	if len(merged) != 0 {
		t.Fatal("expired nodes retained")
	}
}

func TestQualityPrefersWorkingNodeOverAdvertisedBandwidth(t *testing.T) {
	q := newQualityStore(t.TempDir())
	q.record("fast-dead", false, 0)
	q.record("working", true, 100)
	nodes := q.rank([]Node{{HostName: "fast-dead", SpeedMbps: 1000}, {HostName: "working", SpeedMbps: 100}})
	if nodes[0].HostName != "working" {
		t.Fatal("dead advertised-fast node selected first")
	}
}

func TestDiagnosticPreservesRealityFieldsAndUsesLocalEntry(t *testing.T) {
	cfg, err := diagnosticClientConfig("vless://test-id@1.2.3.4:1234?security=reality&type=tcp&sni=example.com&pbk=public&sid=abcd&flow=xtls-rprx-vision", 12345)
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(cfg)
	var decoded map[string]any
	_ = json.Unmarshal(blob, &decoded)
	out := decoded["outbounds"].([]any)[0].(map[string]any)
	v := out["settings"].(map[string]any)["vnext"].([]any)[0].(map[string]any)
	if v["address"] != "127.0.0.1" || v["port"] != float64(1234) {
		t.Fatal("diagnostic must target existing local entry")
	}
	if v["users"].([]any)[0].(map[string]any)["flow"] != "xtls-rprx-vision" {
		t.Fatal("flow lost")
	}
}
