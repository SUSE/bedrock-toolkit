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
	off, on := UserInvoke("123456789012", "bedrock-user-a", "bedrock-a", "", nil), UserInvoke("123456789012", "bedrock-user-a", "bedrock-a", "us-east-1", nil)
	if strings.Contains(off, "bedrock-agentcore") {
		t.Fatalf("web search off must not allow gateways: %s", off)
	}
	for _, want := range []string{`"arn:aws:budgets::123456789012:budget/bedrock-a"`, `"arn:aws:budgets::123456789012:budget/bedrock-a/action/*"`,
		"budgets:ViewBudget", `"arn:aws:iam::123456789012:role/bedrock-users/bedrock-user-a"`, "iam:ListRoleTags"} {
		if !strings.Contains(off, want) {
			t.Fatalf("self-read %s missing: %s", want, off)
		}
	}
	if !strings.Contains(on, `"arn:aws:bedrock-agentcore:us-east-1:123456789012:gateway/*"`) || !strings.Contains(on, "bedrock-agentcore:ListGateways") || !strings.Contains(on, "bedrock-agentcore:GetGateway") {
		t.Fatalf("web search on: %s", on)
	}
	if !slices.Contains(DenyActions, "bedrock-agentcore:InvokeGateway") || !strings.Contains(Deny(), "bedrock-agentcore:InvokeGateway") {
		t.Fatal("a pause must stop web search")
	}
	trust := WebSearchGatewayTrust("eu-west-1", "123456789012")
	if !strings.Contains(trust, "bedrock-agentcore.amazonaws.com") || !strings.Contains(trust, "arn:aws:bedrock-agentcore:eu-west-1:123456789012:gateway/*") {
		t.Fatalf("trust: %s", trust)
	}
	perms := WebSearchGatewayPermissions("eu-west-1", "123456789012")
	if !strings.Contains(perms, "arn:aws:bedrock-agentcore:eu-west-1:aws:tool/web-search.v1") || strings.Contains(perms, "bedrock:") {
		t.Fatalf("permissions: %s", perms)
	}
}

func TestUserInvokeModels(t *testing.T) {
	any := UserInvoke("123456789012", "r", "b", "", nil)
	if !strings.Contains(any, "foundation-model/anthropic.claude-*") || !strings.Contains(any, "DenyUnapprovedFableModels") {
		t.Fatalf("no models must allow any Claude model: %s", any)
	}
	if m, err := ListedModels(any); err != nil || m != nil {
		t.Fatalf("ListedModels(any) = %v, %v", m, err)
	}
	models := []string{"us.anthropic.claude-haiku-5-5", "anthropic.claude-opus-5-5"}
	listed := UserInvoke("123456789012", "r", "b", "", models)
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
