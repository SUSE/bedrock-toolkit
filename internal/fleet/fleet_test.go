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
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

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

func TestBuildProducts(t *testing.T) {
	us := users()
	us[1].Product = "longhorn"
	us[3].Product = "longhorn"
	s := Setup{ID: "acme", Product: "harvester", IncludeEmails: true}
	r := Build(s, "123456789012", StatusActive, SourceApply, us, now)
	want := []ProductSummary{
		{Product: "harvester", Users: Counts{Onboarded: 2, SetUp: 2, Paused: 1}, SpendUSD: 10},
		{Product: "longhorn", Users: Counts{Onboarded: 2, Paused: 1, IAMUsers: 1}, SpendUSD: 5},
	}
	if r.Product != "harvester" || len(r.Products) != 2 || r.Products[0] != want[0] || r.Products[1] != want[1] {
		t.Fatalf("%s %+v", r.Product, r.Products)
	}
	if r.People[0].Product != "harvester" || r.People[1].Product != "longhorn" {
		t.Fatalf("people's products: %+v", r.People)
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

type store map[string][]byte

func (s store) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	b, _ := io.ReadAll(in.Body)
	s[aws.ToString(in.Key)] = b
	return &s3.PutObjectOutput{}, nil
}

func (s store) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	out := &s3.ListObjectsV2Output{IsTruncated: aws.Bool(false)}
	for k := range s {
		out.Contents = append(out.Contents, s3types.Object{Key: aws.String(k)})
	}
	return out, nil
}

func (s store) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if _, ok := s[aws.ToString(in.Key)]; !ok {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey"}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(strings.NewReader(string(s[aws.ToString(in.Key)])))}, nil
}

func TestRead(t *testing.T) {
	s := store{}
	now := time.Date(2025, 10, 21, 7, 0, 0, 0, time.UTC)
	for _, r := range []Report{
		Build(Setup{ID: "b"}, "222222222222", StatusActive, SourceLambda, nil, now),
		Build(Setup{ID: "a"}, "222222222222", StatusUninstalled, SourceUninstall, nil, now),
		Build(Setup{ID: "z"}, "111111111111", StatusActive, SourceApply, nil, now),
	} {
		if err := Send(context.Background(), s, "central", r); err != nil {
			t.Fatal(err)
		}
	}
	s["333333333333/x.json"] = []byte(`{"account":"111111111111","id":"z"}`)
	s["333333333333/y.json"] = []byte(`not json`)
	s["README.txt"] = []byte("skip")
	s[UsageKey("222222222222", "b", "2025-10")] = []byte(`{"schema":1}`)
	got, problems, err := Read(context.Background(), s, "central")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range got {
		keys = append(keys, Key(r.Account, r.ID))
	}
	if strings.Join(keys, " ") != "111111111111/z.json 222222222222/a.json 222222222222/b.json" {
		t.Errorf("reports %v", keys)
	}
	if len(problems) != 2 {
		t.Errorf("problems %v", problems)
	}
}

func TestFilter(t *testing.T) {
	reports := []Report{{Account: "111111111111", ID: "bedrock"}, {Account: "222222222222", ID: "mlm1"}, {Account: "222222222222", ID: "bedrock"}}
	accts := func(rs []Report) string {
		var s []string
		for _, r := range rs {
			s = append(s, r.Account+"/"+r.ID)
		}
		return strings.Join(s, ",")
	}
	for _, tc := range []struct {
		accounts, ids []string
		want, err     string
	}{
		{want: "111111111111/bedrock,222222222222/mlm1,222222222222/bedrock"},
		{ids: []string{"bedrock"}, want: "111111111111/bedrock,222222222222/bedrock"},
		{accounts: []string{"222222222222"}, want: "222222222222/mlm1,222222222222/bedrock"},
		{accounts: []string{"222222222222"}, ids: []string{"bedrock"}, want: "222222222222/bedrock"},
		{accounts: []string{"111111111111"}, ids: []string{"mlm1"}, err: "no setup has account 111111111111 and id mlm1"},
		{ids: []string{"mlm1", "nope"}, err: "no setup has id nope; known ids: bedrock, mlm1"},
		{accounts: []string{"333333333333"}, err: "no setup has account 333333333333; known accounts: 111111111111, 222222222222"},
	} {
		got, err := Filter(reports, tc.accounts, tc.ids)
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("%v %v: got error %v, want %q", tc.accounts, tc.ids, err, tc.err)
			}
			continue
		}
		if err != nil || accts(got) != tc.want {
			t.Errorf("%v %v: got %s %v, want %s", tc.accounts, tc.ids, accts(got), err, tc.want)
		}
	}
}

func TestSortReports(t *testing.T) {
	reports := func() []Report {
		return []Report{
			{Account: "2", ID: "a", SpendUSD: 5, Users: Counts{Onboarded: 3, SetUp: 1}},
			{Account: "1", ID: "b", SpendUSD: 9, Users: Counts{Onboarded: 3, SetUp: 0}},
			{Account: "1", ID: "a", SpendUSD: 5, Users: Counts{Onboarded: 7, SetUp: 2}},
		}
	}
	keys := func(rs []Report) string {
		var s []string
		for _, r := range rs {
			s = append(s, r.Account+"/"+r.ID)
		}
		return strings.Join(s, " ")
	}
	for by, want := range map[string]string{
		"":          "1/b 1/a 2/a",
		"spend":     "1/b 1/a 2/a",
		"set-up":    "1/a 2/a 1/b",
		"onboarded": "1/a 1/b 2/a",
	} {
		rs := reports()
		if err := SortReports(rs, by); err != nil {
			t.Fatal(err)
		}
		if got := keys(rs); got != want {
			t.Errorf("%q: got %s, want %s", by, got, want)
		}
	}
	if err := SortReports(reports(), "account"); err == nil || !strings.Contains(err.Error(), "--sort must be one of") {
		t.Errorf("bad order: %v", err)
	}
}
