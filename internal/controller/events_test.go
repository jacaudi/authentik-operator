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

package controller

import (
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

func TestTruncateNote(t *testing.T) {
	cases := []struct {
		name, in string
		max      int
		want     string
	}{
		{"short text is unchanged", "hello", 16, "hello"},
		{"exactly the limit is unchanged", "0123456789", 10, "0123456789"},
		{"long ASCII is cut with an ellipsis inside the limit", "0123456789abcdef", 10, "0123456..."},
		{"a multibyte rune is never split", "aaaaaaaa\u00e9", 10, "aaaaaaaa\u00e9"},
		{"a cut inside a multibyte rune backs up to the rune start", "aaaaaa\u00e9\u00e9\u00e9", 10, "aaaaaa..."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := truncateNote(c.in, c.max)
			if got != c.want {
				t.Errorf("truncateNote(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
			}
			if len(got) > c.max {
				t.Errorf("result is %d bytes, over the limit %d", len(got), c.max)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result %q is not valid UTF-8", got)
			}
		})
	}
}

func TestDryRunReportAnnouncesOncePerChange(t *testing.T) {
	patch := map[string]any{jsonSubMode: "hashed_user_id"}
	live := map[string]any{jsonSubMode: "user_email"}

	msg, announce := dryRunReport(nil, `application "x"`, patch, live, []string{"subMode"})
	if !announce || !strings.Contains(msg, jsonSubMode) || !strings.Contains(msg, "user_email") {
		t.Fatalf("first report = %q, announce %t; want it announced with the field and old value", msg, announce)
	}
	conds := []metav1.Condition{{Type: ConditionReady, Reason: reasonAdoptionDryRun, Message: msg}}
	if _, again := dryRunReport(conds, `application "x"`, patch, live, []string{"subMode"}); again {
		t.Error("an identical report must not be announced again")
	}
	none, announce := dryRunReport(conds, `application "x"`, nil, live, nil)
	if !announce || !strings.Contains(none, "would change no fields") {
		t.Errorf("report with nothing to change = %q, announce %t", none, announce)
	}
}

func TestEmitCapsTheNoteAtTheAPILimitInBytes(t *testing.T) {
	rec := events.NewFakeRecorder(1)
	p := &authentikiov1alpha1.AuthentikOAuthProvider{}
	// 600 three-byte runes: 1800 bytes but only 600 characters, so a
	// character-based cap would let it through.
	long := strings.Repeat("\u20ac", 600)

	emit(rec, p, corev1.EventTypeNormal, "Test", "Test", "dry run: %s", long)

	got := <-rec.Events
	_, note, ok := strings.Cut(got, "Normal Test ")
	if !ok {
		t.Fatalf("unexpected event %q", got)
	}
	if len(note) > maxEventNoteBytes {
		t.Errorf("note is %d bytes, over the events.k8s.io limit %d", len(note), maxEventNoteBytes)
	}
	if !utf8.ValidString(note) || !strings.HasSuffix(note, "...") {
		t.Errorf("note %q is not valid UTF-8 ending in an ellipsis", note[:40])
	}
}
