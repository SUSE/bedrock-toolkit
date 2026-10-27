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

	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/usage"
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

func usageFiles() map[string]*usage.UserFile {
	return map[string]*usage.UserFile{
		"2026-04": {Month: "2026-04", ExportAsOf: "2026-04-30T23:00:00Z", Days: []usage.DayUSD{{Date: "2026-04-30", USD: 3}},
			Models: []usage.Amount{{Key: "claude-old", USD: 3}}, TotalUSD: 3},
		"2026-05": {Month: "2026-05", ExportAsOf: "2026-05-13T22:00:00Z",
			Days:   []usage.DayUSD{{Date: "2026-05-01", USD: 1}, {Date: "2026-05-12", USD: 4}, {Date: "2026-05-13", USD: 2}},
			Models: []usage.Amount{{Key: "claude-sonnet", USD: 6}, {Key: "claude-haiku", USD: 1}}, TotalUSD: 7},
	}
}

func TestUsageDaily(t *testing.T) {
	f := newFake()
	f.budget = Budget{Limit: 100, Spent: 7, Pause: pause.Status{State: pause.Armed}}
	f.files = usageFiles()
	code, out := runUsage(t, f, ssoConfig+"\n"+wantBlock, "--days", "15")
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	if f.filesAsked[0] != "bedrock-user-bedrock-achen 2026-04,2026-05" {
		t.Fatalf("asked %v", f.filesAsked)
	}
	for _, w := range []string{"Per day (from the cost export, as of May 13 22:00 UTC)", "2026-04-30  $3.00 ",
		"2026-05-12  $4.00   ████████████████████████", "2026-05-13  $2.00*", "2026-05-14  $0.00*", "* still being billed",
		"Per model this month", "claude-sonnet  $6.00", "Spent      $7.00"} {
		if !strings.Contains(out, w) {
			t.Fatalf("want %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "2026-04-29") || strings.Contains(out, "claude-old") {
		t.Fatalf("15 days start on Apr 30, models are this month's:\n%s", out)
	}

	f.filesAsked = nil
	if code, out = runUsage(t, f, ssoConfig+"\n"+wantBlock, "--days", "3", "--json"); code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	for _, w := range []string{`"days": [`, `"date": "2026-05-12"`, `"partial_from": "2026-05-13"`, `"export_as_of": "2026-05-13T22:00:00Z"`, `"key": "claude-sonnet"`} {
		if !strings.Contains(out, w) {
			t.Fatalf("want %s in %s", w, out)
		}
	}
	if f.filesAsked[0] != "bedrock-user-bedrock-achen 2026-05" {
		t.Fatalf("asked %v", f.filesAsked)
	}
}

func TestUsageMonth(t *testing.T) {
	f := newFake()
	f.files = usageFiles()
	code, out := runUsage(t, f, ssoConfig+"\n"+wantBlock, "--month", "2026-04")
	if code != 0 {
		t.Fatalf("code %d: %s", code, out)
	}
	for _, w := range []string{"Your spend in April 2026 (from the cost export, as of Apr 30 23:00 UTC)", "2026-04-01  $0.00",
		"2026-04-30  $3.00 ", "Per model", "claude-old  $3.00", "Total $3.00"} {
		if !strings.Contains(out, w) {
			t.Fatalf("want %q in:\n%s", w, out)
		}
	}
	if f.budgetArgs != "" || strings.Contains(out, "Budget") || strings.Contains(out, "*") {
		t.Fatalf("a past month has no budget and no partial days:\n%s", out)
	}
	if code, out = runUsage(t, f, ssoConfig+"\n"+wantBlock, "--month", "2026-04", "--json"); code != 0 || !strings.Contains(out, `"total_usd": 3`) {
		t.Fatalf("code %d: %s", code, out)
	}
}

func TestUsageDailyHints(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no file", nil, "No daily usage yet: it is written every day at 07:00 UTC"},
		{"no tag", errNoUsageTag, "Your role cannot read its daily usage yet: ask your admin to run bedrock-admin apply"},
		{"denied", &smithy.GenericAPIError{Code: "AccessDenied"}, "Your role cannot read its daily usage yet"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			f.budget = Budget{Limit: 100, Pause: pause.Status{State: pause.Armed}}
			f.filesErr = c.err
			code, out := runUsage(t, f, ssoConfig+"\n"+wantBlock)
			if code != 0 || !strings.Contains(out, c.want) || strings.Contains(out, "DATE") {
				t.Fatalf("code %d, want %q in:\n%s", code, c.want, out)
			}
		})
	}
}

func TestUsageFlags(t *testing.T) {
	for _, c := range [][]string{{"--days", "0"}, {"--days", "63"}, {"--month", "2026-13"}, {"--month", "2026-06"},
		{"--month", "2026-04", "--days", "3"}} {
		if code, out := runUsage(t, newFake(), ssoConfig+"\n"+wantBlock, c...); code == 0 {
			t.Fatalf("%v: want an error, got %s", c, out)
		}
	}
}
