package admincli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	btypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"

	"github.com/innobead/bedrock-toolkit/internal/config"
	"github.com/innobead/bedrock-toolkit/internal/policy"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	a := New("test")
	a.In, a.Out, a.Err = strings.NewReader(""), &out, &errb
	a.ModelsURL = ""
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
	if strings.Contains(out, "models:") {
		t.Errorf("a new file must not limit models:\n%s", out)
	}
	if _, err := os.Stat(f); err == nil {
		t.Error("--dry-run wrote the file")
	}
}

func TestDefaultModels(t *testing.T) {
	for region, want := range map[string]string{
		"us-west-2":      "us.anthropic.claude-opus-5-5,us.anthropic.claude-sonnet-5-5,us.anthropic.claude-haiku-5-5",
		"eu-central-1":   "eu.anthropic.claude-opus-5-5,eu.anthropic.claude-sonnet-5-5,eu.anthropic.claude-haiku-5-5",
		"ap-northeast-1": "global.anthropic.claude-opus-5-5,global.anthropic.claude-sonnet-5-5,global.anthropic.claude-haiku-5-5",
	} {
		m := config.DefaultModels(region)
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
	if got := c.TestModels(); !slices.Equal(got, []string{"eu.anthropic.claude-haiku-5-5"}) {
		t.Fatalf("no models: %v", got)
	}
	c.Models = []string{"eu.anthropic.claude-opus-5-5"}
	if got := c.TestModels(); !slices.Equal(got, c.Models) {
		t.Fatalf("listed: %v", got)
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

func TestRepoModelsToml(t *testing.T) {
	b, err := os.ReadFile("../../models.toml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := config.ParseSuggestedModels(b)
	if err != nil || len(m) == 0 {
		t.Fatalf("models.toml: %v, %v", m, err)
	}
	if got := config.WithGeo("us-west-2", m); !slices.Equal(got, config.DefaultModels("us-west-2")) {
		t.Fatalf("models.toml with us.: %v", got)
	}
}

func TestConfigureSuggestedModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models.toml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`models = ["anthropic.claude-haiku-5-5", "us.anthropic.claude-opus-5-5", "anthropic.claude-fable-5", "meta.llama3"]`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	t.Setenv("AWS_PROFILE", "bedrock-admin-test-none")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "none"))
	configure := func(url string) (string, string) {
		var out, errb bytes.Buffer
		a := New("test")
		a.In, a.Out, a.Err, a.ModelsURL = strings.NewReader(""), &out, &errb, url
		if code := a.Main(context.Background(), []string{"configure", "--yes", "--dry-run", "-f", filepath.Join(dir, "bedrock.yaml"),
			"--account", "123456789012", "--region", "eu-west-1"}); code != 0 {
			t.Fatalf("code %d\n%s", code, errb.String())
		}
		return out.String(), errb.String()
	}
	out, _ := configure(srv.URL + "/models.toml")
	if !strings.Contains(out, "models:\n  - eu.anthropic.claude-haiku-5-5\n  - us.anthropic.claude-opus-5-5\n") || strings.Contains(out, "fable") || strings.Contains(out, "llama") {
		t.Fatalf("suggested models:\n%s", out)
	}
	out, errs := configure(srv.URL + "/missing.toml")
	if strings.Contains(out, "models:") || !strings.Contains(errs, "models: stays empty") {
		t.Fatalf("missing models.toml:\n%s\n%s", out, errs)
	}
}
