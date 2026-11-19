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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
)

// Conditions and reasons of spec.access.
const (
	// ConditionAccessInSync reports whether the application's group bindings in
	// Authentik match spec.access. It is absent under type Unmanaged and when a
	// ReadOnly application has no spec.access. A binding problem never flips
	// Ready, except AccessPending (see syncAccess).
	ConditionAccessInSync = "AccessInSync"
	// ConditionAccessOpen is True when the application has no enabled policy
	// binding of any kind, so every user may open it unless the instance
	// setting is off.
	ConditionAccessOpen = "AccessOpen"

	reasonGroupNotFound    = "GroupNotFound"
	reasonAccessNarrowed   = "AccessNarrowed"
	reasonWaitingForGroups = "WaitingForGroups"
	reasonAccessPending    = "AccessPending"
	reasonNoBindings       = "NoBindings"
	reasonBound            = "Bound"

	// indexAccessGroup maps an AuthentikApplication to the group names it
	// lists in spec.access.groups.
	indexAccessGroup = ".spec.access.groups"
)

// accessGroups is what the Authentik names in spec.access.groups resolved to:
// the pk of every group found, by name, and the names that were not found.
type accessGroups struct {
	pks     map[string]string
	missing []string
}

// accessState is what syncApplication learned that the access sync needs: the
// client of the connection, the resolved groups, and whether this reconcile
// created (or recreated) the application.
type accessState struct {
	client  *authentik.Client
	groups  accessGroups
	created bool
}

// wantsGroups reports whether the application lists groups to bind.
func wantsGroups(a *authentikiov1alpha1.AuthentikApplication) bool {
	return a.Spec.Access != nil && a.Spec.Access.Type == authentikiov1alpha1.AccessTypeGroups
}

// resolveAccessGroups looks every name up in Authentik. A name that is not
// there is reported in missing; any other failure is returned, because an
// unreadable group is not a missing one.
func resolveAccessGroups(ctx context.Context, c *authentik.Client, names []string) (accessGroups, error) {
	groups := accessGroups{pks: make(map[string]string, len(names))}
	for _, name := range names {
		g, err := c.FindGroupByName(ctx, name)
		switch {
		case err == nil:
			groups.pks[name] = g.Pk
		case authentik.IsNotFound(err):
			groups.missing = append(groups.missing, name)
		default:
			return accessGroups{}, fmt.Errorf("resolve group %q: %w", name, err)
		}
	}
	return groups, nil
}

// requireGroup refuses a write that could leave an application with no group
// binding and so open to every user: it is the guard on every create and every
// takeover. It passes when the application does not list groups, or when at
// least one listed group resolved.
func requireGroup(a *authentikiov1alpha1.AuthentikApplication, st *accessState) error {
	if !wantsGroups(a) || len(st.groups.pks) > 0 {
		return nil
	}
	return condErrf(reasonWaitingForGroups, "none of the groups in spec.access.groups exist in Authentik yet (%s); "+
		"the application is not created or taken over until one does, so it is never open to every user",
		quoted(a.Spec.Access.Groups))
}

// bindingGroupPK is the pk of the group a binding is for, or "".
func bindingGroupPK(b *api.PolicyBinding) string {
	if pk := b.Group.Get(); pk != nil {
		return *pk
	}
	return ""
}

// bindingGroupName is the name of the group a binding is for (its pk when
// Authentik did not expand it).
func bindingGroupName(b *api.PolicyBinding) string {
	if g := b.GroupObj.Get(); g != nil {
		return g.Name
	}
	return bindingGroupPK(b)
}

// bindingEnabled reports whether a binding is enabled; an unset flag is enabled.
func bindingEnabled(b *api.PolicyBinding) bool { return b.Enabled == nil || *b.Enabled }

// managedBinding reports whether the operator owns a binding: one for a group,
// not negated and not expiring.
func managedBinding(b *api.PolicyBinding) bool {
	return bindingGroupPK(b) != "" && !b.GetNegate() && !b.Expiring
}

// describeBinding renders a binding for status and messages: group <name>,
// user <username> or policy <name>, then (negated), (expiring), (disabled).
func describeBinding(b api.PolicyBinding) string {
	var s string
	switch {
	case bindingGroupPK(&b) != "":
		s = "group " + bindingGroupName(&b)
	case b.User.Get() != nil:
		s = fmt.Sprintf("user %d", *b.User.Get())
		if u := b.UserObj.Get(); u != nil {
			s = "user " + u.Username
		}
	case b.Policy.Get() != nil:
		s = "policy " + *b.Policy.Get()
		if p := b.PolicyObj.Get(); p != nil {
			s = "policy " + p.Name
		}
	default:
		s = "binding " + b.Pk
	}
	if b.GetNegate() {
		s += " (negated)"
	}
	if b.Expiring {
		s += " (expiring)"
	}
	if !bindingEnabled(&b) {
		s += " (disabled)"
	}
	return s
}

