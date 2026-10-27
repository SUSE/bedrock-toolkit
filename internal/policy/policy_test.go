package policy

import (
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestEqualIgnoresFormatting(t *testing.T) {
	a := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["b","a"],"Resource":["x"]}]}`
	b := url.QueryEscape(`{"Statement":{"Resource":"x","Action":["a","b"],"Effect":"Allow"},"Version":"2012-10-17"}`)
	if !Equal(a, b) {
		t.Fatal("want equal")
	}
	if Equal(a, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["a","b"],"Resource":"x"}]}`) {
		t.Fatal("want different")
	}
}

func TestHasBlock(t *testing.T) {
	if !HasBlock(Block("BedrockAdmin")) {
		t.Fatal("block policy must contain the block")
	}
	if !HasBlock(`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"},{"Effect":"Deny","Action":"bedrock:*","Resource":"*"}]}`) {
		t.Fatal("bedrock:* deny covers it")
	}
	if HasBlock(`{"Statement":{"Effect":"Deny","Action":["bedrock:InvokeModel"],"Resource":"*"}}`) {
		t.Fatal("partial deny is not the block")
	}
}

func TestAllowsAssume(t *testing.T) {
	if !AllowsAssumePersonalRole(AssumePersonalRoles()) {
		t.Fatal("want allowed")
	}
	if AllowsAssumePersonalRole(`{"Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"arn:aws:iam::1:role/other"}}`) {
		t.Fatal("want not allowed")
	}
}

func TestWebSearchPolicies(t *testing.T) {
	off, on := UserInvoke("123456789012", "t", "bedrock-user-t-a", "bedrock-t-a", "", nil), UserInvoke("123456789012", "t", "bedrock-user-t-a", "bedrock-t-a", "us-east-1", nil)
	if strings.Contains(off, "bedrock-agentcore") {
		t.Fatalf("web search off must not allow gateways: %s", off)
	}
	for _, want := range []string{`"arn:aws:budgets::123456789012:budget/bedrock-t-a"`, `"arn:aws:budgets::123456789012:budget/bedrock-t-a/action/*"`,
		"budgets:ViewBudget", `"arn:aws:iam::123456789012:role/bedrock-users/t/bedrock-user-t-a"`, "iam:ListRoleTags"} {
		if !strings.Contains(off, want) {
			t.Fatalf("self-read %s missing: %s", want, off)
		}
	}
	if !strings.Contains(on, `"arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/bedrock-web-search-t-*"`) || !strings.Contains(on, "bedrock-agentcore:ListGateways") || !strings.Contains(on, "bedrock-agentcore:GetGateway") {
		t.Fatalf("web search on: %s", on)
	}
	if !slices.Contains(DenyActions, "bedrock-agentcore:InvokeGateway") || !strings.Contains(Deny(), "bedrock-agentcore:InvokeGateway") {
		t.Fatal("a pause must stop web search")
	}
	trust := WebSearchGatewayTrust("eu-west-1", "123456789012", "t")
	if !strings.Contains(trust, "bedrock-agentcore.amazonaws.com") || !strings.Contains(trust, "arn:aws:bedrock-agentcore:eu-west-1:123456789012:gateway/bedrock-web-search-t-*") {
		t.Fatalf("trust: %s", trust)
	}
	perms := WebSearchGatewayPermissions("eu-west-1", "123456789012", "t")
	if !strings.Contains(perms, "arn:aws:bedrock-agentcore:eu-west-1:aws:tool/web-search.v1") || strings.Contains(perms, "bedrock:") {
		t.Fatalf("permissions: %s", perms)
	}
}

func TestUserInvokeModels(t *testing.T) {
	any := UserInvoke("123456789012", "t", "r", "b", "", nil)
	if !strings.Contains(any, "foundation-model/anthropic.claude-*") || !strings.Contains(any, "DenyUnapprovedFableModels") {
		t.Fatalf("no models must allow any Claude model: %s", any)
	}
	if m, err := ListedModels(any); err != nil || m != nil {
		t.Fatalf("ListedModels(any) = %v, %v", m, err)
	}
	models := []string{"us.anthropic.claude-haiku-5-5", "anthropic.claude-opus-5-5"}
	listed := UserInvoke("123456789012", "t", "r", "b", "", models)
	for _, want := range []string{`"arn:aws:bedrock:*:123456789012:inference-profile/us.anthropic.claude-haiku-5-5"`,
		`"arn:aws:bedrock:*::foundation-model/anthropic.claude-haiku-5-5"`, `"arn:aws:bedrock:*::foundation-model/anthropic.claude-opus-5-5"`,
		"DenyUnapprovedFableModels"} {
		if !strings.Contains(listed, want) {
			t.Fatalf("%s missing: %s", want, listed)
		}
	}
	if strings.Contains(listed, "anthropic.claude-*\"") || strings.Contains(listed, "inference-profile/anthropic.claude-opus") {
		t.Fatalf("listed models must not allow others: %s", listed)
	}
	got, err := ListedModels(listed)
	if err != nil || !slices.Equal(got, models) {
		t.Fatalf("ListedModels = %v, %v", got, err)
	}
}

