package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"

	"github.com/SUSE/high-impact-ai-initiative/internal/awsx"
	"github.com/SUSE/high-impact-ai-initiative/internal/iamrole"
	"github.com/SUSE/high-impact-ai-initiative/internal/plan"
	"github.com/SUSE/high-impact-ai-initiative/internal/policy"
	"github.com/SUSE/high-impact-ai-initiative/internal/websearch"
)

// WebSearchRoleSpec is the Step 1.8 gateway service role.
func WebSearchRoleSpec(region, account string) iamrole.Spec {
	return iamrole.Spec{Name: policy.WebSearchRole, Path: policy.Path,
		Description: "Lets the bedrock-web-search AgentCore gateway call Web Search",
		Trust:       policy.WebSearchGatewayTrust(region, account), InlineName: policy.WebSearchPolicy,
		Inline: policy.WebSearchGatewayPermissions(region, account)}
}

func webSearchRoleArn(account string) string {
	return fmt.Sprintf("arn:aws:iam::%s:role/bedrock/%s", account, policy.WebSearchRole)
}

// webSearch is Step 1.8. It shows only when web search is on, or when there is something to remove.
func (e *Env) webSearch(ctx context.Context) ([]*plan.Item, error) {
	region, account := e.Cfg.Region, e.C.Account
	g, err := websearch.FindGateway(ctx, e.C.AgentCore, policy.WebSearchGateway)
	if err != nil {
		if !e.Cfg.WebSearch.Enabled && (awsx.IsAccessDenied(err) || isUnsupportedRegion(err)) {
			return nil, nil
		}
		return nil, err
	}
	if !e.Cfg.WebSearch.Enabled {
		return e.webSearchRemove(ctx, g)
	}

	role, err := e.roleItem(ctx, "1.8", "web search role "+policy.WebSearchRole, WebSearchRoleSpec(region, account))
	if err != nil {
		return nil, err
	}
	out := []*plan.Item{role}
	gw := e.item("1.8", "web search gateway "+policy.WebSearchGateway)
	out = append(out, gw)
	target := e.item("1.8", "web search target "+policy.WebSearchTarget)
	exclude := e.Cfg.WebSearch.ExcludeDomains

	if g == nil {
		gatewayState(gw, nil, account)
		gw.Apply = func(ctx context.Context) error { _, err := e.createGateway(ctx); return err }
		targetState(target, nil, exclude)
		target.Apply = func(ctx context.Context) error { return e.createTarget(ctx, exclude) }
		return append(out, target), nil
	}
	if !e.owned(gw, g.OwnerID) {
		return out, nil
	}
	if !gatewayState(gw, g, account) {
		return out, nil
	}
	if gw.Op == plan.Update {
		id := g.ID
		gw.Apply = func(ctx context.Context) error {
			_, err := e.C.AgentCore.UpdateGateway(ctx, &ac.UpdateGatewayInput{GatewayIdentifier: aws.String(id),
				Name: aws.String(policy.WebSearchGateway), RoleArn: aws.String(webSearchRoleArn(account)),
				ProtocolType: actypes.GatewayProtocolTypeMcp, AuthorizerType: actypes.AuthorizerTypeAwsIam})
			if err != nil {
				return fmt.Errorf("updating gateway %s: %w", id, err)
			}
			_, err = websearch.WaitGatewayReady(ctx, e.C.AgentCore, id)
			return err
		}
	}
	t, err := websearch.FindTarget(ctx, e.C.AgentCore, g.ID, policy.WebSearchTarget)
	if err != nil {
		return nil, err
	}
	targetState(target, t, exclude)
	switch target.Op {
	case plan.Create:
		target.Apply = func(ctx context.Context) error { return e.createTarget(ctx, exclude) }
	case plan.Update:
		gid, tid := g.ID, t.ID
		target.Apply = func(ctx context.Context) error {
			_, err := e.C.AgentCore.UpdateGatewayTarget(ctx, &ac.UpdateGatewayTargetInput{GatewayIdentifier: aws.String(gid),
				TargetId: aws.String(tid), Name: aws.String(policy.WebSearchTarget),
				TargetConfiguration: websearch.TargetConfig(exclude), CredentialProviderConfigurations: websearch.Creds()})
			if err != nil {
				return fmt.Errorf("updating target %s: %w", policy.WebSearchTarget, err)
			}
			return websearch.WaitTargetReady(ctx, e.C.AgentCore, gid, policy.WebSearchTarget)
		}
	}
	return append(out, target), nil
}

// gatewayState sets the gateway row from our gateway (nil: missing). It returns false when the
// target can't be planned, because the gateway failed.
func gatewayState(it *plan.Item, g *websearch.Gateway, account string) bool {
	switch {
	case g == nil:
		it.Op, it.Details = plan.Create, []string{"create (MCP, IAM auth)"}
		it.Notes = []string{"$7 per 1,000 searches"}
	case websearch.Failed(string(g.Status)):
		it.Op, it.Status = plan.Conflict, fmt.Sprintf("%s: %s; remove web_search, apply, then add it back", g.Status, strings.Join(g.Reasons, "; "))
		return false
	default:
		it.Status = strings.ToLower(string(g.Status)) + ", " + g.URL
		if g.RoleArn != webSearchRoleArn(account) || g.Authorizer != actypes.AuthorizerTypeAwsIam {
			it.Op, it.Details = plan.Update, []string{"role or authorizer changed outside the file"}
		}
	}
	return true
}

