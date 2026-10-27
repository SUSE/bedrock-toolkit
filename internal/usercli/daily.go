package usercli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/policy"
	"github.com/SUSE/bedrock-toolkit/internal/usage"
)

// errNoUsageTag means the personal role doesn't point at its usage files yet.
var errNoUsageTag = errors.New("no " + policy.UsageTag + " tag on your role")

// dailyBarWidth is the width of the largest day's bar.
const dailyBarWidth = 24

// daily is the per-day part of bedrock usage, from the files the Lambda writes.
type daily struct {
	Days   []usage.DayUSD `json:"days"`
	Models []usage.Amount `json:"models"`
	// AsOf is the newest cost export hour the files saw.
	AsOf string `json:"export_as_of,omitempty"`
	// Partial is the first day whose spend may still grow, or "".
	Partial string `json:"partial_from,omitempty"`
	// Note says why there are no days, for the text view.
	Note string `json:"note,omitempty"`
}

// readDaily reads the usage files of dates' months and fills every date, also days without spend.
// Models are of month. A missing file or a role without access gives a Note, not an error.
func (a *App) readDaily(ctx context.Context, profile, role, month string, dates []string, now time.Time) (daily, error) {
	var months []string
	for _, d := range dates {
		if m := d[:7]; !slices.Contains(months, m) {
			months = append(months, m)
		}
	}
	files, err := a.AWS.UsageFiles(ctx, profile, role, months)
	switch {
	case errors.Is(err, errNoUsageTag), awsx.IsAccessDenied(err):
		return daily{Days: []usage.DayUSD{}, Models: []usage.Amount{},
			Note: "Your role cannot read its daily usage yet: ask your admin to run bedrock-admin apply."}, nil
	case err != nil:
		return daily{}, err
	}
	spend := map[string]float64{}
	out := daily{Days: []usage.DayUSD{}, Models: []usage.Amount{}}
	for _, m := range months {
		f := files[m]
		if f == nil {
			continue
		}
		for _, d := range f.Days {
			spend[d.Date] = d.USD
		}
		if m == month {
			out.Models = f.Models
		}
		if f.ExportAsOf > out.AsOf {
			out.AsOf = f.ExportAsOf
		}
	}
	if len(files) == 0 {
		out.Note = "No daily usage yet: it is written every day at 07:00 UTC, once your admin runs bedrock-admin apply."
	}
	for _, d := range dates {
		out.Days = append(out.Days, usage.DayUSD{Date: d, USD: spend[d]})
	}
	latest, _ := time.Parse(time.RFC3339, out.AsOf)
	out.Partial = usage.PartialFrom(now.UTC().Format("2006-01"), latest, now)
	if out.Partial == "" || len(dates) == 0 || dates[len(dates)-1] < out.Partial {
		out.Partial = ""
	}
	return out, nil
}

// lastDays are the n dates ending today.
func lastDays(n int, now time.Time) []string {
	out := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		out = append(out, now.UTC().AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return out
}

// write prints the days with bars, then the models of the month.
func (d daily) write(w io.Writer, title, modelsTitle string) {
	fmt.Fprintln(w, title)
	if d.Note != "" {
		fmt.Fprintln(w, d.Note)
		return
	}
	top := 0.0
	for _, r := range d.Days {
		top = max(top, r.USD)
	}
	rows, bars := [][]string{{"DATE", "SPEND"}}, []string{""}
	for _, r := range d.Days {
		mark := " "
		if d.Partial != "" && r.Date >= d.Partial {
			mark = "*"
		}
		rows = append(rows, []string{r.Date, usd(r.USD) + mark})
		bars = append(bars, usage.Bar(r.USD, top, dailyBarWidth))
	}
	usage.WriteTable(w, rows, bars)
	if d.Partial != "" {
		fmt.Fprintln(w, "* still being billed: AWS adds spend up to a day late.")
	}
	if len(d.Models) > 0 {
		fmt.Fprintf(w, "\n%s\n", modelsTitle)
		rows, bars = nil, nil
		for _, m := range d.Models {
			rows = append(rows, []string{m.Key, usd(m.USD)})
			bars = append(bars, "")
		}
		usage.WriteTable(w, rows, bars)
	}
}

// asOfText is the export time for a title, like "as of May 14 06:00 UTC".
func asOfText(asOf string) string {
	t, err := time.Parse(time.RFC3339, asOf)
	if err != nil {
		return "from the cost export"
	}
	return "from the cost export, as of " + t.UTC().Format("Jan 2 15:04 MST")
}

// UsageFiles reads the role's usage files of months; months without a file are missing from the map.
func (sdkAWS) UsageFiles(ctx context.Context, profile, role string, months []string) (map[string]*usage.UserFile, error) {
	cfg, err := load(ctx, profile, "")
	if err != nil {
		return nil, err
	}
	tags, err := iam.NewFromConfig(cfg).ListRoleTags(ctx, &iam.ListRoleTagsInput{RoleName: aws.String(role)})
	if err != nil {
		return nil, err
	}
	var loc string
	for _, t := range tags.Tags {
		if aws.ToString(t.Key) == policy.UsageTag {
			loc = aws.ToString(t.Value)
		}
	}
	bucket, prefix, ok := strings.Cut(strings.TrimPrefix(loc, "s3://"), "/")
	if !strings.HasPrefix(loc, "s3://") || !ok || bucket == "" {
		return nil, errNoUsageTag
	}
	c := s3.NewFromConfig(cfg, awsx.QuietS3)
	if l, err := c.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(bucket)}); err == nil {
		region := string(l.LocationConstraint)
		if region == "" {
			region = "us-east-1"
		}
		c = s3.NewFromConfig(cfg, awsx.QuietS3, func(o *s3.Options) { o.Region = region })
	}
	out := map[string]*usage.UserFile{}
	for _, m := range months {
		r, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(prefix + m + ".json")})
		if awsx.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var f usage.UserFile
		err = json.NewDecoder(r.Body).Decode(&f)
		_ = r.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading your usage of %s: %w", m, err)
		}
		out[m] = &f
	}
	return out, nil
}
