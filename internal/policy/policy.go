// Package policy holds every IAM and S3 policy document the tool creates, and a way to compare
// them with what AWS returns.
package policy

import (
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
)

// Paths and fixed names. Every account resource name ends in the setup's id (see Names below), so
// several setups can share an account.
const (
	// DefaultID is the id configure suggests and bedrock setup assumes.
	DefaultID           = "bedrock"
	Path                = "/bedrock/"
	UsersPath           = "/bedrock-users/"
	BudgetActionsPolicy = "attach-bedrock-deny"
	LambdaPolicy        = "monthly-unpause"
	SchedulerPolicy     = "invoke-unpause-lambda"
	ScheduleExpression  = "cron(0 6 1 * ? *)"
	// ReportScheduleExpression runs the central report daily; ReportInput tells the Lambda to report.
	ReportScheduleExpression = "cron(0 7 * * ? *)"
	ReportInput              = `{"action":"report"}`
	InvokePolicyName         = "bedrock-invoke"
	BlockPolicyName          = "bedrock-personal-role-only"
	BlockSid                 = "BedrockOnlyViaPersonalRole"
	SSOInlinePolicyName      = "AwsSSOInlinePolicy"
	SSORolePathPrefix        = "/aws-reserved/sso.amazonaws.com/"
	OwnerTag                 = "owner"
	ProductTag               = "product"
	IDTag                    = "bedrock-admin:id"
	CostAllocationOwnerTag   = "iamPrincipal/owner"
	WebSearchPolicy          = "web-search"
	WebSearchTarget          = "web-search"
	WebSearchConnector       = "web-search"
	// WebSearchConnectorVersion is the tested AgentCore Web Search connector version.
	WebSearchConnectorVersion = "1.2.0"
)

// Names of an id's account resources. An id is 1-20 lowercase letters and digits, so every name
// fits AWS limits (IAM role names: 64) and a name maps back to exactly one id.
func UserPath(id string) string           { return UsersPath + id + "/" }
func DenyPolicyName(id string) string     { return "bedrock-deny-" + id }
func BudgetActionsRole(id string) string  { return "bedrock-budget-actions-" + id }
func LambdaName(id string) string         { return "bedrock-monthly-unpause-" + id }
func LambdaRole(id string) string         { return "bedrock-monthly-unpause-" + id }
func SchedulerRole(id string) string      { return "bedrock-monthly-unpause-scheduler-" + id }
func ScheduleName(id string) string       { return "bedrock-monthly-unpause-" + id }
func ReportScheduleName(id string) string { return "bedrock-daily-report-" + id }
func WebSearchRole(id string) string      { return "bedrock-web-search-gateway-" + id }
func WebSearchGateway(id string) string   { return "bedrock-web-search-" + id }
func ExportName(id string) string         { return "bedrock-cur-" + id }
func IAMUsersPolicyName(id string) string { return "bedrock-iam-users-" + id }
func BucketName(account, id string) string {
	return "bedrock-cur-" + account + "-" + id
}

// UserRoleName and BudgetName are a user's personal role and budget: bedrock-user-<id>-<name> and
// bedrock-<id>-<name>.
func UserRoleName(id, name string) string { return "bedrock-user-" + id + "-" + name }
func BudgetName(id, name string) string   { return "bedrock-" + id + "-" + name }

// RoleForBudget maps a budget name to its personal role: bedrock-X ↔ bedrock-user-X.
func RoleForBudget(budget string) string {
	return "bedrock-user-" + strings.TrimPrefix(budget, "bedrock-")
}

// BudgetForRole maps a personal role name to its budget: bedrock-user-X ↔ bedrock-X.
func BudgetForRole(role string) string {
	return "bedrock-" + strings.TrimPrefix(role, "bedrock-user-")
}

// SplitUserRole returns the id and user name of a personal role name, ok false for other names.
func SplitUserRole(role string) (id, name string, ok bool) {
	rest, found := strings.CutPrefix(role, "bedrock-user-")
	if !found {
		return "", "", false
	}
	id, name, ok = strings.Cut(rest, "-")
	if !ok || id == "" || name == "" {
		return "", "", false
	}
	return id, name, true
}

