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
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	api "goauthentik.io/api/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
)

const (
	// ConditionMembersInSync reports whether the group's membership matches
	// spec.members. It is absent when spec.members is, and under ReadOnly. A
	// members problem never makes Ready false: the group exists and the other
	// members are correct.
	ConditionMembersInSync = "MembersInSync"

	// reasonMemberIdentityChanged, reasonMemberNotFound and reasonMemberHeld are
	// the False reasons of MembersInSync, in the order they take priority.
	reasonMemberIdentityChanged = "MemberIdentityChanged"
	reasonMemberNotFound        = "MemberNotFound"
	reasonMemberHeld            = "MemberHeld"
)

// memberInputs is what planMembers decides from: the usernames the spec lists,
// the pk each resolved to (a username with no entry does not exist), who is in
// the group now, the pins in status.members, the usernames to hold out, and the
// operator token's own user.
type memberInputs struct {
	Spec        []string
	Resolved    map[string]int32
	Live        []api.PartialUser
	Pins        []authentikiov1alpha1.GroupMemberStatus
	Held        map[string]bool
	TokenUserPK int32
}

// memberPlan is what to change and what to report. Every list is sorted by
// username.
type memberPlan struct {
	// Add and Remove are the users to add to the group and take out of it.
	Add, Remove []api.PartialUser
	// NotFound, IdentityChanged and Held name the listed usernames that are
	// reported instead of applied.
	NotFound, IdentityChanged, Held []string
	// Pins is the new status.members.
	Pins []authentikiov1alpha1.GroupMemberStatus
	// TokenUserKept is set when the operator's own user is in the group, though
	// the spec does not list it, and so is not removed.
	TokenUserKept bool
}

// planMembers works out which users to add and remove so the group holds
// exactly the listed usernames that can be applied. A listed username is
// applied when it resolved, is not held, and still resolves to the user it was
// pinned to; one that fails a check is reported and left as it is (a held or
// identity-changed username's users stay members, so a removal never rides on
// a lookup that went wrong; the pinned account of an identity-changed username
// stays too, held or not). A held username that does not resolve is Held, not
// NotFound. The operator's own user is never removed.
func planMembers(in memberInputs) memberPlan {
	var plan memberPlan
	pins := make(map[string]int32, len(in.Pins))
	for _, p := range in.Pins {
		pins[p.Username] = p.PK
	}
	live := make(map[int32]bool, len(in.Live))
	for _, u := range in.Live {
		live[u.Pk] = true
	}
	keep := map[int32]bool{in.TokenUserPK: true}
	names := slices.Compact(slices.Sorted(slices.Values(in.Spec)))
	for _, name := range names {
		pk, resolved := in.Resolved[name]
		pinned, hasPin := pins[name]
		held := in.Held[name]
		if held {
			plan.Held = append(plan.Held, name)
		}
		switch {
		case resolved && hasPin && pinned != pk:
			plan.IdentityChanged = append(plan.IdentityChanged, name)
			keep[pk], keep[pinned] = true, true
		case held:
			// A held username with no account yet is reported as held, not as missing.
			if resolved {
				keep[pk] = true
			}
		case !resolved:
			plan.NotFound = append(plan.NotFound, name)
		default:
			keep[pk] = true
			if !live[pk] {
				plan.Add = append(plan.Add, api.PartialUser{Pk: pk, Username: name})
			}
			if !hasPin {
				plan.Pins = append(plan.Pins, authentikiov1alpha1.GroupMemberStatus{Username: name, PK: pk})
			}
		}
		if hasPin {
			plan.Pins = append(plan.Pins, authentikiov1alpha1.GroupMemberStatus{Username: name, PK: pinned})
		}
	}
	plan.Remove, plan.TokenUserKept = unlisted(in, names, keep)
	return plan
}

// unlisted is the live users not in keep, sorted by username, and whether the
// operator's own user is live though names does not list it.
func unlisted(in memberInputs, names []string, keep map[int32]bool) (remove []api.PartialUser, tokenKept bool) {
	for _, u := range in.Live {
		if !keep[u.Pk] {
			remove = append(remove, u)
		}
		if u.Pk == in.TokenUserPK && !slices.Contains(names, u.Username) {
			tokenKept = true
		}
	}
	slices.SortFunc(remove, func(a, b api.PartialUser) int { return cmp.Compare(a.Username, b.Username) })
	return remove, tokenKept
}

