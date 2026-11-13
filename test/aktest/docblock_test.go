/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aktest

import (
	"os"
	"strings"
	"testing"
)

func TestDocBlockAndSetVars(t *testing.T) {
	md := "x\n<!-- e2e:a:begin -->\n```sh\nA=1; B='two words'\nC=3\necho \"$A\"\n```\n<!-- e2e:a:end -->\ny\n"
	got, err := DocBlock(md, "a")
	if err != nil || got != "A=1; B='two words'\nC=3\necho \"$A\"\n" {
		t.Fatalf("got %q, %v", got, err)
	}
	out, err := SetVars(got, map[string]string{"A": "9", "B": "it's", "C": "c d"})
	if err != nil {
		t.Fatal(err)
	}
	want := "A='9'; B='it'\\''s'\nC='c d'\necho \"$A\"\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if _, err := SetVars(got, map[string]string{"NOPE": "1"}); err == nil {
		t.Fatal("an unassigned variable must be an error")
	}
	if _, err := DocBlock(md, "missing"); err == nil {
		t.Fatal("missing marker must be an error")
	}
}

// The runbook must keep the blocks the end-to-end suite executes.
func TestRunbookHasTheMarkedBlocks(t *testing.T) {
	b, err := os.ReadFile("../../docs/migration-from-jeffresc.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"backup", "compare", "retire-one", "uninstall"} {
		blk, err := DocBlock(string(b), name)
		if err != nil || strings.TrimSpace(blk) == "" {
			t.Errorf("%s: %q, %v", name, blk, err)
		}
	}
}

func TestSetVarsIsTwoWay(t *testing.T) {
	script := "bash -euo pipefail <<'EOF'\n# a comment, X=ignored\nA=1; B=2   # trailing\n\n" +
		"C=$(date)\nD=4\nkubectl get \"$A\"\n"
	// Header is A and B: C is computed and ends the header, so D is a later use.
	if _, err := SetVars(script, map[string]string{"A": "x", "B": "y"}); err != nil {
		t.Fatalf("exact set rejected: %v", err)
	}
	// A header variable the caller does not set must fail (runbook gained one).
	if _, err := SetVars(script, map[string]string{"A": "x"}); err == nil {
		t.Fatal("a header variable without a value must be an error")
	}
	// A key that is not in the header must fail (runbook dropped one).
	if _, err := SetVars(script, map[string]string{"A": "x", "B": "y", "D": "z"}); err == nil {
		t.Fatal("a value for a non-header variable must be an error")
	}
	out, err := SetVars(script, map[string]string{"A": "x", "B": "y"})
	if err != nil || !strings.Contains(out, "A='x'; B='y'   # trailing") || !strings.Contains(out, "D=4") {
		t.Fatalf("unexpected rewrite: %q, %v", out, err)
	}
}

func TestReplaceAllFailsOnMissingPlaceholder(t *testing.T) {
	got, err := ReplaceAll("a OLD b OLD", "OLD", "x")
	if err != nil || got != "a x b x" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ReplaceAll("a b", "OLD", "x"); err == nil {
		t.Fatal("a missing placeholder must be an error")
	}
}

// The header variables of the real runbook script, so adding or renaming one
// fails here (and in the suite) rather than leaving a doc default in place.
func TestRunbookRetireHeaderVariables(t *testing.T) {
	b, err := os.ReadFile("../../docs/migration-from-jeffresc.md")
	if err != nil {
		t.Fatal(err)
	}
	blk, err := DocBlock(string(b), "retire-one")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range headerVars(blk) {
		got[n] = true
	}
	for _, n := range []string{"OLD_OP_NS", "OLD_DEPLOY", "OLD_NS", "OLD_WEBHOOK", "R", "NAME", "NEW_NS"} {
		if !got[n] {
			t.Errorf("header variable %s not found in %v", n, got)
		}
	}
	if len(got) != 7 {
		t.Errorf("unexpected header variables: %v", got)
	}
}
