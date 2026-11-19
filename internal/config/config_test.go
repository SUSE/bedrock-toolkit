package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const base = `id: acmeprod
account: "123456789012"
region: us-west-2
product: genai-tools
models: [us.anthropic.claude-sonnet-4-5-20250929-v1:0]
defaults:
  limit_usd: 50
cost_export:
  bucket: acme-bedrock-cur
`

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsAndResolve(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "bedrock.yaml", base+`users:
  - email: Alice.Smith@acme.com
  - email: bob@acme.com
    limit_usd: 100
    pause_at_percent: 110
    alert_at_percent: []
  - email: carol@acme.com
    name: carol-x
    notify: lead@acme.com
    product: other-team
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.CostExport.Prefix != "cur" || c.CostExport.Name != "bedrock-cur-acmeprod" || c.LegacyBlock != nil {
		t.Fatalf("defaults not applied: %+v %v", c.CostExport, c.LegacyBlock)
	}
	us := c.Resolve()
	a, b, cc := us[0], us[1], us[2]
	if a.Name != "alice.smith" || a.RoleName != "bedrock-user-acmeprod-alice.smith" || a.BudgetName != "bedrock-acmeprod-alice.smith" {
		t.Errorf("alice names: %+v", a)
	}
	if a.LimitUSD != 50 || a.PauseAtPercent != 100 || len(a.AlertAtPercent) != 1 || a.AlertAtPercent[0] != 80 || a.Notify != a.Email {
		t.Errorf("alice defaults: %+v", a)
	}
	if !slices.Equal(a.NotifyEmails(), []string{a.Email}) {
		t.Errorf("default recipients: %v", a.NotifyEmails())
	}
	if b.LimitUSD != 100 || b.PauseAtPercent != 110 || len(b.AlertAtPercent) != 0 || b.TriggerUSD() != 110 {
		t.Errorf("bob overrides: %+v trigger %v", b, b.TriggerUSD())
	}
	if a.Product != "genai-tools" {
		t.Errorf("alice product: %q", a.Product)
	}
	if cc.Name != "carol-x" || !cc.CustomName || cc.Notify != "lead@acme.com" || cc.Product != "other-team" {
		t.Errorf("carol: %+v", cc)
	}
	if c.ExportPrefix() != "cur/bedrock-cur-acmeprod" {
		t.Errorf("export prefix %q", c.ExportPrefix())
	}
}

func TestAdditionalNotifyEmails(t *testing.T) {
	p := write(t, t.TempDir(), "bedrock.yaml", strings.Replace(base, "  limit_usd: 50\n",
		"  limit_usd: 50\n  additional_notify_emails: [ops@example.com]\n", 1)+`users:
  - email: alice@example.com
    additional_notify_emails: []
  - email: bob@example.com
    notify: lead@example.com
    additional_notify_emails: [OPS@example.com, Lead@example.com, finance@example.com]
  - email: carol@example.com
    additional_notify_emails: [regional@example.com]
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	c, err = Load(write(t, t.TempDir(), "roundtrip.yaml", string(b)))
	if err != nil {
		t.Fatal(err)
	}
	wants := [][]string{
		{"alice@example.com", "ops@example.com"},
		{"lead@example.com", "ops@example.com", "finance@example.com"},
		{"carol@example.com", "ops@example.com", "regional@example.com"},
	}
	for i, u := range c.Resolve() {
		if got := u.NotifyEmails(); !slices.Equal(got, wants[i]) {
			t.Errorf("%s recipients: %v, want %v", u.Email, got, wants[i])
		}
	}
}

