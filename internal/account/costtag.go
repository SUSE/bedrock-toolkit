package account

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	btypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"

	"github.com/innobead/bedrock-toolkit/internal/plan"
	"github.com/innobead/bedrock-toolkit/internal/policy"
	"github.com/innobead/bedrock-toolkit/internal/usage"
)

// costTagUnreadable is Step 1.6 when this account can't read cost allocation tags (only the
// management account can). Budget spend or owner tags in the cost export show the tag is active;
// without either, it's a warning, not a change: the admin can't check or fix it, but budgets count
// nothing until it's on.
func (e *Env) costTagUnreadable(ctx context.Context, it *plan.Item) {
	if why := e.costTagEvidence(ctx); why != "" {
		it.Op, it.Status = plan.OK, "active ("+why+")"
		return
	}
	it.Op, it.Status = plan.Info, "can't check from this account (only the management account can); budgets count spend only once it's active"
	it.Warnings = []string{"ask your org admin to activate " + policy.CostAllocationOwnerTag + " in the management account (Billing → Cost allocation tags)"}
}

// costTagEvidence says why the owner tag is known to be active, or "" if nothing shows it.
func (e *Env) costTagEvidence(ctx context.Context) string {
	var all []btypes.Budget
	p := budgets.NewDescribeBudgetsPaginator(e.C.Budgets, &budgets.DescribeBudgetsInput{AccountId: aws.String(e.C.Account)})
	for p.HasMorePages() {
		r, err := p.NextPage(ctx)
		if err != nil {
			break
		}
		all = append(all, r.Budgets...)
	}
	if name := tagBudgetWithSpend(all); name != "" {
		return "budget " + name + " counts spend"
	}
	if e.Cfg.CostExport.Bucket == "" {
		return ""
	}
	src := &usage.S3Source{S3: e.C.BucketS3(ctx, e.Cfg.CostExport.Bucket), Bucket: e.Cfg.CostExport.Bucket, Prefix: e.Cfg.ExportPrefix()}
	now := time.Now().UTC()
	for _, month := range []string{now.Format("2006-01"), now.AddDate(0, -1, 0).Format("2006-01")} {
		if found, _ := exportHasOwnerTag(ctx, src, month); found {
			return "the cost export of " + month + " has " + policy.CostAllocationOwnerTag
		}
	}
	return ""
}

// tagBudgetWithSpend returns a budget that filters on the owner tag and has spend this month. Budgets
// count spend only for active tags, so one is enough to show the tag is active.
func tagBudgetWithSpend(bs []btypes.Budget) string {
	for i := range bs {
		b := &bs[i]
		f := b.FilterExpression
		if f == nil || f.Tags == nil || aws.ToString(f.Tags.Key) != policy.CostAllocationOwnerTag {
			continue
		}
		if b.CalculatedSpend == nil || b.CalculatedSpend.ActualSpend == nil {
			continue
		}
		if v, err := strconv.ParseFloat(aws.ToString(b.CalculatedSpend.ActualSpend.Amount), 64); err == nil && v > 0 {
			return aws.ToString(b.BudgetName)
		}
	}
	return ""
}

// exportHasOwnerTag reports whether a month's cost export has a row with the owner tag. The export
// carries only active cost allocation tags.
func exportHasOwnerTag(ctx context.Context, src usage.Source, month string) (bool, error) {
	found := false
	_, err := usage.ReadMonth(ctx, src, month, func(l usage.Line) {
		found = found || l.Owner != ""
	})
	return found, err
}
