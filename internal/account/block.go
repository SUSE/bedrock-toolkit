package account

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// BlockPolicyFile is what bedrock-admin block policy writes for the org admin.
const BlockPolicyFile = "bedrock-block-policy.json"

// SSORole is one AWSReservedSSO_* role: a permission set assigned to this account.
type SSORole struct {
	PermissionSet string
	Inline        string // AwsSSOInlinePolicy, "" if none
	Managed       []string
}

// PermissionSetName extracts the permission set from AWSReservedSSO_<name>_<hash>.
func PermissionSetName(role string) string {
	n := strings.TrimPrefix(role, "AWSReservedSSO_")
	if i := strings.LastIndex(n, "_"); i > 0 {
		return n[:i]
	}
	return n
}

// BlockCheck is the result of checking every permission set.
type BlockCheck struct {
	Checked       []string
	MissingBlock  []string
	MissingAssume []string
}

// CheckSSORoles finds the permission sets (other than the admin one) missing the bedrock-admin block
// deny for account or sts:AssumeRole on the personal roles.
func CheckSSORoles(roles []SSORole, adminPermissionSet, account string) BlockCheck {
	var c BlockCheck
	for _, r := range roles {
		if r.PermissionSet == adminPermissionSet {
			continue
		}
		c.Checked = append(c.Checked, r.PermissionSet)
		if !policy.HasBlockFor(r.Inline, account) {
			c.MissingBlock = append(c.MissingBlock, r.PermissionSet)
		}
		if !policy.AllowsAssumePersonalRole(r.Inline) && !broadManaged(r.Managed) {
			c.MissingAssume = append(c.MissingAssume, r.PermissionSet)
		}
	}
	sort.Strings(c.Checked)
	sort.Strings(c.MissingBlock)
	sort.Strings(c.MissingAssume)
	return c
}

func broadManaged(names []string) bool {
	for _, n := range names {
		if n == "AdministratorAccess" || n == "PowerUserAccess" {
			return true
		}
	}
	return false
}

// SSORoles reads every permission set role in the account.
func SSORoles(ctx context.Context, c *iam.Client) ([]SSORole, error) {
	var out []SSORole
	p := iam.NewListRolesPaginator(c, &iam.ListRolesInput{PathPrefix: aws.String(policy.SSORolePathPrefix)})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, role := range r.Roles {
			name := aws.ToString(role.RoleName)
			if !strings.HasPrefix(name, "AWSReservedSSO_") {
				continue
			}
			s := SSORole{PermissionSet: PermissionSetName(name)}
			pol, err := c.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String(policy.SSOInlinePolicyName)})
			switch {
			case err == nil:
				s.Inline = policy.Decode(aws.ToString(pol.PolicyDocument))
			case !isNoSuchEntity(err):
				return nil, err
			}
			ap, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(name)})
			if err != nil {
				return nil, err
			}
			for _, a := range ap.AttachedPolicies {
				s.Managed = append(s.Managed, aws.ToString(a.PolicyName))
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// AdminPermissionSet is the permission set an existing deny exempts, or the default.
func AdminPermissionSet(roles []SSORole) string {
	for _, r := range roles {
		if ps := policy.ExemptPermissionSet(r.Inline); ps != "" {
			return ps
		}
	}
	return config.DefaultAdminPermissionSet
}

// Instructions is the text for the org admin who applies the deny.
func Instructions(path, account, adminPS string) string {
	return fmt.Sprintf(`
Wrote %[1]s. Blocking model calls outside personal roles needs AWS Organizations rights.
Send the file to your Organizations admin with these instructions:

  Block Bedrock model calls outside personal roles in account %[2]s, either way:
  - SCP (preferred; covers every permission set, including future ones; has no effect if %[2]s
    is the management account):
      bedrock-admin block apply --account %[2]s --method scp --admin-permission-set %[3]s
    or create an SCP from the file and attach it to the account.
  - Permission sets:
      bedrock-admin block apply --account %[2]s --method permission-set --admin-permission-set %[3]s
    or add the statement in the file to the inline policy of every permission set assigned to
    the account except %[3]s, then re-provision them.

Check the result with: bedrock-admin block status
`, path, account, adminPS)
}
