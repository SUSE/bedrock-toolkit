// Package sso signs in to an AWS SSO (IAM Identity Center) profile with the device authorization
// flow, as aws sso login does, and writes the token to the cache the AWS CLI and SDKs read.
package sso

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc/types"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
)

// DefaultScope is the registration scope when the sso-session sets no sso_registration_scopes.
const DefaultScope = "sso:account:access"

// OIDC is what the sign-in calls in AWS.
type OIDC interface {
	RegisterClient(ctx context.Context, in *ssooidc.RegisterClientInput, opts ...func(*ssooidc.Options)) (*ssooidc.RegisterClientOutput, error)
	StartDeviceAuthorization(ctx context.Context, in *ssooidc.StartDeviceAuthorizationInput, opts ...func(*ssooidc.Options)) (*ssooidc.StartDeviceAuthorizationOutput, error)
	CreateToken(ctx context.Context, in *ssooidc.CreateTokenInput, opts ...func(*ssooidc.Options)) (*ssooidc.CreateTokenOutput, error)
}

// Login signs in to one SSO profile. The zero value uses the real AWS calls, browser and clock.
type Login struct {
	Out io.Writer
	// NoBrowser only prints the URL and code.
	NoBrowser bool
	// Client returns the OIDC client for the profile and SSO region; nil means the SDK's.
	Client func(ctx context.Context, profile, region string) (OIDC, error)
	// Browser opens a URL; nil means the system's browser.
	Browser func(url string) error
	// Portal returns the SSO portal client of an SSO region, which lists accounts and roles; nil
	// means the SDK's.
	Portal func(ctx context.Context, region string) (Portal, error)
	// Choose picks one of options (each "value  description") and returns its value; nil means
	// there is no one to ask, so Create fails and lists them.
	Choose func(question string, options []string) (string, error)
	// Now and After are the clock; nil means the real one.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

// Settings are the SSO settings of a profile.
type Settings struct {
	Profile, StartURL, Region string
	Scopes                    []string
	// CacheKey is hashed into the cache file name: the sso-session name, or the start URL of a
	// legacy profile without one.
	CacheKey string
	// Name is what was signed in to, for the output: the profile, or the sso-session that several
	// profiles share.
	Name string
}

// Resolve picks the SSO sign-in. A non-empty profile must exist; otherwise the first of defaults
// that exists in the file, else the only SSO sign-in in the file: one SSO profile, or several that
// share one sso-session. A profile that assumes a role leads to its SSO profile through source_profile.
func Resolve(file *awsconf.File, path, profile string, defaults ...string) (Settings, error) {
	if profile != "" {
		return resolveChain(file, path, profile)
	}
	for _, d := range defaults {
		if d != "" && file.Profile(d) != nil {
			return resolveChain(file, path, d)
		}
	}
	ps := file.SSOProfiles()
	if len(ps) == 0 {
		return Settings{}, fmt.Errorf("no SSO profile in %s: add one as the user guide shows, then pass --profile", path)
	}
	var first Settings
	for i, name := range ps {
		s, err := resolveSSO(file, path, name)
		if err != nil {
			return Settings{}, err
		}
		if i == 0 {
			first = s
		} else if s.CacheKey != first.CacheKey || s.StartURL != first.StartURL || s.Region != first.Region {
			return Settings{}, fmt.Errorf("several SSO profiles in %s (%s): pass --profile", path, strings.Join(ps, ", "))
		}
	}
	if len(ps) > 1 {
		first.Name = fmt.Sprintf("sso-session %s (profiles %s)", first.CacheKey, strings.Join(ps, ", "))
	}
	return first, nil
}

// resolveChain follows source_profile from profile to the SSO profile it signs in with.
func resolveChain(file *awsconf.File, path, profile string) (Settings, error) {
	name, seen := profile, map[string]bool{}
	for {
		p := file.Profile(name)
		if p == nil {
			return Settings{}, fmt.Errorf("no profile %q in %s", name, path)
		}
		if p.IsSSO() {
			return resolveSSO(file, path, name)
		}
		src := p.Keys["source_profile"]
		if src == "" || seen[name] {
			if name != profile {
				return Settings{}, fmt.Errorf("profile %q in %s signs in through %q, which is not an SSO profile", profile, path, name)
			}
			return Settings{}, fmt.Errorf("profile %q in %s is not an SSO profile", profile, path)
		}
		seen[name] = true
		name = src
	}
}

// resolveSSO reads the settings of an SSO profile.
func resolveSSO(file *awsconf.File, path, profile string) (Settings, error) {
	p := file.Profile(profile)
	s := Settings{Profile: profile, Name: profile, StartURL: p.Keys["sso_start_url"], Region: p.Keys["sso_region"], CacheKey: p.Keys["sso_start_url"]}
	if name := p.Keys["sso_session"]; name != "" {
		sess := file.Section("sso-session " + name)
		if sess == nil {
			return Settings{}, fmt.Errorf("profile %q uses sso_session %s, but %s has no [sso-session %s]", profile, name, path, name)
		}
		s.StartURL, s.Region, s.CacheKey = sess.Keys["sso_start_url"], sess.Keys["sso_region"], name
		for _, sc := range strings.Split(sess.Keys["sso_registration_scopes"], ",") {
			if sc = strings.TrimSpace(sc); sc != "" {
				s.Scopes = append(s.Scopes, sc)
			}
		}
	}
	if s.StartURL == "" || s.Region == "" {
		return Settings{}, fmt.Errorf("SSO profile %q needs sso_start_url and sso_region", profile)
	}
	if len(s.Scopes) == 0 {
		s.Scopes = []string{DefaultScope}
	}
	return s, nil
}

// Run signs in with the device authorization flow and writes the token cache.
func (l *Login) Run(ctx context.Context, s Settings) error {
	_, err := l.signIn(ctx, s)
	return err
}

// signIn is Run, returning the access token.
func (l *Login) signIn(ctx context.Context, s Settings) (string, error) {
	client, err := l.client(ctx, s)
	if err != nil {
		return "", err
	}
	reg, err := client.RegisterClient(ctx, &ssooidc.RegisterClientInput{ClientName: aws.String("bedrock-toolkit"),
		ClientType: aws.String("public"), Scopes: s.Scopes})
	if err != nil {
		return "", fmt.Errorf("registering with IAM Identity Center in %s: %w", s.Region, err)
	}
	auth, err := client.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{ClientId: reg.ClientId,
		ClientSecret: reg.ClientSecret, StartUrl: aws.String(s.StartURL)})
	if err != nil {
		return "", fmt.Errorf("starting the sign-in at %s: %w", s.StartURL, err)
	}
	l.prompt(auth)
	interval := time.Duration(auth.Interval) * time.Second
	if interval < time.Second {
		interval = time.Second
	}
	deadline := l.now().Add(time.Duration(auth.ExpiresIn) * time.Second)
	for l.now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-l.after(interval):
		}
		token, err := client.CreateToken(ctx, &ssooidc.CreateTokenInput{ClientId: reg.ClientId, ClientSecret: reg.ClientSecret,
			DeviceCode: auth.DeviceCode, GrantType: aws.String("urn:ietf:params:oauth:grant-type:device_code")})
		var pending *types.AuthorizationPendingException
		var slow *types.SlowDownException
		switch {
		case errors.As(err, &pending):
			continue
		case errors.As(err, &slow):
			interval += 5 * time.Second
			continue
		case err != nil:
			return "", fmt.Errorf("signing in: %w", err)
		}
		if err := l.writeCache(s, reg, token); err != nil {
			return "", err
		}
		l.printf("Signed in to %s.\n", s.Name)
		return aws.ToString(token.AccessToken), nil
	}
	return "", errors.New("the sign-in code expired before it was approved: run login again")
}

