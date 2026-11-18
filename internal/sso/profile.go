package sso

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssso "github.com/aws/aws-sdk-go-v2/service/sso"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/SUSE/bedrock-toolkit/internal/awsx"
)

// Portal lists the accounts and roles a signed-in SSO user may use.
type Portal interface {
	ListAccounts(ctx context.Context, in *awssso.ListAccountsInput, opts ...func(*awssso.Options)) (*awssso.ListAccountsOutput, error)
	ListAccountRoles(ctx context.Context, in *awssso.ListAccountRolesInput, opts ...func(*awssso.Options)) (*awssso.ListAccountRolesOutput, error)
}

// NewProfile is an SSO profile to find or create, as aws configure sso does.
type NewProfile struct {
	// Name is the profile; "" means a matching profile, else DefaultName.
	Name, DefaultName string
	// Session is the sso-session name; "" means one with the same start URL, else one named
	// after the start URL's host.
	Session string
	// StartURL and Region are the SSO start URL and the SSO region (required).
	StartURL, Region string
	// Account and Role are the account ID and permission set role; "" means pick from the ones
	// the user may use.
	Account, Role string
	// ProfileRegion is the profile's region; "" means Region.
	ProfileRegion string
	// AccountFlag and RoleFlag name the caller's flags for Account and Role, for the error when
	// there are several to pick from and no terminal to ask.
	AccountFlag, RoleFlag string
	// Force signs in even when a matching profile has a valid token.
	Force bool
}

var nonName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// Create finds the SSO profile of p in file, or signs in, picks the account and role and writes
// the profile and its sso-session to path. It returns the profile name.
func (l *Login) Create(ctx context.Context, file *awsconf.File, path string, p NewProfile) (string, error) {
	u, err := url.Parse(p.StartURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("SSO start URL %q: must be https://..., such as https://example.awsapps.com/start", p.StartURL)
	}
	if p.Region == "" {
		return "", fmt.Errorf("the SSO start URL needs its SSO region (the region of IAM Identity Center)")
	}
	match, err := matchProfile(file, path, p)
	if err != nil {
		return "", err
	}
	var s Settings
	var session string
	newSession := false
	if match != "" {
		if s, err = resolveSSO(file, path, match); err != nil {
			return "", err
		}
		if !p.Force && l.validToken(s) {
			return match, nil
		}
	} else {
		if session, newSession, err = pickSession(file, path, p, u.Host); err != nil {
			return "", err
		}
		s = Settings{Name: "sso-session " + session, StartURL: p.StartURL, Region: p.Region, CacheKey: session, Scopes: []string{DefaultScope}}
		if s2, ok := sessionSettings(file, session); ok && !newSession {
			s.Scopes = s2.Scopes
		}
	}
	token, err := l.signIn(ctx, s)
	if err != nil {
		return "", err
	}
	if match != "" {
		return match, nil
	}

	name := p.Name
	if name == "" {
		name = p.DefaultName
		if file.Profile(name) != nil {
			return "", fmt.Errorf("%s already has a profile %s for another sign-in: name the new profile with --profile-name", path, name)
		}
	}
	portal, err := l.portal(ctx, p.Region)
	if err != nil {
		return "", err
	}
	account := p.Account
	if account == "" {
		if account, err = l.pickAccount(ctx, portal, token, p.AccountFlag); err != nil {
			return "", err
		}
	}
	role := p.Role
	if role == "" {
		if role, err = l.pickRole(ctx, portal, token, account, p.RoleFlag); err != nil {
			return "", err
		}
	}
	region := p.ProfileRegion
	if region == "" {
		region = p.Region
	}
	if newSession {
		file.SetSection("sso-session "+session, []awsconf.KV{{Key: "sso_start_url", Value: p.StartURL},
			{Key: "sso_region", Value: p.Region}, {Key: "sso_registration_scopes", Value: DefaultScope}})
	}
	file.SetProfile(name, []awsconf.KV{{Key: "sso_session", Value: session}, {Key: "sso_account_id", Value: account},
		{Key: "sso_role_name", Value: role}, {Key: "region", Value: region}})
	backup, err := file.Write(path)
	if err != nil {
		return "", fmt.Errorf("writing %s: %w", path, err)
	}
	l.printf("Wrote profile %s (account %s, role %s) to %s", name, account, role, path)
	if backup != "" {
		l.printf(" (previous file: %s)", backup)
	}
	l.printf(".\n")
	return name, nil
}

