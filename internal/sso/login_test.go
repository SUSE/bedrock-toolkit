package sso

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc/types"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
)

const sessionConfig = `[profile sso]
sso_session = corp
sso_account_id = 111122223333
sso_role_name = Dev

[sso-session corp]
sso_start_url = https://example.awsapps.com/start
sso_region = us-east-1
sso_registration_scopes = sso:account:access, codewhisperer:completions
`

func TestResolve(t *testing.T) {
	chain := "[profile bedrock]\nrole_arn = x\nsource_profile = sso\n"
	legacy := "[profile old]\nsso_start_url = https://old.awsapps.com/start\nsso_region = eu-west-1\nsso_account_id = 1\nsso_role_name = Dev\n"
	cases := []struct {
		name, config, profile string
		defaults              []string
		want                  Settings
		err                   string
	}{
		{name: "only SSO profile", config: sessionConfig + "[profile bedrock]\nrole_arn = x\nsource_profile = sso\n",
			want: Settings{Profile: "sso", StartURL: "https://example.awsapps.com/start", Region: "us-east-1",
				Scopes: []string{"sso:account:access", "codewhisperer:completions"}, CacheKey: "corp"}},
		{name: "legacy profile", config: legacy, profile: "old",
			want: Settings{Profile: "old", StartURL: "https://old.awsapps.com/start", Region: "eu-west-1",
				Scopes: []string{DefaultScope}, CacheKey: "https://old.awsapps.com/start"}},
		{name: "no SSO profile", config: "[profile k]\nregion = us-east-1\n", err: "no SSO profile in"},
		{name: "several", config: sessionConfig + legacy, err: "several SSO profiles in config (old, sso): pass --profile"},
		{name: "several sharing a session", config: sessionConfig + "[profile sso2]\nsso_session = corp\nsso_account_id = 2\nsso_role_name = Admin\n",
			want: Settings{Profile: "sso", StartURL: "https://example.awsapps.com/start", Region: "us-east-1",
				Scopes: []string{"sso:account:access", "codewhisperer:completions"}, CacheKey: "corp",
				Name: "sso-session corp (profiles sso, sso2)"}},
		{name: "default through source_profile", config: legacy + sessionConfig + chain, defaults: []string{"", "missing", "bedrock"},
			want: Settings{Profile: "sso", StartURL: "https://example.awsapps.com/start", Region: "us-east-1",
				Scopes: []string{"sso:account:access", "codewhisperer:completions"}, CacheKey: "corp"}},
		{name: "profile wins over defaults", config: legacy + sessionConfig + chain, profile: "old", defaults: []string{"bedrock"},
			want: Settings{Profile: "old", StartURL: "https://old.awsapps.com/start", Region: "eu-west-1",
				Scopes: []string{DefaultScope}, CacheKey: "https://old.awsapps.com/start"}},
		{name: "chain to a key", config: "[profile bedrock]\nrole_arn = x\nsource_profile = key\n[profile key]\nregion = us-east-1\n",
			defaults: []string{"bedrock"}, err: `profile "bedrock" in config signs in through "key", which is not an SSO profile`},
		{name: "loop", config: "[profile a]\nsource_profile = b\n[profile b]\nsource_profile = a\n", profile: "a", err: "not an SSO profile"},
		{name: "not SSO", config: "[profile k]\nregion = us-east-1\n", profile: "k", err: `profile "k" in config is not an SSO profile`},
		{name: "missing", config: sessionConfig, profile: "nope", err: `no profile "nope" in config`},
		{name: "no session", config: "[profile sso]\nsso_session = gone\n", profile: "sso", err: "has no [sso-session gone]"},
		{name: "no region", config: "[profile sso]\nsso_start_url = https://x\n", profile: "sso", err: "needs sso_start_url and sso_region"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Resolve(awsconf.Parse(c.config), "config", c.profile, c.defaults...)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || got.Profile != c.want.Profile || got.StartURL != c.want.StartURL || got.Region != c.want.Region ||
				got.CacheKey != c.want.CacheKey || !slices.Equal(got.Scopes, c.want.Scopes) ||
				(c.want.Name != "" && got.Name != c.want.Name) {
				t.Fatalf("got %+v, %v\nwant %+v", got, err, c.want)
			}
		})
	}
}

type fakeOIDC struct {
	reg    *ssooidc.RegisterClientInput
	start  *ssooidc.StartDeviceAuthorizationInput
	tokens []error // CreateToken errors in order; nil returns the token
	calls  int
	region string
}

func (f *fakeOIDC) RegisterClient(_ context.Context, in *ssooidc.RegisterClientInput, _ ...func(*ssooidc.Options)) (*ssooidc.RegisterClientOutput, error) {
	f.reg = in
	return &ssooidc.RegisterClientOutput{ClientId: aws.String("cid"), ClientSecret: aws.String("csecret"),
		ClientSecretExpiresAt: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC).Unix()}, nil
}

func (f *fakeOIDC) StartDeviceAuthorization(_ context.Context, in *ssooidc.StartDeviceAuthorizationInput, _ ...func(*ssooidc.Options)) (*ssooidc.StartDeviceAuthorizationOutput, error) {
	f.start = in
	return &ssooidc.StartDeviceAuthorizationOutput{DeviceCode: aws.String("dev"), UserCode: aws.String("ABCD-EFGH"),
		VerificationUri:         aws.String("https://device.sso.us-east-1.amazonaws.com/"),
		VerificationUriComplete: aws.String("https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH"),
		Interval:                2, ExpiresIn: 60}, nil
}

