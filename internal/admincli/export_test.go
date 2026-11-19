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

func TestNonEmailSubscribers(t *testing.T) {
	const owner = "owner@example.com"
	const topic = "arn:aws:sns:us-east-1:111122223333:budget-alerts"
	const b = "bedrock-test-user"
	for _, tc := range []struct {
		name  string
		state users.Observed
		want  []string
	}{
		{"emails only", users.Observed{ActionSubscribers: []string{owner}, Alerts: map[int][]string{80: {owner}, 90: {"lead@example.com"}}}, nil},
		{"no subscribers", users.Observed{}, nil},
		{"SNS action", users.Observed{ActionExists: true, ActionSubscribers: []string{topic}}, []string{b + ": " + topic}},
		{"mixed action", users.Observed{ActionExists: true, ActionSubscribers: []string{owner, topic}}, []string{b + ": " + topic}},
		{"SNS alert with action", users.Observed{ActionExists: true, ActionSubscribers: []string{owner}, Alerts: map[int][]string{80: {owner, topic}}}, []string{b + ": " + topic}},
		{"SNS later alert without action", users.Observed{Alerts: map[int][]string{80: {owner}, 90: {topic}}}, []string{b + ": " + topic}},
		{"same topic everywhere listed once", users.Observed{ActionSubscribers: []string{topic}, Alerts: map[int][]string{80: {topic}, 90: {topic}}}, []string{b + ": " + topic}},
		{"invalid address", users.Observed{ActionSubscribers: []string{"not-an-email"}}, []string{b + ": not-an-email"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nonEmailSubscribers(tc.state, b); !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMixedRecipients(t *testing.T) {
	const a, b = "a@example.com", "b@example.com"
	for _, tc := range []struct {
		name  string
		state users.Observed
		mixed bool
	}{
		{"same everywhere, any order and case", users.Observed{ActionExists: true, ActionSubscribers: []string{a, b}, Alerts: map[int][]string{80: {"B@example.com", a}, 90: {a, b}}}, false},
		{"no action, alerts match", users.Observed{Alerts: map[int][]string{80: {a}, 90: {a}}}, false},
		{"nothing", users.Observed{}, false},
		{"alert has an extra address", users.Observed{ActionExists: true, ActionSubscribers: []string{a}, Alerts: map[int][]string{80: {a}, 90: {a, b}}}, true},
		{"alerts differ without action", users.Observed{Alerts: map[int][]string{80: {a}, 90: {b}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mixedRecipients(tc.state, "bedrock-test-user")
			if (got != "") != tc.mixed || (tc.mixed && !strings.Contains(got, "bedrock-test-user")) {
				t.Fatalf("got %q, want mixed=%v", got, tc.mixed)
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