// observeMembers reads what planMembers needs from Authentik and returns the
// plan, with the operator's own username for the report. A username Authentik
// does not know is left unresolved; any other failure is returned. The group is
// read again for its members: the adoption target was found by a lookup that
// does not carry them. A username an AuthentikUser is inviting is held out (see
// heldUsernames).
func (r *AuthentikGroupReconciler) observeMembers(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup, pk string,
	pins []authentikiov1alpha1.GroupMemberStatus) (memberPlan, string, error) {
	spec := *g.Spec.Members
	resolved := make(map[string]int32, len(spec))
	// ponytail: one lookup per listed username on every reconcile; list the users once if a group outgrows that.
	for _, name := range spec {
		user, err := c.FindUserByUsername(ctx, name)
		switch {
		case err == nil:
			resolved[name] = user.Pk
		case authentik.IsNotFound(err):
			// Reported as MemberNotFound.
		default:
			return memberPlan{}, "", fmt.Errorf("look up user %q: %w", name, err)
		}
	}
	group, err := c.GetGroup(ctx, pk)
	if err != nil {
		return memberPlan{}, "", fmt.Errorf("read the members of group %q: %w", pk, err)
	}
	me, err := c.Me(ctx)
	if err != nil {
		return memberPlan{}, "", fmt.Errorf("read the operator's own user: %w", err)
	}
	held, err := r.heldUsernames(ctx, g, spec)
	if err != nil {
		return memberPlan{}, "", err
	}
	return planMembers(memberInputs{Spec: spec, Resolved: resolved, Live: group.UsersObj, Pins: pins, Held: held,
		TokenUserPK: me.Pk}), me.Username, nil
}

// heldUsernames is the subset of usernames that an AuthentikUser, in any
// namespace, is inviting: the person has no account yet, so the group neither
// adds nor removes the username until the account is recognised. A resource
// inviting a username holds it only while it is on the same Authentik as g,
// passes the identity gate for its own namespace and ref, has a policy that
// creates (Create or AdoptOrCreate), has no provenance yet, and has an
// invitation in status; an adopted, ReadOnly or gated-out resource holds nobody.
func (r *AuthentikGroupReconciler) heldUsernames(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup,
	usernames []string) (map[string]bool, error) {
	var held map[string]bool
	for _, name := range usernames {
		var list authentikiov1alpha1.AuthentikUserList
		if err := r.List(ctx, &list, client.MatchingFields{indexUserUsername: name}); err != nil {
			return nil, fmt.Errorf("list AuthentikUsers named %q: %w", name, err)
		}
		for i := range list.Items {
			holds, err := r.holdsUsername(ctx, g, &list.Items[i])
			if err != nil {
				return nil, err
			}
			if holds {
				if held == nil {
					held = map[string]bool{}
				}
				held[name] = true
				break
			}
		}
	}
	return held, nil
}

// holdsUsername reports whether u is inviting its username for the benefit of g
// (see heldUsernames). The cheap checks on u itself come before the ones that
// read the cluster.
func (r *AuthentikGroupReconciler) holdsUsername(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup,
	u *authentikiov1alpha1.AuthentikUser) (bool, error) {
	if u.Status.Invitation == nil || u.Status.Provenance != "" || !u.Spec.ManagementPolicy.MayCreate() {
		return false, nil
	}
	same, err := sameConnection(ctx, r.Connections, groupClaim(g), userClaim(u))
	if err != nil || !same {
		return false, err
	}
	return identityGate{r.APIReader, r.IdentityNamespaceSelector}.allowed(ctx, u.Namespace, u.Spec.AuthentikRef)
}