func (f *fakeOIDC) CreateToken(_ context.Context, in *ssooidc.CreateTokenInput, _ ...func(*ssooidc.Options)) (*ssooidc.CreateTokenOutput, error) {
	f.calls++
	if aws.ToString(in.DeviceCode) != "dev" || aws.ToString(in.ClientSecret) != "csecret" {
		return nil, errors.New("wrong device code or client")
	}
	if f.calls <= len(f.tokens) && f.tokens[f.calls-1] != nil {
		return nil, f.tokens[f.calls-1]
	}
	return &ssooidc.CreateTokenOutput{AccessToken: aws.String("access"), RefreshToken: aws.String("refresh"), ExpiresIn: 28800}, nil
}

// newLogin returns a Login on f with a fake clock that advances by each wait, recorded in waits.
func newLogin(f *fakeOIDC, out *strings.Builder, waits *[]time.Duration, opened *[]string) *Login {
	now := time.Date(2026, 11, 18, 4, 0, 0, 0, time.UTC)
	return &Login{Out: out,
		Client: func(_ context.Context, _, region string) (OIDC, error) { f.region = region; return f, nil },
		Browser: func(url string) error {
			*opened = append(*opened, url)
			return nil
		},
		Now: func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			*waits = append(*waits, d)
			now = now.Add(d)
			c := make(chan time.Time, 1)
			c <- now
			return c
		}}
}

func settings(t *testing.T) Settings {
	t.Helper()
	s, err := Resolve(awsconf.Parse(sessionConfig), "config", "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLoginWritesCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	f := &fakeOIDC{tokens: []error{&types.AuthorizationPendingException{}, &types.SlowDownException{}}}
	var out strings.Builder
	var waits []time.Duration
	var opened []string
	if err := newLogin(f, &out, &waits, &opened).Run(context.Background(), settings(t)); err != nil {
		t.Fatal(err)
	}
	if f.region != "us-east-1" || !slices.Equal(f.reg.Scopes, []string{"sso:account:access", "codewhisperer:completions"}) ||
		aws.ToString(f.start.StartUrl) != "https://example.awsapps.com/start" {
		t.Fatalf("region %s, register %+v, start %+v", f.region, f.reg, f.start)
	}
	if !slices.Equal(waits, []time.Duration{2 * time.Second, 2 * time.Second, 7 * time.Second}) {
		t.Fatalf("waits %v: want the interval, then 5s more after a slow-down", waits)
	}
	if !slices.Equal(opened, []string{"https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH"}) {
		t.Fatalf("opened %v", opened)
	}
	for _, want := range []string{"Opened https://device.sso.us-east-1.amazonaws.com/?user_code=ABCD-EFGH in your browser.",
		"go to https://device.sso.us-east-1.amazonaws.com/ and enter the code ABCD-EFGH", "Signed in to sso."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// The SDKs find an sso-session's token under the SHA-1 of the session name.
	path := filepath.Join(home, ".aws", "sso", "cache", "ee0bfd2552fbd840c02cc48b6e823320543c450f.json")
	if got := CachePath(home, "corp"); got != path {
		t.Fatalf("CachePath = %s, want %s", got, path)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("cache file mode %v, want 0600", st.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var got map[string]string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"startUrl": "https://example.awsapps.com/start", "region": "us-east-1", "accessToken": "access",
		"expiresAt": "2026-11-18T12:00:11Z", "refreshToken": "refresh", "clientId": "cid", "clientSecret": "csecret",
		"registrationExpiresAt": "2027-02-01T00:00:00Z"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("cache dir has %d files, want 1 (no temp file left)", len(entries))
	}
}

func TestLoginNoBrowser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out strings.Builder
	var waits []time.Duration
	var opened []string
	l := newLogin(&fakeOIDC{}, &out, &waits, &opened)
	l.NoBrowser = true
	if err := l.Run(context.Background(), settings(t)); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 0 || strings.Contains(out.String(), "Opened") ||
		!strings.Contains(out.String(), "go to https://device.sso.us-east-1.amazonaws.com/ and enter the code ABCD-EFGH") {
		t.Fatalf("opened %v\n%s", opened, out.String())
	}
}

func TestLoginFailures(t *testing.T) {
	pending := make([]error, 100)
	for i := range pending {
		pending[i] = &types.AuthorizationPendingException{}
	}
	t.Run("code expires", func(t *testing.T) {
		var out strings.Builder
		var waits []time.Duration
		var opened []string
		err := newLogin(&fakeOIDC{tokens: pending}, &out, &waits, &opened).Run(context.Background(), settings(t))
		if err == nil || !strings.Contains(err.Error(), "expired before it was approved") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("denied", func(t *testing.T) {
		var out strings.Builder
		var waits []time.Duration
		var opened []string
		err := newLogin(&fakeOIDC{tokens: []error{&types.AccessDeniedException{}}}, &out, &waits, &opened).Run(context.Background(), settings(t))
		var denied *types.AccessDeniedException
		if !errors.As(err, &denied) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		var out strings.Builder
		var waits []time.Duration
		var opened []string
		l := newLogin(&fakeOIDC{tokens: pending}, &out, &waits, &opened)
		l.After = func(time.Duration) <-chan time.Time { return nil }
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := l.Run(ctx, settings(t)); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	})
}
