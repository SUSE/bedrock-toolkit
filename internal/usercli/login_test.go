package usercli

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

// login runs bedrock login with a sign-in that stops at the AWS call and returns the profile and
// SSO region it was asked for.
func login(t *testing.T, config string, args ...string) (signedIn, out string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	var o bytes.Buffer
	a := &App{Version: "test", In: strings.NewReader(""), Out: &o, Err: &o, AWS: newFake(),
		Login: &sso.Login{Client: func(_ context.Context, profile, region string) (sso.OIDC, error) {
			signedIn = profile + " " + region
			return nil, errors.New("stop")
		}}}
	a.Main(context.Background(), append([]string{"login"}, args...))
	return signedIn, o.String()
}

func TestLoginPicksProfile(t *testing.T) {
	config := ssoConfig + "sso_region = us-east-1\n"
	if got, out := login(t, config); got != "sso us-east-1" {
		t.Fatalf("signed in to %q\n%s", got, out)
	}
	two := config + "\n[profile other]\nsso_start_url = https://y\nsso_region = eu-west-1\n"
	if got, out := login(t, two, "--profile", "other"); got != "other eu-west-1" {
		t.Fatalf("signed in to %q\n%s", got, out)
	}
	if got, out := login(t, two); got != "" || !strings.Contains(out, "several SSO profiles") {
		t.Fatalf("signed in to %q\n%s", got, out)
	}
	// After bedrock setup, the bedrock profile's source_profile picks the SSO profile.
	if got, out := login(t, two+"\n"+wantBlock); got != "sso us-east-1" {
		t.Fatalf("signed in to %q\n%s", got, out)
	}
	// login only signs in again: bedrock configure sso creates the profile.
	if got, out := login(t, ""); got != "" || !strings.Contains(out, "no SSO profile") || !strings.Contains(out, "bedrock configure sso") {
		t.Fatalf("signed in to %q\n%s", got, out)
	}
}

// configureSSO runs bedrock configure sso with a sign-in that stops at the AWS call, recording the
// profile and SSO region it was asked for.
func configureSSO(t *testing.T, config string, args ...string) (code int, signedIn, out, path string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	cache := sso.CachePath(home, "corp")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cache, []byte(`{"accessToken":"t","expiresAt":"2099-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	var o bytes.Buffer
	a := &App{Version: "test", In: strings.NewReader(""), Out: &o, Err: &o, AWS: newFake(), Terminal: func() bool { return false },
		Login: &sso.Login{Client: func(_ context.Context, profile, region string) (sso.OIDC, error) {
			signedIn = profile + " " + region
			return nil, errors.New("stop")
		}}}
	code = a.Main(context.Background(), append([]string{"configure", "sso"}, args...))
	return code, signedIn, o.String(), path
}

func TestConfigureSSO(t *testing.T) {
	config := "[profile sso]\nsso_session = corp\nsso_account_id = 111122223333\nsso_role_name = Dev\nregion = us-west-2\n\n[sso-session corp]\nsso_start_url = https://x.awsapps.com/start\nsso_region = us-east-1\n"
	// A profile with the same sign-in and a valid token is reused without signing in again.
	code, signedIn, out, path := configureSSO(t, config, "--start-url", "https://x.awsapps.com/start", "--sso-region", "us-east-1")
	if code != 0 || signedIn != "" || !strings.Contains(out, "Profile sso") || !strings.Contains(out, "bedrock setup --source-profile sso") || read(t, path) != config {
		t.Fatalf("code %d, signed in %q\n%s\n%s", code, signedIn, out, read(t, path))
	}
	// A new sign-in signs in first.
	code, signedIn, out, _ = configureSSO(t, config, "--start-url", "https://y.awsapps.com/start", "--sso-region", "eu-west-1")
	if code == 0 || signedIn != " eu-west-1" || !strings.Contains(out, "stop") {
		t.Fatalf("code %d, signed in %q\n%s", code, signedIn, out)
	}
	for _, c := range [][]string{{"--start-url", "https://y.awsapps.com/start"}, {"--sso-region", "us-east-1"}} {
		if code, signedIn, out, _ := configureSSO(t, config, c...); code == 0 || signedIn != "" || !strings.Contains(out, "no --") {
			t.Fatalf("%v: code %d, signed in %q\n%s", c, code, signedIn, out)
		}
	}
}
