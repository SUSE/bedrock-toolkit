package policy

import (
	"strings"
	"testing"
)

func TestMergeBlock(t *testing.T) {
	other := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`

	got, changed, err := MergeBlock("", "BedrockAdmin", "111122223333")
	if err != nil || !changed || !HasBlockFor(got, "111122223333") || HasBlockFor(got, "444455556666") {
		t.Fatalf("empty: %v %v\n%s", changed, err, got)
	}
	if ExemptPermissionSet(got) != "BedrockAdmin" {
		t.Errorf("exempt: %q", ExemptPermissionSet(got))
	}

	got, changed, err = MergeBlock(other, "BedrockAdmin", "111122223333")
	if err != nil || !changed || !strings.Contains(got, "s3:GetObject") || !HasBlockFor(got, "111122223333") {
		t.Fatalf("keeps other statements: %v %v\n%s", changed, err, got)
	}

	// A second account is added to the same statement.
	two, changed, _ := MergeBlock(got, "BedrockAdmin", "444455556666")
	if !changed || !HasBlockFor(two, "111122223333") || !HasBlockFor(two, "444455556666") || strings.Count(two, BlockSid) != 1 {
		t.Fatalf("second account:\n%s", two)
	}
	if _, changed, _ := MergeBlock(two, "BedrockAdmin", "444455556666"); changed {
		t.Error("an account already covered changes the policy")
	}

	// A deny in every account (no aws:PrincipalAccount) already covers it.
	all := Block("BedrockAdmin")
	if _, changed, _ := MergeBlock(all, "BedrockAdmin", "111122223333"); changed {
		t.Error("an account-wide deny is changed")
	}

	if _, _, err := MergeBlock("not json", "BedrockAdmin", "1"); err == nil {
		t.Error("bad document: no error")
	}
}

func TestMergeAssume(t *testing.T) {
	got, changed, err := MergeAssume("")
	if err != nil || !changed || !AllowsAssumePersonalRole(got) {
		t.Fatalf("%v %v\n%s", changed, err, got)
	}
	if _, changed, _ := MergeAssume(got); changed {
		t.Error("changed twice")
	}
}