// syncMembers makes the group's membership match spec.members: every add_user
// before any remove_user, never a users list in a create or PATCH. It reports
// the outcome in MembersInSync (a failed write as AuthentikError, without
// stopping the other writes) and pins the usernames in status.members. An
// absent spec.members is neither read nor written and leaves no condition or
// pin behind; under ReadOnly, which never writes, only the pins remain.
func (r *AuthentikGroupReconciler) syncMembers(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup, group *api.Group) error {
	if g.Spec.Members == nil {
		meta.RemoveStatusCondition(&g.Status.Conditions, ConditionMembersInSync)
		g.Status.Members = nil
		return nil
	}
	if readOnly(g.Spec.ManagementPolicy) {
		// ReadOnly reports drift instead. The pins stay: they record which account a username meant.
		meta.RemoveStatusCondition(&g.Status.Conditions, ConditionMembersInSync)
		return nil
	}
	plan, tokenUser, err := r.observeMembers(ctx, c, g, group.Pk, g.Status.Members)
	if err != nil {
		return err
	}
	// A failed write never stops the others: a person removed in Git must lose membership although
	// an unrelated add_user keeps failing. The failures are reported in MembersInSync.
	var added, removed []string
	var failed error
	for _, u := range plan.Add {
		if err := c.AddUserToGroup(ctx, group.Pk, u.Pk); err != nil {
			failed = errors.Join(failed, fmt.Errorf("add user %q to group %q: %w", u.Username, group.Name, err))
			continue
		}
		added = append(added, u.Username)
	}
	for _, u := range plan.Remove {
		if err := c.RemoveUserFromGroup(ctx, group.Pk, u.Pk); err != nil {
			failed = errors.Join(failed, fmt.Errorf("remove user %q from group %q: %w", u.Username, group.Name, err))
			continue
		}
		removed = append(removed, u.Username)
	}
	g.Status.Members = plan.Pins
	meta.SetStatusCondition(&g.Status.Conditions, membersCondition(g.Generation, plan, tokenUser, failed))
	if len(added)+len(removed) > 0 {
		logf.FromContext(ctx).Info("Updated Group members", "name", group.Name, "pk", group.Pk,
			"added", added, "removed", removed)
		r.event(g, corev1.EventTypeNormal, "MembersUpdated", "Update",
			"Updated members of group %q: added [%s], removed [%s]", group.Name,
			strings.Join(added, ", "), strings.Join(removed, ", "))
	}
	return nil
}

// membersCondition is MembersInSync after plan's adds and removes were applied:
// True when nothing is held back, else False under the highest-priority reason
// with a message listing every category that has a name. A write that failed
// (failed, non-nil) outranks them all, as AuthentikError, and its message
// comes first.
func membersCondition(generation int64, plan memberPlan, tokenUser string, failed error) metav1.Condition {
	cond := metav1.Condition{Type: ConditionMembersInSync, Status: metav1.ConditionTrue, Reason: reasonInSync,
		Message: "members match spec.members", ObservedGeneration: generation}
	if plan.TokenUserKept {
		cond.Message += fmt.Sprintf("; the operator's own user %q is a member and is never removed", tokenUser)
	}
	var parts []string
	for _, problem := range []struct {
		reason, what string
		names        []string
	}{
		{reasonMemberIdentityChanged, "identity changed (pk differs from status.members; remove the entry from " +
			"status.members to accept the new account)", plan.IdentityChanged},
		{reasonMemberNotFound, "not found", plan.NotFound},
		{reasonMemberHeld, "held while an AuthentikUser invitation is pending", plan.Held},
	} {
		if len(problem.names) == 0 {
			continue
		}
		if cond.Status == metav1.ConditionTrue {
			cond.Status, cond.Reason = metav1.ConditionFalse, problem.reason
		}
		parts = append(parts, fmt.Sprintf("%s: [%s]", problem.what, quoted(problem.names)))
	}
	if failed != nil {
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonAuthentikError
		parts = append([]string{strings.ReplaceAll(failed.Error(), "\n", "; ")}, parts...)
	}
	if len(parts) > 0 {
		cond.Message = strings.Join(parts, "; ")
	}
	return cond
}

// membersReport is what applying plan would change, for a dry run or a
// ReadOnly drift message: "would add [...], would remove [...]", usernames quoted.
func membersReport(plan memberPlan) string {
	return fmt.Sprintf("would add [%s], would remove [%s]", quoted(usernames(plan.Add)), quoted(usernames(plan.Remove)))
}

// usernames lists the usernames of users, in order.
func usernames(users []api.PartialUser) []string {
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Username
	}
	return names
}

// quoted renders names as `"a", "b"`.
func quoted(names []string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(parts, ", ")
}

// recordGroupDrift sets Drifted for a ReadOnly group from the fields that
// differ and, when spec.members is present, the members that would change. It
// reads membership, never writes it.
func (r *AuthentikGroupReconciler) recordGroupDrift(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup, live *api.Group, plan groupChanges) error {
	var diffs []string
	if len(plan.changed) > 0 {
		patch, current := plan.reportView(live)
		diffs = append(diffs, describeChanges(patch, current))
	}
	if g.Spec.Members != nil {
		members, _, err := r.observeMembers(ctx, c, g, live.Pk, g.Status.Members)
		if err != nil {
			return err
		}
		if len(members.Add)+len(members.Remove) > 0 {
			diffs = append(diffs, "members: "+membersReport(members))
		}
	}
	setDrift(&g.Status.Conditions, g.Generation, diffs...)
	return nil
}