// targetState sets the target row from the gateway's target (nil: missing).
func targetState(it *plan.Item, t *websearch.Target, exclude []string) {
	switch {
	case t == nil:
		it.Op, it.Details = plan.Create, []string{"create (connector " + policy.WebSearchConnectorVersion + ")"}
	case websearch.Failed(string(t.Status)):
		it.Op, it.Status = plan.Conflict, fmt.Sprintf("%s: %s; remove web_search, apply, then add it back", t.Status, strings.Join(t.Reasons, "; "))
	default:
		it.Status = strings.ToLower(string(t.Status)) + ", connector " + t.Version
		if d := t.Drift(exclude); len(d) > 0 {
			it.Op, it.Details = plan.Update, d
		}
	}
}

// webSearchRemove plans deleting what a disabled web search left behind (ours only, silently).
func (e *Env) webSearchRemove(ctx context.Context, g *websearch.Gateway) ([]*plan.Item, error) {
	var out []*plan.Item
	if g != nil && g.OwnerID == e.Cfg.ID {
		it := e.item("1.8", "web search gateway "+policy.WebSearchGateway)
		it.Op, it.Details = plan.Delete, []string{"remove (web_search is off)"}
		id := g.ID
		it.Apply = func(ctx context.Context) error { return websearch.Delete(ctx, e.C.AgentCore, id) }
		out = append(out, it)
	}
	st, err := iamrole.Observe(ctx, e.C.IAM, iamrole.Spec{Name: policy.WebSearchRole, Path: policy.Path})
	if err != nil {
		return nil, err
	}
	if st.Exists && st.ID() == e.Cfg.ID {
		it := e.item("1.8", "web search role "+policy.WebSearchRole)
		it.Op, it.Details = plan.Delete, []string{"remove (web_search is off)"}
		it.Apply = func(ctx context.Context) error { return iamrole.Delete(ctx, e.C.IAM, policy.WebSearchRole) }
		out = append(out, it)
	}
	return out, nil
}

// createGateway creates the gateway, retrying while the new role propagates, and waits for READY.
func (e *Env) createGateway(ctx context.Context) (string, error) {
	var id string
	err := iamrole.Retry(ctx, time.Minute, roleNotReady, func() error {
		r, err := e.C.AgentCore.CreateGateway(ctx, &ac.CreateGatewayInput{Name: aws.String(policy.WebSearchGateway),
			Description: aws.String("Web search for Claude Code (bedrock-admin)"),
			RoleArn:     aws.String(webSearchRoleArn(e.C.Account)), ProtocolType: actypes.GatewayProtocolTypeMcp,
			AuthorizerType: actypes.AuthorizerTypeAwsIam, Tags: e.tags()})
		if err != nil {
			return err
		}
		id = aws.ToString(r.GatewayId)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("creating gateway %s: %w", policy.WebSearchGateway, err)
	}
	e.logf("    waiting for gateway %s", id)
	_, err = websearch.WaitGatewayReady(ctx, e.C.AgentCore, id)
	return id, err
}

// createTarget adds the Web Search target to the gateway (found by name: it may have just been created).
func (e *Env) createTarget(ctx context.Context, exclude []string) error {
	g, err := websearch.FindGateway(ctx, e.C.AgentCore, policy.WebSearchGateway)
	if err != nil {
		return err
	}
	if g == nil {
		return fmt.Errorf("gateway %s not found", policy.WebSearchGateway)
	}
	if g.Status != actypes.GatewayStatusReady {
		if g, err = websearch.WaitGatewayReady(ctx, e.C.AgentCore, g.ID); err != nil {
			return err
		}
	}
	_, err = e.C.AgentCore.CreateGatewayTarget(ctx, &ac.CreateGatewayTargetInput{GatewayIdentifier: aws.String(g.ID),
		Name: aws.String(policy.WebSearchTarget), Description: aws.String("AgentCore Web Search"),
		TargetConfiguration: websearch.TargetConfig(exclude), CredentialProviderConfigurations: websearch.Creds()})
	if err != nil {
		return fmt.Errorf("creating target %s: %w", policy.WebSearchTarget, err)
	}
	e.logf("    waiting for target %s", policy.WebSearchTarget)
	return websearch.WaitTargetReady(ctx, e.C.AgentCore, g.ID, policy.WebSearchTarget)
}

// roleNotReady matches AgentCore's errors while a new role can't be assumed yet.
func roleNotReady(err error) bool {
	if awsx.ErrorCode(err) != "ValidationException" && !awsx.IsAccessDenied(err) {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "role") || strings.Contains(m, "assume")
}

// isUnsupportedRegion reports an AgentCore call in a region without the service.
func isUnsupportedRegion(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "no such host") || strings.Contains(m, "unknownendpoint") || strings.Contains(m, "not supported")
}
