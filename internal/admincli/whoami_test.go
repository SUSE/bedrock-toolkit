package admincli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWhoami(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "bedrock.yaml")
	if err := os.WriteFile(f, []byte("id: acme\naccount: \"111122223333\"\nregion: us-west-2\nproduct: acme\nprofile: bedrock-admin\nusers: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_PROFILE", "")
	whoami := func(arn string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		a := New("test")
		a.In, a.Out, a.Err = strings.NewReader(""), &out, &errb
		a.Identity = func(_ context.Context, profile string) (string, error) {
			if profile != "bedrock-admin" {
				t.Errorf("profile %q", profile)
			}
			return arn, nil
		}
		code := a.Main(context.Background(), append([]string{"whoami", "-f", f}, args...))
		return code, out.String(), errb.String()
	}
	code, out, errs := whoami("arn:aws:sts::111122223333:assumed-role/AWSReservedSSO_BedrockAdmin_0123abcd/achen@example.com")
	if code != 0 || !strings.Contains(out, "Profile:  bedrock-admin\nAccount:  111122223333\nIdentity: SSO user achen@example.com, permission set BedrockAdmin\n") {
		t.Fatalf("code %d\n%s%s", code, out, errs)
	}
	code, out, _ = whoami("arn:aws:sts::444455556666:assumed-role/AWSReservedSSO_BedrockAdmin_0123abcd/achen@example.com", "--json")
	if code != 2 || !strings.Contains(out, `"config_account": "111122223333"`) {
		t.Fatalf("other account: code %d\n%s", code, out)
	}
}
