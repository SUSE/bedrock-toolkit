package admincli

import (
	"strings"
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/fleet"
)

func TestWriteSetups(t *testing.T) {
	last := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	v := SetupsJSON{Account: "111122223333", Current: "team1", Setups: []fleet.SetupSummary{
		{ID: "bedrock", Product: "bedrock", Users: 5, LimitUSD: 500, Idle: true},
		{ID: "team1", Product: "rancher", Users: 12, Paused: 1, LastUsed: last, SpendUSD: 210.4, LimitUSD: 1200},
	}}
	var b strings.Builder
	if err := writeSetups(&b, v, "profile bedrock-admin"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"Setups in account 111122223333 (profile bedrock-admin)",
		"ID", "LAST USED",
		"never", "idle",
		"team1 *", "2026-10-31", "210.40", "1200.00",
		"* the id of the loaded bedrock.yaml.",
		"idle: no personal role used for 30+ days (bedrock)",
		"bedrock-admin export --id <id> > <id>.yaml, set users: [] in it",
		"bedrock-admin apply -f <id>.yaml --yes && bedrock-admin uninstall -f <id>.yaml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	b.Reset()
	_ = writeSetups(&b, SetupsJSON{Account: "111122223333", Setups: v.Setups[1:]}, "default credentials")
	if out := b.String(); strings.Contains(out, "idle") || strings.Contains(out, "*") {
		t.Errorf("no idle, no file:\n%s", out)
	}

	b.Reset()
	_ = writeSetups(&b, SetupsJSON{Account: "111122223333"}, "default credentials")
	if !strings.Contains(b.String(), "None: no bedrock-deny-<id> policy") {
		t.Errorf("none:\n%s", b.String())
	}
}

func TestIdleNotes(t *testing.T) {
	setups := []fleet.SetupSummary{{ID: "team1", Idle: true}, {ID: "team2"}, {ID: "old", Idle: true}}
	got := idleNotes("team1", setups)
	if len(got) != 1 || !strings.Contains(got[0], "Setup old in this account") || !strings.Contains(got[0], "bedrock-admin setups") {
		t.Fatalf("got %q", got)
	}
	if got := idleNotes("team1", setups[:2]); got != nil {
		t.Fatalf("live tenants: %q", got)
	}
}
