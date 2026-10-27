package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	ltypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/iamrole"
	"github.com/SUSE/bedrock-toolkit/internal/lambdacode"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

// Environment variables of the monthly Lambda.
const (
	EnvSnapshotBucket = "SNAPSHOT_BUCKET"
	EnvSnapshotPrefix = "SNAPSHOT_PREFIX"
	EnvDenyPolicyArn  = "DENY_POLICY_ARN"
	EnvID             = "BEDROCK_ID"
	// EnvExportPrefix is the cost export's <prefix>/<name> in SNAPSHOT_BUCKET, read by the daily run.
	EnvExportPrefix = "EXPORT_PREFIX"
	// EnvReportSetup is the JSON fleet.Setup; set only when central reporting is on.
	EnvReportSetup = "REPORT_SETUP"
)

const scheduleIDPrefix = "bedrock-admin:id="

// LambdaMemoryMB is enough to read a month of the hourly cost export.
const LambdaMemoryMB = 1024

// LambdaRoleSpec and SchedulerRoleSpec are the Step 1.4 roles.
func LambdaRoleSpec(region, account, id, bucket, snapshotPrefix, exportPrefix, reportBucket string) iamrole.Spec {
	return iamrole.Spec{Name: policy.LambdaRole(id), Path: policy.Path,
		Description: "Runs the monthly Bedrock unpause Lambda",
		Trust:       policy.ServiceTrust("lambda.amazonaws.com", account), InlineName: policy.LambdaPolicy,
		Inline: policy.LambdaPermissions(region, account, id, bucket, snapshotPrefix, exportPrefix, reportBucket)}
}

func SchedulerRoleSpec(region, account, id string) iamrole.Spec {
	return iamrole.Spec{Name: policy.SchedulerRole(id), Path: policy.Path,
		Description: "Lets EventBridge Scheduler invoke the monthly Bedrock unpause Lambda",
		Trust:       policy.ServiceTrust("scheduler.amazonaws.com", account), InlineName: policy.SchedulerPolicy,
		Inline: policy.SchedulerPermissions(region, account, id)}
}

func (e *Env) lambdaEnv() map[string]string {
	env := map[string]string{
		EnvSnapshotBucket: e.Cfg.CostExport.Bucket,
		EnvSnapshotPrefix: e.Cfg.CostExport.SnapshotPrefix,
		EnvDenyPolicyArn:  policy.DenyPolicyArn(e.C.Account, e.Cfg.ID),
		EnvID:             e.Cfg.ID,
		EnvExportPrefix:   e.Cfg.ExportPrefix(),
	}
	if e.Cfg.Report.Enabled() {
		b, _ := json.Marshal(fleet.SetupFrom(e.Cfg, e.Version))
		env[EnvReportSetup] = string(b)
	}
	return env
}

// envChange describes how the Lambda's environment differs from want.
func envChange(have, want map[string]string) string {
	if maps.Equal(have, want) {
		return ""
	}
	for k := range mergeKeys(have, want) {
		if k != EnvReportSetup && have[k] != want[k] {
			return "settings changed outside the file"
		}
	}
	switch {
	case have[EnvReportSetup] == "":
		return "turn on the central report"
	case want[EnvReportSetup] == "":
		return "turn off the central report"
	}
	return "update the central report settings"
}

