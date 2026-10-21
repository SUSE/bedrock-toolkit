package account

import (
	"fmt"
	"testing"
)

func TestScheduleID(t *testing.T) {
	desc := fmt.Sprintf("Runs bedrock-monthly-unpause at 06:00 UTC on the 1st (%s%s)", scheduleIDPrefix, "e2e")
	for in, want := range map[string]string{desc: "e2e", "made by hand": "", "": "", "bedrock-admin:id=x": "x"} {
		if got := ScheduleID(in); got != want {
			t.Errorf("ScheduleID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvChange(t *testing.T) {
	base := map[string]string{EnvID: "x", EnvSnapshotBucket: "b"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, c := range []struct {
		have, want map[string]string
		out        string
	}{
		{base, with(), ""},
		{base, with(EnvReportSetup, "{}"), "turn on the central report"},
		{with(EnvReportSetup, "{}"), base, "turn off the central report"},
		{with(EnvReportSetup, "{}"), with(EnvReportSetup, `{"version":"2"}`), "update the central report settings"},
		{with(EnvID, "y"), with(EnvReportSetup, "{}"), "settings changed outside the file"},
	} {
		if got := envChange(c.have, c.want); got != c.out {
			t.Errorf("envChange(%v, %v) = %q, want %q", c.have, c.want, got, c.out)
		}
	}
}
