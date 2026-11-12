// Package orgblock applies the deny of bedrock-admin block: an SCP attached to the account, or a
// statement in the inline policy of the permission sets assigned to it. Both need AWS Organizations
// rights, so it runs as the org admin, in the management account or a delegated admin account.
package orgblock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	otypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	stypes "github.com/aws/aws-sdk-go-v2/service/ssoadmin/types"

	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// Orgs is the part of the AWS Organizations API the SCP method uses.
type Orgs interface {
	DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error)
	ListPolicies(context.Context, *organizations.ListPoliciesInput, ...func(*organizations.Options)) (*organizations.ListPoliciesOutput, error)
	DescribePolicy(context.Context, *organizations.DescribePolicyInput, ...func(*organizations.Options)) (*organizations.DescribePolicyOutput, error)
	CreatePolicy(context.Context, *organizations.CreatePolicyInput, ...func(*organizations.Options)) (*organizations.CreatePolicyOutput, error)
	UpdatePolicy(context.Context, *organizations.UpdatePolicyInput, ...func(*organizations.Options)) (*organizations.UpdatePolicyOutput, error)
	ListTargetsForPolicy(context.Context, *organizations.ListTargetsForPolicyInput, ...func(*organizations.Options)) (*organizations.ListTargetsForPolicyOutput, error)
	AttachPolicy(context.Context, *organizations.AttachPolicyInput, ...func(*organizations.Options)) (*organizations.AttachPolicyOutput, error)
}

// SSO is the part of the IAM Identity Center admin API the permission-set method uses.
type SSO interface {
	ListInstances(context.Context, *ssoadmin.ListInstancesInput, ...func(*ssoadmin.Options)) (*ssoadmin.ListInstancesOutput, error)
	ListPermissionSetsProvisionedToAccount(context.Context, *ssoadmin.ListPermissionSetsProvisionedToAccountInput, ...func(*ssoadmin.Options)) (*ssoadmin.ListPermissionSetsProvisionedToAccountOutput, error)
	DescribePermissionSet(context.Context, *ssoadmin.DescribePermissionSetInput, ...func(*ssoadmin.Options)) (*ssoadmin.DescribePermissionSetOutput, error)
	GetInlinePolicyForPermissionSet(context.Context, *ssoadmin.GetInlinePolicyForPermissionSetInput, ...func(*ssoadmin.Options)) (*ssoadmin.GetInlinePolicyForPermissionSetOutput, error)
	PutInlinePolicyToPermissionSet(context.Context, *ssoadmin.PutInlinePolicyToPermissionSetInput, ...func(*ssoadmin.Options)) (*ssoadmin.PutInlinePolicyToPermissionSetOutput, error)
	ProvisionPermissionSet(context.Context, *ssoadmin.ProvisionPermissionSetInput, ...func(*ssoadmin.Options)) (*ssoadmin.ProvisionPermissionSetOutput, error)
	DescribePermissionSetProvisioningStatus(context.Context, *ssoadmin.DescribePermissionSetProvisioningStatusInput, ...func(*ssoadmin.Options)) (*ssoadmin.DescribePermissionSetProvisioningStatusOutput, error)
}

// PollInterval is how often provisioning is checked; tests shorten it.
var PollInterval = 3 * time.Second

// SCPName is the SCP for one account.
func SCPName(account string) string { return policy.BlockPolicyName + "-" + account }

