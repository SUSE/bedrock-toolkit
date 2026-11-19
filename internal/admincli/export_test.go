package admincli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/users"
)

func TestValidateExportSubscribers(t *testing.T) {
	const owner = "owner@example.com"
	const topic = "arn:aws:sns:us-east-1:111122223333:budget-alerts"
	for _, tc := range []struct {
		name    string
		state   users.Observed
		wantErr string
	}{
		{"emails only", users.Observed{ActionSubscribers: []string{owner}, Alerts: map[int][]string{80: {owner}, 90: {"lead@example.com"}}}, ""},
		{"no subscribers", users.Observed{}, ""},
		{"SNS action", users.Observed{ActionExists: true, ActionSubscribers: []string{topic}}, topic},
		{"mixed action", users.Observed{ActionExists: true, ActionSubscribers: []string{owner, topic}}, topic},
		{"SNS alert with action", users.Observed{ActionExists: true, ActionSubscribers: []string{owner}, Alerts: map[int][]string{80: {owner, topic}}}, topic},
		{"SNS later alert without action", users.Observed{Alerts: map[int][]string{80: {owner}, 90: {topic}}}, topic},
		{"invalid address", users.Observed{ActionSubscribers: []string{"not-an-email"}}, "not-an-email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExportSubscribers(tc.state, "bedrock-test-user")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "bedrock-test-user") || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want budget and %q", err, tc.wantErr)
			}
		})
	}
}

func TestExportNotifyDefaults(t *testing.T) {
	defaults := []string{"ops@example.com", "finance@example.com"}
	found := []exportedUser{
		{r: config.Resolved{Email: "alice@example.com", Name: "alice"}, limit: 50, pause: 100, alerts: []int{80}, notify: "alice@example.com", emails: []string{"alice@example.com", "OPS@example.com", "finance@example.com"}},
		{r: config.Resolved{Email: "bob@example.com", Name: "bob"}, limit: 50, pause: 100, alerts: []int{80}, notify: "Ops@example.com", emails: []string{"Ops@example.com"}},
	}
	got := exportNotifyDefaults(defaults, found)
	if !slices.Equal(got, []string{"ops@example.com"}) {
		t.Fatalf("defaults = %v, want shared ops address", got)
	}
	if !slices.Equal(defaults, []string{"ops@example.com", "finance@example.com"}) {
		t.Fatalf("original defaults modified: %v", defaults)
	}
	if got := exportNotifyDefaults(defaults, nil); !slices.Equal(got, defaults) {
		t.Fatalf("no users: defaults = %v", got)
	}

	cfg := config.Config{ID: "acme", Defaults: config.Defaults{AdditionalNotifyEmails: got}}
	cfg.ApplyDefaults()
	alice := exportConfigUser(&cfg, found[0], "")
	bob := exportConfigUser(&cfg, found[1], "")
	if !slices.Equal(alice.AdditionalNotifyEmails, []string{"finance@example.com"}) {
		t.Fatalf("Alice's exported additional emails = %v", alice.AdditionalNotifyEmails)
	}
	if len(bob.AdditionalNotifyEmails) != 0 || bob.Notify != "Ops@example.com" {
		t.Fatalf("Bob's exported recipients = %+v", bob)
	}
	u := cfg.ResolveUser(alice)
	o := users.Observed{
		RoleExists: true, RoleID: "acme", BudgetExists: true, BudgetID: "acme", Limit: 50, FilterOK: true,
		Alerts: map[int][]string{80: {"alice@example.com", "OPS@example.com", "finance@example.com"}}, ActionExists: true,
		ActionShapeOK: true, ActionThreshold: 100, ActionSubscribers: []string{"alice@example.com", "OPS@example.com", "finance@example.com"},
		Pause: pause.Status{State: pause.Armed},
	}
	if change := users.Diff(u, o, cfg.ID, time.Time{}); change.Op != plan.OK {
		t.Fatalf("case-only difference caused drift after export: %+v", change)
	}
	u = cfg.ResolveUser(bob)
	o.Alerts[80] = []string{"Ops@example.com"}
	o.ActionSubscribers = []string{"Ops@example.com"}
	if change := users.Diff(u, o, cfg.ID, time.Time{}); change.Op != plan.OK {
		t.Fatalf("primary matching default caused drift after export: %+v", change)
	}
}

func TestExportNotify(t *testing.T) {
	const owner = "owner@example.com"
	const lead = "lead@example.com"
	const ops = "ops@example.com"
	for _, tc := range []struct {
		name       string
		emails     []string
		configured string
		want       string
	}{
		{"configured primary", []string{ops, owner, lead}, lead, lead},
		{"configured primary with different case", []string{ops, "LEAD@example.com"}, lead, "LEAD@example.com"},
		{"owner without config", []string{ops, owner}, "", owner},
		{"owner with different case", []string{ops, "OWNER@example.com"}, "", "OWNER@example.com"},
		{"owner absent", []string{lead, ops}, owner, lead},
		{"no observed recipients", nil, lead, owner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exportNotify(tc.emails, tc.configured, owner); got != tc.want {
				t.Errorf("primary recipient = %q, want %q", got, tc.want)
			}
		})
	}
}
