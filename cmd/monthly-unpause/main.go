// Command monthly-unpause is the Lambda that runs at 06:00 UTC on the 1st of each month. It saves
// a snapshot of the limit and pause state of its id's users for the month that ended, then re-arms
// every pause action of the id that is paused or off, keeping pauses by hand in place.
// A daily schedule invokes it with {"action":"daily"} to write each user's usage file (their spend
// per day and model, from the cost export) and, with central reporting on, the setup's report and
// daily usage to the central bucket. bedrock-admin apply deploys one per id.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/budgets"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/SUSE/bedrock-toolkit/internal/fleet"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
	"github.com/SUSE/bedrock-toolkit/internal/usage"
)

// event is the schedule's input; an empty action is the monthly run.
type event struct {
	Action string `json:"action"`
}

func handler(ctx context.Context, ev event) (any, error) {
	switch ev.Action {
	case "":
		return monthly(ctx)
	case "daily", "report":
		return nil, daily(ctx)
	}
	return nil, fmt.Errorf("unknown action %q", ev.Action)
}

// manager builds the pause manager of the Lambda's id; budgets lives in us-east-1.
func manager(ctx context.Context, setupID string) (*pause.Manager, aws.Config, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryMaxAttempts(10)) // as awsx.RetryMaxAttempts: Budgets throttles
	if err != nil {
		return nil, cfg, err
	}
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, cfg, err
	}
	budgetsCfg := cfg.Copy()
	budgetsCfg.Region = "us-east-1"
	return &pause.Manager{Budgets: budgets.NewFromConfig(budgetsCfg), IAM: iam.NewFromConfig(cfg), Account: aws.ToString(id.Account), ID: setupID}, cfg, nil
}

func putJSON(ctx context.Context, c *s3.Client, bucket, key string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key),
		Body: bytes.NewReader(b), ContentType: aws.String("application/json")})
	return err
}

// daily writes the users' usage files and, with central reporting on, the report.
func daily(ctx context.Context) error {
	bucket, prefix, setupID := os.Getenv("SNAPSHOT_BUCKET"), os.Getenv("EXPORT_PREFIX"), os.Getenv("BEDROCK_ID")
	if bucket == "" || prefix == "" || setupID == "" {
		return fmt.Errorf("SNAPSHOT_BUCKET, EXPORT_PREFIX and BEDROCK_ID must be set")
	}
	var setup *fleet.Setup
	if v := os.Getenv("REPORT_SETUP"); v != "" {
		setup = &fleet.Setup{}
		if err := json.Unmarshal([]byte(v), setup); err != nil || setup.Bucket == "" || setup.BucketRegion == "" {
			return fmt.Errorf("REPORT_SETUP must be the report setup: %w", err)
		}
	}
	m, cfg, err := manager(ctx, setupID)
	if err != nil {
		return err
	}
	snap, _, _, err := m.TakeSnapshot(ctx, "")
	if err != nil {
		return err
	}
	var us []usage.User
	for _, u := range snap.Users {
		us = append(us, usage.User{Email: u.Email, Role: u.Role, Product: u.Product})
	}
	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.DisableLogOutputChecksumValidationSkipped = true })
	run := usage.DailyRun{Account: m.Account, ID: setupID, Users: us, Now: time.Now(),
		Source:   &usage.S3Source{S3: s3c, Bucket: bucket, Prefix: prefix},
		PutUsage: func(ctx context.Context, key string, v any) error { return putJSON(ctx, s3c, bucket, key, v) }}
	var central *s3.Client
	if setup != nil {
		central = centralClient(cfg, setup.BucketRegion)
		run.PutFleet = func(ctx context.Context, month string, v usage.FleetUsage) error {
			return putJSON(ctx, central, setup.Bucket, fleet.UsageKey(m.Account, setupID, month), v)
		}
	}
	err = run.Run(ctx, func(f string, a ...any) { log.Printf(f, a...) })
	if setup != nil {
		err = errors.Join(err, report(ctx, m, cfg, central, *setup))
	}
	return err
}

func centralClient(cfg aws.Config, region string) *s3.Client {
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.Region = region
		o.DisableLogOutputChecksumValidationSkipped = true
	})
}

func report(ctx context.Context, m *pause.Manager, cfg aws.Config, s3c *s3.Client, s fleet.Setup) error {
	us, err := fleet.Collect(ctx, m, iam.NewFromConfig(cfg))
	if err != nil {
		return err
	}
	r := fleet.Build(s, m.Account, fleet.StatusActive, fleet.SourceLambda, us, time.Now())
	if err := fleet.Send(ctx, s3c, s.Bucket, r); err != nil {
		return err
	}
	log.Printf("sent s3://%s/%s (%s)", s.Bucket, fleet.Key(r.Account, r.ID), r.Users.Text())
	return nil
}

func monthly(ctx context.Context) (pause.MonthlyResult, error) {
	bucket, prefix, setupID := os.Getenv("SNAPSHOT_BUCKET"), os.Getenv("SNAPSHOT_PREFIX"), os.Getenv("BEDROCK_ID")
	if bucket == "" || prefix == "" || setupID == "" {
		return pause.MonthlyResult{}, fmt.Errorf("SNAPSHOT_BUCKET, SNAPSHOT_PREFIX and BEDROCK_ID must be set")
	}
	m, cfg, err := manager(ctx, setupID)
	if err != nil {
		return pause.MonthlyResult{}, err
	}
	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.DisableLogOutputChecksumValidationSkipped = true })
	save := func(ctx context.Context, s pause.Snapshot) error {
		b, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return err
		}
		key := pause.SnapshotKey(prefix, s.Month)
		_, err = s3c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key),
			Body: bytes.NewReader(b), ContentType: aws.String("application/json")})
		if err == nil {
			log.Printf("saved s3://%s/%s (%d users)", bucket, key, len(s.Users))
		}
		return err
	}
	res, err := m.Monthly(ctx, save)
	for b, d := range res.Done {
		log.Printf("%s: %s", b, d)
	}
	return res, err
}

func main() { lambda.Start(handler) }
