package admincli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SUSE/bedrock-toolkit/internal/sso"
)

func TestLoginPicksAdminProfile(t *testing.T) {
	dir := t.TempDir()
	aws := filepath.Join(dir, "config")
	if err := os.WriteFile(aws, []byte("[profile admin]\nsso_start_url = https://x\nsso_region = us-east-1\n\n"+
		"[profile other]\nsso_start_url = https://y\nsso_region = eu-west-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := filepath.Join(dir, "bedrock.yaml")
	if err := os.WriteFile(yaml, []byte("profile: admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", aws)
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("BEDROCK_ADMIN_CONFIG", "")
	login := func(args ...string) string {
		t.Helper()
		var got string
		var out bytes.Buffer
		a := New("test")
		a.In, a.Out, a.Err = strings.NewReader(""), &out, &out
		a.Login = &sso.Login{Client: func(_ context.Context, profile, region string) (sso.OIDC, error) {
			got = profile + " " + region
			return nil, errors.New("stop")
		}}
		a.Main(context.Background(), append([]string{"login"}, args...))
		if got == "" {
			t.Fatalf("login %v did not sign in:\n%s", args, out.String())
		}
		return got
	}
	if got := login("-f", yaml); got != "admin us-east-1" {
		t.Errorf("profile: from bedrock.yaml: signed in to %q", got)
	}
	if got := login("-f", yaml, "--profile", "other"); got != "other eu-west-1" {
		t.Errorf("--profile: signed in to %q", got)
	}
	t.Setenv("AWS_PROFILE", "other")
	if got := login("-f", yaml); got != "other eu-west-1" {
		t.Errorf("AWS_PROFILE: signed in to %q", got)
	}
}
