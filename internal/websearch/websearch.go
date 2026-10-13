// Package websearch finds, creates and removes the AgentCore gateway that gives Claude Code web
// search on Bedrock.
package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/document"
	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"

	"github.com/innobead/bedrock-toolkit/internal/awsx"
	"github.com/innobead/bedrock-toolkit/internal/policy"
)

// ToolName is the MCP tool the target exposes (<target>___<tool>).
const ToolName = policy.WebSearchTarget + "___WebSearch"

// ProxyPackage is the tested mcp-proxy-for-aws version users run (it gets their AWS credentials,
// so it's pinned).
const ProxyPackage = "mcp-proxy-for-aws@1.7.0"

// Poll and Wait control how long create and delete wait for AgentCore.
var (
	Poll = 3 * time.Second
	Wait = 2 * time.Minute
)

// Gateway is the web search gateway as it exists.
type Gateway struct {
	ID         string
	Arn        string
	URL        string
	RoleArn    string
	Authorizer actypes.AuthorizerType
	Status     actypes.GatewayStatus
	Reasons    []string
	// OwnerID is its bedrock-admin:id tag ("" if none).
	OwnerID string
}

// Target is the web search target as it exists.
type Target struct {
	ID      string
	Status  actypes.TargetStatus
	Reasons []string
	// Connector and Version are the connector's source; Exclude is the target-level domain list.
	Connector, Version string
	Exclude            []string
	GatewayRole        bool
}

// FindGateway looks the gateway up by name (its ID is generated). It returns nil when it doesn't exist.
func FindGateway(ctx context.Context, c *ac.Client, name string) (*Gateway, error) {
	p := ac.NewListGatewaysPaginator(c, &ac.ListGatewaysInput{})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing AgentCore gateways: %w", err)
		}
		for _, g := range r.Items {
			if aws.ToString(g.Name) == name && g.Status != actypes.GatewayStatusDeleting {
				return GetGateway(ctx, c, aws.ToString(g.GatewayId))
			}
		}
	}
	return nil, nil
}

// GetGateway reads a gateway and its bedrock-admin:id tag.
func GetGateway(ctx context.Context, c *ac.Client, id string) (*Gateway, error) {
	r, err := c.GetGateway(ctx, &ac.GetGatewayInput{GatewayIdentifier: aws.String(id)})
	if err != nil {
		if awsx.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading gateway %s: %w", id, err)
	}
	g := &Gateway{ID: aws.ToString(r.GatewayId), Arn: aws.ToString(r.GatewayArn), URL: aws.ToString(r.GatewayUrl),
		RoleArn: aws.ToString(r.RoleArn), Authorizer: r.AuthorizerType, Status: r.Status, Reasons: r.StatusReasons}
	t, err := c.ListTagsForResource(ctx, &ac.ListTagsForResourceInput{ResourceArn: aws.String(g.Arn)})
	if err != nil {
		// Users can find the gateway but can't read its tags; the owner only matters to bedrock-admin.
		if !awsx.IsAccessDenied(err) {
			return nil, fmt.Errorf("reading tags of gateway %s: %w", g.ID, err)
		}
	} else {
		g.OwnerID = t.Tags[policy.IDTag]
	}
	return g, nil
}

// FindTarget returns the gateway's target with this name, or nil.
func FindTarget(ctx context.Context, c *ac.Client, gatewayID, name string) (*Target, error) {
	p := ac.NewListGatewayTargetsPaginator(c, &ac.ListGatewayTargetsInput{GatewayIdentifier: aws.String(gatewayID)})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing targets of gateway %s: %w", gatewayID, err)
		}
		for _, t := range r.Items {
			if aws.ToString(t.Name) != name || t.Status == actypes.TargetStatusDeleting {
				continue
			}
			g, err := c.GetGatewayTarget(ctx, &ac.GetGatewayTargetInput{GatewayIdentifier: aws.String(gatewayID), TargetId: t.TargetId})
			if err != nil {
				return nil, fmt.Errorf("reading target %s: %w", name, err)
			}
			return ReadTarget(aws.ToString(g.TargetId), g.Status, g.StatusReasons, g.TargetConfiguration, g.CredentialProviderConfigurations), nil
		}
	}
	return nil, nil
}

// ReadTarget extracts what bedrock-admin manages from a target's configuration.
func ReadTarget(id string, status actypes.TargetStatus, reasons []string, cfg actypes.TargetConfiguration,
	creds []actypes.CredentialProviderConfiguration) *Target {
	t := &Target{ID: id, Status: status, Reasons: reasons}
	for _, cr := range creds {
		if cr.CredentialProviderType == actypes.CredentialProviderTypeGatewayIamRole {
			t.GatewayRole = true
		}
	}
	mcp, ok := cfg.(*actypes.TargetConfigurationMemberMcp)
	if !ok {
		return t
	}
	conn, ok := mcp.Value.(*actypes.McpTargetConfigurationMemberConnector)
	if !ok {
		return t
	}
	if s := conn.Value.Source; s != nil {
		t.Connector, t.Version = aws.ToString(s.ConnectorId), aws.ToString(s.Version)
	}
	for _, c := range conn.Value.Configurations {
		if aws.ToString(c.Name) != "WebSearch" || c.ParameterValues == nil {
			continue
		}
		raw, err := c.ParameterValues.MarshalSmithyDocument()
		if err != nil {
			continue
		}
		var v struct {
			DomainFilter struct {
				Exclude []string `json:"exclude"`
			} `json:"domainFilter"`
		}
		if json.Unmarshal(raw, &v) == nil {
			t.Exclude = v.DomainFilter.Exclude
		}
	}
	return t
}