func mergeKeys(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func (e *Env) monthlyLambda(ctx context.Context) ([]*plan.Item, error) {
	region, account := e.Cfg.Region, e.C.Account
	lr, err := e.roleItem(ctx, "1.4", "Lambda role "+policy.LambdaRole(e.Cfg.ID),
		LambdaRoleSpec(region, account, e.Cfg.ID, e.Cfg.CostExport.Bucket, e.Cfg.CostExport.SnapshotPrefix, e.Cfg.ExportPrefix(), e.Cfg.Report.Bucket))
	if err != nil {
		return nil, err
	}
	sr, err := e.roleItem(ctx, "1.4", "scheduler role "+policy.SchedulerRole(e.Cfg.ID), SchedulerRoleSpec(region, account, e.Cfg.ID))
	if err != nil {
		return nil, err
	}
	fn, err := e.function(ctx)
	if err != nil {
		return nil, err
	}
	sch, err := e.schedule(ctx, e.monthlySchedule())
	if err != nil {
		return nil, err
	}
	daily, err := e.schedule(ctx, e.dailySchedule())
	if err != nil {
		return nil, err
	}
	old, err := e.schedule(ctx, e.oldReportSchedule())
	if err != nil {
		return nil, err
	}
	out := []*plan.Item{lr, sr, fn, sch, daily}
	if old != nil {
		out = append(out, old)
	}
	return out, nil
}

func (e *Env) function(ctx context.Context) (*plan.Item, error) {
	it := e.item("1.4", "Lambda "+policy.LambdaName(e.Cfg.ID))
	zip, zerr := lambdacode.Zip()
	got, err := e.C.Lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(policy.LambdaName(e.Cfg.ID))})
	if err != nil && !awsx.IsNotFound(err) {
		return nil, fmt.Errorf("reading Lambda %s: %w", policy.LambdaName(e.Cfg.ID), err)
	}
	env := e.lambdaEnv()
	if err != nil {
		if zerr != nil {
			it.Op, it.Status = plan.Conflict, zerr.Error()
			return it, nil
		}
		it.Op, it.Details = plan.Create, []string{"create (runs 06:00 UTC on the 1st)"}
		it.Apply = func(ctx context.Context) error {
			in := &lambda.CreateFunctionInput{FunctionName: aws.String(policy.LambdaName(e.Cfg.ID)),
				Role:    aws.String(fmt.Sprintf("arn:aws:iam::%s:role%s%s", e.C.Account, policy.Path, policy.LambdaRole(e.Cfg.ID))),
				Runtime: ltypes.RuntimeProvidedal2023, Handler: aws.String("bootstrap"),
				Architectures: []ltypes.Architecture{ltypes.ArchitectureArm64},
				Code:          &ltypes.FunctionCode{ZipFile: zip}, Timeout: aws.Int32(300), MemorySize: aws.Int32(LambdaMemoryMB),
				Description: aws.String(lambdaDescription),
				Environment: &ltypes.Environment{Variables: env}, Tags: e.tags()}
			// A new role can't be assumed by Lambda for a few seconds.
			err := iamrole.Retry(ctx, 90*time.Second, func(err error) bool {
				var ip *ltypes.InvalidParameterValueException
				return errors.As(err, &ip) && strings.Contains(err.Error(), "assume")
			}, func() error { _, err := e.C.Lambda.CreateFunction(ctx, in); return err })
			if err != nil {
				return fmt.Errorf("creating Lambda %s: %w", policy.LambdaName(e.Cfg.ID), err)
			}
			return lambda.NewFunctionActiveV2Waiter(e.C.Lambda).Wait(ctx,
				&lambda.GetFunctionInput{FunctionName: aws.String(policy.LambdaName(e.Cfg.ID))}, 2*time.Minute)
		}
		return it, nil
	}
	if !e.owned(it, got.Tags[policy.IDTag]) {
		return it, nil
	}
	if zerr != nil {
		it.Op, it.Status = plan.Conflict, zerr.Error()
		return it, nil
	}
	c := got.Configuration
	codeChanged := aws.ToString(c.CodeSha256) != lambdacode.Sha256(zip)
	var have map[string]string
	if c.Environment != nil {
		have = c.Environment.Variables
	}
	change := envChange(have, env)
	if aws.ToInt32(c.MemorySize) != LambdaMemoryMB {
		change = strings.TrimPrefix(change+fmt.Sprintf("; memory %d → %d MB", aws.ToInt32(c.MemorySize), LambdaMemoryMB), "; ")
	}
	envChanged := change != ""
	if codeChanged {
		it.Details = append(it.Details, "update code (deployed code differs from this bedrock-admin's)")
	}
	if envChanged {
		it.Details = append(it.Details, change)
	}
	if codeChanged || envChanged {
		it.Op = plan.Update
		it.Apply = func(ctx context.Context) error {
			w := lambda.NewFunctionUpdatedV2Waiter(e.C.Lambda)
			get := &lambda.GetFunctionInput{FunctionName: aws.String(policy.LambdaName(e.Cfg.ID))}
			if codeChanged {
				if _, err := e.C.Lambda.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
					FunctionName: aws.String(policy.LambdaName(e.Cfg.ID)), ZipFile: zip,
					Architectures: []ltypes.Architecture{ltypes.ArchitectureArm64}}); err != nil {
					return fmt.Errorf("updating Lambda code: %w", err)
				}
				if err := w.Wait(ctx, get, 2*time.Minute); err != nil {
					return err
				}
			}
			if envChanged {
				if _, err := e.C.Lambda.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
					FunctionName: aws.String(policy.LambdaName(e.Cfg.ID)), Environment: &ltypes.Environment{Variables: env},
					MemorySize: aws.Int32(LambdaMemoryMB), Description: aws.String(lambdaDescription)}); err != nil {
					return fmt.Errorf("updating Lambda settings: %w", err)
				}
				return w.Wait(ctx, get, 2*time.Minute)
			}
			return nil
		}
	}
	return it, nil
}

