package admincli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	stypes "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/spf13/cobra"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/cli"
	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/plan"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
)

var (
	orgIDRe   = regexp.MustCompile(`^o-[a-z0-9]{10,32}$`)
	accountRe = regexp.MustCompile(`^\d{12}$`)
)

func (a *App) fleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "For the central admin only: create the report bucket and read every setup's report",
		Long: `fleet is for the central admin who collects reports from many setups (accounts and ids).

  bedrock-admin fleet init     create the bucket other setups report to (bedrock-reports-<account>)
  bedrock-admin fleet report   show the latest report of every setup

Admins of the setups then add the report: section that fleet init prints to their bedrock.yaml and
run bedrock-admin apply. They can write their own account's reports only; they can't read any.
Neither command needs a bedrock.yaml; they use --profile (or AWS_PROFILE) of the central account.
With -f, they use the file's profile, and its region when the profile has none.
fleet init tags the bucket, so later both commands find it without --bucket or --region.`,
	}
	cmd.AddCommand(a.fleetInitCmd(), a.fleetReportCmd())
	return cmd
}

// fleetClients connects to the central account. Fleet commands need no bedrock.yaml, but one given
// with -f supplies the profile and, when the profile has none, the region. The clients use
// us-east-1 when nothing gives a region; fleetBucket then picks the bucket's own region.
func (a *App) fleetClients(ctx context.Context, region string) (*awsx.Clients, error) {
	if a.File != "" {
		cfg, err := a.load()
		if err != nil {
			return nil, err
		}
		a.fleetCfg = cfg
	}
	return a.connect(ctx, a.fleetCfg, a.fleetClientRegion(ctx, region), "")
}

// fleetClientRegion is --region, else fleetRegion, else us-east-1.
func (a *App) fleetClientRegion(ctx context.Context, region string) string {
	if region == "" {
		region = a.fleetRegion(ctx)
	}
	if region == "" {
		region = "us-east-1"
	}
	return region
}

// fleetRegion is the profile's region, else the region of the file given with -f.
func (a *App) fleetRegion(ctx context.Context) string {
	if r := a.profileRegion(ctx, a.fleetCfg); r != "" {
		return r
	}
	if a.fleetCfg != nil {
		return a.fleetCfg.Region
	}
	return ""
}

// reportBucket is a central report bucket and its region.
type reportBucket struct{ Name, Region string }

// findReportBuckets lists the account's buckets that fleet init tagged as report buckets.
func findReportBuckets(ctx context.Context, c *awsx.Clients) ([]reportBucket, error) {
	var all []reportBucket
	pg := s3.NewListBucketsPaginator(c.S3, &s3.ListBucketsInput{MaxBuckets: aws.Int32(1000)})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing the account's buckets: %w", err)
		}
		for _, b := range page.Buckets {
			all = append(all, reportBucket{aws.ToString(b.Name), aws.ToString(b.BucketRegion)})
		}
	}
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		found []reportBucket
		first error
	)
	sem := make(chan struct{}, 8)
	for _, b := range all {
		wg.Add(1)
		sem <- struct{}{}
		go func(b reportBucket) {
			defer func() { <-sem; wg.Done() }()
			if b.Region == "" {
				r, ok, err := c.BucketRegion(ctx, b.Name)
				if err != nil || !ok {
					return
				}
				b.Region = r
			}
			t, err := c.S3In(b.Region).GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(b.Name)})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case awsx.IsNotFound(err) || awsx.IsAccessDenied(err):
			case err != nil:
				if first == nil {
					first = fmt.Errorf("reading the tags of %s: %w", b.Name, err)
				}
			case hasTag(t.TagSet, policy.ReportBucketTag):
				found = append(found, b)
			}
		}(b)
	}
	wg.Wait()
	if first != nil {
		return nil, first
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })
	return found, nil
}

func hasTag(tags []stypes.Tag, key string) bool {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return true
		}
	}
	return false
}

// DefaultReportBucket is the bucket fleet init creates without --bucket.
func DefaultReportBucket(account string) string { return "bedrock-reports-" + account }

