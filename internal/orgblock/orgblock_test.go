package orgblock

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	otypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/ssoadmin"
	stypes "github.com/aws/aws-sdk-go-v2/service/ssoadmin/types"

	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

const acct = "111122223333"

type fakeOrgs struct {
	Orgs
	master   string
	policies map[string]string // id -> content
	names    map[string]string // id -> name
	targets  map[string][]string
	created  int
	updated  int
}

func (f *fakeOrgs) DescribeOrganization(context.Context, *organizations.DescribeOrganizationInput, ...func(*organizations.Options)) (*organizations.DescribeOrganizationOutput, error) {
	return &organizations.DescribeOrganizationOutput{Organization: &otypes.Organization{MasterAccountId: aws.String(f.master)}}, nil
}

func (f *fakeOrgs) ListPolicies(context.Context, *organizations.ListPoliciesInput, ...func(*organizations.Options)) (*organizations.ListPoliciesOutput, error) {
	var out []otypes.PolicySummary
	for id, n := range f.names {
		out = append(out, otypes.PolicySummary{Id: aws.String(id), Name: aws.String(n)})
	}
	return &organizations.ListPoliciesOutput{Policies: out}, nil
}

func (f *fakeOrgs) DescribePolicy(_ context.Context, in *organizations.DescribePolicyInput, _ ...func(*organizations.Options)) (*organizations.DescribePolicyOutput, error) {
	return &organizations.DescribePolicyOutput{Policy: &otypes.Policy{Content: aws.String(f.policies[*in.PolicyId])}}, nil
}

func (f *fakeOrgs) CreatePolicy(_ context.Context, in *organizations.CreatePolicyInput, _ ...func(*organizations.Options)) (*organizations.CreatePolicyOutput, error) {
	f.created++
	f.policies["p-new"], f.names["p-new"] = *in.Content, *in.Name
	return &organizations.CreatePolicyOutput{Policy: &otypes.Policy{PolicySummary: &otypes.PolicySummary{Id: aws.String("p-new")}}}, nil
}

func (f *fakeOrgs) UpdatePolicy(_ context.Context, in *organizations.UpdatePolicyInput, _ ...func(*organizations.Options)) (*organizations.UpdatePolicyOutput, error) {
	f.updated++
	f.policies[*in.PolicyId] = *in.Content
	return &organizations.UpdatePolicyOutput{}, nil
}

func (f *fakeOrgs) ListTargetsForPolicy(_ context.Context, in *organizations.ListTargetsForPolicyInput, _ ...func(*organizations.Options)) (*organizations.ListTargetsForPolicyOutput, error) {
	var out []otypes.PolicyTargetSummary
	for _, t := range f.targets[*in.PolicyId] {
		out = append(out, otypes.PolicyTargetSummary{TargetId: aws.String(t)})
	}
	return &organizations.ListTargetsForPolicyOutput{Targets: out}, nil
}

func (f *fakeOrgs) AttachPolicy(_ context.Context, in *organizations.AttachPolicyInput, _ ...func(*organizations.Options)) (*organizations.AttachPolicyOutput, error) {
	f.targets[*in.PolicyId] = append(f.targets[*in.PolicyId], *in.TargetId)
	return &organizations.AttachPolicyOutput{}, nil
}

func newOrgs() *fakeOrgs {
	return &fakeOrgs{master: "999999999999", policies: map[string]string{}, names: map[string]string{}, targets: map[string][]string{}}
}