// pickSession returns the sso-session for p: p.Session, else one with the same start URL and
// region, else a new one named after host. new reports that it isn't in the file yet.
func pickSession(file *awsconf.File, path string, p NewProfile, host string) (name string, isNew bool, err error) {
	same := func(sec *awsconf.Section) bool {
		return strings.TrimSuffix(sec.Keys["sso_start_url"], "/") == strings.TrimSuffix(p.StartURL, "/") && sec.Keys["sso_region"] == p.Region
	}
	if p.Session != "" {
		sec := file.Section("sso-session " + p.Session)
		if sec == nil {
			return p.Session, true, nil
		}
		if !same(sec) {
			return "", false, fmt.Errorf("[sso-session %s] in %s has another start URL or region: pick another --sso-session", p.Session, path)
		}
		return p.Session, false, nil
	}
	for _, sec := range file.Sections {
		if n, ok := strings.CutPrefix(sec.Header, "sso-session "); ok && same(sec) {
			return strings.TrimSpace(n), false, nil
		}
	}
	base := nonName.ReplaceAllString(strings.Split(host, ".")[0], "-")
	if base == "" {
		base = "sso"
	}
	name = base
	for i := 2; file.Section("sso-session "+name) != nil; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name, true, nil
}

func sessionSettings(file *awsconf.File, session string) (Settings, bool) {
	for _, name := range file.SSOProfiles() {
		if file.Profile(name).Keys["sso_session"] == session {
			s, err := resolveSSO(file, "", name)
			return s, err == nil
		}
	}
	return Settings{}, false
}

// matchProfile returns the SSO profile of p's start URL, account and role: p.Name, which must
// match when it exists, else the only one that matches.
func matchProfile(file *awsconf.File, path string, p NewProfile) (string, error) {
	matches := func(name string) bool {
		s, err := resolveSSO(file, path, name)
		k := file.Profile(name).Keys
		return err == nil && strings.TrimSuffix(s.StartURL, "/") == strings.TrimSuffix(p.StartURL, "/") && s.Region == p.Region &&
			(p.Account == "" || k["sso_account_id"] == p.Account) && (p.Role == "" || k["sso_role_name"] == p.Role) &&
			k["sso_account_id"] != "" && k["sso_role_name"] != ""
	}
	if p.Name != "" {
		sec := file.Profile(p.Name)
		switch {
		case sec == nil:
			return "", nil
		case !sec.IsSSO() || !matches(p.Name):
			return "", fmt.Errorf("%s already has a profile %s for another sign-in: pick another name, or remove it", path, p.Name)
		}
		return p.Name, nil
	}
	var found []string
	for _, name := range file.SSOProfiles() {
		if matches(name) {
			found = append(found, name)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	return "", nil
}

// validToken reports whether the cached token of s still works, or the SDK can refresh it.
func (l *Login) validToken(s Settings) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	b, err := os.ReadFile(CachePath(home, s.CacheKey))
	if err != nil {
		return false
	}
	var c cacheEntry
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	now := l.now()
	if exp, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil && exp.After(now.Add(5*time.Minute)) {
		return true
	}
	reg, err := time.Parse(time.RFC3339, c.RegistrationExpiresAt)
	return c.RefreshToken != "" && err == nil && reg.After(now)
}

func (l *Login) pickAccount(ctx context.Context, portal Portal, token, flag string) (string, error) {
	var opts []string
	var next *string
	for {
		out, err := portal.ListAccounts(ctx, &awssso.ListAccountsInput{AccessToken: aws.String(token), NextToken: next})
		if err != nil {
			return "", fmt.Errorf("listing your SSO accounts: %w", err)
		}
		for _, a := range out.AccountList {
			opts = append(opts, aws.ToString(a.AccountId)+"  "+aws.ToString(a.AccountName))
		}
		if next = out.NextToken; next == nil {
			break
		}
	}
	return l.pick("account", flag, opts)
}

func (l *Login) pickRole(ctx context.Context, portal Portal, token, account, flag string) (string, error) {
	var opts []string
	var next *string
	for {
		out, err := portal.ListAccountRoles(ctx, &awssso.ListAccountRolesInput{AccessToken: aws.String(token),
			AccountId: aws.String(account), NextToken: next})
		if err != nil {
			return "", fmt.Errorf("listing your SSO roles in account %s: %w", account, err)
		}
		for _, r := range out.RoleList {
			opts = append(opts, aws.ToString(r.RoleName))
		}
		if next = out.NextToken; next == nil {
			break
		}
	}
	return l.pick("role", flag, opts)
}

// pick returns the only option, or asks which one.
func (l *Login) pick(what, flag string, opts []string) (string, error) {
	switch {
	case len(opts) == 0:
		return "", fmt.Errorf("your SSO sign-in has no %s to use: ask your admin for access", what)
	case len(opts) == 1:
		return strings.Fields(opts[0])[0], nil
	case l.Choose == nil:
		if flag == "" {
			flag = "--" + what
		}
		return "", fmt.Errorf("you may use several of %s: pass %s:\n  %s", what+"s", flag, strings.Join(opts, "\n  "))
	}
	v, err := l.Choose("Which "+what+"?", opts)
	if err != nil {
		return "", err
	}
	return strings.Fields(v)[0], nil
}

func (l *Login) portal(ctx context.Context, region string) (Portal, error) {
	if l.Portal != nil {
		return l.Portal(ctx, region)
	}
	cfg, err := awsx.Load(ctx, "", region)
	if err != nil {
		return nil, err
	}
	return awssso.NewFromConfig(cfg), nil
}