// ScheduleID reads the bedrock-admin id from a schedule's description (schedules have no tags).
func ScheduleID(description string) string {
	for _, f := range strings.Fields(description) {
		if v, ok := strings.CutPrefix(strings.TrimPrefix(f, "("), scheduleIDPrefix); ok {
			return strings.TrimSuffix(v, ")")
		}
	}
	return ""
}

// scheduleSpec is one EventBridge Scheduler schedule of the Lambda.
type scheduleSpec struct {
	Name, Expression, Description, Input string
	// Want is false when the schedule must not exist; Gone says why.
	Want bool
	Gone string
	// When is the human form of Expression for the plan.
	When string
}

func (e *Env) monthlySchedule() scheduleSpec {
	return scheduleSpec{Name: policy.ScheduleName(e.Cfg.ID), Expression: policy.ScheduleExpression, Want: true,
		Description: fmt.Sprintf("Runs %s at 06:00 UTC on the 1st (%s%s)", policy.LambdaName(e.Cfg.ID), scheduleIDPrefix, e.Cfg.ID),
		When:        policy.ScheduleExpression + " UTC"}
}

const lambdaDescription = "Unpauses and re-arms Bedrock users on the 1st and saves the monthly snapshot; daily, writes each user's usage file and the central report (bedrock-admin)"

func (e *Env) dailySchedule() scheduleSpec {
	when := "daily 07:00 UTC, users' usage files"
	if e.Cfg.Report.Enabled() {
		when += " and central report to s3://" + e.Cfg.Report.Bucket
	}
	return scheduleSpec{Name: policy.DailyScheduleName(e.Cfg.ID), Expression: policy.DailyScheduleExpression,
		Input: policy.DailyInput, Want: true, When: when,
		Description: fmt.Sprintf("Runs %s daily at 07:00 UTC: usage files and central report (%s%s)", policy.LambdaName(e.Cfg.ID), scheduleIDPrefix, e.Cfg.ID)}
}

// oldReportSchedule is bedrock-admin 1.1's report-only schedule, which the daily schedule replaces.
func (e *Env) oldReportSchedule() scheduleSpec {
	return scheduleSpec{Name: policy.ReportScheduleName(e.Cfg.ID), Gone: "replaced by " + policy.DailyScheduleName(e.Cfg.ID)}
}

