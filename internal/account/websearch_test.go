package account

import (
	"strings"
	"testing"

	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"

	"github.com/SUSE/high-impact-ai-initiative/internal/plan"
	"github.com/SUSE/high-impact-ai-initiative/internal/policy"
	"github.com/SUSE/high-impact-ai-initiative/internal/websearch"
)

func TestGatewayState(t *testing.T) {
	const account = "123456789012"
	ok := &websearch.Gateway{Status: actypes.GatewayStatusReady, URL: "https://g/mcp", RoleArn: webSearchRoleArn(account),
		Authorizer: actypes.AuthorizerTypeAwsIam}
	otherRole := *ok
	otherRole.RoleArn = "arn:aws:iam::123456789012:role/x"
	failed := *ok
	failed.Status, failed.Reasons = actypes.GatewayStatusFailed, []string{"boom"}
	for _, tc := range []struct {
		name   string
		g      *websearch.Gateway
		op     plan.Op
		target bool
		want   string
	}{
		{"missing", nil, plan.Create, true, "$7 per 1,000"},
		{"ready", ok, plan.OK, true, "ready, https://g/mcp"},
		{"role drift", &otherRole, plan.Update, true, "role or authorizer changed"},
		{"failed", &failed, plan.Conflict, false, "FAILED: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := &plan.Item{Op: plan.OK, Status: "ok"}
			got := gatewayState(it, tc.g, account)
			all := it.Status + strings.Join(it.Details, ",") + strings.Join(it.Notes, ",")
			if it.Op != tc.op || got != tc.target || !strings.Contains(all, tc.want) {
				t.Errorf("op %s, target %v, %q", it.Op, got, all)
			}
		})
	}
}

func TestTargetState(t *testing.T) {
	ok := &websearch.Target{Status: actypes.TargetStatusReady, Connector: policy.WebSearchConnector,
		Version: policy.WebSearchConnectorVersion, Exclude: []string{"b.com", "a.com"}, GatewayRole: true}
	old := *ok
	old.Version = "1.0.0"
	failed := *ok
	failed.Status, failed.Reasons = actypes.TargetStatusFailed, []string{"bad config"}
	for _, tc := range []struct {
		name    string
		t       *websearch.Target
		exclude []string
		op      plan.Op
		want    string
	}{
		{"missing", nil, nil, plan.Create, "connector " + policy.WebSearchConnectorVersion},
		{"ready", ok, []string{"a.com", "b.com"}, plan.OK, "ready, connector " + policy.WebSearchConnectorVersion},
		{"exclude drift", ok, []string{"a.com"}, plan.Update, "exclude_domains [b.com, a.com] → [a.com]"},
		{"old connector", &old, []string{"a.com", "b.com"}, plan.Update, "connector version 1.0.0 → "},
		{"failed", &failed, nil, plan.Conflict, "FAILED: bad config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := &plan.Item{Op: plan.OK, Status: "ok"}
			targetState(it, tc.t, tc.exclude)
			all := it.Status + strings.Join(it.Details, ",")
			if it.Op != tc.op || !strings.Contains(all, tc.want) {
				t.Errorf("op %s, %q", it.Op, all)
			}
		})
	}
}