// GatewaysArn matches the id's AgentCore gateway in region: AgentCore makes a gateway's ID from its
// name, a '-' and a random suffix.
func GatewaysArn(region, account, id string) string {
	return fmt.Sprintf("arn:aws:bedrock-agentcore:%s:%s:gateway/%s-*", region, account, WebSearchGateway(id))
}

// WebSearchToolArn is the AWS-owned Web Search tool.
func WebSearchToolArn(region string) string {
	return fmt.Sprintf("arn:aws:bedrock-agentcore:%s:aws:tool/web-search.v1", region)
}

func DenyPolicyArn(account, id string) string {
	return fmt.Sprintf("arn:aws:iam::%s:policy/bedrock/%s", account, DenyPolicyName(id))
}
func IAMUsersPolicyArn(account, id string) string {
	return fmt.Sprintf("arn:aws:iam::%s:policy/bedrock/%s", account, IAMUsersPolicyName(id))
}
func BudgetActionsRoleArn(account, id string) string {
	return fmt.Sprintf("arn:aws:iam::%s:role/bedrock/%s", account, BudgetActionsRole(id))
}

// UserRoleArn is the ARN of a personal role, under /bedrock-users/<id>/.
func UserRoleArn(account, id, role string) string {
	return fmt.Sprintf("arn:aws:iam::%s:role%s%s", account, UserPath(id), role)
}

// UserRolesArn matches every personal role of an id.
func UserRolesArn(account, id string) string { return UserRoleArn(account, id, "*") }
func LambdaArn(region, account, id string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", region, account, LambdaName(id))
}

// DenyActions are the model-call actions blocked by a pause.
var DenyActions = []string{"bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream",
	"bedrock:CreateModelInvocationJob", "bedrock:CallWithBearerToken", "bedrock-agentcore:InvokeGateway"}

// BlockActions are the actions Step 1.7 blocks outside personal roles.
var BlockActions = []string{"bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream",
	"bedrock:CreateModelInvocationJob", "bedrock:CallWithBearerToken",
	"bedrock:InvokeAgent", "bedrock:InvokeFlow", "bedrock:RetrieveAndGenerate"}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type doc = map[string]any
type stmt = map[string]any

func document(s ...stmt) string {
	list := make([]any, len(s))
	for i := range s {
		list[i] = s[i]
	}
	return mustJSON(doc{"Version": "2012-10-17", "Statement": list})
}

// Deny is the pause policy AWS Budgets (or pause) attaches to a personal role and its IAM user.
func Deny() string {
	return document(stmt{"Sid": "PauseBedrockModelUsage", "Effect": "Deny", "Action": DenyActions, "Resource": "*"})
}

func BudgetActionsTrust(account string) string {
	return document(stmt{"Effect": "Allow", "Principal": doc{"Service": "budgets.amazonaws.com"},
		"Action": "sts:AssumeRole", "Condition": doc{
			"StringEquals": doc{"aws:SourceAccount": account},
			"ArnLike":      doc{"aws:SourceArn": fmt.Sprintf("arn:aws:budgets::%s:budget/*", account)}}})
}

// BudgetActionsPermissions lets AWS Budgets attach and detach the id's deny policy, on the id's
// personal roles and the IAM users tagged with the id only.
func BudgetActionsPermissions(account, id string) string {
	return document(
		stmt{"Sid": "PauseRoles", "Effect": "Allow", "Action": []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			"Resource":  UserRolesArn(account, id),
			"Condition": doc{"ArnEquals": doc{"iam:PolicyARN": DenyPolicyArn(account, id)}}},
		stmt{"Sid": "PauseIAMUsers", "Effect": "Allow", "Action": []string{"iam:AttachUserPolicy", "iam:DetachUserPolicy"},
			"Resource": IAMUsersArn(account), "Condition": idUserCondition(account, id)},
	)
}

// IAMUsersArn matches every IAM user of the account.
func IAMUsersArn(account string) string { return fmt.Sprintf("arn:aws:iam::%s:user/*", account) }

func idUserCondition(account, id string) doc {
	return doc{"ArnEquals": doc{"iam:PolicyARN": DenyPolicyArn(account, id)},
		"StringEquals": doc{"aws:ResourceTag/" + IDTag: id}}
}