// schedule returns nil when the schedule is neither wanted nor present.
func (e *Env) schedule(ctx context.Context, s scheduleSpec) (*plan.Item, error) {
	it := e.item("1.4", "schedule "+s.Name)
	target := &schtypes.Target{Arn: aws.String(policy.LambdaArn(e.Cfg.Region, e.C.Account, e.Cfg.ID)),
		RoleArn: aws.String(fmt.Sprintf("arn:aws:iam::%s:role%s%s", e.C.Account, policy.Path, policy.SchedulerRole(e.Cfg.ID)))}
	if s.Input != "" {
		target.Input = aws.String(s.Input)
	}
	got, err := e.C.Scheduler.GetSchedule(ctx, &scheduler.GetScheduleInput{Name: aws.String(s.Name)})
	if err != nil && !awsx.IsNotFound(err) {
		return nil, fmt.Errorf("reading schedule %s: %w", s.Name, err)
	}
	if err != nil {
		if !s.Want {
			return nil, nil
		}
		it.Op, it.Details = plan.Create, []string{"create (" + s.When + ")"}
		it.Apply = func(ctx context.Context) error {
			in := &scheduler.CreateScheduleInput{Name: aws.String(s.Name),
				ScheduleExpression: aws.String(s.Expression), ScheduleExpressionTimezone: aws.String("UTC"),
				FlexibleTimeWindow: &schtypes.FlexibleTimeWindow{Mode: schtypes.FlexibleTimeWindowModeOff},
				Target:             target, Description: aws.String(s.Description), State: schtypes.ScheduleStateEnabled}
			err := iamrole.Retry(ctx, 60*time.Second, func(err error) bool { return awsx.ErrorCode(err) == "ValidationException" },
				func() error { _, err := e.C.Scheduler.CreateSchedule(ctx, in); return err })
			if err != nil {
				return fmt.Errorf("creating schedule %s: %w", s.Name, err)
			}
			return nil
		}
		return it, nil
	}
	if !e.owned(it, ScheduleID(aws.ToString(got.Description))) {
		return it, nil
	}
	if !s.Want {
		it.Op, it.Details = plan.Delete, []string{"remove (" + s.Gone + ")"}
		it.Apply = func(ctx context.Context) error {
			_, err := e.C.Scheduler.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(s.Name)})
			return err
		}
		return it, nil
	}
	var d []string
	if aws.ToString(got.ScheduleExpression) != s.Expression || aws.ToString(got.ScheduleExpressionTimezone) != "UTC" {
		d = append(d, fmt.Sprintf("schedule %s %s → %s UTC", aws.ToString(got.ScheduleExpression),
			aws.ToString(got.ScheduleExpressionTimezone), s.Expression))
	}
	if got.State != schtypes.ScheduleStateEnabled {
		d = append(d, "disabled → enabled")
	}
	if got.Target == nil || aws.ToString(got.Target.Arn) != aws.ToString(target.Arn) || aws.ToString(got.Target.RoleArn) != aws.ToString(target.RoleArn) ||
		aws.ToString(got.Target.Input) != aws.ToString(target.Input) {
		d = append(d, "target changed outside the file")
	}
	if len(d) > 0 {
		it.Op, it.Details = plan.Update, d
		it.Apply = func(ctx context.Context) error {
			_, err := e.C.Scheduler.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{Name: aws.String(s.Name),
				ScheduleExpression: aws.String(s.Expression), ScheduleExpressionTimezone: aws.String("UTC"),
				FlexibleTimeWindow: &schtypes.FlexibleTimeWindow{Mode: schtypes.FlexibleTimeWindowModeOff},
				Target:             target, Description: aws.String(s.Description), State: schtypes.ScheduleStateEnabled})
			return err
		}
	}
	return it, nil
}

// DeployedReport is the report: setting of the id's deployed Lambda; zero when reporting is off or
// the Lambda is missing.
func DeployedReport(ctx context.Context, l interface {
	GetFunctionConfiguration(context.Context, *lambda.GetFunctionConfigurationInput, ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error)
}, id string) (config.Report, error) {
	f, err := l.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(policy.LambdaName(id))})
	if awsx.IsNotFound(err) {
		return config.Report{}, nil
	}
	if err != nil {
		return config.Report{}, fmt.Errorf("reading Lambda %s: %w", policy.LambdaName(id), err)
	}
	if f.Environment == nil || f.Environment.Variables[EnvReportSetup] == "" {
		return config.Report{}, nil
	}
	var s fleet.Setup
	if err := json.Unmarshal([]byte(f.Environment.Variables[EnvReportSetup]), &s); err != nil {
		return config.Report{}, fmt.Errorf("reading %s of Lambda %s: %w", EnvReportSetup, policy.LambdaName(id), err)
	}
	return config.Report{Bucket: s.Bucket, Region: s.BucketRegion, IncludeEmails: s.IncludeEmails}, nil
}
