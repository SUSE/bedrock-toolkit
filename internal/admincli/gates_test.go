package admincli

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/innobead/bedrock-toolkit/internal/cli"
	"github.com/innobead/bedrock-toolkit/internal/plan"
	"github.com/innobead/bedrock-toolkit/internal/usage"
)

func TestNeedsYes(t *testing.T) {
	do := func(context.Context) error { return nil }
	p := &plan.Plan{}
	p.Add(
		&plan.Item{Target: "a@x", Op: plan.Delete, NeedsYes: true, Apply: do},
		&plan.Item{Target: "b@x", Op: plan.Update, NeedsYes: true, Apply: do},
		&plan.Item{Target: "c@x", Op: plan.Update, Apply: do},
		&plan.Item{Target: "d@x", Op: plan.Info, NeedsYes: true}, // nothing to apply
	)
	if got := needsYes(p); !reflect.DeepEqual(got, []string{"a@x", "b@x"}) {
		t.Fatalf("needsYes = %v", got)
	}
	if got := needsYes(&plan.Plan{}); got != nil {
		t.Fatalf("empty plan: %v", got)
	}
}

func TestUsageExit(t *testing.T) {
	both := usage.Sections{Tracked: true, Untracked: true}
	trackedOnly := usage.Sections{Tracked: true}
	var ee *cli.ExitError
	if err := usageExit(both, true); !errors.As(err, &ee) || ee.Code != cli.Problem {
		t.Fatalf("untracked spend shown: %v", err)
	}
	if err := usageExit(trackedOnly, true); err != nil {
		t.Fatalf("--tracked: %v", err)
	}
	if err := usageExit(both, false); err != nil {
		t.Fatalf("no untracked spend: %v", err)
	}
}
