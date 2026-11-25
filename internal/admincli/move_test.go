package admincli

import (
	"strings"
	"testing"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

func moveConfig() *config.Config {
	return &config.Config{Path: "new.yaml", ID: "new", Users: []config.User{{Email: "achen@example.com"}, {Email: "bdiaz@example.com"}}}
}

func TestCheckMove(t *testing.T) {
	cfg := moveConfig()
	applied := []string{policy.UserRoleName("new", "achen"), policy.UserRoleName("new", "bdiaz")}
	for _, c := range []struct {
		name, from string
		ids, roles []string
		want       string
	}{
		{"ok", "old", []string{"old", "new"}, applied, ""},
		{"same id", "new", []string{"old", "new"}, applied, "is this file's id"},
		{"no old setup", "gone", []string{"old", "new"}, applied, "no setup with id gone"},
		{"new not applied", "old", []string{"old"}, nil, "isn't set up in this account"},
		{"user without a new role", "old", []string{"old", "new"}, applied[:1], "1 users of this file have no role in id new yet (bdiaz@example.com)"},
	} {
		err := checkMove(cfg, c.from, c.ids, c.roles)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
}

func TestMoveNotes(t *testing.T) {
	offboard := []*plan.Item{
		{Op: plan.Delete, Email: "ACHEN@example.com"},
		{Op: plan.Delete, Email: "gone@example.com"},
		{Op: plan.Info, Email: "other@example.com"}, // another id's role, not removed
	}
	got := strings.Join(moveNotes(moveConfig(), "old", offboard), "\n")
	for _, want := range []string{"lose access until they run their setup command for id new",
		"1 users of id old aren't in new.yaml and lose access for good: gone@example.com."} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if moveNotes(moveConfig(), "old", nil) != nil {
		t.Error("an old id without users needs no notes")
	}
}
