package usage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DailyRun is the Lambda's daily run: each user's usage file and, with central reporting on, the
// setup's fleet usage, for this month and, on the 1st and 2nd, last month too (billing lags a day).
type DailyRun struct {
	Account, ID string
	Source      Source
	Users       []User
	// PutUsage writes a user's file to the cost export bucket.
	PutUsage func(ctx context.Context, key string, v any) error
	// PutFleet writes the fleet usage to the central bucket; nil with central reporting off.
	PutFleet func(ctx context.Context, month string, v FleetUsage) error
	Now      time.Time
}

// DailyMonths are the months a daily run at now writes.
func DailyMonths(now time.Time) []string {
	now = now.UTC()
	this := now.Format("2006-01")
	if now.Day() <= 2 {
		return []string{now.AddDate(0, 0, -now.Day()).Format("2006-01"), this}
	}
	return []string{this}
}

// Run writes the files; it goes on after a failed file and returns every error.
func (d DailyRun) Run(ctx context.Context, log func(string, ...any)) error {
	var errs []error
	for _, month := range DailyMonths(d.Now) {
		var lines []Line
		files, err := ReadMonth(ctx, d.Source, month, func(l Line) { lines = append(lines, l) })
		if err != nil {
			errs = append(errs, fmt.Errorf("reading the cost export of %s: %w", month, err))
			continue
		}
		written := 0
		for _, f := range UserFiles(month, d.ID, lines, d.Users, d.Now) {
			if f.Role == "" {
				continue
			}
			if err := d.PutUsage(ctx, UsageKey(d.ID, f.Role, month), f); err != nil {
				errs = append(errs, fmt.Errorf("writing %s's usage of %s: %w", f.Role, month, err))
				continue
			}
			written++
		}
		log("%s: %d export files, %d lines, %d usage files", month, files, len(lines), written)
		if d.PutFleet != nil {
			if err := d.PutFleet(ctx, month, BuildFleetUsage(d.Account, d.ID, month, lines, d.Users, d.Now)); err != nil {
				errs = append(errs, fmt.Errorf("writing the fleet usage of %s: %w", month, err))
			}
		}
	}
	return errors.Join(errs...)
}
