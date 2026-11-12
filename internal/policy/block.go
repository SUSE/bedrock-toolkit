package policy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// The deny that keeps model calls to personal roles (bedrock-admin block) is written by the org
// admin, so these functions read and merge policy documents someone else may have edited.

const (
	ssoRoleArn       = "arn:aws:iam::*:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_*"
	ssoRoleArnPrefix = "arn:aws:iam::*:role/aws-reserved/sso.amazonaws.com/*/AWSReservedSSO_"
	principalAccount = "aws:PrincipalAccount"
	assumeSid        = "AssumeBedrockPersonalRole"
)

// BlockStatementFor is the deny for a permission set's inline policy: like BlockStatement, but only
// in accounts, because a permission set's policy applies in every account it is assigned to.
func BlockStatementFor(adminPermissionSet string, accounts []string) map[string]any {
	s := BlockStatement(adminPermissionSet)
	if len(accounts) > 0 {
		a := slices.Sorted(slices.Values(accounts))
		s["Condition"].(doc)["StringEquals"] = doc{principalAccount: strAny(slices.Compact(a))}
	}
	return s
}

// AssumeStatement lets an SSO role assume the personal roles.
func AssumeStatement() map[string]any {
	return stmt{"Sid": assumeSid, "Effect": "Allow", "Action": "sts:AssumeRole", "Resource": "arn:aws:iam::*:role/bedrock-users/*"}
}

// HasBlock reports whether a policy document denies every blocked action on all resources.
// The org admin may adjust the statement, so only the effect, actions and resource are checked.
func HasBlock(document string) bool { return HasBlockFor(document, "") }

// HasBlockFor is HasBlock, but a statement limited to some accounts (aws:PrincipalAccount) counts
// only when it names account. With account "", any such statement counts.
func HasBlockFor(document, account string) bool {
	list, err := statements(document)
	if err != nil {
		return false
	}
	for _, s := range list {
		if !isBlock(s) {
			continue
		}
		accounts, scoped := blockAccounts(s)
		if account == "" || !scoped || slices.Contains(accounts, account) {
			return true
		}
	}
	return false
}

// ExemptPermissionSet is the permission set the deny in document leaves out (its ArnNotLike), or "".
func ExemptPermissionSet(document string) string {
	list, err := statements(document)
	if err != nil {
		return ""
	}
	for _, s := range list {
		if !isBlock(s) {
			continue
		}
		cond, _ := s["Condition"].(map[string]any)
		notLike, _ := cond["ArnNotLike"].(map[string]any)
		for _, arn := range strList(notLike["aws:PrincipalArn"]) {
			if rest, ok := strings.CutPrefix(arn, ssoRoleArnPrefix); ok {
				return strings.TrimSuffix(rest, "_*")
			}
		}
	}
	return ""
}

// MergeBlock adds the deny for account to a permission set's inline policy (document, "" for none).
// An existing statement with BlockSid keeps its accounts and gets account added; one that applies
// in every account is left as it is. It reports whether the document changed.
func MergeBlock(document, adminPermissionSet, account string) (string, bool, error) {
	list, err := statements(document)
	if err != nil {
		return "", false, err
	}
	if HasBlockFor(document, account) {
		return document, false, nil
	}
	accounts := []string{account}
	out := make([]any, 0, len(list)+1)
	for _, s := range list {
		if s["Sid"] == BlockSid {
			if a, scoped := blockAccounts(s); scoped {
				accounts = append(accounts, a...)
			}
			continue
		}
		out = append(out, s)
	}
	out = append(out, BlockStatementFor(adminPermissionSet, accounts))
	return withStatements(document, out), true, nil
}

// MergeAssume adds AssumeStatement to a permission set's inline policy unless it already allows it.
func MergeAssume(document string) (string, bool, error) {
	list, err := statements(document)
	if err != nil {
		return "", false, err
	}
	if AllowsAssumePersonalRole(document) {
		return document, false, nil
	}
	out := make([]any, 0, len(list)+1)
	for _, s := range list {
		out = append(out, s)
	}
	out = append(out, AssumeStatement())
	return withStatements(document, out), true, nil
}

func isBlock(s map[string]any) bool {
	if s["Effect"] != "Deny" || !covers(strList(s["Resource"]), "*") {
		return false
	}
	actions := strList(s["Action"])
	for _, a := range BlockActions {
		if !covers(actions, a) {
			return false
		}
	}
	return true
}

// blockAccounts returns the aws:PrincipalAccount values a statement is limited to, if any.
func blockAccounts(s map[string]any) ([]string, bool) {
	cond, _ := s["Condition"].(map[string]any)
	for _, op := range []string{"StringEquals", "StringLike"} {
		m, _ := cond[op].(map[string]any)
		for k, v := range m {
			if strings.EqualFold(k, principalAccount) {
				return strList(v), true
			}
		}
	}
	return nil, false
}

// statements parses a policy document's statements; "" has none.
func statements(document string) ([]map[string]any, error) {
	if strings.TrimSpace(document) == "" {
		return nil, nil
	}
	var d struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(Decode(document)), &d); err != nil {
		return nil, fmt.Errorf("not a policy document: %w", err)
	}
	if len(d.Statement) == 0 {
		return nil, nil
	}
	var list []map[string]any
	if json.Unmarshal(d.Statement, &list) != nil {
		var one map[string]any
		if err := json.Unmarshal(d.Statement, &one); err != nil {
			return nil, fmt.Errorf("not a policy document: %w", err)
		}
		list = []map[string]any{one}
	}
	return list, nil
}

// withStatements replaces document's statements, keeping its other keys (Version, Id).
func withStatements(document string, list []any) string {
	d := doc{}
	if strings.TrimSpace(document) != "" {
		_ = json.Unmarshal([]byte(Decode(document)), &d)
	}
	if d["Version"] == nil {
		d["Version"] = "2012-10-17"
	}
	d["Statement"] = list
	b, _ := json.MarshalIndent(d, "", "  ")
	return string(b)
}

func strAny(s []string) any {
	if len(s) == 1 {
		return s[0]
	}
	return s
}