func TestNames(t *testing.T) {
	id := strings.Repeat("x", 20)
	for _, n := range []string{DenyPolicyName(id), BudgetActionsRole(id), LambdaRole(id), SchedulerRole(id), WebSearchRole(id),
		UserRoleName(id, strings.Repeat("n", 30))} {
		if len(n) > 64 {
			t.Errorf("%s: %d characters, IAM allows 64", n, len(n))
		}
	}
	if b := BucketName("123456789012", id); len(b) > 63 {
		t.Errorf("%s: %d characters, S3 allows 63", b, len(b))
	}
	if UserRoleArn("1", "acme", "bedrock-user-acme-bob") != "arn:aws:iam::1:role/bedrock-users/acme/bedrock-user-acme-bob" {
		t.Error(UserRoleArn("1", "acme", "bedrock-user-acme-bob"))
	}
	if RoleForBudget("bedrock-acme-bob") != "bedrock-user-acme-bob" || BudgetForRole("bedrock-user-acme-bob") != "bedrock-acme-bob" {
		t.Error("role and budget must map to each other")
	}
	for role, want := range map[string][3]string{
		"bedrock-user-acme-bob.smith": {"acme", "bob.smith", "ok"},
		"bedrock-user-acme-a-b":       {"acme", "a-b", "ok"},
		"bedrock-user-acme":           {},
		"bedrock-budget-actions-acme": {},
	} {
		id, name, ok := SplitUserRole(role)
		if got := [3]string{id, name, map[bool]string{true: "ok"}[ok]}; got != want {
			t.Errorf("SplitUserRole(%s) = %v, want %v", role, got, want)
		}
	}
}

func TestUserTrustIAMUser(t *testing.T) {
	sso := Decode(UserTrust("123456789012", "a@x.io", ""))
	if strings.Contains(sso, "aws:username") || strings.Contains(sso, "IAMUser") {
		t.Fatalf("SSO-only trust must not admit IAM users: %s", sso)
	}
	both := UserTrust("123456789012", "a@x.io", "a.dev")
	for _, want := range []string{`"aws:username":"a.dev"`, `"aws:PrincipalType":"User"`, `"Sid":"IAMUser"`, "AWSReservedSSO_"} {
		if !strings.Contains(both, want) {
			t.Errorf("want %s in %s", want, both)
		}
	}
	if strings.Count(both, `"sts:RoleSessionName":"a@x.io"`) != 2 {
		t.Errorf("both statements must pin the session name to the email: %s", both)
	}
}

func TestIAMUsersPolicy(t *testing.T) {
	off, on := IAMUsers("123456789012", "t", false), IAMUsers("123456789012", "t", true)
	if HasBlock(off) || !HasBlock(on) {
		t.Fatalf("block: off %v on %v", HasBlock(off), HasBlock(on))
	}
	if !strings.Contains(off, UserRolesArn("123456789012", "t")) || !strings.Contains(off, "user/${aws:username}") {
		t.Fatalf("got %s", off)
	}
	if IAMUsersPolicyName("t") != "bedrock-iam-users-t" || IAMUsersPolicyArn("1", "t") != "arn:aws:iam::1:policy"+Path+"bedrock-iam-users-t" {
		t.Fatalf("names %s %s", IAMUsersPolicyName("t"), IAMUsersPolicyArn("1", "t"))
	}
}

func TestLambdaPermissionsReport(t *testing.T) {
	off := LambdaPermissions("us-east-1", "123456789012", "x", "b", "snap", "cur/bedrock-cur-x", "")
	if strings.Contains(off, "CentralReport") {
		t.Fatalf("report statement without a bucket:\n%s", off)
	}
	for _, want := range []string{`"arn:aws:s3:::b/cur/bedrock-cur-x/*"`, `"s3:prefix":"cur/bedrock-cur-x/*"`, `"arn:aws:s3:::b/bedrock-admin/usage/x/*"`} {
		if !strings.Contains(off, want) {
			t.Errorf("lacks %s:\n%s", want, off)
		}
	}
	on := LambdaPermissions("us-east-1", "123456789012", "x", "b", "snap", "cur/bedrock-cur-x", "central")
	if !strings.Contains(on, `"arn:aws:s3:::central/123456789012/*"`) || !strings.Contains(on, "iam:GetRole") {
		t.Fatalf("report statement:\n%s", on)
	}
}

func TestReportBucketPolicy(t *testing.T) {
	org := ReportBucketPolicy("central", "o-abcdefghij", nil)
	for _, want := range []string{`"aws:PrincipalOrgID":"o-abcdefghij"`, `"arn:aws:s3:::central/${aws:PrincipalAccount}/*"`,
		`"aws:SecureTransport":"false"`, `"Principal":"*"`} {
		if !strings.Contains(org, want) {
			t.Errorf("org policy lacks %s:\n%s", want, org)
		}
	}
	if strings.Contains(org, "GetObject") || strings.Contains(org, "ListBucket") {
		t.Errorf("org policy lets setups read:\n%s", org)
	}
	acc := ReportBucketPolicy("central", "", []string{"111111111111", "222222222222"})
	if !strings.Contains(acc, `"AWS":["arn:aws:iam::111111111111:root","arn:aws:iam::222222222222:root"]`) || strings.Contains(acc, "PrincipalOrgID") {
		t.Errorf("accounts policy:\n%s", acc)
	}
}