// describeBindings renders every binding, sorted.
func describeBindings(bs []api.PolicyBinding) []string {
	out := make([]string, len(bs))
	for i := range bs {
		out[i] = describeBinding(bs[i])
	}
	slices.Sort(out)
	return out
}

// bindingPlan is what has to change for an application's group bindings to be
// exactly its listed groups. Add holds group names, ordered by name; they take
// the orders NextOrder, NextOrder+1, and so on.
type bindingPlan struct {
	Add       []string
	Enable    []api.PolicyBinding
	Remove    []api.PolicyBinding
	NextOrder int32
}

// empty reports whether the plan changes nothing.
func (p bindingPlan) empty() bool { return len(p.Add)+len(p.Enable)+len(p.Remove) == 0 }

// cmpBinding orders bindings by order, then pk.
func cmpBinding(a, b api.PolicyBinding) int {
	return cmp.Or(cmp.Compare(a.Order, b.Order), cmp.Compare(a.Pk, b.Pk))
}

// planBindings works out the changes that make the group bindings of an
// application the listed groups. A binding is managed when it is for a group
// and neither negated nor expiring. Of the managed bindings of a listed group
// the lowest order (then lowest pk) is kept, enabled; the rest, and those of
// unlisted groups, are removed. A negated group binding depends on the
// application's live policy engine mode: under any it admits every user outside
// its group, so it is removed; under all it excludes its group, so it is left
// alone. Expiring, user and policy bindings are never touched. While a listed
// group did not resolve the picture is incomplete and nothing is removed.
func planBindings(live []api.PolicyBinding, groups accessGroups, mode api.PolicyEngineMode) bindingPlan {
	var plan bindingPlan
	for i := range live {
		plan.NextOrder = max(plan.NextOrder, live[i].Order+1)
	}
	listed := make(map[string]bool, len(groups.pks))
	for _, pk := range groups.pks {
		listed[pk] = true
	}
	kept := map[string]bool{}
	sorted := slices.SortedFunc(slices.Values(live), cmpBinding)
	for i := range sorted {
		b := &sorted[i]
		switch pk := bindingGroupPK(b); {
		case pk == "" || b.Expiring:
		case b.GetNegate():
			if mode != api.POLICYENGINEMODE_ALL {
				plan.Remove = append(plan.Remove, *b)
			}
		case !listed[pk] || kept[pk]:
			plan.Remove = append(plan.Remove, *b)
		default:
			kept[pk] = true
			if !bindingEnabled(b) {
				plan.Enable = append(plan.Enable, *b)
			}
		}
	}
	for name, pk := range groups.pks {
		if !kept[pk] {
			plan.Add = append(plan.Add, name)
		}
	}
	slices.Sort(plan.Add)
	if len(groups.missing) > 0 {
		plan.Remove = nil
	}
	return plan
}

// groupNames is the sorted, distinct group names a set of bindings is for.
func groupNames(bs []api.PolicyBinding) []string {
	names := make([]string, len(bs))
	for i := range bs {
		names[i] = bindingGroupName(&bs[i])
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// toBind is the sorted names of the groups the plan binds or re-enables.
func (p bindingPlan) toBind() []string {
	names := slices.Concat(p.Add, groupNames(p.Enable))
	slices.Sort(names)
	return names
}

// joinOrNone joins items, or says none.
func joinOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

// grantsAccess describes the bindings the operator never changes and that let
// a user in whatever spec.access lists: user, policy and expiring bindings.
func grantsAccess(live []api.PolicyBinding) string {
	var others []api.PolicyBinding
	for i := range live {
		if bindingGroupPK(&live[i]) == "" || live[i].Expiring {
			others = append(others, live[i])
		}
	}
	return joinOrNone(describeBindings(others))
}

// accessDryRun is the access part of the adoption dry-run message, built from
// the application's live bindings. mode is the policy engine mode the
// application would have once taken over.
func accessDryRun(a *authentikiov1alpha1.AuthentikApplication, st *accessState, live []api.PolicyBinding,
	mode api.PolicyEngineMode) string {
	switch {
	case a.Spec.Access == nil:
		return ""
	case !wantsGroups(a):
		return "; access: unmanaged; also grants access: " + joinOrNone(describeBindings(live))
	case len(st.groups.missing) > 0:
		return fmt.Sprintf("; access: groups not found [%s], nothing would be removed; also grants access: %s",
			quoted(st.groups.missing), grantsAccess(live))
	}
	plan := planBindings(live, st.groups, mode)
	return fmt.Sprintf("; access: would bind groups [%s]; would remove group bindings [%s]; also grants access: %s",
		quoted(plan.toBind()), quoted(groupNames(plan.Remove)), grantsAccess(live))
}

// setAccessCondition records one of the access conditions.
func setAccessCondition(a *authentikiov1alpha1.AuthentikApplication, condType string, status metav1.ConditionStatus,
	reason, msg string) {
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type: condType, Status: status, Reason: reason, Message: msg, ObservedGeneration: a.Generation,
	})
}