func ServiceTrust(service, account string) string {
	return document(stmt{"Effect": "Allow", "Principal": doc{"Service": service}, "Action": "sts:AssumeRole",
		"Condition": doc{"StringEquals": doc{"aws:SourceAccount": account}}})
}

// WebSearchGatewayTrust lets only the id's AgentCore gateway assume the gateway role.
func WebSearchGatewayTrust(region, account, id string) string {
	return document(stmt{"Effect": "Allow", "Principal": doc{"Service": "bedrock-agentcore.amazonaws.com"},
		"Action": "sts:AssumeRole", "Condition": doc{
			"StringEquals": doc{"aws:SourceAccount": account},
			"ArnLike":      doc{"aws:SourceArn": GatewaysArn(region, account, id)}}})
}

// WebSearchGatewayPermissions lets the gateway call Web Search, and nothing else.
func WebSearchGatewayPermissions(region, account, id string) string {
	return document(
		stmt{"Sid": "InvokeGateway", "Effect": "Allow", "Action": "bedrock-agentcore:InvokeGateway",
			"Resource": GatewaysArn(region, account, id)},
		stmt{"Sid": "WebSearch", "Effect": "Allow", "Action": "bedrock-agentcore:InvokeWebSearch",
			"Resource": WebSearchToolArn(region)},
	)
}

// LambdaPermissions lets the monthly Lambda re-arm budget actions, keep manual pauses in place and
// write snapshots, for the id's users only; with reportBucket, also write this account's central
// report.
func LambdaPermissions(region, account, id, bucket, snapshotPrefix, reportBucket string) string {
	s := []stmt{
		{"Sid": "Budgets", "Effect": "Allow", "Action": []string{"budgets:DescribeBudgetActionsForAccount",
			"budgets:DescribeBudgetAction", "budgets:ExecuteBudgetAction", "budgets:DescribeBudgetActionHistories",
			"budgets:ViewBudget"}, "Resource": "*"},
		{"Sid": "ReadUserRoles", "Effect": "Allow", "Action": []string{"iam:ListRoleTags", "iam:ListAttachedRolePolicies", "iam:GetRole"},
			"Resource": UserRolesArn(account, id)},
		{"Sid": "KeepManualPauses", "Effect": "Allow", "Action": "iam:AttachRolePolicy",
			"Resource":  UserRolesArn(account, id),
			"Condition": doc{"ArnEquals": doc{"iam:PolicyARN": DenyPolicyArn(account, id)}}},
		{"Sid": "KeepManualPausesIAMUsers", "Effect": "Allow", "Action": "iam:AttachUserPolicy",
			"Resource": IAMUsersArn(account), "Condition": idUserCondition(account, id)},
		{"Sid": "Snapshots", "Effect": "Allow", "Action": "s3:PutObject",
			"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/*", bucket, snapshotPrefix)},
		{"Sid": "Logs", "Effect": "Allow", "Action": []string{"logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"},
			"Resource": fmt.Sprintf("arn:aws:logs:%s:%s:log-group:/aws/lambda/%s:*", region, account, LambdaName(id))},
	}
	if reportBucket != "" {
		s = append(s, stmt{"Sid": "CentralReport", "Effect": "Allow", "Action": "s3:PutObject",
			"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/*", reportBucket, account)})
	}
	return document(s...)
}

func SchedulerPermissions(region, account, id string) string {
	return document(stmt{"Effect": "Allow", "Action": "lambda:InvokeFunction", "Resource": LambdaArn(region, account, id)})
}

// CurBucket lets only AWS Data Exports write to the cost export bucket.
func CurBucket(bucket, account string) string {
	return document(stmt{"Sid": "AllowDataExportsToWrite", "Effect": "Allow",
		"Principal": doc{"Service": []string{"bcm-data-exports.amazonaws.com", "billingreports.amazonaws.com"}},
		"Action":    []string{"s3:PutObject", "s3:GetBucketPolicy"},
		"Resource":  []string{"arn:aws:s3:::" + bucket, "arn:aws:s3:::" + bucket + "/*"},
		"Condition": doc{"StringLike": doc{"aws:SourceAccount": account,
			"aws:SourceArn": []string{fmt.Sprintf("arn:aws:cur:us-east-1:%s:definition/*", account),
				fmt.Sprintf("arn:aws:bcm-data-exports:us-east-1:%s:export/*", account)}}}})
}

