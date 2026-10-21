package config

import (
	"strings"
	"testing"
)

func TestReportValidation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"off", "", ""},
		{"on", "report:\n  bucket: central-reports\n  region: us-east-1\n", ""},
		{"emails", "report:\n  bucket: central-reports\n  region: us-east-1\n  include_emails: true\n", ""},
		{"no region", "report:\n  bucket: central-reports\n", "report.region"},
		{"bad bucket", "report:\n  bucket: Central_Reports\n  region: us-east-1\n", "report.bucket"},
		{"no bucket", "report:\n  include_emails: true\n", "bucket is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := write(t, t.TempDir(), "bedrock.yaml", base+tc.body+"users: []\n")
			c, err := Load(p)
			switch {
			case tc.want == "" && err != nil:
				t.Fatal(err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want %q, got %v", tc.want, err)
			case tc.want == "" && c.Report.Enabled() != strings.Contains(tc.body, "bucket"):
				t.Fatalf("enabled: %+v", c.Report)
			}
		})
	}
}