// accessWrites is what applying a plan did.
type accessWrites struct {
	bound, unbound []string
	attempted      bool
	err            error
}

// writeBindings applies the plan: bindings are added first, then re-enabled,
// then removed, and the first failed write stops the rest. A deletion is
// re-checked against appPK by the client.
func writeBindings(ctx context.Context, st accessState, appPK string, plan bindingPlan) (w accessWrites) {
	order := plan.NextOrder
	for _, name := range plan.Add {
		w.attempted = true
		req := api.NewPolicyBindingRequest(appPK, order)
		req.SetGroup(st.groups.pks[name])
		req.SetEnabled(true)
		req.SetNegate(false)
		if _, err := st.client.CreateBinding(ctx, *req); err != nil {
			w.err = fmt.Errorf("bind group %q: %w", name, err)
			return w
		}
		order++
		w.bound = append(w.bound, name)
	}
	for i := range plan.Enable {
		b := &plan.Enable[i]
		w.attempted = true
		patch := api.NewPatchedPolicyBindingRequest()
		patch.SetEnabled(true)
		if _, err := st.client.PatchBinding(ctx, b.Pk, *patch); err != nil {
			w.err = fmt.Errorf("enable the binding of group %q: %w", bindingGroupName(b), err)
			return w
		}
		w.bound = append(w.bound, bindingGroupName(b))
	}
	for i := range plan.Remove {
		b := &plan.Remove[i]
		w.attempted = true
		if err := st.client.DeleteBinding(ctx, b, appPK); err != nil {
			w.err = fmt.Errorf("remove the binding %q: %w", describeBinding(*b), err)
			return w
		}
		w.unbound = append(w.unbound, bindingGroupName(b))
	}
	return w
}

// syncAccess brings the application's group bindings to spec.access and
// records the result: the bindings in status, AccessOpen, and AccessInSync.
// An access problem never flips Ready, except AccessPending: an application
// created now whose bindings cannot be read, and one that has no binding of its
// listed groups yet. In those cases Ready is set to False here and readyHeld is
// true, so the caller does not set Ready to True first: setting a condition to
// True and back to False within one reconcile stamps a new transition time and
// makes every reconcile patch the status, which re-enqueues the object at once.
// It returns a result that is not empty when the reconcile has to come back
// sooner, and an error to back off on.
func (r *AuthentikApplicationReconciler) syncAccess(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication, got *api.Application,
	st accessState) (res ctrl.Result, readyHeld bool, err error) {
	live, err := st.client.ListBindings(ctx, got.Pk)
	if err != nil {
		return ctrl.Result{}, st.created, r.accessReadFailed(a, st, err)
	}
	mode := got.GetPolicyEngineMode()
	var w accessWrites
	switch {
	case !wantsGroups(a):
		meta.RemoveStatusCondition(&a.Status.Conditions, ConditionAccessInSync)
	case readOnly(a.Spec.ManagementPolicy):
		reportReadOnlyAccess(a, planBindings(live, st.groups, mode), st.groups)
	default:
		w = r.applyBindings(ctx, a, st, got.Pk, planBindings(live, st.groups, mode))
		if w.attempted {
			// The status and AccessOpen describe what Authentik holds now,
			// never the list read before the writes.
			if live, err = st.client.ListBindings(ctx, got.Pk); err != nil {
				return ctrl.Result{}, st.created, errors.Join(w.err, r.accessReadFailed(a, st, err))
			}
		}
	}
	recordBindings(a, live)
	if !wantsGroups(a) || readOnly(a.Spec.ManagementPolicy) {
		return ctrl.Result{}, false, nil
	}
	return r.finishAccess(a, st, live, mode, w)
}

