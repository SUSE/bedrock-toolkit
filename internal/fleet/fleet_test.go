package fleet

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/SUSE/bedrock-toolkit/internal/pause"
)

var now = time.Date(2026, 5, 20, 10, 30, 15, 0, time.UTC)

func users() []User {
	return []User{
		{Email: "a@x.com", Kind: KindSSO, LastUsed: now.Add(-time.Hour), State: pause.Armed, SpendUSD: 10.004},
		{Email: "b@x.com", Kind: KindIAMUser, State: pause.Paused, SpendUSD: 5},
		{Email: "c@x.com", Kind: KindSSO, LastUsed: now, State: pause.PausedByHand},
		{Email: "d@x.com", Kind: KindSSO, State: pause.Off},
	}
}

func TestCount(t *testing.T) {
	c := Count(users())
	if c != (Counts{Onboarded: 4, SetUp: 2, Paused: 2, IAMUsers: 1}) {
		t.Fatalf("%+v", c)
	}
	if got := c.Text(); got != "4 onboarded, 2 set up, 2 paused (1 with an IAM user)" {
		t.Fatal(got)
	}
}

func TestBuild(t *testing.T) {
	s := Setup{ID: "acme", Region: "us-west-2", Version: "1.2.3", Bucket: "central", BucketRegion: "us-east-1"}
	r := Build(s, "123456789012", StatusActive, SourceApply, users(), now)
	if r.People != nil {
		t.Fatalf("emails sent without include_emails: %+v", r.People)
	}
	if r.SpendUSD != 15 || r.Month != "2026-05" || r.Users.Onboarded != 4 || r.Schema != SchemaVersion {
		t.Fatalf("%+v", r)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "@") {
		t.Fatalf("report without include_emails has an email: %s", b)
	}
	s.IncludeEmails = true
	r = Build(s, "123456789012", StatusActive, SourceLambda, users(), now)
	if len(r.People) != 4 || r.People[1] != (Person{Email: "b@x.com", Kind: KindIAMUser, Paused: true}) {
		t.Fatalf("%+v", r.People)
	}
}

type putter struct {
	in   *s3.PutObjectInput
	body string
}

func (p *putter) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	p.in = in
	b, _ := io.ReadAll(in.Body)
	p.body = string(b)
	return &s3.PutObjectOutput{}, nil
}

func TestSend(t *testing.T) {
	p := &putter{}
	r := Build(Setup{ID: "acme"}, "123456789012", StatusUninstalled, SourceUninstall, nil, now)
	if err := Send(context.Background(), p, "central", r); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(p.in.Key) != "123456789012/acme.json" || aws.ToString(p.in.Bucket) != "central" {
		t.Fatalf("%+v", p.in)
	}
	var back Report
	if err := json.Unmarshal([]byte(p.body), &back); err != nil || back.Status != StatusUninstalled {
		t.Fatalf("%v %+v", err, back)
	}
}