func (l *Login) prompt(auth *ssooidc.StartDeviceAuthorizationOutput) {
	uri, code := aws.ToString(auth.VerificationUri), aws.ToString(auth.UserCode)
	full := aws.ToString(auth.VerificationUriComplete)
	if full != "" && !l.NoBrowser && l.browser(full) == nil {
		l.printf("Opened %s in your browser.\n", full)
	}
	l.printf("If no browser opened, go to %s and enter the code %s\n", uri, code)
}

// cacheEntry is the token cache file of the AWS CLI v2 and SDKs.
type cacheEntry struct {
	StartURL              string `json:"startUrl"`
	Region                string `json:"region"`
	AccessToken           string `json:"accessToken"`
	ExpiresAt             string `json:"expiresAt"`
	RefreshToken          string `json:"refreshToken,omitempty"`
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	RegistrationExpiresAt string `json:"registrationExpiresAt"`
}

// CachePath is the token cache file of a cache key under home.
func CachePath(home, key string) string {
	sum := sha1.Sum([]byte(key))
	return filepath.Join(home, ".aws", "sso", "cache", hex.EncodeToString(sum[:])+".json")
}

func (l *Login) writeCache(s Settings, reg *ssooidc.RegisterClientOutput, token *ssooidc.CreateTokenOutput) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := CachePath(home, s.CacheKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cacheEntry{StartURL: s.StartURL, Region: s.Region, AccessToken: aws.ToString(token.AccessToken),
		ExpiresAt:    l.now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339),
		RefreshToken: aws.ToString(token.RefreshToken), ClientID: aws.ToString(reg.ClientId), ClientSecret: aws.ToString(reg.ClientSecret),
		RegistrationExpiresAt: time.Unix(reg.ClientSecretExpiresAt, 0).UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bedrock-login-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (l *Login) client(ctx context.Context, s Settings) (OIDC, error) {
	if l.Client != nil {
		return l.Client(ctx, s.Profile, s.Region)
	}
	// The shared config brings the profile's ca_bundle, AWS_CA_BUNDLE and the proxy settings.
	cfg, err := awsx.Load(ctx, s.Profile, s.Region)
	if err != nil {
		return nil, err
	}
	return ssooidc.NewFromConfig(cfg), nil
}

func (l *Login) browser(url string) error {
	if l.Browser != nil {
		return l.Browser(url)
	}
	return OpenBrowser(url)
}

// OpenBrowser starts the system's browser on url without waiting for it. It fails where no
// browser can show up, such as an SSH session without a display.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if os.Getenv("SSH_CONNECTION") != "" {
			return errors.New("no browser over SSH")
		}
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (l *Login) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Login) after(d time.Duration) <-chan time.Time {
	if l.After != nil {
		return l.After(d)
	}
	return time.After(d)
}

func (l *Login) printf(format string, args ...any) {
	if l.Out != nil {
		_, _ = fmt.Fprintf(l.Out, format, args...)
	}
}