// accessReadFailed records that the live bindings could not be read and returns
// the error to back off on. A just-created application whose bindings cannot be
// read may be open to every user, which Ready says.
func (r *AuthentikApplicationReconciler) accessReadFailed(a *authentikiov1alpha1.AuthentikApplication,
	st accessState, err error) error {
	err = fmt.Errorf("list policy bindings: %w", err)
	switch {
	case st.created:
		r.setReady(a, metav1.ConditionFalse, reasonAccessPending, "the policy bindings of the new application could "+
			"not be read yet; until they can be, every user may open it unless the instance setting is off")
	case wantsGroups(a):
		setAccessCondition(a, ConditionAccessInSync, metav1.ConditionFalse, reasonAuthentikError, err.Error())
	}
	return err
}

const accessPendingMessage = "waiting for the first binding of spec.access.groups; until it exists every user " +
	"may open the application unless the instance setting is off"

// finishAccess records the outcome of applying the plan to a Groups
// application that is not ReadOnly. readyHeld is true when it set Ready.
func (r *AuthentikApplicationReconciler) finishAccess(a *authentikiov1alpha1.AuthentikApplication, st accessState,
	live []api.PolicyBinding, mode api.PolicyEngineMode, w accessWrites) (res ctrl.Result, readyHeld bool, err error) {
	switch {
	case (st.created || a.Status.Provenance == authentikiov1alpha1.ProvenanceCreated) &&
		len(st.groups.pks) > 0 && !holdsManagedBinding(live, st.groups):
		r.setReady(a, metav1.ConditionFalse, reasonAccessPending, accessPendingMessage)
		if w.err == nil {
			return ctrl.Result{RequeueAfter: userErrorRecheck}, true, nil
		}
		setAccessCondition(a, ConditionAccessInSync, metav1.ConditionFalse, reasonAuthentikError, w.err.Error())
		r.event(a, corev1.EventTypeWarning, "AccessBindingFailed", "Bind",
			"Could not bind the groups of spec.access to the application: %v", w.err)
		return ctrl.Result{}, true, w.err
	case w.err != nil:
		setAccessCondition(a, ConditionAccessInSync, metav1.ConditionFalse, reasonAuthentikError, w.err.Error())
		return ctrl.Result{}, false, w.err
	}
	reason, msg, inSync := accessOutcome(a, st.groups, live, mode)
	status := metav1.ConditionTrue
	if !inSync {
		status = metav1.ConditionFalse
	}
	setAccessCondition(a, ConditionAccessInSync, status, reason, msg)
	return ctrl.Result{}, false, nil
}

// accessOutcome is the AccessInSync condition once the writes succeeded: the
// groups that did not resolve, then what the live policy engine mode narrows,
// else in sync.
func accessOutcome(a *authentikiov1alpha1.AuthentikApplication, groups accessGroups, live []api.PolicyBinding,
	mode api.PolicyEngineMode) (reason, msg string, inSync bool) {
	if len(groups.missing) > 0 {
		return reasonGroupNotFound, fmt.Sprintf("groups not found: [%s]; no binding is removed until every listed "+
			"group exists", quoted(groups.missing)), false
	}
	if narrowed := narrowing(a, groups, live, mode); len(narrowed) > 0 {
		return reasonAccessNarrowed, strings.Join(narrowed, "; "), false
	}
	return reasonInSync, "the group bindings of the application match spec.access", true
}

// narrowing says where the live policy engine mode all makes access narrower
// than the list reads, which the operator leaves alone (changing it would
// widen access): more than one listed group, and a negated binding of a listed
// group.
func narrowing(a *authentikiov1alpha1.AuthentikApplication, groups accessGroups, live []api.PolicyBinding,
	mode api.PolicyEngineMode) []string {
	if mode != api.POLICYENGINEMODE_ALL {
		return nil
	}
	var out []string
	if n := len(a.Spec.Access.Groups); n > 1 {
		out = append(out, fmt.Sprintf("the live policyEngineMode is all, so a user needs membership of every one of "+
			"the %d listed groups; set spec.policyEngineMode to any to grant access per group", n))
	}
	listed := map[string]bool{}
	for _, pk := range groups.pks {
		listed[pk] = true
	}
	var excluded []api.PolicyBinding
	for i := range live {
		if b := &live[i]; listed[bindingGroupPK(b)] && b.GetNegate() && !b.Expiring {
			excluded = append(excluded, *b)
		}
	}
	if len(excluded) > 0 {
		out = append(out, "the live policyEngineMode is all and a binding excludes a listed group: "+
			strings.Join(describeBindings(excluded), ", ")+"; the operator leaves it, remove it in Authentik to "+
			"grant that group access")
	}
	return out
}