// pickReportBucket chooses the bucket when --bucket isn't given.
func pickReportBucket(found []reportBucket, account string) (reportBucket, error) {
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return reportBucket{}, fmt.Errorf("no report bucket in account %s: create one with fleet init, or pass --bucket", account)
	}
	var names []string
	for _, b := range found {
		names = append(names, b.Name+" ("+b.Region+")")
	}
	return reportBucket{}, fmt.Errorf("account %s has %d report buckets: %s; pass --bucket", account, len(found), strings.Join(names, ", "))
}

// fleetBucket resolves --bucket and --region: without --bucket it finds the tagged bucket, or, for
// fleet init (create) when there is none, uses DefaultReportBucket; without --region it uses the
// bucket's region, or, for a new bucket, fleetRegion. exists reports whether the bucket exists.
func (a *App) fleetBucket(ctx context.Context, c *awsx.Clients, bucket, region string, create bool) (b reportBucket, exists bool, err error) {
	if bucket == "" {
		found, err := findReportBuckets(ctx, c)
		if err != nil {
			return b, false, err
		}
		if len(found) == 0 && create {
			bucket = DefaultReportBucket(c.Account)
		} else if b, err = pickReportBucket(found, c.Account); err != nil {
			return b, false, err
		}
		if bucket == "" {
			if region != "" && region != b.Region {
				return b, false, fmt.Errorf("bucket %s is in %s, not %s", b.Name, b.Region, region)
			}
			a.info("Using report bucket %s in %s.\n", b.Name, b.Region)
			return b, true, nil
		}
	}
	got, exists, err := c.BucketRegion(ctx, bucket)
	switch {
	case awsx.IsAccessDenied(err):
		return b, false, fmt.Errorf("bucket %s belongs to another account; pick another name with --bucket", bucket)
	case err != nil:
		return b, false, fmt.Errorf("reading bucket %s: %w", bucket, err)
	case exists && region != "" && got != region:
		return b, false, fmt.Errorf("bucket %s is in %s; pass --region %s", bucket, got, got)
	case exists:
		return reportBucket{bucket, got}, true, nil
	}
	if region == "" {
		region = a.fleetRegion(ctx)
	}
	if region == "" {
		return b, false, errors.New("no region: the profile has none; pass --region")
	}
	return reportBucket{bucket, region}, false, nil
}

