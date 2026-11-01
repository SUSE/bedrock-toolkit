package plan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderNotes(t *testing.T) {
	p := &Plan{Notes: []string{"Setup old in this account is idle."}}
	var b bytes.Buffer
	Render(&b, p, false)
	if !strings.Contains(b.String(), "Note: Setup old in this account is idle.\n") {
		t.Fatalf("text:\n%s", b.String())
	}
	b.Reset()
	if err := RenderJSON(&b, p); err != nil {
		t.Fatal(err)
	}
	var got JSON
	if err := json.Unmarshal(b.Bytes(), &got); err != nil || len(got.Notes) != 1 {
		t.Fatalf("json: %v\n%s", err, b.String())
	}
}
