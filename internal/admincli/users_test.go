package admincli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
)

func TestUsersView(t *testing.T) {
	cfg := &config.Config{ID: "acme", Users: []config.User{{Email: "a@x.com"}, {Email: "b@x.com", IAMUser: "bob"}, {Email: "c@x.com"}}}
	got := []fleet.User{
		{Email: "a@x.com", Kind: fleet.KindSSO, LastUsed: time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC), State: pause.Armed},
		{Email: "c@x.com", Kind: fleet.KindSSO, State: pause.Paused},
		{Email: "old@x.com", Kind: fleet.KindSSO, State: pause.Armed},
	}
	v := UsersView(cfg, got)
	want := []UserRow{
		{Email: "a@x.com", Kind: "sso", SetUp: "2026-05-02", Pause: "armed", InFile: true},
		{Email: "b@x.com", Kind: "iam-user", SetUp: "not applied", Pause: "-", InFile: true},
		{Email: "c@x.com", Kind: "sso", SetUp: "never", Pause: "paused", InFile: true},
		{Email: "old@x.com", Kind: "sso", SetUp: "never", Pause: "armed"},
	}
	if len(v.Users) != len(want) {
		t.Fatalf("%+v", v.Users)
	}
	for i := range want {
		if v.Users[i] != want[i] {
			t.Errorf("row %d: %+v, want %+v", i, v.Users[i], want[i])
		}
	}
	if v.Totals != (fleet.Counts{Onboarded: 3, SetUp: 1, Paused: 1}) || v.NotApplied != 1 {
		t.Fatalf("%+v %d", v.Totals, v.NotApplied)
	}
	var b bytes.Buffer
	if err := writeUsers(&b, v); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "3 onboarded, 1 set up, 1 paused; 1 in the file not applied yet.") ||
		!strings.Contains(b.String(), "old@x.com (not in the file: apply offboards)") {
		t.Fatal(b.String())
	}
}