// UserTrust lets only the matching SSO identity assume a personal role, with its email as the
// session name, and, when iamUser is set, that IAM user of the account, with the same session name.
func UserTrust(account, email, iamUser string) string {
	root := doc{"AWS": fmt.Sprintf("arn:aws:iam::%s:root", account)}
	s := []stmt{{"Effect": "Allow", "Principal": root,
		"Action": "sts:AssumeRole", "Condition": doc{
			"ArnLike":      doc{"aws:PrincipalArn": fmt.Sprintf("arn:aws:iam::%s:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_*", account)},
			"StringLike":   doc{"aws:userid": "*:" + email},
			"StringEquals": doc{"sts:RoleSessionName": email}}}}
	if iamUser != "" {
		s = append(s, stmt{"Sid": "IAMUser", "Effect": "Allow", "Principal": root, "Action": "sts:AssumeRole",
			"Condition": doc{"StringEquals": doc{"aws:PrincipalType": "User", "aws:username": iamUser,
				"sts:RoleSessionName": email}}})
	}
	return document(s...)
}

// IAMUsers is the managed policy attached to the IAM users that may use personal roles: assume a
// personal role (each role's trust admits only its own IAM user), read their own tags (bedrock
// setup reads the email from them) and, with block, the Step 1.7 deny, so their access keys can't
// call models directly.
func IAMUsers(account, id string, block bool) string {
	s := []stmt{
		{"Sid": "AssumeBedrockPersonalRole", "Effect": "Allow", "Action": "sts:AssumeRole", "Resource": UserRolesArn(account, id)},
		{"Sid": "ReadOwnTags", "Effect": "Allow", "Action": "iam:ListUserTags", "Resource": []string{
			fmt.Sprintf("arn:aws:iam::%s:user/${aws:username}", account),
			fmt.Sprintf("arn:aws:iam::%s:user/*/${aws:username}", account)}},
	}
	if block {
		s = append(s, stmt{"Sid": BlockSid, "Effect": "Deny", "Action": BlockActions, "Resource": "*"})
	}
	return document(s...)
}

// AllowedModelsText describes the models AllowedModel accepts, for messages.
const AllowedModelsText = "Anthropic Claude models except Fable"

// AllowedModel reports whether UserInvoke can let personal roles call a model ID or inference profile
// ID. It is the only rule on which models models: and models.toml may list; allowing other providers
// means changing it and AllowedModelsText.
func AllowedModel(model string) bool {
	return strings.Contains(model, "anthropic.claude-") && !strings.Contains(model, "anthropic.claude-fable")
}

var geoPrefixes = []string{"us.", "eu.", "apac.", "global.", "jp.", "au.", "ca.", "us-gov."}

// FoundationModelID strips the cross-region inference profile prefix (us., eu., ...).
func FoundationModelID(model string) string {
	for _, p := range geoPrefixes {
		if m, ok := strings.CutPrefix(model, p); ok {
			return m
		}
	}
	return model
}

// ListedModelsSid is the statement that allows only the models listed in the config.
const ListedModelsSid = "InvokeListedModelsOnly"

// invokeStmt allows calling the listed models, through their inference profiles and the foundation
// models those route to in any region, or any Claude model when none are listed.
func invokeStmt(account string, models []string) stmt {
	actions := []string{"bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream"}
	if len(models) == 0 {
		return stmt{"Sid": "InvokeClaudeModelsOnly", "Effect": "Allow", "Action": actions,
			"Resource": []string{"arn:aws:bedrock:*::foundation-model/anthropic.claude-*",
				fmt.Sprintf("arn:aws:bedrock:*:%s:inference-profile/*anthropic.claude-*", account)}}
	}
	var res []string
	seen := map[string]bool{}
	add := func(r string) {
		if !seen[r] {
			seen[r] = true
			res = append(res, r)
		}
	}
	for _, m := range models {
		if fm := FoundationModelID(m); fm != m {
			add(fmt.Sprintf("arn:aws:bedrock:*:%s:inference-profile/%s", account, m))
		}
		add("arn:aws:bedrock:*::foundation-model/" + FoundationModelID(m))
	}
	return stmt{"Sid": ListedModelsSid, "Effect": "Allow", "Action": actions, "Resource": res}
}

