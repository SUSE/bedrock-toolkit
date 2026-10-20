package users

import (
	"strings"
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
)

var now = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func want(limit float64, pauseAt int) config.Resolved {
	return config.Resolved{Email: "dave@acme.com", Name: "dave", RoleName: "bedrock-user-acme-dave", BudgetName: "bedrock-acme-dave",
		LimitUSD: limit, PauseAtPercent: pauseAt, AlertAtPercent: []int{80}, Notify: "dave@acme.com"}
}

func observed(limit float64, pauseAt int, spend float64, st pause.State) Observed {
	return Observed{RoleExists: true, RoleID: "acme", BudgetExists: true, BudgetID: "acme", Limit: limit, FilterOK: true,
		Spend: spend, Alerts: map[int][]string{80: {"dave@acme.com"}}, ActionExists: true, ActionID: "a1",
		ActionShapeOK: true, ActionThreshold: pauseAt, ActionSubscribers: []string{"dave@acme.com"}, Pause: pause.Status{State: st}}
}

func line(c Change) string {
	s := strings.Join(c.Details, ", ")
	if len(c.Notes) > 0 {
		s += "  (" + strings.Join(c.Notes, "; ") + ")"
	}
	return s
}

func TestLimitChanges(t *testing.T) {
	cases := []struct {
		name  string
		want  config.Resolved
		obs   Observed
		line  string
		rearm bool
	}{
		{"armed", want(80, 100), observed(50, 100, 10, pause.Armed), "limit 50 → 80", false},
		{"armed below spend", want(40, 100), observed(50, 100, 45, pause.Armed), "limit 50 → 40  (warning: spent $45.00, will be paused)", false},
		{"paused, raised above", want(80, 100), observed(50, 100, 52, pause.Paused), "limit 50 → 80  (paused → unpaused, spent $52.00)", true},
		{"paused, still over", want(52, 100), observed(50, 100, 52.10, pause.Paused), "limit 50 → 52  (still over: spent $52.10, stays paused)", false},
		{"pause_at raised", want(50, 110), observed(50, 100, 52.10, pause.Paused), "pause_at 100% → 110%  (paused → unpaused, trigger $55.00, spent $52.10)", true},
		{"by hand", want(80, 100), observed(50, 100, 10, pause.PausedByHand), "limit 50 → 80  (paused by hand, unchanged)", false},
		{"off", want(60, 100), observed(30, 100, 30.90, pause.Off), "limit 30 → 60  (pause off until Nov 1, unchanged)", false},
		{"off below spend", want(20, 100), observed(30, 100, 30.90, pause.Off), "limit 30 → 20  (pause off until Nov 1, unchanged; warning: pause off until Nov 1; spending is not capped)", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Diff(tc.want, tc.obs, "acme", now)
			if c.Op != plan.Update {
				t.Errorf("op = %s", c.Op)
			}
			if got := line(c); got != tc.line {
				t.Errorf("got  %q\nwant %q", got, tc.line)
			}
			if c.Rearm != tc.rearm {
				t.Errorf("rearm = %v", c.Rearm)
			}
			if c.CreateAction || c.RecreateAction {
				t.Error("a limit change must keep the same action")
			}
		})
	}
}

func TestNoChange(t *testing.T) {
	c := Diff(want(50, 100), observed(50, 100, 10, pause.Armed), "acme", now)
	if c.Op != plan.OK || len(c.Details) != 0 {
		t.Fatalf("got %s %v", c.Op, c.Details)
	}
}

func TestOnboard(t *testing.T) {
	c := Diff(want(50, 100), Observed{}, "acme", now)
	if c.Op != plan.Create || !c.CreateRole || !c.CreateBudget || !c.CreateAction || line(c) != "onboard, limit 50" {
		t.Fatalf("got %+v", c)
	}
}

func TestOtherID(t *testing.T) {
	o := observed(50, 100, 0, pause.Armed)
	o.RoleID = "other"
	if c := Diff(want(80, 100), o, "acme", now); c.Op != plan.Conflict || c.UpdateBudget {
		t.Fatalf("got %+v", c)
	}
	o.RoleID = ""
	if c := Diff(want(80, 100), o, "acme", now); c.Op != plan.Conflict {
		t.Fatalf("got %+v", c)
	}
}

