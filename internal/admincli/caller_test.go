package admincli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	itypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/SUSE/bedrock-toolkit/internal/config"
)

type fakeTags map[string]string

func (f fakeTags) ListUserTags(_ context.Context, in *iam.ListUserTagsInput, _ ...func(*iam.Options)) (*iam.ListUserTagsOutput, error) {
	if f == nil {
		return nil, errors.New("AccessDenied")
	}
	id, ok := f[aws.ToString(in.UserName)]
	if !ok {
		return &iam.ListUserTagsOutput{}, nil
	}
	return &iam.ListUserTagsOutput{Tags: []itypes.Tag{{Key: aws.String("owner"), Value: aws.String("x@y.z")},
		{Key: aws.String("bedrock-admin:id"), Value: aws.String(id)}}}, nil
}

func TestCheckAdmin(t *testing.T) {
	cfg := &config.Config{Users: []config.User{{Email: "dave@acme.com", IAMUser: "Dave.Dev"}}}
	tagged := fakeTags{"bob": "acme"}
	for name, tc := range map[string]struct {
		arn  string
		cfg  *config.Config
		tags fakeTags
		want string
	}{
		"sso admin":       {arn: "arn:aws:sts::1:assumed-role/AWSReservedSSO_BedrockAdmin_abc/me@acme.com", cfg: cfg, tags: tagged},
		"other role":      {arn: "arn:aws:sts::1:assumed-role/AWSReservedSSO_AWSAdministratorAccess_abc/me", tags: tagged},
		"admin iam user":  {arn: "arn:aws:iam::1:user/dko", cfg: cfg, tags: tagged},
		"personal role":   {arn: "arn:aws:sts::1:assumed-role/bedrock-user-acme-dave/dave@acme.com", cfg: cfg, tags: tagged, want: "personal role bedrock-user-acme-dave"},
		"no file":         {arn: "arn:aws:sts::1:assumed-role/bedrock-user-acme-dave/dave@acme.com", want: "personal role"},
		"iam_user":        {arn: "arn:aws:iam::1:user/dave.dev", cfg: cfg, tags: tagged, want: "iam_user of dave@acme.com"},
		"tagged iam user": {arn: "arn:aws:iam::1:user/team/bob", tags: tagged, want: "Bedrock user of setup acme"},
		"tags denied":     {arn: "arn:aws:iam::1:user/bob"},
		"root":            {arn: "arn:aws:iam::1:root", tags: tagged},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkAdmin(context.Background(), tc.tags, tc.arn, tc.cfg)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "bedrock-admin is for admins") {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}
