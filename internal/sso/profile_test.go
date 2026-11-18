package sso

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssso "github.com/aws/aws-sdk-go-v2/service/sso"
	ssotypes "github.com/aws/aws-sdk-go-v2/service/sso/types"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
)

type fakePortal struct {
	accounts map[string]string   // ID: name
	roles    map[string][]string // account: roles
	token    string
}

func (f *fakePortal) ListAccounts(_ context.Context, in *awssso.ListAccountsInput, _ ...func(*awssso.Options)) (*awssso.ListAccountsOutput, error) {
	f.token = aws.ToString(in.AccessToken)
	out := &awssso.ListAccountsOutput{}
	for id, name := range f.accounts {
		out.AccountList = append(out.AccountList, ssotypes.AccountInfo{AccountId: aws.String(id), AccountName: aws.String(name)})
	}
	return out, nil
}

func (f *fakePortal) ListAccountRoles(_ context.Context, in *awssso.ListAccountRolesInput, _ ...func(*awssso.Options)) (*awssso.ListAccountRolesOutput, error) {
	out := &awssso.ListAccountRolesOutput{}
	for _, r := range f.roles[aws.ToString(in.AccountId)] {
		out.RoleList = append(out.RoleList, ssotypes.RoleInfo{RoleName: aws.String(r)})
	}
	return out, nil
}

// create runs Create on config text and returns the profile, the new file, the output and the
// number of sign-ins.
func create(t *testing.T, config string, p NewProfile, portal *fakePortal, choose func(string, []string) (string, error)) (name, file, out string, signIns int, err error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if config != "" {
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := awsconf.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{}
	var o strings.Builder
	var waits []time.Duration
	var opened []string
	l := newLogin(f, &o, &waits, &opened)
	l.Portal = func(context.Context, string) (Portal, error) { return portal, nil }
	l.Choose = choose
	name, err = l.Create(context.Background(), cfg, path, p)
	b, _ := os.ReadFile(path)
	return name, string(b), o.String(), f.calls, err
}

func TestCreateProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	one := &fakePortal{accounts: map[string]string{"111122223333": "qa"}, roles: map[string][]string{"111122223333": {"Developer"}}}
	p := NewProfile{DefaultName: "bedrock-sso", StartURL: "https://example.awsapps.com/start", Region: "us-east-1", ProfileRegion: "us-west-2"}

	name, file, out, signIns, err := create(t, "[profile other]\nregion = eu-west-1\n", p, one, nil)
	want := `[profile other]
region = eu-west-1

[sso-session example]
sso_start_url = https://example.awsapps.com/start
sso_region = us-east-1
sso_registration_scopes = sso:account:access

[profile bedrock-sso]
sso_session = example
sso_account_id = 111122223333
sso_role_name = Developer
region = us-west-2
`
	if err != nil || name != "bedrock-sso" || file != want || signIns != 1 || one.token != "access" ||
		!strings.Contains(out, "Wrote profile bedrock-sso (account 111122223333, role Developer)") {
		t.Fatalf("%v %q %d\n%s\n%s", err, name, signIns, file, out)
	}

	// The profile exists now: with a valid token, Create returns it without signing in.
	name, file2, _, signIns, err := create(t, file, p, one, nil)
	if err != nil || name != "bedrock-sso" || file2 != file || signIns != 0 {
		t.Fatalf("%v %q %d\n%s", err, name, signIns, file2)
	}
	p.Force = true
	if _, _, _, signIns, err := create(t, file, p, one, nil); err != nil || signIns != 1 {
		t.Fatalf("%v %d", err, signIns)
	}
	p.Force = false

	// A new role for the same start URL reuses the sso-session; the default name is taken.
	two := &fakePortal{accounts: one.accounts, roles: map[string][]string{"111122223333": {"Developer", "Admin"}}}
	p2 := p
	p2.Role = "Admin"
	if _, _, _, _, err := create(t, file, p2, two, nil); err == nil || !strings.Contains(err.Error(), "already has a profile bedrock-sso for another sign-in") {
		t.Fatal(err)
	}
	p2.Name = "admin"
	name, file3, _, _, err := create(t, file, p2, two, nil)
	if err != nil || name != "admin" || strings.Count(file3, "[sso-session") != 1 || !strings.Contains(file3, "[profile admin]\nsso_session = example\nsso_account_id = 111122223333\nsso_role_name = Admin\n") {
		t.Fatalf("%v\n%s", err, file3)
	}

	// Several roles: without anyone to ask, the error lists them; else the answer is used.
	if _, _, _, _, err := create(t, "", p, two, nil); err == nil || !strings.Contains(err.Error(), "pass --role:\n  Developer\n  Admin") {
		t.Fatal(err)
	}
	var asked []string
	choose := func(q string, opts []string) (string, error) { asked = append(asked, q); return opts[1], nil }
	if _, file4, _, _, err := create(t, "", p, two, choose); err != nil || !strings.Contains(file4, "sso_role_name = Admin") || len(asked) != 1 {
		t.Fatalf("%v %v\n%s", err, asked, file4)
	}

	// A session name in use for another start URL.
	p3 := p
	p3.Session, p3.StartURL = "corp", "https://other.awsapps.com/start"
	if _, _, _, _, err := create(t, sessionConfig, p3, one, nil); err == nil || !strings.Contains(err.Error(), "[sso-session corp]") {
		t.Fatal(err)
	}
	p3.StartURL = "http://example.awsapps.com/start"
	if _, _, _, _, err := create(t, "", p3, one, nil); err == nil || !strings.Contains(err.Error(), "must be https://") {
		t.Fatal(err)
	}
}
