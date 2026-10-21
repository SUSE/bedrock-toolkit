package admincli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/fleet"
)

func TestWriteFleet(t *testing.T) {
	now := time.Date(2025, 10, 21, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	a := &App{Out: &out}
	writeFleet(a, []fleet.Report{
		{Account: "111111111111", ID: "bedrock", Region: "us-east-1", Version: "v1.1.0", Status: fleet.StatusActive,
			ReportedAt: now.Add(-2 * time.Hour), Month: "2025-10", SpendUSD: 12.5, Users: fleet.Counts{Onboarded: 5, SetUp: 3, Paused: 1}},
		{Account: "222222222222", ID: "old", Status: fleet.StatusUninstalled, ReportedAt: now.Add(-72 * time.Hour), Month: "2025-10",
			SpendUSD: 99, Users: fleet.Counts{Onboarded: 0}},
	}, now)
	s := out.String()
	for _, want := range []string{"2h ago", "3d ago", "uninstalled", "1 installed setups: 5 onboarded, 3 set up, 1 paused; $12.50 this month."} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %q:\n%s", want, s)
		}
	}
}

func TestFleetInitFlags(t *testing.T) {
	if code, _, errs := run(t, "fleet", "init", "--bucket", "b", "--org-id", "o-abcdefghij", "--accounts", "111111111111"); code == 0 || !strings.Contains(errs, "not both") {
		t.Errorf("both: %d %s", code, errs)
	}
	if code, _, errs := run(t, "fleet", "init", "--bucket", "b", "--accounts", "12"); code == 0 || !strings.Contains(errs, "12-digit") {
		t.Errorf("bad account: %d %s", code, errs)
	}
	if code, _, errs := run(t, "fleet", "report"); code == 0 || !strings.Contains(errs, "bucket") {
		t.Errorf("no bucket: %d %s", code, errs)
	}
}
