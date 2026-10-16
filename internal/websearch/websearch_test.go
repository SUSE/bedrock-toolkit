package websearch

import (
	"strings"
	"testing"

	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
)

func TestTargetRoundTrip(t *testing.T) {
	cfg := TargetConfig([]string{"b.com", "a.com"})
	got := ReadTarget("T1", actypes.TargetStatusReady, nil, cfg, Creds())
	if got.Connector != "web-search" || got.Version != "1.2.0" || !got.GatewayRole {
		t.Fatalf("target: %+v", got)
	}
	if d := got.Drift([]string{"a.com", "b.com"}); len(d) != 0 {
		t.Fatalf("same list in another order is no drift: %v", d)
	}
	if d := got.Drift(nil); len(d) != 1 || !strings.Contains(d[0], "exclude_domains") {
		t.Fatalf("want exclude drift: %v", d)
	}
	empty := ReadTarget("T1", actypes.TargetStatusReady, nil, TargetConfig(nil), Creds())
	if len(empty.Exclude) != 0 || len(empty.Drift(nil)) != 0 {
		t.Fatalf("empty: %+v", empty)
	}
}

func TestDriftVersionAndCreds(t *testing.T) {
	old := &Target{Connector: "web-search", Version: "1.1.0"}
	d := strings.Join(old.Drift(nil), "; ")
	if !strings.Contains(d, "1.1.0 → 1.2.0") || !strings.Contains(d, "gateway role") {
		t.Fatalf("drift: %s", d)
	}
}

func TestFailed(t *testing.T) {
	for s, want := range map[string]bool{"READY": false, "CREATING": false, "FAILED": true, "UPDATE_UNSUCCESSFUL": true} {
		if Failed(s) != want {
			t.Errorf("%s: want %v", s, want)
		}
	}
}

func TestURLRegion(t *testing.T) {
	for url, want := range map[string]string{
		"https://g-1.gateway.bedrock-agentcore.eu-west-1.amazonaws.com/mcp": "eu-west-1",
		"https://example.com/mcp": "fallback",
		"":                        "fallback",
	} {
		if got := URLRegion(url, "fallback"); got != want {
			t.Errorf("URLRegion(%q) = %q", url, got)
		}
	}
}
