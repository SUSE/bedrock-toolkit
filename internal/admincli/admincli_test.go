package admincli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	btypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/policy"

	bedrocktoolkit "github.com/SUSE/bedrock-toolkit"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	a := New("test")
	a.In, a.Out, a.Err = strings.NewReader(""), &out, &errb
	code := a.Main(context.Background(), args)
	return code, out.String(), errb.String()
}

func TestHelpAndVersion(t *testing.T) {
	code, out, _ := run(t, "--help")
	if code != 0 || !strings.Contains(out, "bedrock-admin plan") {
		t.Fatalf("help: code %d\n%s", code, out)
	}
	code, out, _ = run(t, "--version")
	if code != 0 || !strings.Contains(out, "test") {
		t.Fatalf("version: code %d %q", code, out)
	}
}

func TestBadConfigIsError(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "bedrock.yaml")
	if err := os.WriteFile(f, []byte("id: Bad_ID\naccount: 12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run(t, "plan", "-f", f)
	if code != 1 || !strings.Contains(errs, "account:") || !strings.Contains(errs, "id:") {
		t.Fatalf("code %d\n%s", code, errs)
	}
	code, _, _ = run(t, "plan", "-f", filepath.Join(dir, "missing.yaml"))
	if code != 1 {
		t.Fatalf("missing file: code %d", code)
	}
}

func TestUnknownCommandIsError(t *testing.T) {
	if code, _, _ := run(t, "frobnicate"); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestConfigureKeepsUsersInclude(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "bedrock.yaml")
	if err := os.WriteFile(f, []byte("id: team\nproduct: p\nusers: !include users.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "users.yaml"), []byte("- email: a@example.test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_PROFILE", "bedrock-admin-test-none")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	code, _, errs := run(t, "configure", "--yes", "-f", f, "--account", "123456789012", "--region", "eu-central-1", "--limit", "75")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, errs)
	}
	b, _ := os.ReadFile(f)
	s := string(b)
	for _, want := range []string{"users: !include users.yaml", "id: team", "product: p", "limit_usd: 75", `account: "123456789012"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
}

func TestConfigureNewFileDefaults(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "bedrock.yaml")
	t.Setenv("AWS_PROFILE", "bedrock-admin-test-none")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "none"))
	code, out, errs := run(t, "configure", "--yes", "--dry-run", "-f", f, "--account", "123456789012", "--region", "ap-northeast-1")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, errs)
	}
	for _, want := range []string{"enabled: true", "users: []", "bucket: bedrock-cur-123456789012"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if !strings.Contains(out, "models:\n  - global.anthropic.claude-opus-5-5\n") {
		t.Errorf("a new file starts with the baseline models:\n%s", out)
	}
	if _, err := os.Stat(f); err == nil {
		t.Error("--dry-run wrote the file")
	}
}

func TestClaudeModels(t *testing.T) {
	for region, want := range map[string]string{
		"us-west-2":      "us.anthropic.claude-opus-5-5,us.anthropic.claude-sonnet-5-5,us.anthropic.claude-haiku-5-5",
		"eu-central-1":   "eu.anthropic.claude-opus-5-5,eu.anthropic.claude-sonnet-5-5,eu.anthropic.claude-haiku-5-5",
		"ap-northeast-1": "global.anthropic.claude-opus-5-5,global.anthropic.claude-sonnet-5-5,global.anthropic.claude-haiku-5-5",
	} {
		opus, sonnet, haiku := config.ClaudeModels(region)
		m := []string{opus, sonnet, haiku}
		if got := strings.Join(m, ","); got != want {
			t.Errorf("%s: %s", region, got)
		}
		for _, id := range m {
			if !policy.AllowedModel(id) {
				t.Errorf("%s: personal roles can't call %s", region, id)
			}
		}
	}
}

func TestUsageMonths(t *testing.T) {
	got, err := usageMonths(usageFlags{months: 3, monthsSet: true}, "2026-02")
	if err != nil || strings.Join(got, ",") != "2025-12,2026-01,2026-02" {
		t.Fatal(got, err)
	}
	if got, _ := usageMonths(usageFlags{}, "2026-02"); strings.Join(got, ",") != "2026-02" {
		t.Fatal(got)
	}
	for _, f := range []usageFlags{{month: "2026-03"}, {month: "26-1"}, {month: "2026-01", months: 2, monthsSet: true}, {months: 25, monthsSet: true}, {months: 0, monthsSet: true}} {
		if _, err := usageMonths(f, "2026-02"); err == nil {
			t.Errorf("%+v: no error", f)
		}
	}
	if code, _, errs := run(t, "usage", "a@example.test", "--months", "2"); code != 1 || !strings.Contains(errs, "--month") {
		t.Fatalf("code %d %s", code, errs)
	}
}

func TestUsageProductFlag(t *testing.T) {
	if code, _, errs := run(t, "usage", "--product", "web", "--untracked"); code != 1 || !strings.Contains(errs, "no product") {
		t.Fatalf("code %d %s", code, errs)
	}
	if err := checkUsageFlags(usageFlags{by: "day", months: 2, monthsSet: true}, ""); err == nil || !strings.Contains(err.Error(), "one month") {
		t.Errorf("--by day --months: %v", err)
	}
	if err := checkUsageFlags(usageFlags{by: "day", month: "2026-09"}, "a@x.test"); err != nil {
		t.Error(err)
	}
	if err := checkUsageFlags(usageFlags{by: "product", product: []string{"web"}}, ""); err != nil {
		t.Error(err)
	}
}

func TestShortAWSError(t *testing.T) {
	in := "creating bucket b: operation error S3: CreateBucket, https response error StatusCode: 409, RequestID: R1, HostID: H/1=, api error OperationAborted: try again."
	if got, want := shortAWSError(in), "creating bucket b: OperationAborted: try again."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	in = "creating cost export x: operation error BCM Data Exports: CreateExport, https response error StatusCode: 400, RequestID: 3c, ValidationException: bad"
	if got, want := shortAWSError(in), "creating cost export x: ValidationException: bad"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTestModels(t *testing.T) {
	c := &config.Config{Region: "eu-west-1"}
	if got := c.TestModels(); !slices.Equal(got, config.BaselineModels("eu-west-1")) {
		t.Fatalf("no models: %v", got)
	}
	c.Models = []string{"eu.anthropic.claude-3-7-sonnet-20250219-v1:0", "global.anthropic.claude-opus-5-5"}
	if got := c.EnableModels(); len(got) != 2+len(config.BaselineAllGeos())-1 || !slices.Contains(got, "us.anthropic.claude-haiku-5-5") {
		t.Fatalf("enable: %v", got)
	}
	for m, want := range map[string]string{"us.anthropic.claude-haiku-5-5": "us-east-1", "eu.x": "eu-west-1", "global.x": "eu-west-1", "anthropic.x": "eu-west-1"} {
		if got := config.ModelRegion("eu-west-1", m); got != want {
			t.Fatalf("region of %s: %s", m, got)
		}
	}
	if got := config.ModelRegion("ap-northeast-1", "eu.x"); got != "eu-central-1" {
		t.Fatalf("eu. from apac: %s", got)
	}
	if got := (&config.Config{Region: "us-east-1", Models: c.Models}).TestModels(); slices.Contains(got, c.Models[0]) {
		t.Fatalf("doctor tests another geography's profile: %v", got)
	}
	euBaseline := []string{"eu.anthropic.claude-opus-5-5", "eu.anthropic.claude-sonnet-5-5", "eu.anthropic.claude-haiku-5-5"}
	// A listed baseline model with another prefix (global. in the EU) is allowed but not tested.
	if got := c.TestModels(); !slices.Equal(got, append([]string{c.Models[0]}, euBaseline...)) {
		t.Fatalf("listed plus baseline: %v", got)
	}
	// The policy allows the baseline with every prefix, as users may pick another region.
	allowed := c.AllowedModels()
	for _, m := range []string{"us.anthropic.claude-haiku-5-5", "eu.anthropic.claude-haiku-5-5", "global.anthropic.claude-haiku-5-5", c.Models[0]} {
		if !slices.Contains(allowed, m) {
			t.Fatalf("allowed lacks %s: %v", m, allowed)
		}
	}
	// export keeps only the region's baseline: a listed global. baseline model is allowed anyway.
	want := append([]string{c.Models[0]}, euBaseline...)
	if got := withoutOtherGeoBaseline("eu-west-1", allowed); !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("export: %v", got)
	}
}

func TestModelRows(t *testing.T) {
	p := func(id string) btypes.InferenceProfileSummary {
		return btypes.InferenceProfileSummary{InferenceProfileId: aws.String(id), InferenceProfileName: aws.String(id), Status: "ACTIVE"}
	}
	profiles := []btypes.InferenceProfileSummary{p("us.anthropic.claude-sonnet-5-5"), p("us.anthropic.claude-fable-1"), p("us.anthropic.claude-haiku-5-5"), p("us.amazon.nova-pro-v1:0")}
	users := func(rows []modelRow) string {
		var s []string
		for _, r := range rows {
			s = append(s, r.ID+"="+r.Users)
		}
		return strings.Join(s, ",")
	}
	if got := users(modelRows(profiles, nil, "")); got != "us.amazon.nova-pro-v1:0=never,us.anthropic.claude-fable-1=never,us.anthropic.claude-haiku-5-5=allowed,us.anthropic.claude-sonnet-5-5=allowed" {
		t.Fatalf("no models: %s", got)
	}
	if got := users(modelRows(profiles, []string{"us.anthropic.claude-haiku-5-5"}, "")); got != "us.amazon.nova-pro-v1:0=never,us.anthropic.claude-fable-1=never,us.anthropic.claude-haiku-5-5=allowed,us.anthropic.claude-sonnet-5-5=not listed" {
		t.Fatalf("listed: %s", got)
	}
	if got := users(modelRows(profiles, nil, "HAIKU")); got != "us.anthropic.claude-haiku-5-5=allowed" {
		t.Fatalf("filter: %s", got)
	}
	if got := users(modelRows(profiles, nil, "nova")); got != "us.amazon.nova-pro-v1:0=never" {
		t.Fatalf("filter non-Claude: %s", got)
	}
}

func TestProfileRegions(t *testing.T) {
	m := func(arn string) btypes.InferenceProfileModel {
		return btypes.InferenceProfileModel{ModelArn: aws.String(arn)}
	}
	got := profileRegions([]btypes.InferenceProfileModel{
		m("arn:aws:bedrock:us-west-2::foundation-model/anthropic.claude-haiku-5-5"),
		m("arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-haiku-5-5"),
		m("arn:aws:bedrock:us-west-2::foundation-model/anthropic.claude-haiku-5-5"),
		m("not-an-arn"),
	})
	if !slices.Equal(got, []string{"us-east-1", "us-west-2"}) {
		t.Fatalf("regions: %v", got)
	}
	for r, want := range map[*modelRow]string{
		{}:                             "-",
		{Regions: []string{anyRegion}}: "any region (global)",
		{Regions: []string{"eu-central-1", "eu-west-1"}}: "eu-central-1 eu-west-1",
	} {
		if got := regionsCell(*r); got != want {
			t.Fatalf("cell %v: %q", r.Regions, got)
		}
	}
}

func TestBaselineModels(t *testing.T) {
	m, err := config.ParseModelsTOML(bedrocktoolkit.ModelsTOML)
	if err != nil || len(m) == 0 {
		t.Fatalf("models.toml: %v, %v", m, err)
	}
	if got := config.BaselineAllGeos(); !slices.Equal(got, m) {
		t.Fatalf("all geos: %v", got)
	}
	for region, want := range map[string]int{"us-east-1": len(m) / 3, "eu-west-1": len(m) / 3, "ap-northeast-1": len(m) / 3} {
		got := config.BaselineModels(region)
		if len(got) != want {
			t.Fatalf("%s: %v", region, got)
		}
		for _, id := range got {
			if !strings.HasPrefix(id, config.Geo(region)) {
				t.Fatalf("%s: %v", region, got)
			}
		}
	}
	got, err := config.ParseModelsTOML([]byte(`models = ["anthropic.claude-haiku-5-5", "us.anthropic.claude-opus-5-5", "anthropic.claude-fable-5", "meta.llama3"]`))
	if err != nil || !slices.Equal(got, []string{"anthropic.claude-haiku-5-5", "us.anthropic.claude-opus-5-5"}) {
		t.Fatalf("parse: %v, %v", got, err)
	}
}

func TestConfigureBaselineModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_PROFILE", "bedrock-admin-test-none")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "none"))
	code, out, errs := run(t, "configure", "--yes", "--dry-run", "-f", filepath.Join(dir, "bedrock.yaml"),
		"--account", "123456789012", "--region", "eu-west-1")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, errs)
	}
	want := "models:\n"
	for _, m := range config.BaselineModels("eu-west-1") {
		want += "  - " + m + "\n"
	}
	if !strings.Contains(out, want) || strings.Contains(out, "global.") {
		t.Fatalf("baseline models:\n%s", out)
	}
	code, out, errs = run(t, "configure", "--yes", "--dry-run", "-f", filepath.Join(dir, "bedrock.yaml"),
		"--account", "123456789012", "--region", "eu-west-1", "--model", "eu.anthropic.claude-opus-5-5")
	if code != 0 || !strings.Contains(out, "models:\n  - eu.anthropic.claude-opus-5-5\n") || strings.Contains(out, "global.") {
		t.Fatalf("--model: %d\n%s\n%s", code, out, errs)
	}
}

func TestConfigureReport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_PROFILE", "bedrock-admin-test-none")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "none"))
	args := []string{"configure", "--yes", "--dry-run", "-f", filepath.Join(dir, "bedrock.yaml"), "--account", "123456789012", "--region", "eu-west-1"}
	code, out, errs := run(t, append(args, "--report-bucket", "central-reports", "--report-region", "us-east-1")...)
	if code != 0 || !strings.Contains(out, "report:\n  bucket: central-reports\n  region: us-east-1\n") {
		t.Fatalf("report: %d\n%s\n%s", code, out, errs)
	}
	code, _, errs = run(t, append(args, "--report-bucket", "central-reports")...)
	if code == 0 || !strings.Contains(errs, "report.region") {
		t.Fatalf("missing region: %d\n%s", code, errs)
	}
	if _, out, _ := run(t, args...); strings.Contains(out, "report:") {
		t.Fatalf("report: without a bucket:\n%s", out)
	}
}
