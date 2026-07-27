package main

import (
	"encoding/json"
	"testing"
)

func TestRenameExitSuffix(t *testing.T) {
	cases := []struct{ remark, label, want string }{
		{"KR-248", "JP-132", "JP-132"},
		{"线路A-KR-248", "JP-132", "线路A-JP-132"},
		{"inbound-47525-KR-248", "JP-132", "inbound-47525-JP-132"},
		{"无格式", "JP-132", "无格式"},
		{"", "JP-132", ""},
	}
	for _, c := range cases {
		got := renameExitSuffix(c.remark, c.label)
		if got != c.want {
			t.Errorf("renameExitSuffix(%q) = %q, want %q", c.remark, got, c.want)
		}
	}
}

func TestResolvedInboundTagPrefersAPITag(t *testing.T) {
	stream := json.RawMessage(`{"network":"ws"}`)
	got := resolvedInboundTag("in-12080-tcp", 12080, stream)
	if got != "in-12080-tcp" {
		t.Fatalf("resolvedInboundTag() = %q, want API tag %q", got, "in-12080-tcp")
	}
}

func TestResolvedInboundTagFallsBackForLegacyAPI(t *testing.T) {
	stream := json.RawMessage(`{"network":"ws"}`)
	got := resolvedInboundTag("", 12080, stream)
	if got != "in-12080-ws" {
		t.Fatalf("resolvedInboundTag() = %q, want reconstructed tag %q", got, "in-12080-ws")
	}
}
