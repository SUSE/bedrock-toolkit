package usercli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/innobead/bedrock-toolkit/internal/pause"
)

func runUsage(t *testing.T, f *fakeAWS, cfg string, args ...string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	var o, e bytes.Buffer
	a := &App{Version: "test", In: strings.NewReader(""), Out: &o, Err: &e, AWS: f,
		Now: func() time.Time { return time.Date(2026, 5, 14, 9, 0, 0, 0, time.UTC) }}
	code := a.Main(context.Background(), append([]string{"usage"}, args...))
	return code, o.String() + e.String()
}

func TestUsage(t *testing.T) {
	cfg := ssoConfig + "\n" + wantBlock
	updated := time.Date(2026, 5, 14, 6, 30, 0, 0, time.UTC)
	cases := []struct {
		name  string
		pause pause.Status
		want  []string
	}{
		{"active", pause.Status{State: pause.Armed}, []string{
			"Budget     bedrock-bedrock-achen, May 2026 (resets Jun 1)", "Spent      $42.50 of $100.00 (42%)",
			"Forecast   $90.00 by the end of the month", "Pauses at  $90.00 (90%)", "Access     active\n",
			"As of      May 14 06:30 UTC", "Web search is not counted"}},
		{"paused by budget", pause.Status{State: pause.Paused, PausedAt: time.Date(2026, 5, 12, 3, 0, 0, 0, time.UTC)},
			[]string{"PAUSED: monthly budget used up on May 12; back on Jun 1"}},
		{"paused by hand", pause.Status{State: pause.PausedByHand, By: "admin@example.com", Reason: "leaving"},
			[]string{"PAUSED by your admin (admin@example.com): leaving"}},
		{"pause off", pause.Status{State: pause.Off}, []string{"active; your admin turned the pause off until Jun 1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.budget = Budget{Limit: 100, Spent: 42.5, Forecast: 90, Updated: updated, PauseAt: 90, Pause: c.pause}
			code, out := runUsage(t, f, cfg)
			if code != 0 {
				t.Fatalf("code %d: %s", code, out)
			}
			if f.budgetArgs != "bedrock 111122223333 bedrock-user-bedrock-achen bedrock-bedrock-achen" {
				t.Fatalf("args: %s", f.budgetArgs)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Fatalf("want %q in:\n%s", w, out)
				}
			}
		})
	}
}

func TestUsageJSON(t *testing.T) {
	f := newFake()
	f.budget = Budget{Limit: 200, Spent: 10, PauseAt: 150, PauseAtUSD: true, Pause: pause.Status{State: pause.Armed}}
	code, out := runUsage(t, f, ssoConfig+"\n"+wantBlock, "--json")
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	for _, w := range []string{`"month": "2026-05"`, `"budget": "bedrock-bedrock-achen"`, `"spent_usd": 10`, `"pause_at_usd": 150`, `"state": "active"`} {
		if !strings.Contains(out, w) {
			t.Fatalf("want %s in %s", w, out)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	cfg := ssoConfig + "\n" + wantBlock
	cases := []struct {
		name, cfg string
		err       error
		want      string
	}{
		{"no profile", ssoConfig, nil, "run bedrock setup"},
		{"not a personal role", ssoConfig + "\n[profile bedrock]\nrole_arn = arn:aws:iam::1:role/Admin\n", nil, "is not a personal Bedrock role"},
		{"old role", cfg, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}, "ask your admin to run bedrock-admin apply"},
		{"no budget", cfg, &smithy.GenericAPIError{Code: "NotFoundException", Message: "no"}, "no budget bedrock-bedrock-achen"},
		{"signed out", cfg, &smithy.GenericAPIError{Code: "ExpiredToken", Message: "expired"}, "aws sso login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.budgetErr = c.err
			code, out := runUsage(t, f, c.cfg)
			if code != 1 || !strings.Contains(out, c.want) {
				t.Fatalf("code %d, want %q in:\n%s", code, c.want, out)
			}
		})
	}
}
