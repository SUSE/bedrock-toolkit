package users

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	itypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/SUSE/bedrock-toolkit/internal/account"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/iamrole"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// IAMUserState is the IAM user a user signs in with, when the file names one.
type IAMUserState struct {
	Exists   bool
	Name     string // the name in AWS; IAM matches names case-insensitively, aws:username doesn't
	ID       string // bedrock-admin:id tag
	Owner    string // owner tag: the email, so the user's budget covers the IAM user's spend
	Product  string // product tag
	Attached bool   // the id's IAM user policy is attached
}

// diffIAMUser compares the file's iam_user with the IAM user. fix means tag and attach it.
func diffIAMUser(want config.Resolved, o IAMUserState, id string) (details, warnings []string, fix bool) {
	if want.IAMUser == "" {
		return nil, nil, false
	}
	switch {
	case !o.Exists:
		return nil, []string{"IAM user " + want.IAMUser + " doesn't exist; create it, then apply again"}, false
	case o.Name != want.IAMUser:
		return nil, []string{"IAM user is named " + o.Name + " in AWS, not " + want.IAMUser + "; fix iam_user (the trust policy is case-sensitive)"}, false
	case o.ID != "" && o.ID != id:
		return nil, []string{"IAM user " + o.Name + " is managed by " + o.ID + "; left alone"}, false
	}
	if o.ID != id || o.Owner != want.Email || o.Product != want.Product {
		details = append(details, "tag IAM user "+o.Name)
	}
	if !o.Attached {
		details = append(details, "attach "+policy.IAMUsersPolicyName(id)+" to IAM user "+o.Name)
	}
	return details, nil, len(details) > 0
}

func (e *Env) observeIAMUser(ctx context.Context, name string) (IAMUserState, error) {
	var o IAMUserState
	r, err := e.C.IAM.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(name)})
	if err != nil {
		if isNoSuchEntity(err) {
			return o, nil
		}
		return o, fmt.Errorf("reading IAM user %s: %w", name, err)
	}
	o.Exists, o.Name = true, aws.ToString(r.User.UserName)
	for _, t := range r.User.Tags {
		switch aws.ToString(t.Key) {
		case policy.IDTag:
			o.ID = aws.ToString(t.Value)
		case policy.OwnerTag:
			o.Owner = aws.ToString(t.Value)
		case policy.ProductTag:
			o.Product = aws.ToString(t.Value)
		}
	}
	arn := policy.IAMUsersPolicyArn(e.C.Account, e.Cfg.ID)
	p := iam.NewListAttachedUserPoliciesPaginator(e.C.IAM, &iam.ListAttachedUserPoliciesInput{UserName: aws.String(o.Name)})
	for p.HasMorePages() && !o.Attached {
		pg, err := p.NextPage(ctx)
		if err != nil {
			return o, fmt.Errorf("reading the policies of IAM user %s: %w", o.Name, err)
		}
		o.Attached = slices.ContainsFunc(pg.AttachedPolicies, func(a itypes.AttachedPolicy) bool { return aws.ToString(a.PolicyArn) == arn })
	}
	return o, nil
}

// fixIAMUser tags the IAM user like the personal role (owner, product, id) and attaches the IAM user policy, which apply may have just created.
func (e *Env) fixIAMUser(ctx context.Context, u config.Resolved) error {
	if _, err := e.C.IAM.TagUser(ctx, &iam.TagUserInput{UserName: aws.String(u.IAMUser), Tags: []itypes.Tag{
		{Key: aws.String(policy.IDTag), Value: aws.String(e.Cfg.ID)},
		{Key: aws.String(policy.OwnerTag), Value: aws.String(u.Email)},
		{Key: aws.String(policy.ProductTag), Value: aws.String(u.Product)}}}); err != nil {
		return fmt.Errorf("tagging IAM user %s: %w", u.IAMUser, err)
	}
	arn := policy.IAMUsersPolicyArn(e.C.Account, e.Cfg.ID)
	err := iamrole.Retry(ctx, 30*time.Second, isNoSuchEntity, func() error {
		_, err := e.C.IAM.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: aws.String(u.IAMUser), PolicyArn: aws.String(arn)})
		return err
	})
	if err != nil {
		return fmt.Errorf("attaching %s to IAM user %s: %w", policy.IAMUsersPolicyName(e.Cfg.ID), u.IAMUser, err)
	}
	return nil
}

// planIAMUserRemovals returns an item per IAM user the policy is attached to that the file no longer names.
func (e *Env) planIAMUserRemovals(ctx context.Context, noDelete bool) ([]*plan.Item, error) {
	attached, err := account.AttachedIAMUsers(ctx, e.C.IAM, e.C.Account, e.Cfg.ID)
	if err != nil {
		return nil, fmt.Errorf("listing the IAM users of %s: %w", policy.IAMUsersPolicyName(e.Cfg.ID), err)
	}
	var out []*plan.Item
	for _, name := range attached {
		if slices.ContainsFunc(e.Cfg.Users, func(u config.User) bool { return strings.EqualFold(u.IAMUser, name) }) {
			continue
		}
		it := &plan.Item{Section: "users", Target: "IAM user " + name, Op: plan.Delete,
			Details: []string{"detach " + policy.IAMUsersPolicyName(e.Cfg.ID) + " and untag (not in the file)"}}
		if noDelete {
			it.Op, it.Details, it.Status = plan.Info, nil, "detach skipped (--no-delete)"
		} else {
			it.NeedsYes = true
			it.Apply = func(ctx context.Context) error { return e.releaseIAMUser(ctx, name) }
		}
		out = append(out, it)
	}
	return out, nil
}

// releaseIAMUser detaches the IAM user policy and any pause, and removes the tags apply added. The IAM user stays.
func (e *Env) releaseIAMUser(ctx context.Context, name string) error {
	arn := policy.IAMUsersPolicyArn(e.C.Account, e.Cfg.ID)
	// NoSuchEntity: Step 1.9 may already have detached and deleted the policy in this apply; still untag.
	if _, err := e.C.IAM.DetachUserPolicy(ctx, &iam.DetachUserPolicyInput{UserName: aws.String(name), PolicyArn: aws.String(arn)}); err != nil && !isNoSuchEntity(err) {
		return fmt.Errorf("detaching %s from IAM user %s: %w", policy.IAMUsersPolicyName(e.Cfg.ID), name, err)
	}
	o, err := e.observeIAMUser(ctx, name)
	if err != nil || !o.Exists || o.ID != e.Cfg.ID {
		return err
	}
	if err := e.Pause.DetachFromUsers(ctx, []string{name}); err != nil {
		return err
	}
	if _, err := e.C.IAM.UntagUser(ctx, &iam.UntagUserInput{UserName: aws.String(name), TagKeys: []string{policy.IDTag, policy.OwnerTag, policy.ProductTag}}); err != nil {
		return fmt.Errorf("untagging IAM user %s: %w", name, err)
	}
	return nil
}

func isNoSuchEntity(err error) bool {
	var e *itypes.NoSuchEntityException
	return errors.As(err, &e)
}