// SCPItems plans the SCP that carries the deny and its attachment to account.
func SCPItems(ctx context.Context, o Orgs, account, adminPS string) ([]*plan.Item, error) {
	org, err := o.DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
	if err != nil {
		return nil, fmt.Errorf("reading the organization: %w", err)
	}
	if aws.ToString(org.Organization.MasterAccountId) == account {
		return nil, fmt.Errorf("%s is the management account, where SCPs have no effect; use --method permission-set", account)
	}
	name := SCPName(account)
	want := compact(policy.Block(adminPS))
	id, err := findSCP(ctx, o, name)
	if err != nil {
		return nil, err
	}
	scp := &plan.Item{Section: "account", Target: "SCP " + name}
	attach := &plan.Item{Section: "account", Target: "SCP attached to " + account}
	attachTo := func(ctx context.Context, id string) error {
		if _, err := o.AttachPolicy(ctx, &organizations.AttachPolicyInput{PolicyId: aws.String(id), TargetId: aws.String(account)}); err != nil {
			return fmt.Errorf("attaching %s to %s: %w", name, account, err)
		}
		return nil
	}
	if id == "" {
		scp.Op, scp.Details = plan.Create, []string{"create"}
		attach.Op, attach.Details = plan.Create, []string{"attach"}
		scp.Apply = func(ctx context.Context) error {
			r, err := o.CreatePolicy(ctx, &organizations.CreatePolicyInput{Name: aws.String(name), Type: otypes.PolicyTypeServiceControlPolicy,
				Content: aws.String(want), Description: aws.String("Bedrock model calls in " + account + " only via personal roles (bedrock-admin block)")})
			if err != nil {
				return scpError(fmt.Errorf("creating %s: %w", name, err))
			}
			id = aws.ToString(r.Policy.PolicySummary.Id)
			return nil
		}
		attach.Apply = func(ctx context.Context) error { return attachTo(ctx, id) }
		return []*plan.Item{scp, attach}, nil
	}
	d, err := o.DescribePolicy(ctx, &organizations.DescribePolicyInput{PolicyId: aws.String(id)})
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}
	if policy.Equal(aws.ToString(d.Policy.Content), want) {
		scp.Op, scp.Status = plan.OK, "ok"
	} else {
		scp.Op, scp.Details = plan.Update, []string{"policy differs"}
		scp.Apply = func(ctx context.Context) error {
			if _, err := o.UpdatePolicy(ctx, &organizations.UpdatePolicyInput{PolicyId: aws.String(id), Content: aws.String(want)}); err != nil {
				return fmt.Errorf("updating %s: %w", name, err)
			}
			return nil
		}
	}
	attached, err := isAttached(ctx, o, id, account)
	if err != nil {
		return nil, err
	}
	if attached {
		attach.Op, attach.Status = plan.OK, "ok"
	} else {
		attach.Op, attach.Details = plan.Create, []string{"attach"}
		attach.Apply = func(ctx context.Context) error { return attachTo(ctx, id) }
	}
	return []*plan.Item{scp, attach}, nil
}

func findSCP(ctx context.Context, o Orgs, name string) (string, error) {
	in := &organizations.ListPoliciesInput{Filter: otypes.PolicyTypeServiceControlPolicy}
	for {
		r, err := o.ListPolicies(ctx, in)
		if err != nil {
			return "", scpError(fmt.Errorf("listing SCPs: %w", err))
		}
		for _, p := range r.Policies {
			if aws.ToString(p.Name) == name {
				return aws.ToString(p.Id), nil
			}
		}
		if r.NextToken == nil {
			return "", nil
		}
		in.NextToken = r.NextToken
	}
}

func isAttached(ctx context.Context, o Orgs, id, account string) (bool, error) {
	in := &organizations.ListTargetsForPolicyInput{PolicyId: aws.String(id)}
	for {
		r, err := o.ListTargetsForPolicy(ctx, in)
		if err != nil {
			return false, fmt.Errorf("reading the targets of %s: %w", id, err)
		}
		for _, t := range r.Targets {
			if aws.ToString(t.TargetId) == account {
				return true, nil
			}
		}
		if r.NextToken == nil {
			return false, nil
		}
		in.NextToken = r.NextToken
	}
}

func scpError(err error) error {
	var off *otypes.PolicyTypeNotEnabledException
	if errors.As(err, &off) {
		return fmt.Errorf("%w (SCPs are not enabled in the organization: enable them in AWS Organizations > Policies, or use --method permission-set)", err)
	}
	return err
}

// Instance is the IAM Identity Center instance ARN: instanceArn if given, else the only one.
func Instance(ctx context.Context, s SSO, instanceArn string) (string, error) {
	if instanceArn != "" {
		return instanceArn, nil
	}
	r, err := s.ListInstances(ctx, &ssoadmin.ListInstancesInput{})
	if err != nil {
		return "", fmt.Errorf("reading the IAM Identity Center instance (check --sso-region): %w", err)
	}
	switch len(r.Instances) {
	case 0:
		return "", errors.New("no IAM Identity Center instance in this region (pass --sso-region)")
	case 1:
		return aws.ToString(r.Instances[0].InstanceArn), nil
	}
	return "", errors.New("more than one IAM Identity Center instance; pass --instance-arn")
}