func TestAdditionalNotifyEmailsPerUserOnly(t *testing.T) {
	p := write(t, t.TempDir(), "bedrock.yaml", base+`users:
  - email: alice@example.com
    additional_notify_emails: [ops@example.com]
  - email: bob@example.com
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	us := c.Resolve()
	if got := us[0].NotifyEmails(); !slices.Equal(got, []string{"alice@example.com", "ops@example.com"}) {
		t.Errorf("alice recipients: %v", got)
	}
	if got := us[1].NotifyEmails(); !slices.Equal(got, []string{"bob@example.com"}) {
		t.Errorf("bob recipients: %v", got)
	}
}

func TestAdditionalNotifyEmailsLimit(t *testing.T) {
	defaults := strings.Replace(base, "  limit_usd: 50\n", `  limit_usd: 50
  additional_notify_emails: [a@example.com, b@example.com, c@example.com, d@example.com, e@example.com]
`, 1)
	user := `users:
  - email: owner@example.com
    additional_notify_emails: [A@example.com, f@example.com, g@example.com, h@example.com, i@example.com%s]
`
	p := write(t, t.TempDir(), "bedrock.yaml", defaults+strings.Replace(user, "%s", "", 1))
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
	p = write(t, t.TempDir(), "bedrock.yaml", defaults+strings.Replace(user, "%s", ", j@example.com", 1))
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "at most 10 distinct email recipients including notify (got 11)") {
		t.Fatalf("want merged recipient limit error, got %v", err)
	}
	ten := strings.Replace(base, "  limit_usd: 50\n", `  limit_usd: 50
  additional_notify_emails: [a@x.io, b@x.io, c@x.io, d@x.io, e@x.io, f@x.io, g@x.io, h@x.io, i@x.io, j@x.io]
`, 1)
	p = write(t, t.TempDir(), "bedrock.yaml", ten+"users: [{email: u1@x.io}, {email: u2@x.io}]\n")
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "defaults.additional_notify_emails: AWS Budgets allows at most 10 distinct email recipients including notify (got 11)") ||
		strings.Contains(err.Error(), "u1@x.io") {
		t.Fatalf("want one defaults error and no per-user errors, got %v", err)
	}
	p = write(t, t.TempDir(), "bedrock.yaml", strings.Replace(ten, "  limit_usd: 50\n", "  limit_usd: 50\n  notify: a@x.io\n", 1)+"users: [{email: u1@x.io}]\n")
	if _, err := Load(p); err != nil {
		t.Fatalf("a fixed notify among the ten defaults is 10 recipients: %v", err)
	}
}

func TestInclude(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "users.yaml", "- email: a@x.io\n- email: b@x.io\n")
	p := write(t, dir, "bedrock.yaml", base+"users: !include users.yaml\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Users) != 2 || c.Users[1].Email != "b@x.io" {
		t.Fatalf("include: %+v", c.Users)
	}
}

func TestIncludeLoop(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.yaml", "x: !include b.yaml\n")
	write(t, dir, "b.yaml", "y: !include a.yaml\n")
	if _, err := Parse(filepath.Join(dir, "a.yaml")); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("want loop error, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"bad email":    {base + "users: [{email: nope}]\n", "not an email"},
		"bad limit":    {base + "users: [{email: a@x.io, limit_usd: -1}]\n", "limit_usd"},
		"limit max":    {base + "users: [{email: a@x.io, limit_usd: 1000001}]\n", "at most 1000000 (got 1000001)"},
		"long name":    {base + "users: [{email: a@x.io, name: " + strings.Repeat("x", 31) + "}]\n", "must be 1-30"},
		"id with dash": {strings.Replace(base, "id: acmeprod", "id: acme-prod", 1), "id: must be 1-20"},
		"id too long":  {strings.Replace(base, "id: acmeprod", "id: "+strings.Repeat("a", 21), 1), "id: must be 1-20"},
		"default pause": {strings.Replace(base, "  limit_usd: 50\n", "  limit_usd: 50\n  pause_at_percent: 250\n", 1),
			"defaults.pause_at_percent: must be 1-200 (got 250)"},
		"decimals":   {base + "users: [{email: a@x.io, limit_usd: 1.234}]\n", "two decimals"},
		"bad name":   {base + "users: [{email: a@x.io, name: Bad_Name}]\n", "name:"},
		"dup email":  {base + "users: [{email: a@x.io}, {email: A@x.io}]\n", "duplicate"},
		"dup name":   {base + "users: [{email: a@x.io}, {email: a@y.io}]\n", "also used"},
		"non-claude": {strings.Replace(base, "models: [us.anthropic.claude-sonnet-4-5-20250929-v1:0]", "models: [us.meta.llama3-3-70b-instruct-v1:0]", 1), "can't be called from personal roles"},
		"fable":      {strings.Replace(base, "models: [us.anthropic.claude-sonnet-4-5-20250929-v1:0]", "models: [us.anthropic.claude-fable-1-v1:0]", 1), "can't be called from personal roles"},
		"pause 0":    {base + "users: [{email: a@x.io, pause_at_percent: 0}]\n", "1-200"},
		"pause 201":  {base + "users: [{email: a@x.io, pause_at_percent: 201}]\n", "1-200"},
		"alert 300":  {base + "users: [{email: a@x.io, alert_at_percent: [300]}]\n", "1-200"},
		"bad notify": {base + "users: [{email: a@x.io, notify: someone}]\n", "notify"},
		"bad additional default": {strings.Replace(base, "  limit_usd: 50\n", "  limit_usd: 50\n  additional_notify_emails: [owner]\n", 1),
			"defaults.additional_notify_emails[0]"},
		"bad additional user": {base + "users: [{email: a@x.io, additional_notify_emails: [nope]}]\n", "users[0] (a@x.io): additional_notify_emails[0]"},
		"bad user product":    {base + "users: [{email: a@x.io, product: \"a,b\"}]\n", "users[0] (a@x.io): product:"},
		"bad account":         {strings.Replace(base, `"123456789012"`, `"12"`, 1), "account"},
		"bad id":              {strings.Replace(base, "acmeprod", "Acme Prod", 1), "id:"},
		"web search region":   {base + "web_search: {enabled: true}\n", "only available in us-east-1"},
		"web search domain":   {strings.Replace(base, "us-west-2", "us-east-1", 1) + "web_search: {enabled: true, exclude_domains: [Example.com]}\n", "exclude_domains[0]"},
		"bad iam_user":        {base + "users: [{email: a@x.io, iam_user: \"a b\"}]\n", "iam_user: not an IAM user name"},
		"dup iam_user":        {base + "users: [{email: a@x.io, iam_user: dave}, {email: b@x.io, iam_user: Dave}]\n", "iam_user: Dave is also used by users[0]"},
		"no limit any":        {strings.Replace(base, "  limit_usd: 50\n", "", 1) + "users: [{email: a@x.io}]\n", "limit_usd: not set"},
		"tag aws prefix":      {base + "  tags: {\"AWS:x\": y}\n", `key "AWS:x": the aws: prefix is reserved`},
		"tag id key":          {base + "  tags: {\"bedrock-admin:id\": y}\n", `key "bedrock-admin:id" is set by bedrock-admin`},
		"tag bad key":         {base + "  tags: {\"a,b\": y}\n", `key "a,b" must be 1-128`},
		"tag long value":      {base + "  tags: {k: " + strings.Repeat("v", 257) + "}\n", "cost_export.tags.k: value must be at most 256"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := write(t, t.TempDir(), "bedrock.yaml", tc.body)
			_, err := Load(p)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want validation error, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in %v", tc.want, err)
			}
		})
	}
}

func TestWebSearchEnabled(t *testing.T) {
	p := write(t, t.TempDir(), "bedrock.yaml", strings.Replace(base, "us-west-2", "eu-west-1", 1)+
		"web_search:\n  enabled: true\n  exclude_domains: [example.com, a.b.co.uk]\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.WebSearch.Enabled || len(c.WebSearch.ExcludeDomains) != 2 {
		t.Fatalf("web search: %+v", c.WebSearch)
	}
	// Disabled web search doesn't restrict the region.
	if _, err := Load(write(t, t.TempDir(), "bedrock.yaml", base+"web_search: {enabled: false}\n")); err != nil {
		t.Fatal(err)
	}
}

func TestValidationListsEveryProblem(t *testing.T) {
	p := write(t, t.TempDir(), "bedrock.yaml", base+"users: [{email: nope}, {email: b@x.io, limit_usd: 0}, {email: c@x.io, pause_at_percent: 0}]\n")
	_, err := Load(p)
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Problems) != 3 {
		t.Fatalf("want 3 problems, got %v", err)
	}
	if !strings.HasPrefix(err.Error(), "invalid config:\n  - users[0] (nope): email: not an email address") {
		t.Fatalf("got %q", err)
	}
}

func TestPauseBounds(t *testing.T) {
	for _, v := range []string{"1", "200"} {
		p := write(t, t.TempDir(), "bedrock.yaml", base+"users: [{email: a@x.io, pause_at_percent: "+v+"}]\n")
		if _, err := Load(p); err != nil {
			t.Errorf("pause %s: %v", v, err)
		}
	}
}

func TestFindOrder(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	t.Setenv(EnvConfig, "")
	if _, err := Find(""); err == nil {
		t.Fatal("want error when nothing exists")
	}
	write(t, dir, "bedrock.yaml", base)
	if p, _ := Find(""); p != "bedrock.yaml" {
		t.Errorf("cwd: %q", p)
	}
	t.Setenv(EnvConfig, "/env.yaml")
	if p, _ := Find(""); p != "/env.yaml" {
		t.Errorf("env: %q", p)
	}
	if p, _ := Find("/flag.yaml"); p != "/flag.yaml" {
		t.Errorf("flag: %q", p)
	}
}

func TestNameFromEmail(t *testing.T) {
	for in, want := range map[string]string{
		"alice@acme.com":     "alice",
		"Bob.Jones@acme.com": "bob.jones",
		"x+tag@acme.com":     "x-tag",
		"_a_@acme.com":       "a",
	} {
		if got := NameFromEmail(in); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	p := write(t, t.TempDir(), "bedrock.yaml", base+"  tags: {pcw_ignore: \"1\"}\nusers: [{email: a@x.io, alert_at_percent: []}]\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	p2 := write(t, t.TempDir(), "b.yaml", string(out))
	c2, err := Load(p2)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if c2.Users[0].AlertAtPercent == nil || len(*c2.Users[0].AlertAtPercent) != 0 {
		t.Fatalf("empty alert list lost:\n%s", out)
	}
	if c2.CostExport.Tags["pcw_ignore"] != "1" {
		t.Fatalf("cost_export.tags lost:\n%s", out)
	}
}

func TestIAMUserResolved(t *testing.T) {
	p := write(t, t.TempDir(), "bedrock.yaml", base+"users: [{email: a@x.io, iam_user: a.dev}, {email: b@x.io}]\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	us := cfg.Resolve()
	if us[0].IAMUser != "a.dev" || us[1].IAMUser != "" {
		t.Fatalf("got %q %q", us[0].IAMUser, us[1].IAMUser)
	}
}
