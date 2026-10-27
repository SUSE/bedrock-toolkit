package admincli

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// connect is awsx.New for bedrock-admin: it refuses a Bedrock user's own credentials.
func (a *App) connect(ctx context.Context, cfg *config.Config, region, account string) (*awsx.Clients, error) {
	c, err := awsx.New(ctx, a.profile(cfg), region, account)
	if err != nil {
		return nil, err
	}
	if err := checkAdmin(ctx, c.IAM, c.CallerArn, cfg); err != nil {
		return nil, err
	}
	return c, nil
}

type userTagLister interface {
	ListUserTags(ctx context.Context, in *iam.ListUserTagsInput, optFns ...func(*iam.Options)) (*iam.ListUserTagsOutput, error)
}

// checkAdmin refuses a personal role (bedrock-user-<id>-<name>) and an IAM user onboarded with
// iam_user (in the file, or tagged bedrock-admin:id by apply). It is a guardrail: IAM is what keeps
// users from changing the setup, as their credentials have no admin rights.
func checkAdmin(ctx context.Context, tags userTagLister, arn string, cfg *config.Config) error {
	kind, name := callerName(arn)
	const fix = "; bedrock-admin is for admins: use the admin's profile (--profile, AWS_PROFILE or profile: in bedrock.yaml)"
	switch kind {
	case "role":
		if _, _, ok := policy.SplitUserRole(name); ok {
			return fmt.Errorf("these credentials are the personal role %s of a Bedrock user%s", name, fix)
		}
	case "user":
		if cfg != nil {
			for _, u := range cfg.Users {
				if u.IAMUser != "" && strings.EqualFold(u.IAMUser, name) {
					return fmt.Errorf("these credentials are IAM user %s, the iam_user of %s%s", name, u.Email, fix)
				}
			}
		}
		// A user's IAM user may not read its tags; then it can't run bedrock-admin anyway.
		out, err := tags.ListUserTags(ctx, &iam.ListUserTagsInput{UserName: aws.String(name)})
		if err != nil {
			return nil
		}
		for _, t := range out.Tags {
			if aws.ToString(t.Key) == policy.IDTag {
				return fmt.Errorf("these credentials are IAM user %s, onboarded as a Bedrock user of setup %s%s", name, aws.ToString(t.Value), fix)
			}
		}
	}
	return nil
}

// callerName splits a caller ARN into its kind (role or user) and name. Assumed-role ARNs drop the
// role's path: arn:aws:sts::<account>:assumed-role/<role>/<session>.
func callerName(arn string) (kind, name string) {
	i := strings.LastIndex(arn, ":")
	if i < 0 {
		return "", ""
	}
	parts := strings.Split(arn[i+1:], "/")
	switch {
	case parts[0] == "assumed-role" && len(parts) >= 2:
		return "role", parts[1]
	case (parts[0] == "role" || parts[0] == "user") && len(parts) >= 2:
		return parts[0], parts[len(parts)-1]
	}
	return "", ""
}