func (a *App) fleetInitCmd() *cobra.Command {
	var bucket, region, orgID string
	var accounts []string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create or fix the central report bucket",
		Long: `fleet init creates the central report bucket in the profile's account, or fixes its settings:
public access blocked, objects owned by the bucket owner, versioning on (every report is kept),
default encryption (SSE-S3) and a bucket policy. The policy lets each account of your AWS
organization (or of --accounts) write reports under its own account ID only, and nobody else
read them. Nothing expires: delete old versions yourself if you want to.

It tags the bucket bedrock-admin:report-bucket, so without --bucket fleet init and fleet report
find it (when the account has exactly one). Without --bucket and no tagged bucket, fleet init
creates bedrock-reports-<account>. --org-id defaults to the profile's organization. It asks before changing anything unless --yes
is given; --dry-run only shows what it would do.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if orgID != "" && len(accounts) > 0 {
				return errors.New("pass --org-id or --accounts, not both")
			}
			if orgID != "" && !orgIDRe.MatchString(orgID) {
				return fmt.Errorf("--org-id: not an organization ID (o-...): %q", orgID)
			}
			for _, acc := range accounts {
				if !accountRe.MatchString(acc) {
					return fmt.Errorf("--accounts: not a 12-digit account ID: %q", acc)
				}
			}
			c, err := a.fleetClients(ctx, region)
			if err != nil {
				return err
			}
			rb, exists, err := a.fleetBucket(ctx, c, bucket, region, true)
			if err != nil {
				return err
			}
			if orgID == "" && len(accounts) == 0 {
				o, err := organizations.NewFromConfig(c.Cfg).DescribeOrganization(ctx, &organizations.DescribeOrganizationInput{})
				if err != nil {
					return fmt.Errorf("reading the profile's organization (pass --org-id or --accounts): %w", err)
				}
				orgID = aws.ToString(o.Organization.Id)
			}
			items, err := reportBucketItems(ctx, c, rb, exists, policy.ReportBucketPolicy(rb.Name, orgID, accounts))
			if err != nil {
				return err
			}
			who := "accounts of organization " + orgID
			if len(accounts) > 0 {
				who = "accounts " + strings.Join(accounts, ", ")
			}
			p := &plan.Plan{Header: fmt.Sprintf("Central report bucket s3://%s in %s (account %s), written by %s", rb.Name, rb.Region, c.Account, who),
				Items: items}
			if a.JSON {
				if err := plan.RenderJSON(a.Out, p); err != nil {
					return err
				}
			} else {
				plan.Render(a.Out, p, a.Quiet)
			}
			if a.DryRun {
				if p.Changes() > 0 {
					return cli.Exit(cli.Problem)
				}
				return nil
			}
			if p.Changes() > 0 && !a.Yes {
				if !cli.IsTerminal(a.In) {
					return errors.New("fleet init needs --yes when not run from a terminal")
				}
				ok, err := cli.NewPrompter(a.In, a.Err).Confirm("Make these changes?")
				if err != nil {
					return err
				}
				if !ok {
					return errors.New("cancelled")
				}
			}
			for _, it := range items {
				if it.Apply == nil {
					continue
				}
				a.info("- %s\n", it.Target)
				if err := it.Apply(ctx); err != nil {
					return err
				}
			}
			a.info("\nAdmins of the setups add this to their bedrock.yaml, then run bedrock-admin apply:\n\n"+
				"  report:\n    bucket: %s\n    region: %s\n\n"+
				"(add include_emails: true under it to also send users' emails)\n", rb.Name, rb.Region)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&bucket, "bucket", "", "name of the central report bucket (default: the one fleet init tagged, else bedrock-reports-<account>)")
	fl.StringVar(&region, "region", "", "region of a new bucket (default: the profile's; an existing bucket's own)")
	fl.StringVar(&orgID, "org-id", "", "AWS organization whose accounts may report (default: the profile's)")
	fl.StringSliceVar(&accounts, "accounts", nil, "account IDs that may report, instead of an organization")
	return cmd
}

// reportBucketItems returns the plan for the central report bucket.
func reportBucketItems(ctx context.Context, c *awsx.Clients, rb reportBucket, exists bool, pol string) ([]*plan.Item, error) {
	bucket, region := rb.Name, rb.Region
	b := &plan.Item{Section: "account", Target: "bucket " + bucket, Op: plan.OK}
	s := &plan.Item{Section: "account", Target: "settings of " + bucket, Op: plan.OK}
	pi := &plan.Item{Section: "account", Target: "policy of " + bucket, Op: plan.OK}
	s3c := c.S3In(region)
	tagged := false
	if exists {
		if _, err := s3c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket), ExpectedBucketOwner: aws.String(c.Account)}); err != nil {
			return nil, fmt.Errorf("bucket %s isn't in account %s; pick another name: %w", bucket, c.Account, err)
		}
		t, err := s3c.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucket)})
		if err != nil && !awsx.IsNotFound(err) {
			return nil, fmt.Errorf("reading the tags of %s: %w", bucket, err)
		}
		if err == nil {
			for _, tag := range t.TagSet {
				if aws.ToString(tag.Key) == policy.IDTag {
					return nil, fmt.Errorf("bucket %s belongs to setup %s (its cost export); pick another name", bucket, aws.ToString(tag.Value))
				}
			}
			tagged = hasTag(t.TagSet, policy.ReportBucketTag)
		}
	}
	if !exists {
		b.Op, b.Details = plan.Create, []string{"create in " + region}
		b.Apply = func(ctx context.Context) error {
			in := &s3.CreateBucketInput{Bucket: aws.String(bucket), ObjectOwnership: stypes.ObjectOwnershipBucketOwnerEnforced}
			if region != "us-east-1" {
				in.CreateBucketConfiguration = &stypes.CreateBucketConfiguration{LocationConstraint: stypes.BucketLocationConstraint(region)}
			}
			if _, err := s3c.CreateBucket(ctx, in); err != nil {
				return fmt.Errorf("creating bucket %s: %w", bucket, err)
			}
			return nil
		}
		s.Op, s.Details = plan.Create, []string{"block public access, bucket owner owns objects, versioning, SSE-S3, tag " + policy.ReportBucketTag}
		s.Apply = func(ctx context.Context) error {
			return fixReportBucket(ctx, s3c, bucket, true, true, true, true, true)
		}
		pi.Op, pi.Details = plan.Create, []string{"set"}
		pi.Apply = func(ctx context.Context) error { return putPolicy(ctx, s3c, bucket, pol) }
		return []*plan.Item{b, s, pi}, nil
	}

	var fixPAB, fixOwner, fixVersioning, fixSSE bool
	pab, err := s3c.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket)})
	if err != nil && !awsx.IsNotFound(err) {
		return nil, fmt.Errorf("reading the public access block of %s: %w", bucket, err)
	}
	if err != nil || !allBlocked(pab.PublicAccessBlockConfiguration) {
		fixPAB = true
		s.Details = append(s.Details, "public access not fully blocked")
	}
	oc, err := s3c.GetBucketOwnershipControls(ctx, &s3.GetBucketOwnershipControlsInput{Bucket: aws.String(bucket)})
	if err != nil && !awsx.IsNotFound(err) && awsx.ErrorCode(err) != "OwnershipControlsNotFoundError" {
		return nil, fmt.Errorf("reading the ownership controls of %s: %w", bucket, err)
	}
	if err != nil || oc.OwnershipControls == nil || len(oc.OwnershipControls.Rules) == 0 ||
		oc.OwnershipControls.Rules[0].ObjectOwnership != stypes.ObjectOwnershipBucketOwnerEnforced {
		fixOwner = true
		s.Details = append(s.Details, "objects not owned by the bucket owner (BucketOwnerEnforced)")
	}
	v, err := s3c.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: aws.String(bucket)})
	if err != nil {
		return nil, fmt.Errorf("reading the versioning of %s: %w", bucket, err)
	}
	if v.Status != stypes.BucketVersioningStatusEnabled {
		fixVersioning = true
		s.Details = append(s.Details, "versioning off")
	}
	if _, err := s3c.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)}); err != nil {
		if awsx.ErrorCode(err) != "ServerSideEncryptionConfigurationNotFoundError" {
			return nil, fmt.Errorf("reading the encryption of %s: %w", bucket, err)
		}
		fixSSE = true
		s.Details = append(s.Details, "no default encryption")
	}
	fixTag := !tagged
	if fixTag {
		s.Details = append(s.Details, "no "+policy.ReportBucketTag+" tag")
	}
	if fixPAB || fixOwner || fixVersioning || fixSSE || fixTag {
		s.Op = plan.Update
		s.Apply = func(ctx context.Context) error {
			return fixReportBucket(ctx, s3c, bucket, fixPAB, fixOwner, fixVersioning, fixSSE, fixTag)
		}
	}
	p, err := s3c.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
	switch {
	case awsx.IsNotFound(err):
		pi.Op, pi.Details = plan.Create, []string{"set"}
	case err != nil:
		return nil, fmt.Errorf("reading the policy of %s: %w", bucket, err)
	case !policy.Equal(aws.ToString(p.Policy), pol):
		pi.Op, pi.Details = plan.Update, []string{"replace (it differs from what fleet init sets)"}
	}
	if pi.Op != plan.OK {
		pi.Apply = func(ctx context.Context) error { return putPolicy(ctx, s3c, bucket, pol) }
	}
	return []*plan.Item{b, s, pi}, nil
}

func fixReportBucket(ctx context.Context, s3c *s3.Client, bucket string, pab, owner, versioning, sse, tag bool) error {
	if pab {
		if _, err := s3c.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{Bucket: aws.String(bucket),
			PublicAccessBlockConfiguration: &stypes.PublicAccessBlockConfiguration{BlockPublicAcls: aws.Bool(true),
				IgnorePublicAcls: aws.Bool(true), BlockPublicPolicy: aws.Bool(true), RestrictPublicBuckets: aws.Bool(true)}}); err != nil {
			return fmt.Errorf("blocking public access to %s: %w", bucket, err)
		}
	}
	if owner {
		if _, err := s3c.PutBucketOwnershipControls(ctx, &s3.PutBucketOwnershipControlsInput{Bucket: aws.String(bucket),
			OwnershipControls: &stypes.OwnershipControls{Rules: []stypes.OwnershipControlsRule{
				{ObjectOwnership: stypes.ObjectOwnershipBucketOwnerEnforced}}}}); err != nil {
			return fmt.Errorf("setting the object ownership of %s: %w", bucket, err)
		}
	}
	if versioning {
		if _, err := s3c.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(bucket),
			VersioningConfiguration: &stypes.VersioningConfiguration{Status: stypes.BucketVersioningStatusEnabled}}); err != nil {
			return fmt.Errorf("turning on versioning of %s: %w", bucket, err)
		}
	}
	if sse {
		if _, err := s3c.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{Bucket: aws.String(bucket),
			ServerSideEncryptionConfiguration: &stypes.ServerSideEncryptionConfiguration{Rules: []stypes.ServerSideEncryptionRule{
				{ApplyServerSideEncryptionByDefault: &stypes.ServerSideEncryptionByDefault{SSEAlgorithm: stypes.ServerSideEncryptionAes256}}}}}); err != nil {
			return fmt.Errorf("setting the encryption of %s: %w", bucket, err)
		}
	}
	if tag {
		// Keep the bucket's other tags.
		var tags []stypes.Tag
		t, err := s3c.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucket)})
		switch {
		case err == nil:
			tags = t.TagSet
		case !awsx.IsNotFound(err):
			return fmt.Errorf("reading the tags of %s: %w", bucket, err)
		}
		tags = append(tags, stypes.Tag{Key: aws.String(policy.ReportBucketTag), Value: aws.String("true")})
		if _, err := s3c.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: aws.String(bucket),
			Tagging: &stypes.Tagging{TagSet: tags}}); err != nil {
			return fmt.Errorf("tagging %s: %w", bucket, err)
		}
	}
	return nil
}

func allBlocked(c *stypes.PublicAccessBlockConfiguration) bool {
	return c != nil && aws.ToBool(c.BlockPublicAcls) && aws.ToBool(c.IgnorePublicAcls) &&
		aws.ToBool(c.BlockPublicPolicy) && aws.ToBool(c.RestrictPublicBuckets)
}

func putPolicy(ctx context.Context, s3c *s3.Client, bucket, pol string) error {
	if _, err := s3c.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{Bucket: aws.String(bucket), Policy: aws.String(pol)}); err != nil {
		return fmt.Errorf("setting the policy of %s: %w", bucket, err)
	}
	return nil
}

// FleetJSON is fleet report --json.
type FleetJSON struct {
	Reports  []fleet.Report `json:"reports"`
	Problems []string       `json:"problems,omitempty"`
}

func (a *App) fleetReportCmd() *cobra.Command {
	var bucket, region string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Show the latest report of every setup",
		Long: `fleet report reads the central report bucket and shows one line per setup (account and id):
its products, users (onboarded, set up, paused), this month's spend, when it last reported and
whether it is still installed, then the installed setups' users and spend per product. Setups report on every apply and daily at 07:00 UTC; spend lags up to a day.
--json prints the full reports, with users' emails for setups that send them. Without --bucket it
reads the bucket fleet init tagged.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := a.fleetClients(ctx, region)
			if err != nil {
				return err
			}
			rb, exists, err := a.fleetBucket(ctx, c, bucket, region, false)
			if err != nil {
				return err
			}
			if !exists {
				return fmt.Errorf("bucket %s doesn't exist; create it with fleet init", rb.Name)
			}
			reports, problems, err := fleet.Read(ctx, c.S3In(rb.Region), rb.Name)
			if err != nil {
				return err
			}
			if a.JSON {
				if reports == nil {
					reports = []fleet.Report{}
				}
				enc := json.NewEncoder(a.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(FleetJSON{Reports: reports, Problems: problems})
			}
			writeFleet(a, reports, a.Now())
			for _, p := range problems {
				_, _ = fmt.Fprintf(a.Err, "warning: %s\n", p)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&bucket, "bucket", "", "name of the central report bucket (default: the one fleet init tagged)")
	fl.StringVar(&region, "region", "", "region of the bucket (default: the bucket's own)")
	return cmd
}

// writeFleet prints one line per setup, then the installed setups' totals by product and overall.
func writeFleet(a *App, reports []fleet.Report, now time.Time) {
	if len(reports) == 0 {
		a.printf("No reports yet.\n")
		return
	}
	month := now.UTC().Format("2006-01")
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ACCOUNT\tID\tREGION\tVERSION\tPRODUCTS\tONBOARDED\tSET UP\tPAUSED\tSPEND (USD)\tMONTH\tREPORTED\tSTATUS")
	var total fleet.Counts
	var spend float64
	setups := 0
	type productTotal struct {
		setups int
		users  fleet.Counts
		spend  float64
	}
	byProduct := map[string]*productTotal{}
	known := false
	for _, r := range reports {
		products := reportProducts(r)
		names := make([]string, 0, len(products))
		if r.Product != "" {
			names = append(names, r.Product)
		}
		for _, p := range products {
			if p.Product != r.Product && p.Product != noProduct {
				names = append(names, p.Product)
			}
		}
		list := strings.Join(names, ", ")
		if list == "" {
			list = noProduct
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%.2f\t%s\t%s\t%s\n", r.Account, r.ID, r.Region, r.Version, list,
			r.Users.Onboarded, r.Users.SetUp, r.Users.Paused, r.SpendUSD, r.Month, ago(now, r.ReportedAt), r.Status)
		if r.Status != fleet.StatusActive {
			continue
		}
		setups++
		total = addCounts(total, r.Users)
		if r.Month == month {
			spend += r.SpendUSD
		}
		for _, p := range products {
			known = known || p.Product != noProduct
			t := byProduct[p.Product]
			if t == nil {
				t = &productTotal{}
				byProduct[p.Product] = t
			}
			t.setups++
			t.users = addCounts(t.users, p.Users)
			if r.Month == month {
				t.spend += p.SpendUSD
			}
		}
	}
	_ = w.Flush()
	if known {
		names := make([]string, 0, len(byProduct))
		for p := range byProduct {
			names = append(names, p)
		}
		sort.Slice(names, func(i, j int) bool {
			if byProduct[names[i]].spend != byProduct[names[j]].spend {
				return byProduct[names[i]].spend > byProduct[names[j]].spend
			}
			return names[i] < names[j]
		})
		a.printf("\n")
		w = tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "PRODUCT\tSETUPS\tONBOARDED\tSET UP\tPAUSED\tSPEND (USD)")
		for _, p := range names {
			t := byProduct[p]
			_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%.2f\n", p, t.setups, t.users.Onboarded, t.users.SetUp, t.users.Paused, t.spend)
		}
		_ = w.Flush()
	}
	a.printf("\n%d installed setups: %s; $%.2f this month.\n", setups, total.Text(), spend)
}

// noProduct stands for the product of a report from a version that didn't send it.
const noProduct = "-"

// reportProducts is the report's product split; a report from an older version counts as one
// product: its default, or noProduct.
func reportProducts(r fleet.Report) []fleet.ProductSummary {
	if len(r.Products) > 0 {
		return r.Products
	}
	p := r.Product
	if p == "" {
		p = noProduct
	}
	return []fleet.ProductSummary{{Product: p, Users: r.Users, SpendUSD: r.SpendUSD}}
}

func addCounts(a, b fleet.Counts) fleet.Counts {
	return fleet.Counts{Onboarded: a.Onboarded + b.Onboarded, SetUp: a.SetUp + b.SetUp, Paused: a.Paused + b.Paused,
		IAMUsers: a.IAMUsers + b.IAMUsers}
}

// ago is how long before now t was, in a short form.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
