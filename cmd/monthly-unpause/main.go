// Command monthly-unpause is the Lambda that runs at 06:00 UTC on the 1st of each month. It saves
// a snapshot of the limit and pause state of its id's users for the month that ended, then re-arms
// every pause action of the id that is paused or off, keeping pauses by hand in place.
// With central reporting on, a daily schedule also invokes it with {"action":"report"} to write the
// setup's report to the central bucket. bedrock-admin apply deploys one per id.
package main

import (
	"bytes"
	"context"
	"encoding/json"
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
)

// event is the schedule's input; an empty action is the monthly run.
type event struct {
	Action string `json:"action"`
}

func handler(ctx context.Context, ev event) (any, error) {
	switch ev.Action {
	case "":
		return monthly(ctx)
	case "report":
		return nil, report(ctx)
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

func report(ctx context.Context) error {
	var s fleet.Setup
	if err := json.Unmarshal([]byte(os.Getenv("REPORT_SETUP")), &s); err != nil || s.Bucket == "" || s.BucketRegion == "" {
		return fmt.Errorf("REPORT_SETUP must be set to the report setup: %w", err)
	}
	m, cfg, err := manager(ctx, s.ID)
	if err != nil {
		return err
	}
	us, err := fleet.Collect(ctx, m, iam.NewFromConfig(cfg))
	if err != nil {
		return err
	}
	r := fleet.Build(s, m.Account, fleet.StatusActive, fleet.SourceLambda, us, time.Now())
	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.Region = s.BucketRegion
		o.DisableLogOutputChecksumValidationSkipped = true
	})
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
