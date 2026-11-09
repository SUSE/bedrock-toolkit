package usercli

import (
	"strings"
	"testing"
)

func TestLoginRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, config, want string }{
		{"missing", "", "is not an SSO profile"},
		{"access key", "[profile sso]\nregion = us-east-1\n", "is not an SSO profile"},
		{"missing session", "[profile sso]\nsso_session = missing\n", "requires sso_region and sso_start_url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := run(t, newFake(), tc.config, "", "login", "--profile", "sso")
			if code == 0 || !strings.Contains(out, tc.want) {
				t.Fatalf("code=%d output=%s", code, out)
			}
		})
	}
}
