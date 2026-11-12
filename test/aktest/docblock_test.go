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