// holdsManagedBinding reports whether live has a managed binding of a listed
// group.
func holdsManagedBinding(live []api.PolicyBinding, groups accessGroups) bool {
	listed := map[string]bool{}
	for _, pk := range groups.pks {
		listed[pk] = true
	}
	return slices.ContainsFunc(live, func(b api.PolicyBinding) bool {
		return managedBinding(&b) && listed[bindingGroupPK(&b)]
	})
}

// applyBindings writes the plan and announces what was bound and unbound.
func (r *AuthentikApplicationReconciler) applyBindings(ctx context.Context,
	a *authentikiov1alpha1.AuthentikApplication, st accessState, appPK string, plan bindingPlan) accessWrites {
	w := writeBindings(ctx, st, appPK, plan)
	if len(w.bound)+len(w.unbound) > 0 {
		logf.FromContext(ctx).Info("Updated Application access", "bound", w.bound, "unbound", w.unbound)
		r.event(a, corev1.EventTypeNormal, "AccessUpdated", "Update", "Updated application access: bound groups [%s]; "+
			"unbound groups [%s]", quoted(w.bound), quoted(w.unbound))
	}
	return w
}

// reportReadOnlyAccess sets AccessInSync for a ReadOnly application that lists
// groups: what binding them would take, writing nothing. A group that did not
// resolve makes the picture incomplete, so no removal is claimed.
func reportReadOnlyAccess(a *authentikiov1alpha1.AuthentikApplication, plan bindingPlan, groups accessGroups) {
	bind := fmt.Sprintf("would bind or re-enable groups [%s]", quoted(plan.toBind()))
	switch {
	case len(groups.missing) > 0:
		msg := fmt.Sprintf("groups not found: [%s]", quoted(groups.missing))
		if len(plan.Add)+len(plan.Enable) > 0 {
			msg += "; " + bind
		}
		setAccessCondition(a, ConditionAccessInSync, metav1.ConditionFalse, reasonGroupNotFound, msg)
	case !plan.empty():
		setAccessCondition(a, ConditionAccessInSync, metav1.ConditionFalse, reasonDriftDetected,
			fmt.Sprintf("the bindings in Authentik differ from spec.access: %s; would remove group bindings [%s]",
				bind, quoted(groupNames(plan.Remove))))
	default:
		setAccessCondition(a, ConditionAccessInSync, metav1.ConditionTrue, reasonInSync,
			"the group bindings of the application match spec.access")
	}
}

// recordBindings sets status.access.bindings and AccessOpen from the live
// bindings.
func recordBindings(a *authentikiov1alpha1.AuthentikApplication, live []api.PolicyBinding) {
	a.Status.Access = nil
	if len(live) > 0 {
		a.Status.Access = &authentikiov1alpha1.ApplicationAccessStatus{Bindings: describeBindings(live)}
	}
	if slices.ContainsFunc(live, func(b api.PolicyBinding) bool { return bindingEnabled(&b) }) {
		setAccessCondition(a, ConditionAccessOpen, metav1.ConditionFalse, reasonBound,
			"the application has an enabled policy binding")
		return
	}
	setAccessCondition(a, ConditionAccessOpen, metav1.ConditionTrue, reasonNoBindings,
		"the application has no enabled policy binding, so every user may open it unless the instance setting "+
			`"Applications with no policies bound can be accessed by any user" is off`)
}

// accessGroupIndex indexes applications by the group names they list.
func accessGroupIndex(o client.Object) []string {
	a, ok := o.(*authentikiov1alpha1.AuthentikApplication)
	if !ok || !wantsGroups(a) {
		return nil
	}
	return a.Spec.Access.Groups
}

// applicationsForGroup maps a group to the applications that list its name in
// Authentik, which is spec.name, not necessarily metadata.name.
func (r *AuthentikApplicationReconciler) applicationsForGroup(ctx context.Context,
	o client.Object) []reconcile.Request {
	g, ok := o.(*authentikiov1alpha1.AuthentikGroup)
	if !ok {
		return nil
	}
	var list authentikiov1alpha1.AuthentikApplicationList
	if err := r.List(ctx, &list, client.MatchingFields{indexAccessGroup: groupName(g)}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list AuthentikApplications for group",
			"group", client.ObjectKeyFromObject(o))
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