func TestAlertsAndNotify(t *testing.T) {
	w := want(50, 100)
	w.AlertAtPercent = []int{50, 90}
	w.Notify = "lead@acme.com"
	c := Diff(w, observed(50, 100, 0, pause.Armed), "acme", now)
	if got := line(c); got != "alerts 80% → 50% 90%, notify dave@acme.com → lead@acme.com" {
		t.Fatalf("got %q", got)
	}
	if len(c.AddAlerts) != 2 || len(c.RemoveAlerts) != 1 || !c.UpdateAction {
		t.Fatalf("got %+v", c)
	}
	w = want(50, 100)
	w.AlertAtPercent = []int{}
	c = Diff(w, observed(50, 100, 0, pause.Armed), "acme", now)
	if got := line(c); got != "alerts 80% → none" {
		t.Fatalf("got %q", got)
	}
}

func TestRecreateNeedsYes(t *testing.T) {
	o := observed(50, 100, 0, pause.Armed)
	o.ActionShapeOK = false
	c := Diff(want(50, 100), o, "acme", now)
	if !c.NeedsYes || !c.RecreateAction {
		t.Fatalf("got %+v", c)
	}
}

func TestMissingBudget(t *testing.T) {
	o := Observed{RoleExists: true, RoleID: "acme"}
	c := Diff(want(50, 100), o, "acme", now)
	if c.Op != plan.Update || !c.CreateBudget || !strings.Contains(line(c), "new pause action starts armed") {
		t.Fatalf("got %+v", c)
	}
}

func TestSetupCommand(t *testing.T) {
	cfg := &config.Config{ID: "bedrock", Region: "us-west-2"}
	u := want(50, 100)
	if got := SetupCommand(cfg, "123456789012", "us-west-2", u); got != "bedrock setup" {
		t.Fatalf("got %q", got)
	}
	cfg.ID = "acme"
	if got := SetupCommand(cfg, "123456789012", "us-west-2", u); got != "bedrock setup --id acme" {
		t.Fatalf("got %q", got)
	}
	u.CustomName, u.RoleName = true, "bedrock-user-acme-dv"
	got := SetupCommand(cfg, "123456789012", "eu-west-1", u)
	if got != "bedrock setup --region us-west-2 --role-arn arn:aws:iam::123456789012:role/bedrock-users/acme/bedrock-user-acme-dv" {
		t.Fatalf("got %q", got)
	}
}

func TestRoleSpecProduct(t *testing.T) {
	cfg := &config.Config{ID: "acme", Region: "us-west-2", Product: "team"}
	cfg.Users = []config.User{{Email: "dave@acme.com"}, {Email: "erin@acme.com", Product: "other"}}
	us := cfg.Resolve()
	if got := RoleSpec(cfg, "123456789012", us[0]).Tags["product"]; got != "team" {
		t.Errorf("dave product tag %q", got)
	}
	if got := RoleSpec(cfg, "123456789012", us[1]).Tags["product"]; got != "other" {
		t.Errorf("erin product tag %q", got)
	}
}