func applyAll(t *testing.T, items []*plan.Item) {
	t.Helper()
	for _, it := range items {
		if it.Apply != nil {
			if err := it.Apply(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func pending(items []*plan.Item) int {
	n := 0
	for _, it := range items {
		if it.Op.Pending() {
			n++
		}
	}
	return n
}

func TestSCP(t *testing.T) {
	ctx := context.Background()
	o := newOrgs()
	items, err := SCPItems(ctx, o, acct, "BedrockAdmin")
	if err != nil || pending(items) != 2 {
		t.Fatalf("new: %v %d", err, pending(items))
	}
	applyAll(t, items)
	if o.created != 1 || len(o.targets["p-new"]) != 1 || !policy.HasBlock(o.policies["p-new"]) || strings.Contains(o.policies["p-new"], "\n") {
		t.Fatalf("created %d targets %v content %s", o.created, o.targets, o.policies["p-new"])
	}

	items, _ = SCPItems(ctx, o, acct, "BedrockAdmin")
	if pending(items) != 0 {
		t.Fatalf("second run: %d pending", pending(items))
	}

	// Another admin permission set changes the content but keeps the attachment.
	items, _ = SCPItems(ctx, o, acct, "OrgAdmin")
	if pending(items) != 1 {
		t.Fatalf("admin changed: %d pending", pending(items))
	}
	applyAll(t, items)
	if o.updated != 1 || policy.ExemptPermissionSet(o.policies["p-new"]) != "OrgAdmin" {
		t.Fatalf("updated %d: %s", o.updated, o.policies["p-new"])
	}

	o.master = acct
	if _, err := SCPItems(ctx, o, acct, "BedrockAdmin"); err == nil || !strings.Contains(err.Error(), "management account") {
		t.Fatalf("management account: %v", err)
	}
}

type fakeSSO struct {
	SSO
	sets        map[string]string // arn -> name
	inline      map[string]string // arn -> policy
	provisioned []string
}

func (f *fakeSSO) ListPermissionSetsProvisionedToAccount(context.Context, *ssoadmin.ListPermissionSetsProvisionedToAccountInput, ...func(*ssoadmin.Options)) (*ssoadmin.ListPermissionSetsProvisionedToAccountOutput, error) {
	var arns []string
	for a := range f.sets {
		arns = append(arns, a)
	}
	return &ssoadmin.ListPermissionSetsProvisionedToAccountOutput{PermissionSets: arns}, nil
}

func (f *fakeSSO) DescribePermissionSet(_ context.Context, in *ssoadmin.DescribePermissionSetInput, _ ...func(*ssoadmin.Options)) (*ssoadmin.DescribePermissionSetOutput, error) {
	return &ssoadmin.DescribePermissionSetOutput{PermissionSet: &stypes.PermissionSet{Name: aws.String(f.sets[*in.PermissionSetArn])}}, nil
}

func (f *fakeSSO) GetInlinePolicyForPermissionSet(_ context.Context, in *ssoadmin.GetInlinePolicyForPermissionSetInput, _ ...func(*ssoadmin.Options)) (*ssoadmin.GetInlinePolicyForPermissionSetOutput, error) {
	return &ssoadmin.GetInlinePolicyForPermissionSetOutput{InlinePolicy: aws.String(f.inline[*in.PermissionSetArn])}, nil
}

func (f *fakeSSO) PutInlinePolicyToPermissionSet(_ context.Context, in *ssoadmin.PutInlinePolicyToPermissionSetInput, _ ...func(*ssoadmin.Options)) (*ssoadmin.PutInlinePolicyToPermissionSetOutput, error) {
	f.inline[*in.PermissionSetArn] = *in.InlinePolicy
	return &ssoadmin.PutInlinePolicyToPermissionSetOutput{}, nil
}

func (f *fakeSSO) ProvisionPermissionSet(_ context.Context, in *ssoadmin.ProvisionPermissionSetInput, _ ...func(*ssoadmin.Options)) (*ssoadmin.ProvisionPermissionSetOutput, error) {
	f.provisioned = append(f.provisioned, f.sets[*in.PermissionSetArn]+"@"+*in.TargetId)
	return &ssoadmin.ProvisionPermissionSetOutput{PermissionSetProvisioningStatus: &stypes.PermissionSetProvisioningStatus{
		Status: stypes.StatusValuesInProgress, RequestId: aws.String("r1")}}, nil
}

func (f *fakeSSO) DescribePermissionSetProvisioningStatus(context.Context, *ssoadmin.DescribePermissionSetProvisioningStatusInput, ...func(*ssoadmin.Options)) (*ssoadmin.DescribePermissionSetProvisioningStatusOutput, error) {
	return &ssoadmin.DescribePermissionSetProvisioningStatusOutput{PermissionSetProvisioningStatus: &stypes.PermissionSetProvisioningStatus{
		Status: stypes.StatusValuesSucceeded}}, nil
}

func TestPermissionSets(t *testing.T) {
	PollInterval = 0
	ctx := context.Background()
	s := &fakeSSO{sets: map[string]string{"a": "BedrockAdmin", "d": "Developer", "r": "ReadOnly"}, inline: map[string]string{}}
	items, err := PermissionSetItems(ctx, s, "i", acct, "BedrockAdmin", []string{"Developer"})
	if err != nil || len(items) != 2 || pending(items) != 2 {
		t.Fatalf("%v %d %d", err, len(items), pending(items))
	}
	if len(items[1].Warnings) == 0 || !strings.Contains(items[1].Warnings[0], "ReadOnly") {
		t.Errorf("no note about AssumeRole on ReadOnly: %+v", items[1])
	}
	applyAll(t, items)
	if s.inline["a"] != "" || !policy.HasBlockFor(s.inline["d"], acct) || !policy.HasBlockFor(s.inline["r"], acct) {
		t.Fatalf("inline: %v", s.inline)
	}
	if !policy.AllowsAssumePersonalRole(s.inline["d"]) || policy.AllowsAssumePersonalRole(s.inline["r"]) {
		t.Fatalf("assume only on Developer: %v", s.inline)
	}
	if len(s.provisioned) != 2 || s.provisioned[0] != "Developer@"+acct {
		t.Fatalf("provisioned %v", s.provisioned)
	}

	items, _ = PermissionSetItems(ctx, s, "i", acct, "BedrockAdmin", []string{"Developer"})
	if pending(items) != 0 {
		t.Fatalf("second run: %d pending", pending(items))
	}

	// A second account joins the same statement.
	items, _ = PermissionSetItems(ctx, s, "i", "444455556666", "BedrockAdmin", nil)
	applyAll(t, items)
	if !policy.HasBlockFor(s.inline["d"], acct) || !policy.HasBlockFor(s.inline["d"], "444455556666") {
		t.Fatalf("two accounts: %s", s.inline["d"])
	}

	for _, tc := range []struct{ admin, user, want string }{
		{"Missing", "", "not assigned"},
		{"BedrockAdmin", "Nope", "--user-permission-set Nope"},
		{"BedrockAdmin", "BedrockAdmin", "admin permission set"},
	} {
		var us []string
		if tc.user != "" {
			us = []string{tc.user}
		}
		if _, err := PermissionSetItems(ctx, s, "i", acct, tc.admin, us); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}
