package admincli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/config"
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
}

func TestDefaultReportBucket(t *testing.T) {
	if got := DefaultReportBucket("111111111111"); got != "bedrock-reports-111111111111" {
		t.Error(got)
	}
}

func TestPickReportBucket(t *testing.T) {
	if _, err := pickReportBucket(nil, "111111111111"); err == nil || !strings.Contains(err.Error(), "no report bucket in account 111111111111") {
		t.Errorf("none: %v", err)
	}
	b, err := pickReportBucket([]reportBucket{{"r", "eu-west-1"}}, "111111111111")
	if err != nil || b != (reportBucket{"r", "eu-west-1"}) {
		t.Errorf("one: %v %v", b, err)
	}
	_, err = pickReportBucket([]reportBucket{{"a", "us-east-1"}, {"b", "eu-west-1"}}, "111111111111")
	if err == nil || !strings.Contains(err.Error(), "a (us-east-1), b (eu-west-1); pass --bucket") {
		t.Errorf("two: %v", err)
	}
}
func TestFleetClientRegion(t *testing.T) {
	dir := t.TempDir()
	awsCfg := filepath.Join(dir, "config")
	if err := os.WriteFile(awsCfg, []byte("[profile with-region]\nregion = eu-west-1\n[profile no-region]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", awsCfg)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	ctx := context.Background()
	for _, tc := range []struct {
		name, flag string
		file       *config.Config
		want       string
	}{
		{"flag wins", "ap-south-1", &config.Config{Profile: "with-region", Region: "us-west-2"}, "ap-south-1"},
		{"profile region before the file's", "", &config.Config{Profile: "with-region", Region: "us-west-2"}, "eu-west-1"},
		{"file region when the profile has none", "", &config.Config{Profile: "no-region", Region: "us-west-2"}, "us-west-2"},
		{"no -f and no profile region", "", nil, "us-east-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{fleetCfg: tc.file}
			if tc.file == nil {
				a.Profile = "no-region"
			}
			if got := a.fleetClientRegion(ctx, tc.flag); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}
