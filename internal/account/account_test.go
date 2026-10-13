package account

import (
	"strings"
	"testing"

	"github.com/innobead/bedrock-toolkit/internal/config"
	"github.com/innobead/bedrock-toolkit/internal/plan"
)

func TestOwned(t *testing.T) {
	e := &Env{Cfg: &config.Config{ID: "acme"}}
	for id, want := range map[string]string{"acme": "", "": "without a bedrock-admin:id tag", "beta": "tagged bedrock-admin:id=beta, not acme"} {
		it := &plan.Item{}
		ok := e.owned(it, id)
		if ok != (want == "") || (want != "" && (it.Op != plan.Conflict || !strings.Contains(it.Status, want))) {
			t.Errorf("owned(%q) = %v, %+v", id, ok, it)
		}
	}
}