func TestDiffIAMUser(t *testing.T) {
	w := want(50, 100)
	w.IAMUser = "dave.dev"
	ok := IAMUserState{Exists: true, Name: "dave.dev", ID: "acme", Owner: "dave@acme.com", Attached: true}
	cases := []struct {
		name    string
		iam     IAMUserState
		op      plan.Op
		fix     bool
		detail  string
		warning string
	}{
		{"in sync", ok, plan.OK, false, "", ""},
		{"missing", IAMUserState{}, plan.OK, false, "", "doesn't exist"},
		{"case", IAMUserState{Exists: true, Name: "Dave.Dev"}, plan.OK, false, "", "named Dave.Dev in AWS"},
		{"other id", IAMUserState{Exists: true, Name: "dave.dev", ID: "other"}, plan.OK, false, "", "managed by other"},
		{"new", IAMUserState{Exists: true, Name: "dave.dev"}, plan.Update, true, "tag IAM user dave.dev, attach bedrock-iam-users-acme", ""},
		{"detached", IAMUserState{Exists: true, Name: "dave.dev", ID: "acme", Owner: "dave@acme.com"}, plan.Update, true, "attach", ""},
		{"owner changed", IAMUserState{Exists: true, Name: "dave.dev", ID: "acme", Owner: "old@acme.com", Attached: true}, plan.Update, true, "tag IAM user", ""},
		{"product changed", IAMUserState{Exists: true, Name: "dave.dev", ID: "acme", Owner: "dave@acme.com", Product: "x", Attached: true}, plan.Update, true, "tag IAM user", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := observed(50, 100, 0, pause.Armed)
			o.IAM = tc.iam
			o.ActionUsers = actionUsers(w, tc.iam, "acme")
			c := Diff(w, o, "acme", now)
			if c.Op != tc.op || c.FixIAMUser != tc.fix || !strings.Contains(line(c), tc.detail) ||
				!strings.Contains(strings.Join(c.Warnings, ";"), tc.warning) || (tc.warning == "") != (len(c.Warnings) == 0) {
				t.Fatalf("got %+v", c)
			}
		})
	}
	// A new user with an IAM user is onboarded and the IAM user attached in the same apply.
	c := Diff(w, Observed{IAM: IAMUserState{Exists: true, Name: "dave.dev"}}, "acme", now)
	if c.Op != plan.Create || !c.CreateRole || !c.FixIAMUser {
		t.Fatalf("got %+v", c)
	}
	// No iam_user: the IAM user state is ignored.
	if c := Diff(want(50, 100), observed(50, 100, 0, pause.Armed), "acme", now); c.Op != plan.OK || c.FixIAMUser {
		t.Fatalf("got %+v", c)
	}
}

func TestDiffActionIAMUsers(t *testing.T) {
	w := want(50, 100)
	w.IAMUser = "dave.dev"
	iamOK := IAMUserState{Exists: true, Name: "dave.dev", ID: "acme", Owner: "dave@acme.com", Attached: true}
	cases := []struct {
		name           string
		want           config.Resolved
		iam            IAMUserState
		have           []string
		st             pause.State
		line           string
		pause, unpause []string
	}{
		{"added", w, iamOK, nil, pause.Armed, "pause action IAM users none → dave.dev", nil, nil},
		{"added while paused", w, iamOK, nil, pause.Paused, "pause action IAM users none → dave.dev  (paused: the deny follows the IAM user change)", []string{"dave.dev"}, nil},
		{"removed while paused by hand", want(50, 100), IAMUserState{}, []string{"dave.dev"}, pause.PausedByHand, "pause action IAM users dave.dev → none  (paused: the deny follows the IAM user change)", nil, []string{"dave.dev"}},
		{"IAM user missing", w, IAMUserState{}, nil, pause.Armed, "", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := observed(50, 100, 0, tc.st)
			o.IAM, o.ActionUsers = tc.iam, tc.have
			c := Diff(tc.want, o, "acme", now)
			if got := line(c); got != tc.line || c.UpdateAction != (tc.line != "") ||
				strings.Join(c.PauseIAMUsers, ",") != strings.Join(tc.pause, ",") || strings.Join(c.UnpauseIAMUsers, ",") != strings.Join(tc.unpause, ",") {
				t.Fatalf("got %q %+v", got, c)
			}
		})
	}
}

func TestSetupCommandIAMUser(t *testing.T) {
	u := want(50, 100)
	u.IAMUser = "dave.dev"
	got := SetupCommand(&config.Config{ID: "bedrock", Region: "us-west-2"}, "123456789012", "us-west-2", u)
	if got != "bedrock setup --source-profile <your dave.dev access key profile> --default-region us-west-2" {
		t.Fatalf("got %q", got)
	}
	// With --region already in the command, no --default-region.
	got = SetupCommand(&config.Config{ID: "bedrock", Region: "us-east-1"}, "123456789012", "us-west-2", u)
	if got != "bedrock setup --region us-east-1 --source-profile <your dave.dev access key profile>" {
		t.Fatalf("got %q", got)
	}
}
