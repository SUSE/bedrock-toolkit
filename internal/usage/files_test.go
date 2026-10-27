package usage

import (
	"testing"
)

func TestUserFiles(t *testing.T) {
	us := []User{
		{Email: "a@x.test", Role: "bedrock-user-p-a", Product: "web"},
		{Email: "b@x.test", Role: "bedrock-user-p-b", Product: "data"},
		{Email: "c@x.test", Role: "bedrock-user-p-c"},
	}
	lines := []Line{
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 2, Start: at(1, 3), Model: "Sonnet"},
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 1, Start: at(1, 9), Model: "Haiku"},
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 3, Start: at(3, 9), Model: "Sonnet"},
		{Principal: sts("bedrock-user-p-b", "b@x.test"), Owner: "b@x.test", Cost: 0.004, Start: at(2, 1), Model: "Haiku"},
		{Principal: "arn:aws:iam::111122223333:user/ci", Owner: "a@x.test", Cost: 6, Start: at(2, 5), Model: "Sonnet"},
		{Principal: sts("SomeSSORole", "a@x.test"), Owner: "a@x.test", Cost: 9, Start: at(3, 10), Model: "Sonnet"},
	}
	now := at(4, 12)
	fs := UserFiles("2026-10", "p", lines, us, now)
	if len(fs) != 3 {
		t.Fatalf("%d files", len(fs))
	}
	a, b, c := fs[0], fs[1], fs[2]
	// The IAM user line is tagged owner a, so it counts; the SSO role line doesn't.
	if a.Role != "bedrock-user-p-a" || a.TotalUSD != 12 || len(a.Days) != 3 || a.Days[0] != (DayUSD{"2026-10-01", 3}) ||
		a.Models[0] != (Amount{"Sonnet", 11}) || a.ExportAsOf != "2026-10-03T10:00:00Z" || a.UpdatedAt != "2026-10-04T12:00:00Z" {
		t.Errorf("a: %+v", a)
	}
	if b.TotalUSD != 0 || len(b.Days) != 1 {
		t.Errorf("b: %+v", b)
	}
	if c.Role != "bedrock-user-p-c" || c.TotalUSD != 0 || c.Days == nil || c.Models == nil {
		t.Errorf("c: %+v", c)
	}
	if k := UsageKey("p", "bedrock-user-p-a", "2026-10"); k != "bedrock-admin/usage/p/bedrock-user-p-a/2026-10.json" {
		t.Error(k)
	}
}

func TestFleetUsage(t *testing.T) {
	us := []User{
		{Email: "a@x.test", Role: "bedrock-user-p-a", Product: "web"},
		{Email: "b@x.test", Role: "bedrock-user-p-b", Product: "web"},
		{Email: "c@x.test", Role: "bedrock-user-p-c"},
	}
	lines := []Line{
		{Principal: sts("bedrock-user-p-a", "a@x.test"), Owner: "a@x.test", Cost: 2, Start: at(1, 3)},
		{Principal: sts("bedrock-user-p-b", "b@x.test"), Owner: "b@x.test", Cost: 1, Start: at(2, 3)},
		{Principal: sts("bedrock-user-p-c", "c@x.test"), Owner: "c@x.test", Cost: 0.004, Start: at(2, 3)},
		{Principal: "arn:aws:iam::111122223333:user/ci", Cost: 6, Start: at(2, 5)},
	}
	f := BuildFleetUsage("111122223333", "p", "2026-10", lines, us, at(3, 0))
	if f.Schema != 1 || len(f.Days) != 3 || len(f.Products) != 2 {
		t.Fatalf("%+v", f)
	}
	none, web := f.Products[0], f.Products[1]
	if none.Product != NoProduct || none.Onboarded != 1 || none.ActiveUsers != 0 || none.TrackedUSD != 0 {
		t.Errorf("none %+v", none)
	}
	if web.Onboarded != 2 || web.ActiveUsers != 2 || web.TrackedUSD != 3 {
		t.Errorf("web %+v", web)
	}
	d2 := f.Days[1]
	if d2.ActiveUsers != 1 || d2.TrackedUSD != 1 || len(d2.Products) != 2 || d2.Products[1] != (FleetProduct{Product: "web", ActiveUsers: 1, TrackedUSD: 1}) {
		t.Errorf("day 2 %+v", d2)
	}
	if len(f.Days[2].Products) != 0 {
		t.Errorf("day 3 %+v", f.Days[2])
	}
}
