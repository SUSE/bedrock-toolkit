package websearch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Service is the SigV4 signing name of AgentCore gateways.
const Service = "bedrock-agentcore"

// HTTPError is a non-2xx answer from the gateway.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gateway answered %d: %s", e.Status, strings.TrimSpace(e.Body))
}

// Denied reports whether the gateway refused the caller's IAM permissions.
func Denied(err error) bool {
	var h *HTTPError
	return errors.As(err, &h) && (h.Status == http.StatusForbidden || h.Status == http.StatusUnauthorized)
}

// MCP makes one SigV4-signed MCP JSON-RPC call to a gateway URL and returns its result.
func MCP(ctx context.Context, cfg aws.Config, url, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	creds, err := cfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), Service, cfg.Region, time.Now()); err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: truncate(string(raw))}
	}
	return decodeRPC(resp.Header.Get("Content-Type"), raw)
}

// decodeRPC reads a JSON-RPC answer sent as JSON or as a server-sent event stream.
func decodeRPC(contentType string, raw []byte) (json.RawMessage, error) {
	if strings.HasPrefix(contentType, "text/event-stream") {
		var data []byte
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				data = []byte(strings.TrimSpace(d))
			}
		}
		raw = data
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("unexpected gateway answer: %s", truncate(string(raw)))
	}
	if r.Error != nil {
		return nil, fmt.Errorf("gateway error %d: %s", r.Error.Code, r.Error.Message)
	}
	return r.Result, nil
}

// HasTool reports whether a tools/list result offers ToolName.
func HasTool(result json.RawMessage) (bool, error) {
	var r struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return false, err
	}
	for _, t := range r.Tools {
		if t.Name == ToolName {
			return true, nil
		}
	}
	return false, nil
}

// Search runs one search with one result and returns how many results came back.
func Search(ctx context.Context, cfg aws.Config, url, query string) (int, error) {
	res, err := MCP(ctx, cfg, url, "tools/call", map[string]any{"name": ToolName,
		"arguments": map[string]any{"query": query, "maxResults": 1}})
	if err != nil {
		return 0, err
	}
	var r struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return 0, err
	}
	if r.IsError {
		msg := ""
		if len(r.Content) > 0 {
			msg = r.Content[0].Text
		}
		return 0, fmt.Errorf("search failed: %s", truncate(msg))
	}
	if len(r.Content) == 0 {
		return 0, errors.New("search returned nothing")
	}
	return len(r.Content), nil
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
