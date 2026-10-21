package usercli

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
)

func TestModelFix(t *testing.T) {
	deny := func(msg string) error { return &smithy.GenericAPIError{Code: "AccessDeniedException", Message: msg} }
	const sonnet = "us.anthropic.claude-sonnet-4-5-20250929-v1:0"
	active := pauseInfo{known: true}
	held := pauseInfo{known: true, paused: true, detail: "PAUSED by your admin"}
	cases := []struct {
		err           error
		region, model string
		want, not     string
		p             pauseInfo
	}{
		{deny("... with an explicit deny in an identity-based policy"), "us-east-1", sonnet, "paused", "", pauseInfo{}},
		{deny("... because no identity-based policy allows the bedrock:InvokeModel action"), "us-east-1", sonnet, "doesn't allow this model", "paused", pauseInfo{}},
		{deny("... because no identity-based policy allows the bedrock:InvokeModel action"), "ap-south-1", "global.anthropic.claude-haiku-4-5", "--region us-east-1", "paused", pauseInfo{}},
		{deny("... with an explicit deny in a service control policy"), "us-east-1", sonnet, "organization", "", pauseInfo{}},
		{deny(""), "us-east-1", sonnet, "paused", "--region", pauseInfo{}},
		{deny(""), "us-east-1", "us.anthropic.claude-fable-1-v1:0", "except Fable", "", pauseInfo{}},
		{deny(""), "us-east-1", "amazon.nova-pro-v1:0", "only call Anthropic Claude", "", pauseInfo{}},
		{&smithy.GenericAPIError{Code: "ValidationException"}, "us-east-1", sonnet, "not offered in us-east-1", "", pauseInfo{}},
		// The budget says whether the role is paused.
		{deny(""), "ap-southeast-2", "global.anthropic.claude-opus-5-5", "you are not paused", "budget used up", active},
		{deny(""), "ap-southeast-2", "global.anthropic.claude-opus-5-5", "--region us-east-1", "", active},
		{deny("... with an explicit deny in an identity-based policy"), "us-east-1", sonnet, "not paused, but a policy denies", "budget used up", active},
		{deny(""), "us-east-1", sonnet, "PAUSED by your admin", "doesn't allow", held},
		{deny("... because no identity-based policy allows the bedrock:InvokeModel action"), "us-east-1", sonnet, "PAUSED", "", held},
		{deny("... with an explicit deny in a service control policy"), "us-east-1", sonnet, "organization", "PAUSED", held},
	}
	for _, c := range cases {
		got := modelFix(c.err, c.region, c.model, c.p)
		if !strings.Contains(got, c.want) || (c.not != "" && strings.Contains(got, c.not)) {
			t.Errorf("%v %s %s: %q, want %q without %q", c.err, c.region, c.model, got, c.want, c.not)
		}
	}
}

func TestShort(t *testing.T) {
	for in, want := range map[string]string{
		"operation error Bedrock Runtime: Converse, https response error StatusCode: 403, RequestID: a35e, AccessDeniedException: ":   "AccessDeniedException",
		"operation error STS: AssumeRole, https response error StatusCode: 403, RequestID: x, api error AccessDenied: not authorized": "AccessDenied: not authorized",
		"plain": "plain",
	} {
		if got := short(errors.New(in)).Error(); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
