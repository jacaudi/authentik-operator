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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// flowEnrol is the slug of the enrolment flow the invitation links point at.
const flowEnrol = "enrol"

func TestInvitationLink(t *testing.T) { // Review Focus 4
	cases := []struct{ name, base, flow, pk, want string }{
		{"a path prefix and a trailing slash give no double slash", "https://sso.example.com/auth/", flowEnrol, "p-1",
			"https://sso.example.com/auth/if/flow/enrol/?itoken=p-1"},
		{"a bare host", "https://sso.example.com", flowEnrol, "p-1", "https://sso.example.com/if/flow/enrol/?itoken=p-1"},
		{"the token is query-escaped", "https://sso.example.com", flowEnrol, "a b",
			"https://sso.example.com/if/flow/enrol/?itoken=a+b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := invitationLink(c.base, c.flow, c.pk); got != c.want {
				t.Errorf("invitationLink = %q, want %q", got, c.want)
			}
		})
	}
}

func TestInvitationNaming(t *testing.T) {
	if got, want := invitationName("0b1c-uid", 2), "k8s-0b1c-uid-2"; got != want {
		t.Errorf("invitationName = %q, want %q", got, want)
	}
	user := func(inv *authentikiov1alpha1.InvitationSpec) *authentikiov1alpha1.AuthentikUser {
		return &authentikiov1alpha1.AuthentikUser{
			ObjectMeta: metav1.ObjectMeta{Name: "dave"},
			Spec:       authentikiov1alpha1.AuthentikUserSpec{Invitation: inv},
		}
	}
	attempts := []struct {
		name string
		inv  *authentikiov1alpha1.InvitationSpec
		want int32
	}{
		{"no invitation", nil, 1},
		{"attempt unset", &authentikiov1alpha1.InvitationSpec{}, 1},
		{"attempt 3", &authentikiov1alpha1.InvitationSpec{Attempt: 3}, 3},
	}
	for _, c := range attempts {
		if got := invitationAttempt(user(c.inv)); got != c.want {
			t.Errorf("invitationAttempt (%s) = %d, want %d", c.name, got, c.want)
		}
	}
	secrets := []struct {
		name string
		inv  *authentikiov1alpha1.InvitationSpec
		want string
	}{
		{"unset", &authentikiov1alpha1.InvitationSpec{}, "dave-invitation"},
		{"set", &authentikiov1alpha1.InvitationSpec{SecretName: "x"}, "x"},
	}
	for _, c := range secrets {
		if got := invitationSecretName(user(c.inv)); got != c.want {
			t.Errorf("invitationSecretName (%s) = %q, want %q", c.name, got, c.want)
		}
	}
}