// ListedModels returns the models a UserInvoke document allows, or nil when it allows any Claude model.
func ListedModels(policyDoc string) ([]string, error) {
	var d struct {
		Statement []struct {
			Sid      string
			Resource any
		}
	}
	if err := json.Unmarshal([]byte(policyDoc), &d); err != nil {
		return nil, err
	}
	for _, st := range d.Statement {
		if st.Sid != ListedModelsSid {
			continue
		}
		var res []string
		switch r := st.Resource.(type) {
		case string:
			res = []string{r}
		case []any:
			for _, v := range r {
				if s, ok := v.(string); ok {
					res = append(res, s)
				}
			}
		}
		var out []string
		profiles := map[string]bool{}
		for _, r := range res {
			if _, id, ok := strings.Cut(r, ":inference-profile/"); ok {
				out = append(out, id)
				profiles[FoundationModelID(id)] = true
			}
		}
		for _, r := range res {
			if _, id, ok := strings.Cut(r, "::foundation-model/"); ok && !profiles[id] {
				out = append(out, id)
			}
		}
		return out, nil
	}
	return nil, nil
}

// UserInvoke allows the listed models, or any Claude model when none are listed, and always denies
// the Claude Fable family. It lets the user read their own budget and their own role's tags, for
// bedrock usage. With a webSearchRegion, it also allows calling the id's AgentCore gateway there.
func UserInvoke(account, id, role, budget, webSearchRegion string, models []string) string {
	budgetArn := fmt.Sprintf("arn:aws:budgets::%s:budget/%s", account, budget)
	s := []stmt{
		invokeStmt(account, models),
		{"Sid": "DiscoverModels", "Effect": "Allow", "Action": []string{"bedrock:ListFoundationModels",
			"bedrock:GetFoundationModel", "bedrock:ListInferenceProfiles", "bedrock:GetInferenceProfile"}, "Resource": "*"},
		{"Sid": "DenyUnapprovedFableModels", "Effect": "Deny",
			"Action": []string{"bedrock:InvokeModel", "bedrock:InvokeModelWithResponseStream", "bedrock:CreateModelInvocationJob"},
			"Resource": []string{"arn:aws:bedrock:*::foundation-model/anthropic.claude-fable*",
				fmt.Sprintf("arn:aws:bedrock:*:%s:inference-profile/*anthropic.claude-fable*", account)}},
		{"Sid": "ReadOwnBudget", "Effect": "Allow", "Action": []string{"budgets:ViewBudget",
			"budgets:DescribeBudgetActionsForBudget", "budgets:DescribeBudgetActionHistories"},
			"Resource": []string{budgetArn, budgetArn + "/action/*"}},
		{"Sid": "ReadOwnRole", "Effect": "Allow", "Action": []string{"iam:GetRole", "iam:ListRoleTags", "iam:GetRolePolicy"},
			"Resource": UserRoleArn(account, id, role)},
	}
	if webSearchRegion != "" {
		s = append(s,
			stmt{"Sid": "WebSearch", "Effect": "Allow", "Action": "bedrock-agentcore:InvokeGateway",
				"Resource": GatewaysArn(webSearchRegion, account, id)},
			stmt{"Sid": "FindWebSearchGateway", "Effect": "Allow", "Action": "bedrock-agentcore:ListGateways", "Resource": "*"},
			stmt{"Sid": "ReadWebSearchGateway", "Effect": "Allow", "Action": "bedrock-agentcore:GetGateway",
				"Resource": GatewaysArn(webSearchRegion, account, id)})
	}
	return document(s...)
}

// BlockStatement is the Step 1.7 deny, as one statement (for an SCP or a permission set).
func BlockStatement(adminPermissionSet string) map[string]any {
	return stmt{"Sid": BlockSid, "Effect": "Deny", "Action": BlockActions, "Resource": "*",
		"Condition": doc{
			"ArnLike":    doc{"aws:PrincipalArn": "arn:aws:iam::*:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_*"},
			"ArnNotLike": doc{"aws:PrincipalArn": "arn:aws:iam::*:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_" + adminPermissionSet + "_*"}}}
}