// PermissionSetItems plans the deny for account in the inline policy of every permission set
// provisioned to it except adminPS, and the sts:AssumeRole allow in userSets.
func PermissionSetItems(ctx context.Context, s SSO, instance, account, adminPS string, userSets []string) ([]*plan.Item, error) {
	sets, err := provisioned(ctx, s, instance, account)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, ps := range sets {
		names[ps.name] = true
	}
	if !names[adminPS] {
		return nil, fmt.Errorf("permission set %s is not assigned to %s; pass the one admins use with --admin-permission-set", adminPS, account)
	}
	for _, u := range userSets {
		if u == adminPS {
			return nil, fmt.Errorf("--user-permission-set %s is the admin permission set", u)
		}
		if !names[u] {
			return nil, fmt.Errorf("--user-permission-set %s is not assigned to %s", u, account)
		}
	}
	var items []*plan.Item
	for _, ps := range sets {
		if ps.name == adminPS {
			continue
		}
		it := &plan.Item{Section: "account", Target: "permission set " + ps.name}
		r, err := s.GetInlinePolicyForPermissionSet(ctx, &ssoadmin.GetInlinePolicyForPermissionSetInput{InstanceArn: aws.String(instance), PermissionSetArn: aws.String(ps.arn)})
		if err != nil {
			return nil, fmt.Errorf("reading the inline policy of %s: %w", ps.name, err)
		}
		cur := aws.ToString(r.InlinePolicy)
		doc, blockChanged, err := policy.MergeBlock(cur, adminPS, account)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ps.name, err)
		}
		if blockChanged {
			it.Details = append(it.Details, "add the deny for "+account)
		}
		if slices.Contains(userSets, ps.name) {
			var assumeChanged bool
			if doc, assumeChanged, err = policy.MergeAssume(doc); err != nil {
				return nil, fmt.Errorf("%s: %w", ps.name, err)
			}
			if assumeChanged {
				it.Details = append(it.Details, "allow sts:AssumeRole on the personal roles")
			}
		} else if !policy.AllowsAssumePersonalRole(doc) {
			it.Warnings = append(it.Warnings, "no sts:AssumeRole on role/bedrock-users/*: its users can't use their personal role (add it with --user-permission-set "+ps.name+")")
		}
		if len(it.Details) == 0 {
			it.Op, it.Status = plan.OK, "ok"
			items = append(items, it)
			continue
		}
		it.Op = plan.Update
		if cur == "" {
			it.Op = plan.Create
		}
		it.Apply = func(ctx context.Context) error {
			if _, err := s.PutInlinePolicyToPermissionSet(ctx, &ssoadmin.PutInlinePolicyToPermissionSetInput{InstanceArn: aws.String(instance),
				PermissionSetArn: aws.String(ps.arn), InlinePolicy: aws.String(doc)}); err != nil {
				return fmt.Errorf("updating the inline policy of %s: %w", ps.name, err)
			}
			return provision(ctx, s, instance, ps, account)
		}
		items = append(items, it)
	}
	return items, nil
}

type permissionSet struct{ arn, name string }

func provisioned(ctx context.Context, s SSO, instance, account string) ([]permissionSet, error) {
	var out []permissionSet
	in := &ssoadmin.ListPermissionSetsProvisionedToAccountInput{InstanceArn: aws.String(instance), AccountId: aws.String(account)}
	for {
		r, err := s.ListPermissionSetsProvisionedToAccount(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("listing the permission sets of %s: %w", account, err)
		}
		for _, arn := range r.PermissionSets {
			d, err := s.DescribePermissionSet(ctx, &ssoadmin.DescribePermissionSetInput{InstanceArn: aws.String(instance), PermissionSetArn: aws.String(arn)})
			if err != nil {
				return nil, fmt.Errorf("reading permission set %s: %w", arn, err)
			}
			out = append(out, permissionSet{arn: arn, name: aws.ToString(d.PermissionSet.Name)})
		}
		if r.NextToken == nil {
			break
		}
		in.NextToken = r.NextToken
	}
	slices.SortFunc(out, func(a, b permissionSet) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

// provision pushes the changed permission set to account and waits until it's done.
func provision(ctx context.Context, s SSO, instance string, ps permissionSet, account string) error {
	r, err := s.ProvisionPermissionSet(ctx, &ssoadmin.ProvisionPermissionSetInput{InstanceArn: aws.String(instance), PermissionSetArn: aws.String(ps.arn),
		TargetType: stypes.ProvisionTargetTypeAwsAccount, TargetId: aws.String(account)})
	if err != nil {
		return fmt.Errorf("provisioning %s to %s: %w", ps.name, account, err)
	}
	st := r.PermissionSetProvisioningStatus
	for st.Status == stypes.StatusValuesInProgress {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(PollInterval):
		}
		d, err := s.DescribePermissionSetProvisioningStatus(ctx, &ssoadmin.DescribePermissionSetProvisioningStatusInput{InstanceArn: aws.String(instance),
			ProvisionPermissionSetRequestId: st.RequestId})
		if err != nil {
			return fmt.Errorf("checking the provisioning of %s: %w", ps.name, err)
		}
		st = d.PermissionSetProvisioningStatus
	}
	if st.Status == stypes.StatusValuesFailed {
		return fmt.Errorf("provisioning %s to %s failed: %s", ps.name, account, aws.ToString(st.FailureReason))
	}
	return nil
}

// compact drops the whitespace: SCPs are limited to 5,120 characters, whitespace included.
func compact(s string) string {
	var b bytes.Buffer
	if json.Compact(&b, []byte(s)) != nil {
		return s
	}
	return b.String()
}
