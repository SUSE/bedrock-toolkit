package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	itypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// IAMUserNames are the iam_user values of the file, in order.
func (e *Env) IAMUserNames() []string {
	var out []string
	for _, u := range e.Cfg.Users {
		if u.IAMUser != "" {
			out = append(out, u.IAMUser)
		}
	}
	return out
}

// iamUsersPolicy is Step 1.9: the managed policy the users' IAM users get. It exists only while the
// file names IAM users.
func (e *Env) iamUsersPolicy(ctx context.Context) ([]*plan.Item, error) {
	name, arn := policy.IAMUsersPolicyName(e.Cfg.ID), policy.IAMUsersPolicyArn(e.C.Account, e.Cfg.ID)
	it := e.item("1.9", "IAM user policy "+name)
	id, exists, err := PolicyID(ctx, e.C.IAM, arn)
	if err != nil {
		return nil, err
	}
	names := e.IAMUserNames()
	if len(names) == 0 {
		if !exists {
			return nil, nil
		}
		if e.owned(it, id) {
			it.Op, it.Details, it.Status, it.NeedsYes = plan.Delete, []string{"remove (no iam_user in the file)"}, "", true
			it.Apply = func(ctx context.Context) error { return deletePolicy(ctx, e.C.IAM, arn) }
		}
		return one(it), nil
	}
	want := policy.IAMUsers(e.C.Account, e.Cfg.ID, e.Cfg.BlockDirectCalls.Enabled)
	switch {
	case !exists:
		it.Op, it.Details, it.Status = plan.Create, []string{"create"}, ""
		it.Apply = func(ctx context.Context) error {
			_, err := e.C.IAM.CreatePolicy(ctx, &iam.CreatePolicyInput{PolicyName: aws.String(name),
				Path: aws.String(policy.Path), PolicyDocument: aws.String(want),
				Description: aws.String("Lets the " + e.Cfg.ID + " IAM users use their personal Bedrock role"),
				Tags:        []itypes.Tag{{Key: aws.String(policy.IDTag), Value: aws.String(e.Cfg.ID)}}})
			if err != nil {
				return fmt.Errorf("creating %s: %w", name, err)
			}
			return nil
		}
	case !e.owned(it, id):
		return one(it), nil
	default:
		p, err := e.C.IAM.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
		if err != nil {
			return nil, err
		}
		v, err := e.C.IAM.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{PolicyArn: aws.String(arn), VersionId: p.Policy.DefaultVersionId})
		if err != nil {
			return nil, err
		}
		if !policy.Equal(policy.Decode(aws.ToString(v.PolicyVersion.Document)), want) {
			it.Op, it.Details, it.Status = plan.Update, []string{"policy differs (block_direct_calls changed, or a bedrock-admin upgrade)"}, ""
			it.Apply = func(ctx context.Context) error { return newPolicyVersion(ctx, e.C.IAM, arn, want) }
		} else {
			it.Status = fmt.Sprintf("ok (%d IAM users", len(names))
			if e.Cfg.BlockDirectCalls.Enabled {
				it.Status += "; denies direct model calls with their keys"
			}
			it.Status += ")"
		}
	}
	var missing []string
	for _, n := range names {
		if _, err := e.C.IAM.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(n)}); err != nil {
			if !isNoSuchEntity(err) {
				return nil, fmt.Errorf("reading IAM user %s: %w", n, err)
			}
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		it.Warnings = append(it.Warnings, "IAM users in the file that don't exist (create them, then apply): "+strings.Join(missing, ", "))
	}
	return one(it), nil
}

// AttachedIAMUsers lists the IAM users the id's IAM user policy is attached to.
func AttachedIAMUsers(ctx context.Context, c *iam.Client, account, id string) ([]string, error) {
	var out []string
	p := iam.NewListEntitiesForPolicyPaginator(c, &iam.ListEntitiesForPolicyInput{
		PolicyArn: aws.String(policy.IAMUsersPolicyArn(account, id)), EntityFilter: itypes.EntityTypeUser})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			if isNoSuchEntity(err) {
				return nil, nil
			}
			return nil, err
		}
		for _, u := range r.PolicyUsers {
			out = append(out, aws.ToString(u.UserName))
		}
	}
	return out, nil
}