// Block is the full Step 1.7 policy document, pretty-printed for the org admin.
func Block(adminPermissionSet string) string {
	b, _ := json.MarshalIndent(doc{"Version": "2012-10-17", "Statement": []any{BlockStatement(adminPermissionSet)}}, "", "  ")
	return string(b) + "\n"
}

// AssumePersonalRoles is what users' permission sets need so they can assume their personal role.
func AssumePersonalRoles() string {
	b, _ := json.MarshalIndent(doc{"Version": "2012-10-17", "Statement": []any{stmt{
		"Sid": "AssumeBedrockPersonalRole", "Effect": "Allow", "Action": "sts:AssumeRole",
		"Resource": "arn:aws:iam::*:role/bedrock-users/*"}}}, "", "  ")
	return string(b) + "\n"
}

// Decode turns a policy document as returned by IAM (URL-encoded) into plain JSON.
func Decode(s string) string {
	if strings.HasPrefix(s, "%7B") || strings.HasPrefix(s, "%7b") {
		if d, err := url.QueryUnescape(s); err == nil {
			return d
		}
	}
	return s
}

// Equal compares two policy documents, ignoring formatting, key order, the order of string lists,
// and whether a one-element list is written as a plain string.
func Equal(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(Decode(a)), &x) != nil || json.Unmarshal([]byte(Decode(b)), &y) != nil {
		return false
	}
	return reflect.DeepEqual(normalize(x), normalize(y))
}

func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normalize(e)
		}
		return out
	case []any:
		if len(t) == 1 {
			return normalize(t[0])
		}
		strs := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				out := make([]any, len(t))
				for i := range t {
					out[i] = normalize(t[i])
				}
				return out
			}
			strs = append(strs, s)
		}
		sort.Strings(strs)
		out := make([]any, len(strs))
		for i, s := range strs {
			out[i] = s
		}
		return out
	}
	return v
}

// HasBlock reports whether a policy document denies every Step 1.7 action on all resources.
// The org admin may adjust the statement (for example name the account in the ARNs), so only the
// effect, actions and resource are checked.
func HasBlock(document string) bool {
	var d struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if json.Unmarshal([]byte(Decode(document)), &d) != nil {
		return false
	}
	var list []map[string]any
	if json.Unmarshal(d.Statement, &list) != nil {
		var one map[string]any
		if json.Unmarshal(d.Statement, &one) != nil {
			return false
		}
		list = []map[string]any{one}
	}
	for _, s := range list {
		if s["Effect"] != "Deny" || !covers(strList(s["Resource"]), "*") {
			continue
		}
		actions := strList(s["Action"])
		ok := true
		for _, a := range BlockActions {
			if !covers(actions, a) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// AllowsAssumePersonalRole reports whether a policy document allows sts:AssumeRole on personal roles.
func AllowsAssumePersonalRole(document string) bool {
	var d struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if json.Unmarshal([]byte(Decode(document)), &d) != nil {
		return false
	}
	var list []map[string]any
	if json.Unmarshal(d.Statement, &list) != nil {
		var one map[string]any
		if json.Unmarshal(d.Statement, &one) != nil {
			return false
		}
		list = []map[string]any{one}
	}
	for _, s := range list {
		if s["Effect"] != "Allow" || !covers(strList(s["Action"]), "sts:AssumeRole") {
			continue
		}
		for _, r := range strList(s["Resource"]) {
			if r == "*" || strings.Contains(r, ":role/bedrock-users/") || strings.HasSuffix(r, ":role/*") {
				return true
			}
		}
	}
	return false
}

func strList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// covers reports whether a list of IAM action or resource patterns matches want.
func covers(patterns []string, want string) bool {
	for _, p := range patterns {
		if strings.EqualFold(p, want) || p == "*" {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(strings.ToLower(want), strings.ToLower(strings.TrimSuffix(p, "*"))) {
			return true
		}
	}
	return false
}
