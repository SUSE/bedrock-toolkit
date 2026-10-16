package usercli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/innobead/bedrock-toolkit/internal/cli"
	"github.com/innobead/bedrock-toolkit/internal/policy"
)

func TestModels(t *testing.T) {
	listed := policy.UserInvoke("111122223333", "bedrock", "bedrock-user-bedrock-achen", "b", "",
		[]string{"us.anthropic.claude-opus-4-1", "eu.anthropic.claude-opus-4-1", "global.anthropic.claude-haiku-5-5"})
	cases := []struct {
		name      string
		change    func(*fakeAWS)
		want, not []string
	}{
		{"listed", func(f *fakeAWS) { f.rolePolicy = listed },
			[]string{"Your admin allows these models in us-west-2", "  us.anthropic.claude-opus-4-1\n", "  global.anthropic.claude-haiku-5-5\n"},
			[]string{"eu.", "Other Claude models"}},
		{"any claude", func(f *fakeAWS) { f.rolePolicy = policy.UserInvoke("111122223333", "bedrock", "r", "b", "", nil) },
			[]string{"any Anthropic Claude models except Fable", "  us.anthropic.claude-haiku-5-5\n", "  global.anthropic.claude-opus-5-5\n", "Other Claude models work"},
			[]string{"eu."}},
		{"old role", func(f *fakeAWS) {
			f.policyErr = &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized"}
		},
			[]string{"ask your admin to run bedrock-admin apply", "  us.anthropic.claude-sonnet-5-5\n"}, []string{"eu."}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			c.change(f)
			code, out, _ := run(t, f, ssoConfig+"\n"+wantBlock, "", "models")
			if code != cli.OK || f.policyRole != "bedrock-user-bedrock-achen" {
				t.Fatalf("exit %d role %q\n%s", code, f.policyRole, out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, w := range c.not {
				if strings.Contains(out, w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestModelsJSONAndErrors(t *testing.T) {
	f := newFake()
	f.rolePolicy = policy.UserInvoke("111122223333", "bedrock", "r", "b", "", nil)
	code, out, _ := run(t, f, ssoConfig+"\n"+wantBlock, "", "models", "--json")
	var got modelsJSON
	if err := json.Unmarshal([]byte(out), &got); code != cli.OK || err != nil || !got.AnyClaude || got.Source != "role" ||
		got.Region != "us-west-2" || len(got.Models) != 6 {
		t.Fatalf("exit %d %v %+v\n%s", code, err, got, out)
	}
	if code, out, _ := run(t, newFake(), ssoConfig, "", "models"); code == cli.OK || !strings.Contains(out, "run bedrock setup") {
		t.Fatalf("no profile: exit %d\n%s", code, out)
	}
	f = newFake()
	f.policyErr = &smithy.GenericAPIError{Code: "ExpiredToken", Message: "expired"}
	if code, out, _ := run(t, f, ssoConfig+"\n"+wantBlock, "", "models"); code == cli.OK || !strings.Contains(out, "expired") {
		t.Fatalf("error: exit %d\n%s", code, out)
	}
}
