package account

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	budgettypes "github.com/aws/aws-sdk-go-v2/service/budgets/types"

	"github.com/innobead/bedrock-toolkit/internal/policy"
	"github.com/innobead/bedrock-toolkit/internal/usage"
)

func TestTagBudgetWithSpend(t *testing.T) {
	budget := func(name, key, actual string) budgettypes.Budget {
		b := budgettypes.Budget{BudgetName: aws.String(name)}
		if key != "" {
			b.FilterExpression = &budgettypes.Expression{Tags: &budgettypes.TagValues{Key: aws.String(key)}}
		}
		if actual != "" {
			b.CalculatedSpend = &budgettypes.CalculatedSpend{ActualSpend: &budgettypes.Spend{Amount: aws.String(actual), Unit: aws.String("USD")}}
		}
		return b
	}
	none := []budgettypes.Budget{
		budget("no-filter", "", "5"),
		budget("other-tag", "team", "5"),
		budget("zero", policy.CostAllocationOwnerTag, "0"),
		budget("no-spend", policy.CostAllocationOwnerTag, ""),
	}
	if got := tagBudgetWithSpend(none); got != "" {
		t.Fatalf("got %q, want none", got)
	}
	if got := tagBudgetWithSpend(append(none, budget("bedrock-user-a", policy.CostAllocationOwnerTag, "0.42"))); got != "bedrock-user-a" {
		t.Fatalf("got %q", got)
	}
}

func TestExportHasOwnerTag(t *testing.T) {
	dir := t.TempDir()
	d := filepath.Join(dir, "data", "BILLING_PERIOD=2026-10")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../tests/fixtures/cur-this-month.parquet")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "part-0.parquet"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	src := usage.DirSource{Dir: dir}
	if found, err := exportHasOwnerTag(context.Background(), src, "2026-10"); err != nil || !found {
		t.Fatalf("2026-10: found %v, err %v", found, err)
	}
	if found, err := exportHasOwnerTag(context.Background(), src, "2026-09"); err != nil || found {
		t.Fatalf("2026-09 (no files): found %v, err %v", found, err)
	}
}
