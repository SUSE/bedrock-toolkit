package users

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/budgets"

	"github.com/SUSE/bedrock-toolkit/internal/awsx"
	"github.com/SUSE/bedrock-toolkit/internal/config"
	"github.com/SUSE/bedrock-toolkit/internal/pause"
)

type budgetHTTPFunc func(*http.Request) (*http.Response, error)

func (f budgetHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestApplyBudgetRecipients(t *testing.T) {
	type subscriberJSON struct{ Address, SubscriptionType string }
	type request struct {
		Subscribers                              []subscriberJSON
		NotificationsWithSubscribers             []struct{ Subscribers []subscriberJSON }
		Subscriber, OldSubscriber, NewSubscriber subscriberJSON
	}
	const owner = "dave@acme.com"
	const extra = "ops@example.com"
	const old = "old@example.com"
	cases := []struct {
		name         string
		extras, have []string
		change       Change
		want         []string
	}{
		{"owner only", nil, nil, Change{CreateBudget: true, CreateAction: true},
			[]string{"CreateBudget:" + owner, "CreateBudgetAction:" + owner}},
		{"new budget and action", []string{extra}, nil, Change{CreateBudget: true, CreateAction: true},
			[]string{"CreateBudget:" + owner + "," + extra, "CreateBudgetAction:" + owner + "," + extra}},
		{"new alert", []string{extra}, nil, Change{AddAlerts: []int{90}},
			[]string{"CreateNotification:" + owner + "," + extra}},
		{"add recipient", []string{extra}, []string{owner}, Change{ResubAlert: []int{80}, UpdateAction: true},
			[]string{"CreateSubscriber:" + extra, "UpdateBudgetAction:" + owner + "," + extra}},
		{"remove recipient", nil, []string{owner, extra}, Change{ResubAlert: []int{80}, UpdateAction: true},
			[]string{"DeleteSubscriber:" + extra, "UpdateBudgetAction:" + owner}},
		{"replace last recipient", []string{extra}, []string{old}, Change{ResubAlert: []int{80}},
			[]string{"UpdateSubscriber:" + old + "," + owner, "CreateSubscriber:" + extra}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			client := budgets.New(budgets.Options{Region: "us-east-1", Credentials: aws.AnonymousCredentials{},
				HTTPClient: budgetHTTPFunc(func(r *http.Request) (*http.Response, error) {
					op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AWSBudgetServiceGateway.")
					var in request
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						return nil, err
					}
					subs := in.Subscribers
					switch op {
					case "CreateBudget":
						if len(in.NotificationsWithSubscribers) != 1 {
							t.Fatalf("unexpected notifications: %+v", in.NotificationsWithSubscribers)
						}
						subs = in.NotificationsWithSubscribers[0].Subscribers
					case "CreateSubscriber", "DeleteSubscriber":
						subs = []subscriberJSON{in.Subscriber}
					case "UpdateSubscriber":
						subs = []subscriberJSON{in.OldSubscriber, in.NewSubscriber}
					case "CreateNotification", "CreateBudgetAction", "UpdateBudgetAction":
					default:
						t.Fatalf("unexpected Budgets call %s", op)
					}
					var addresses []string
					for _, sub := range subs {
						if sub.SubscriptionType != "EMAIL" {
							t.Errorf("expected EMAIL subscriber: %+v", sub)
						}
						addresses = append(addresses, sub.Address)
					}
					calls = append(calls, op+":"+strings.Join(addresses, ","))
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
				}),
			})
			e := Env{C: &awsx.Clients{Account: "111122223333", Budgets: client}, Cfg: &config.Config{ID: "acme"}, Pause: &pause.Manager{}}
			u := want(50, 100)
			u.AdditionalNotifyEmails = tc.extras
			o := observed(50, 100, 0, pause.Armed)
			o.Alerts[80] = tc.have
			if err := e.apply(t.Context(), u, o, tc.change); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(calls, tc.want) {
				t.Errorf("calls %v, want %v", calls, tc.want)
			}
		})
	}
}
