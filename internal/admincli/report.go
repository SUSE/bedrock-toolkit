package admincli

import (
	"context"
	"errors"
	"fmt"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
)

// sendReport writes the setup's report to the central bucket when report: is set. A failure is a
// warning: the report must never fail the command that triggered it.
func (a *App) sendReport(ctx context.Context, cfg *config.Config, c *awsx.Clients, status, source string) {
	if !cfg.Report.Enabled() {
		return
	}
	var us []fleet.User
	if status == fleet.StatusActive {
		var err error
		us, err = fleet.Collect(ctx, a.usersEnv(ctx, cfg, c).Pause, c.IAM)
		if err != nil {
			_, _ = fmt.Fprintf(a.Err, "warning: central report not sent: %v\n", err)
			return
		}
	}
	r := fleet.Build(fleet.SetupFrom(cfg, a.Version), c.Account, status, source, us, a.Now())
	if err := fleet.Send(ctx, c.S3In(cfg.Report.Region), cfg.Report.Bucket, r); err != nil {
		_, _ = fmt.Fprintf(a.Err, "warning: central report not sent: %v\n", shortAWSError(err.Error()))
		return
	}
	what := "counts only"
	if cfg.Report.IncludeEmails {
		what = "with emails"
	}
	a.info("Sent the central report to s3://%s/%s (%s).\n", cfg.Report.Bucket, fleet.Key(c.Account, cfg.ID), what)
}

// applied reports whether apply changed or checked everything without a failure (pending items such
// as Step 1.7 still count).
func applied(err error) bool {
	var ee *cli.ExitError
	return err == nil || (errors.As(err, &ee) && ee.Code == cli.Problem)
}
