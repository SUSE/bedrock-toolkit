package admincli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/usage"
)

type usageFlags struct {
	month              string
	months             int
	monthsSet          bool
	tracked, untracked bool
	by                 string
	product            []string
}

func (a *App) usageCmd() *cobra.Command {
	var f usageFlags
	cmd := &cobra.Command{
		Use:   "usage [email]",
		Short: "Where the Bedrock money went: spend per user, and spend outside personal roles",
		Long: `usage reads the cost export and shows spend in personal roles (tracked, with limits and pause
state) and outside them (untracked). Spend counts as tracked only when its owner tag is a user with a
budget. Callers under $0.01 are left out of the list.

  bedrock-admin usage                      this month so far, with a forecast
  bedrock-admin usage --month 2026-09      a past month, with limits and pause state from its snapshot
  bedrock-admin usage --months 3           one column per month
  bedrock-admin usage --by product         spend per product tag instead of per user
  bedrock-admin usage --by day             one row per day: active users, spend, a trend bar
  bedrock-admin usage --product web        only the users of product web (repeatable)
  bedrock-admin usage alice@acme.com       one person: per day, per model, per way of calling

Exit code 2 when there is untracked spend (never with --product, which leaves it out).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			email := ""
			if len(args) == 1 {
				email = args[0]
			}
			f.monthsSet = cmd.Flags().Changed("months")
			return a.usage(cmd.Context(), f, email)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.month, "month", "", "a past month, YYYY-MM (default: this month)")
	fl.IntVar(&f.months, "months", 0, "the last N months, this one included")
	fl.BoolVar(&f.tracked, "tracked", false, "show only spend in personal roles")
	fl.BoolVar(&f.untracked, "untracked", false, "show only spend outside personal roles")
	fl.StringVar(&f.by, "by", "user", "group spend by user, product or day")
	fl.StringSliceVar(&f.product, "product", nil, "only users of this product; repeat for more (untracked spend has no product, so it is left out)")
	return cmd
}

// usageMonths returns the months to report, oldest first.
func usageMonths(f usageFlags, thisMonth string) ([]string, error) {
	if f.month != "" && f.monthsSet {
		return nil, errors.New("use --month or --months, not both")
	}
	if f.monthsSet && (f.months < 1 || f.months > 24) {
		return nil, errors.New("--months must be 1-24")
	}
	if f.month != "" {
		if _, err := usage.MonthStart(f.month); err != nil {
			return nil, err
		}
		if f.month > thisMonth {
			return nil, fmt.Errorf("--month %s is in the future", f.month)
		}
		return []string{f.month}, nil
	}
	if f.months > 0 {
		start, _ := usage.MonthStart(thisMonth)
		var months []string
		for i := f.months - 1; i >= 0; i-- {
			months = append(months, start.AddDate(0, -i, 0).Format("2006-01"))
		}
		return months, nil
	}
	return []string{thisMonth}, nil
}

// checkUsageFlags rejects flag combinations that make no sense.
func checkUsageFlags(f usageFlags, email string) error {
	if f.by != "user" && f.by != "product" && f.by != "day" {
		return fmt.Errorf("--by must be user, product or day (got %q)", f.by)
	}
	if f.by == "day" && f.monthsSet {
		return errors.New("--by day shows one month; use --month, not --months")
	}
	if email != "" && f.by == "product" {
		return errors.New("<email> shows one person; leave out --by product")
	}
	if len(f.product) > 0 && f.untracked {
		return errors.New("untracked spend has no product; use --untracked without --product")
	}
	if email != "" && f.months > 0 {
		return errors.New("<email> shows one month; use it with --month, not --months")
	}
	return nil
}

func (a *App) usage(ctx context.Context, f usageFlags, email string) error {
	if err := checkUsageFlags(f, email); err != nil {
		return err
	}
	now := a.now().UTC()
	thisMonth := now.Format("2006-01")
	months, err := usageMonths(f, thisMonth)
	if err != nil {
		return err
	}
	sec := usage.Sections{Tracked: f.tracked || !f.untracked, Untracked: f.untracked || !f.tracked, ByProduct: f.by == "product"}

	cfg, err := a.load()
	if err != nil {
		return err
	}
	c, err := a.clients(ctx, cfg)
	if err != nil {
		return err
	}
	s3c := c.BucketS3(ctx, cfg.CostExport.Bucket)
	src := &usage.S3Source{S3: s3c, Bucket: cfg.CostExport.Bucket, Prefix: cfg.ExportPrefix()}

	var reports []usage.MonthReport
	var lastLines []usage.Line
	var lastUsers []usage.User
	for _, month := range months {
		var lines []usage.Line
		files, err := usage.ReadMonth(ctx, src, month, func(l usage.Line) { lines = append(lines, l) })
		if err != nil {
			return fmt.Errorf("reading the cost export: %w", err)
		}
		switch {
		case files > 0:
		case month == thisMonth:
			a.info("No cost export files under %s yet. The first ones arrive within 24 h of creating the export.\n", src.Where(month))
		default:
			a.info("No cost export files under %s (the export may not have existed in %s).\n", src.Where(month), month)
		}
		var us []usage.User
		note := ""
		if month == thisMonth {
			us, err = a.liveUsers(ctx, cfg, c)
		} else {
			us, note, err = pastUsers(ctx, cfg, s3c, month)
		}
		if err != nil {
			return err
		}
		r := usage.Summarize(month, cfg.ID, lines, us, now)
		r.Files, r.Note = files, note
		reports = append(reports, r)
		lastLines, lastUsers = lines, us
	}

	products := usage.Products(f.product)
	if err := products.Check(usage.KnownProducts(reports...)); err != nil {
		return err
	}
	for i := range reports {
		reports[i] = reports[i].Filter(products)
	}
	if email != "" && len(products) > 0 && !slices.ContainsFunc(reports[0].Users, func(u usage.UserRow) bool { return strings.EqualFold(u.Email, email) }) {
		return fmt.Errorf("%s is not a user of product %s", email, strings.Join(products, ", "))
	}

	if email != "" {
		d := usage.Person(reports[0], lastLines, email)
		if a.JSON {
			return cli.WriteJSON(a.Out, d)
		}
		d.Write(a.Out)
		return nil
	}
	var untracked bool
	if f.by == "day" {
		d := usage.Daily(reports[0], usage.DailySpend(cfg.ID, lastLines, lastUsers), now)
		if a.JSON {
			if err := cli.WriteJSON(a.Out, d); err != nil {
				return err
			}
		} else {
			d.Write(a.Out, sec)
		}
		return usageExit(sec, reports[0].HasUntracked())
	}
	if len(reports) > 1 {
		m := usage.Combine(reports)
		untracked = m.HasUntracked()
		if a.JSON {
			err = cli.WriteJSON(a.Out, m)
		} else {
			m.Write(a.Out, sec)
		}
	} else {
		untracked = reports[0].HasUntracked()
		if a.JSON {
			err = cli.WriteJSON(a.Out, reports[0])
		} else {
			reports[0].Write(a.Out, sec)
		}
	}
	if err != nil {
		return err
	}
	return usageExit(sec, untracked)
}

// usageExit is exit 2 when the report shows untracked spend; --tracked alone hides it and exits 0.
func usageExit(sec usage.Sections, untracked bool) error {
	if sec.Untracked && untracked {
		return cli.Exit(cli.Problem)
	}
	return nil
}

// liveUsers returns the configured users that have a budget, with their limit and pause state now.
func (a *App) liveUsers(ctx context.Context, cfg *config.Config, c *awsx.Clients) ([]usage.User, error) {
	env := a.usersEnv(ctx, cfg, c)
	resolved := cfg.Resolve()
	out := make([]*usage.User, len(resolved))
	errs := make([]error, len(resolved))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, r := range resolved {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			o, err := env.Observe(ctx, r)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", r.Email, err)
				return
			}
			if !o.BudgetExists {
				return
			}
			pct := o.ActionThreshold
			if pct == 0 {
				pct = r.PauseAtPercent
			}
			out[i] = &usage.User{Email: r.Email, Role: r.RoleName, Product: r.Product, LimitUSD: o.Limit, TriggerUSD: o.Limit * float64(pct) / 100, Pause: o.Pause, PauseKnown: true}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	var us []usage.User
	for _, u := range out {
		if u != nil {
			us = append(us, *u)
		}
	}
	return us, nil
}

// pastUsers returns a past month's users from its snapshot, or the file's users when there is none.
func pastUsers(ctx context.Context, cfg *config.Config, s3c usage.S3API, month string) ([]usage.User, string, error) {
	key := pause.SnapshotKey(cfg.CostExport.SnapshotPrefix, month)
	out, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(cfg.CostExport.Bucket), Key: aws.String(key)})
	if err != nil {
		if code := awsx.ErrorCode(err); code != "NoSuchKey" && code != "NotFound" && code != "AccessDenied" {
			return nil, "", fmt.Errorf("reading the snapshot s3://%s/%s: %w", cfg.CostExport.Bucket, key, err)
		}
		var us []usage.User
		for _, r := range cfg.Resolve() {
			us = append(us, usage.User{Email: r.Email, Role: r.RoleName, Product: r.Product, LimitUSD: r.LimitUSD, TriggerUSD: r.LimitUSD * float64(r.PauseAtPercent) / 100})
		}
		return us, "no snapshot; users and limits from " + cfgName(cfg) + ", pause state unknown", nil
	}
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, "", err
	}
	snap, err := pause.ParseSnapshot(b)
	if err != nil {
		return nil, "", fmt.Errorf("snapshot s3://%s/%s: %w", cfg.CostExport.Bucket, key, err)
	}
	us := usage.UsersFromSnapshot(snap)
	fillProducts(us, cfg)
	return us, "final; limits and pause state from the snapshot of " + snap.TakenAt.UTC().Format("Jan 2"), nil
}

func cfgName(cfg *config.Config) string {
	if cfg.Path != "" {
		return cfg.Path
	}
	return "bedrock.yaml"
}

// fillProducts gives users from a snapshot without products (taken before snapshots had them) the
// product from the file.
func fillProducts(us []usage.User, cfg *config.Config) {
	byEmail := map[string]string{}
	for _, r := range cfg.Resolve() {
		byEmail[strings.ToLower(r.Email)] = r.Product
	}
	for i := range us {
		if us[i].Product == "" {
			us[i].Product = byEmail[strings.ToLower(us[i].Email)]
		}
	}
}
