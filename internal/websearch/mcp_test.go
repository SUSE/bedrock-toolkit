package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

func testCfg() aws.Config {
	return aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("AKID", "SECRET", "TOKEN")}
}

func TestMCPSignsAndDecodes(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); !strings.Contains(a, "/us-east-1/bedrock-agentcore/aws4_request") {
			t.Errorf("Authorization = %q", a)
		}
		if r.Header.Get("X-Amz-Security-Token") != "TOKEN" {
			t.Error("missing session token")
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\""+ToolName+"\"}]}}\n\n")
	}))
	defer srv.Close()
	res, err := MCP(context.Background(), testCfg(), srv.URL+"/mcp", "tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got["method"] != "tools/list" || got["jsonrpc"] != "2.0" {
		t.Errorf("request = %v", got)
	}
	if ok, err := HasTool(res); err != nil || !ok {
		t.Errorf("HasTool = %v, %v", ok, err)
	}
}

func TestMCPErrors(t *testing.T) {
	for _, tc := range []struct {
		name, ctype, body string
		status            int
		denied            bool
		want              string
	}{
		{"denied", "application/json", `{"message":"not authorized"}`, 403, true, "gateway answered 403"},
		{"rpc error", "application/json", `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"no such method"}}`, 200, false, "no such method"},
		{"tool error", "application/json", `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"query too long"}]}}`, 200, false, "query too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			_, err := Search(context.Background(), testCfg(), srv.URL, "q")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if Denied(err) != tc.denied {
				t.Errorf("Denied = %v", Denied(err))
			}
		})
	}
}

func TestSearch(t *testing.T) {
	var args map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params.Name != ToolName {
			t.Errorf("tool = %s", req.Params.Name)
		}
		args = req.Params.Arguments
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"result"}]}}`)
	}))
	defer srv.Close()
	n, err := Search(context.Background(), testCfg(), srv.URL, "aws")
	if err != nil || n != 1 {
		t.Fatalf("Search = %d, %v", n, err)
	}
	if args["query"] != "aws" || args["maxResults"] != float64(1) {
		t.Errorf("arguments = %v", args)
	}
}
