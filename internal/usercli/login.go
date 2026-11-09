package usercli

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SUSE/bedrock-toolkit/internal/awsconf"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
	"github.com/spf13/cobra"
)

func (a *App) loginCmd() *cobra.Command {
	var profile string
	cmd := &cobra.Command{Use: "login", Short: "Sign in to an existing AWS SSO profile using a browser", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&profile, "profile", "", "SSO profile to sign in with (default: the only SSO profile)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		path, err := awsconf.Path()
		if err != nil {
			return err
		}
		file, err := awsconf.Read(path)
		if err != nil {
			return err
		}
		if profile == "" {
			ps := file.SSOProfiles()
			if len(ps) != 1 {
				return fmt.Errorf("specify --profile with an SSO profile")
			}
			profile = ps[0]
		}
		p := file.Profile(profile)
		if p == nil || !p.IsSSO() {
			return fmt.Errorf("profile %q is not an SSO profile", profile)
		}
		keys := p.Keys
		cacheKey := keys["sso_start_url"]
		if session := keys["sso_session"]; session != "" {
			keys = nil
			for _, section := range file.Sections {
				if section.Header == "sso-session "+session {
					keys = section.Keys
					break
				}
			}
			cacheKey = session
		}
		region, start := keys["sso_region"], keys["sso_start_url"]
		if region == "" || start == "" {
			return fmt.Errorf("SSO profile requires sso_region and sso_start_url")
		}
		ctx := cmd.Context()
		client := ssooidc.New(ssooidc.Options{Region: region})
		reg, err := client.RegisterClient(ctx, &ssooidc.RegisterClientInput{ClientName: aws.String("bedrock-toolkit"), ClientType: aws.String("public"), Scopes: []string{"sso:account:access"}})
		if err != nil {
			return err
		}
		auth, err := client.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{ClientId: reg.ClientId, ClientSecret: reg.ClientSecret, StartUrl: aws.String(start)})
		if err != nil {
			return err
		}
		a.printf("Open %s in your browser and enter code %s.\n", aws.ToString(auth.VerificationUri), aws.ToString(auth.UserCode))
		interval := time.Duration(auth.Interval) * time.Second
		if interval < time.Second {
			interval = time.Second
		}
		deadline := time.Now().Add(time.Duration(auth.ExpiresIn) * time.Second)
		for time.Now().Before(deadline) {
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			token, err := client.CreateToken(ctx, &ssooidc.CreateTokenInput{ClientId: reg.ClientId, ClientSecret: reg.ClientSecret, DeviceCode: auth.DeviceCode, GrantType: aws.String("urn:ietf:params:oauth:grant-type:device_code")})
			if err != nil {
				var pending *types.AuthorizationPendingException
				var slow *types.SlowDownException
				if errors.As(err, &pending) {
					continue
				}
				if errors.As(err, &slow) {
					interval += 5 * time.Second
					continue
				}
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			dir := filepath.Join(home, ".aws", "sso", "cache")
			if err := os.MkdirAll(dir, 0700); err != nil {
				return err
			}
			data, err := json.Marshal(map[string]any{"startUrl": start, "region": region, "accessToken": aws.ToString(token.AccessToken), "expiresAt": time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339), "refreshToken": aws.ToString(token.RefreshToken), "clientId": aws.ToString(reg.ClientId), "clientSecret": aws.ToString(reg.ClientSecret), "registrationExpiresAt": time.Unix(reg.ClientSecretExpiresAt, 0).UTC().Format(time.RFC3339)})
			if err != nil {
				return err
			}
			hash := sha1.Sum([]byte(cacheKey))
			tmp, err := os.CreateTemp(dir, ".bedrock-login-*")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			if _, err := tmp.Write(data); err != nil {
				tmp.Close()
				return err
			}
			if err := tmp.Close(); err != nil {
				return err
			}
			if err := os.Rename(tmp.Name(), filepath.Join(dir, hex.EncodeToString(hash[:])+".json")); err != nil {
				return err
			}
			a.printf("Signed in to %s.\n", profile)
			return nil
		}
		return fmt.Errorf("SSO authorization expired; run bedrock login again")
	}
	return cmd
}
