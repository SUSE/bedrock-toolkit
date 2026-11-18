package awsx

import "testing"

func TestDescribe(t *testing.T) {
	for arn, want := range map[string]string{
		"arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_BedrockUser_0123abcd/achen@example.com": "SSO user achen@example.com, permission set BedrockUser",
		"arn:aws:sts::111122223333:assumed-role/bedrock-user-acme-achen/achen@example.com":             "role bedrock-user-acme-achen, session achen@example.com",
		"arn:aws:iam::111122223333:user/dev/achen.dev":                                                 "IAM user achen.dev",
		"arn:aws:iam::111122223333:root":                                                               "root user",
	} {
		if acct, who := Describe(arn); acct != "111122223333" || who != want {
			t.Errorf("%s: got %q %q", arn, acct, who)
		}
	}
}
