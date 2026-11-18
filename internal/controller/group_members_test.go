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
	"reflect"
	"testing"

	api "goauthentik.io/api/v3"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik/fakeauthentik"
)

// Usernames the member specs repeat.
const (
	memberAlice   = "alice"
	memberBob     = "bob"
	memberCarol   = "carol"
	memberDave    = "dave"
	memberWes     = "wes"
	memberZed     = "zed"
	tokenUsername = fakeauthentik.TokenUsername
)

func TestPlanMembers(t *testing.T) {
	user := func(pk int32, username string) api.PartialUser { return api.PartialUser{Pk: pk, Username: username} }
	pin := func(username string, pk int32) authentikiov1alpha1.GroupMemberStatus {
		return authentikiov1alpha1.GroupMemberStatus{Username: username, PK: pk}
	}
	const tokenPK = 1
	token := user(tokenPK, tokenUsername)

	cases := []struct {
		name string
		in   memberInputs
		want memberPlan
	}{
		{
			name: "adds a listed user that is not live and removes a live user that is not listed",
			in: memberInputs{Spec: []string{memberBob, memberAlice},
				Resolved: map[string]int32{memberAlice: 2, memberBob: 3},
				Live:     []api.PartialUser{user(4, "eve")}, TokenUserPK: tokenPK},
			want: memberPlan{Add: []api.PartialUser{user(2, memberAlice), user(3, memberBob)},
				Remove: []api.PartialUser{user(4, "eve")},
				Pins:   []authentikiov1alpha1.GroupMemberStatus{pin(memberAlice, 2), pin(memberBob, 3)}},
		},
		{
			name: "reports an unknown username and still plans every other change",
			in: memberInputs{Spec: []string{memberBob, "ghost"}, Resolved: map[string]int32{memberBob: 3},
				Live: []api.PartialUser{user(4, "eve")}, TokenUserPK: tokenPK},
			want: memberPlan{Add: []api.PartialUser{user(3, memberBob)}, Remove: []api.PartialUser{user(4, "eve")},
				NotFound: []string{"ghost"}, Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberBob, 3)}},
		},
		{
			name: "holds out a username whose resolved pk differs from its pin and keeps the old account",
			in: memberInputs{Spec: []string{memberCarol}, Resolved: map[string]int32{memberCarol: 11},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberCarol, 10)},
				Live: []api.PartialUser{user(10, memberCarol)}, TokenUserPK: tokenPK},
			want: memberPlan{IdentityChanged: []string{memberCarol},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberCarol, 10)}},
		},
		{
			name: "keeps the new account of an identity-changed username out of the group and leaves it alone if live",
			in: memberInputs{Spec: []string{memberCarol}, Resolved: map[string]int32{memberCarol: 11},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberCarol, 10)},
				Live: []api.PartialUser{user(11, memberCarol)}, TokenUserPK: tokenPK},
			want: memberPlan{IdentityChanged: []string{memberCarol},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberCarol, 10)}},
		},
		{
			name: "neither adds nor removes a held username and gives it no pin",
			in: memberInputs{Spec: []string{memberDave}, Resolved: map[string]int32{memberDave: 5},
				Held: map[string]bool{memberDave: true},
				Live: []api.PartialUser{user(5, memberDave)}, TokenUserPK: tokenPK},
			want: memberPlan{Held: []string{memberDave}},
		},
		{
			name: "keeps the pinned account of a held username whose pin differs from what it resolves to",
			in: memberInputs{Spec: []string{memberCarol}, Resolved: map[string]int32{memberCarol: 11},
				Held: map[string]bool{memberCarol: true}, Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberCarol, 10)},
				Live: []api.PartialUser{user(10, memberCarol)}, TokenUserPK: tokenPK},
			want: memberPlan{IdentityChanged: []string{memberCarol}, Held: []string{memberCarol},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberCarol, 10)}},
		},
		{
			name: "reports a held username with no account under Held only",
			in:   memberInputs{Spec: []string{memberDave}, Held: map[string]bool{memberDave: true}, TokenUserPK: tokenPK},
			want: memberPlan{Held: []string{memberDave}},
		},
		{
			name: "does not add a held username that is not yet a member",
			in: memberInputs{Spec: []string{memberDave}, Resolved: map[string]int32{memberDave: 5},
				Held:        map[string]bool{memberDave: true},
				TokenUserPK: tokenPK},
			want: memberPlan{Held: []string{memberDave}},
		},
		{
			name: "never removes the operator's own user and says so when it is not listed",
			in: memberInputs{Spec: []string{memberBob}, Resolved: map[string]int32{memberBob: 3},
				Live: []api.PartialUser{token, user(3, memberBob)}, TokenUserPK: tokenPK},
			want: memberPlan{TokenUserKept: true, Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberBob, 3)}},
		},
		{
			name: "does not call the operator's own user kept when the spec lists it",
			in: memberInputs{Spec: []string{tokenUsername}, Resolved: map[string]int32{tokenUsername: tokenPK},
				Live: []api.PartialUser{token}, TokenUserPK: tokenPK},
			want: memberPlan{Pins: []authentikiov1alpha1.GroupMemberStatus{pin(tokenUsername, tokenPK)}},
		},
		{
			name: "an empty spec removes every live member except the operator's own user",
			in:   memberInputs{Spec: []string{}, Live: []api.PartialUser{token, user(7, memberBob)}, TokenUserPK: tokenPK},
			want: memberPlan{Remove: []api.PartialUser{user(7, memberBob)}, TokenUserKept: true},
		},
		{
			name: "adds a new pin, drops the pin of a username no longer listed and keeps an existing pin",
			in: memberInputs{Spec: []string{memberBob, memberAlice}, Resolved: map[string]int32{memberAlice: 2, memberBob: 3},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin("gone", 9), pin(memberAlice, 2)},
				Live: []api.PartialUser{user(2, memberAlice)}, TokenUserPK: tokenPK},
			want: memberPlan{Add: []api.PartialUser{user(3, memberBob)},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin(memberAlice, 2), pin(memberBob, 3)}},
		},
		{
			name: "sorts every name list",
			in: memberInputs{Spec: []string{memberZed, "yan", "xia", memberWes},
				Resolved: map[string]int32{"xia": 3, memberZed: 6, memberWes: 7},
				Held:     map[string]bool{memberZed: true, memberWes: true}, TokenUserPK: tokenPK},
			want: memberPlan{Add: []api.PartialUser{user(3, "xia")}, NotFound: []string{"yan"},
				Held: []string{memberWes, memberZed},
				Pins: []authentikiov1alpha1.GroupMemberStatus{pin("xia", 3)}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := planMembers(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("planMembers = %+v, want %+v", got, c.want)
			}
		})
	}
}