// TargetConfig is the target configuration for the pinned connector and an exclude list.
func TargetConfig(exclude []string) actypes.TargetConfiguration {
	params := map[string]any{}
	if len(exclude) > 0 {
		params["domainFilter"] = map[string]any{"exclude": exclude}
	}
	return &actypes.TargetConfigurationMemberMcp{Value: &actypes.McpTargetConfigurationMemberConnector{
		Value: actypes.ConnectorTargetConfiguration{
			Source: &actypes.ConnectorSource{ConnectorId: aws.String(policy.WebSearchConnector),
				Version: aws.String(policy.WebSearchConnectorVersion)},
			Configurations: []actypes.ConnectorConfiguration{{Name: aws.String("WebSearch"),
				ParameterValues: document.NewLazyDocument(params)}},
		}}}
}

// Creds makes the gateway call Web Search with its own role.
func Creds() []actypes.CredentialProviderConfiguration {
	return []actypes.CredentialProviderConfiguration{{CredentialProviderType: actypes.CredentialProviderTypeGatewayIamRole}}
}

// Drift lists how the target differs from the wanted connector and exclude list.
func (t *Target) Drift(exclude []string) []string {
	var d []string
	if t.Connector != policy.WebSearchConnector {
		d = append(d, fmt.Sprintf("connector %q → %q", t.Connector, policy.WebSearchConnector))
	}
	if t.Version != policy.WebSearchConnectorVersion {
		d = append(d, fmt.Sprintf("connector version %s → %s", t.Version, policy.WebSearchConnectorVersion))
	}
	if !sameSet(t.Exclude, exclude) {
		d = append(d, fmt.Sprintf("exclude_domains [%s] → [%s]", strings.Join(t.Exclude, ", "), strings.Join(exclude, ", ")))
	}
	if !t.GatewayRole {
		d = append(d, "credentials → gateway role")
	}
	return d
}

func sameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	sort.Strings(x)
	sort.Strings(y)
	return slices.Equal(x, y)
}

// Failed reports whether a gateway or target status means it doesn't work.
func Failed(status string) bool {
	return strings.Contains(status, "FAILED") || strings.Contains(status, "UNSUCCESSFUL")
}

func wait(ctx context.Context, what string, f func() (done bool, err error)) error {
	deadline := time.Now().Add(Wait)
	for {
		done, err := f()
		if err != nil || done {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: still not done after %s", what, Wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(Poll):
		}
	}
}

// WaitGatewayReady polls until the gateway is READY.
func WaitGatewayReady(ctx context.Context, c *ac.Client, id string) (*Gateway, error) {
	var g *Gateway
	err := wait(ctx, "gateway "+id, func() (bool, error) {
		var err error
		if g, err = GetGateway(ctx, c, id); err != nil {
			return false, err
		}
		switch {
		case g == nil:
			return false, fmt.Errorf("gateway %s disappeared", id)
		case Failed(string(g.Status)):
			return false, fmt.Errorf("gateway %s is %s: %s", id, g.Status, strings.Join(g.Reasons, "; "))
		}
		return g.Status == actypes.GatewayStatusReady, nil
	})
	return g, err
}

// WaitTargetReady polls until the target is READY.
func WaitTargetReady(ctx context.Context, c *ac.Client, gatewayID, name string) error {
	return wait(ctx, "target "+name, func() (bool, error) {
		t, err := FindTarget(ctx, c, gatewayID, name)
		switch {
		case err != nil:
			return false, err
		case t == nil:
			return false, fmt.Errorf("target %s disappeared", name)
		case Failed(string(t.Status)):
			return false, fmt.Errorf("target %s is %s: %s", name, t.Status, strings.Join(t.Reasons, "; "))
		}
		return t.Status == actypes.TargetStatusReady, nil
	})
}

// Delete removes every target of the gateway, waits for them to go, then removes the gateway.
func Delete(ctx context.Context, c *ac.Client, id string) error {
	list := func() ([]actypes.TargetSummary, error) {
		var out []actypes.TargetSummary
		p := ac.NewListGatewayTargetsPaginator(c, &ac.ListGatewayTargetsInput{GatewayIdentifier: aws.String(id)})
		for p.HasMorePages() {
			r, err := p.NextPage(ctx)
			if err != nil {
				return nil, err
			}
			out = append(out, r.Items...)
		}
		return out, nil
	}
	ts, err := list()
	if err != nil {
		if awsx.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("listing targets of gateway %s: %w", id, err)
	}
	for _, t := range ts {
		if t.Status == actypes.TargetStatusDeleting {
			continue
		}
		if _, err := c.DeleteGatewayTarget(ctx, &ac.DeleteGatewayTargetInput{GatewayIdentifier: aws.String(id),
			TargetId: t.TargetId}); err != nil && !awsx.IsNotFound(err) {
			return fmt.Errorf("deleting target %s: %w", aws.ToString(t.Name), err)
		}
	}
	if err := wait(ctx, "deleting targets of gateway "+id, func() (bool, error) {
		ts, err := list()
		return len(ts) == 0, err
	}); err != nil {
		return err
	}
	if _, err := c.DeleteGateway(ctx, &ac.DeleteGatewayInput{GatewayIdentifier: aws.String(id)}); err != nil && !awsx.IsNotFound(err) {
		return fmt.Errorf("deleting gateway %s: %w", id, err)
	}
	return wait(ctx, "deleting gateway "+id, func() (bool, error) {
		g, err := GetGateway(ctx, c, id)
		return g == nil, err
	})
}
