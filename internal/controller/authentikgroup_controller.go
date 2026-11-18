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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	api "goauthentik.io/api/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/adopt"
	"github.com/jacaudi/authentik-operator/internal/authentik"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

const (
	// reasonDeletionBlocked marks Ready=False on a group whose deletion waits
	// for the policy bindings that still use it.
	reasonDeletionBlocked = "DeletionBlocked"

	// indexGroupPK maps an AuthentikGroup to the group pk it tracks (see claims.go).
	indexGroupPK = ".status.groupPk"

	// indexGroupName maps an AuthentikGroup to its name in Authentik, which
	// spec.access.groups of an application refers to.
	indexGroupName = ".spec.name"
)

// policyBindingApp and policyBindingModel identify a policy binding among the
// objects Authentik reports as using a group.
const (
	policyBindingApp   = "authentik_policies"
	policyBindingModel = "policybinding"
)

// AuthentikGroupReconciler reconciles a AuthentikGroup object.
type AuthentikGroupReconciler struct {
	client.Client

	Scheme *runtime.Scheme
	// Connections resolves the Authentik client for spec.authentikRef.
	Connections *connection.Resolver
	// Recorder emits Kubernetes events; may be nil in tests.
	Recorder events.EventRecorder
	// APIReader reads Namespaces uncached (the operator has get, not
	// list/watch, on Namespaces); use the manager's API reader.
	APIReader client.Reader
	// AdoptionNamespaceSelector picks the namespaces whose resources may
	// adopt or observe existing objects on the default connection. nil denies
	// every namespace. See adoptionGate.
	AdoptionNamespaceSelector labels.Selector
	// IdentityNamespaceSelector picks the namespaces whose resources the
	// operator reconciles on the default connection, under every
	// managementPolicy. nil denies every namespace. See identityGate.
	IdentityNamespaceSelector labels.Selector
}

// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get
// +kubebuilder:rbac:groups=authentik.io,resources=authentikgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentik.io,resources=authentikgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=authentik.io,resources=authentikgroups/finalizers,verbs=update
// +kubebuilder:rbac:groups=authentik.io,resources=authentikapplications,verbs=get;list;watch
// +kubebuilder:rbac:groups=authentik.io,resources=authentikdeployments,verbs=get
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile makes the group in Authentik match the spec.
//
// Identity: status.groupPk (Authentik's UUID pk) plus status.name. The pk is
// kept until a replacement exists, so a transient 404 cannot lose track of it.
// The policy flow (create, adopt, observe, recover, recreate) is adopt.Decide;
// a create whose outcome was lost is recovered only by the ownership proof the
// create request wrote into the group's attributes.
func (r *AuthentikGroupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var g authentikiov1alpha1.AuthentikGroup
	if err := r.Get(ctx, req.NamespacedName, &g); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !g.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &g)
	}
	base := g.DeepCopy()
	if controllerutil.AddFinalizer(&g, FinalizerName) {
		if err := r.Patch(ctx, &g, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	orig := g.Status.DeepCopy()
	persist := func() error {
		if err := r.patchStatus(ctx, &g, orig); err != nil {
			return err
		}
		orig = g.Status.DeepCopy()
		return nil
	}
	result, syncErr := r.sync(ctx, &g, persist)
	if err := persist(); err != nil {
		return ctrl.Result{}, errors.Join(syncErr, err)
	}
	return result, syncErr
}

// sync does the work and records the outcome in g.Status. persist writes the
// status immediately; it is used to record the pending-create marker before
// the create request is sent, and a fresh claim before it is re-checked.
func (r *AuthentikGroupReconciler) sync(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup,
	persist func() error) (ctrl.Result, error) {
	got, err := r.syncGroup(ctx, g, persist)
	if err != nil {
		return r.failed(g, err)
	}
	g.Status.PendingCreate = ""
	g.Status.GroupPK = got.Pk
	g.Status.Name = groupName(g)
	g.Status.ObservedGeneration = g.Generation
	if readOnly(g.Spec.ManagementPolicy) {
		r.setReady(g, metav1.ConditionTrue, reasonReadOnly, fmt.Sprintf("Group %q found; observed read only", got.Name))
	} else {
		meta.RemoveStatusCondition(&g.Status.Conditions, ConditionDrifted)
		r.setReady(g, metav1.ConditionTrue, reasonSynced, fmt.Sprintf("Group %q is in sync", got.Name))
	}
	return ctrl.Result{RequeueAfter: recheckAfter(g)}, nil
}

// recheckAfter is when to look at g again: soon while a member problem (MembersInSync False) may be
// fixed in Authentik, else the drift-detection interval.
func recheckAfter(g *authentikiov1alpha1.AuthentikGroup) time.Duration {
	if meta.IsStatusConditionFalse(g.Status.Conditions, ConditionMembersInSync) {
		return userErrorRecheck
	}
	return syncedRecheck
}

// syncGroup makes the group in Authentik match the spec and returns it as
// Authentik now has it. The identity gate runs first, under every
// managementPolicy, so a denied namespace reads and writes nothing.
func (r *AuthentikGroupReconciler) syncGroup(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup,
	persist func() error) (*api.Group, error) {
	if err := (identityGate{r.APIReader, r.IdentityNamespaceSelector}).check(ctx, g.Namespace,
		g.Spec.AuthentikRef); err != nil {
		return nil, err
	}
	if err := (adoptionGate{r.APIReader, r.AdoptionNamespaceSelector}).check(ctx, g.Namespace,
		g.Spec.AuthentikRef, g.Spec.ManagementPolicy); err != nil {
		return nil, err
	}
	if tracked, now := g.Status.Name, groupName(g); g.Status.GroupPK != "" && tracked != "" && tracked != now {
		return nil, condErrf(reasonInvalidSpec, "spec.name changed from %q to %q after the group was created or "+
			"adopted; renaming would change every application's bindings by name. Set spec.name back to %q",
			tracked, now, tracked)
	}
	if err := reservedAttributes(g.Spec.Attributes); err != nil {
		return nil, condErr(reasonInvalidSpec, err)
	}
	want, err := specAttributes(g.Spec.Attributes)
	if err != nil {
		return nil, condErr(reasonInvalidSpec, err)
	}
	resolved, err := resolve(ctx, r.Connections, g.Namespace, g.Spec.AuthentikRef)
	if err != nil {
		return nil, err
	}
	work, err := r.observe(ctx, resolved.Client, g)
	if err != nil {
		return nil, err
	}
	work.want = want
	got, err := r.act(ctx, resolved.Client, g, work, persist)
	if err != nil {
		return nil, err
	}
	if err := r.syncMembers(ctx, resolved.Client, g, got); err != nil {
		return nil, err
	}
	return got, nil
}

// groupWork is what one sync works from: what Authentik says about the group
// (the tracked one, status.groupPk, the one found by name, and the facts
// adopt.Decide needs) and what the spec wants of its attributes (nil: the field
// is absent, so they are neither read nor written).
type groupWork struct {
	tracked, target *api.Group
	facts           adopt.Facts
	want            map[string]any
}

// observe looks the group up: by status.groupPk first (a 404 is Gone, and the
// pk is kept), then by name unless the tracked group was found.
func (r *AuthentikGroupReconciler) observe(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup) (groupWork, error) {
	name := groupName(g)
	obs := groupWork{facts: adopt.Facts{
		Policy: g.Spec.ManagementPolicy, Provenance: g.Status.Provenance, DryRun: g.Spec.DryRun,
		OwnCreatePending: g.Status.PendingCreate != "" && g.Status.PendingCreate == name,
		RecreateVanished: true,
	}}
	if pk := g.Status.GroupPK; pk != "" {
		got, err := c.GetGroup(ctx, pk)
		switch {
		case err == nil:
			obs.tracked, obs.facts.Tracked = got, adopt.TrackedFound
			return obs, nil
		case authentik.IsNotFound(err):
			obs.facts.Tracked = adopt.TrackedGone
		default:
			return obs, err
		}
	}
	target, err := c.FindGroupByName(ctx, name)
	switch {
	case err == nil:
		obs.target, obs.facts.Target = target, adopt.TargetFound
		obs.facts.OwnCreateProven = authentik.HasOwnerProof(target.Attributes, string(g.UID))
	case errors.Is(err, authentik.ErrAmbiguous):
		obs.facts.Target = adopt.TargetAmbiguous
	case authentik.IsNotFound(err):
		obs.facts.Target = adopt.TargetMissing
	default:
		return obs, err
	}
	return obs, nil
}

// act carries out the decision adopt.Decide takes from the observation and
// returns the group as Authentik now has it.
func (r *AuthentikGroupReconciler) act(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup, work groupWork, persist func() error) (*api.Group, error) {
	name := groupName(g)
	action := adopt.Decide(work.facts)
	switch action {
	case adopt.ActionUseTracked:
		if err := r.yieldIfOutranked(ctx, r.Client, true, g); err != nil {
			return nil, err
		}
		return r.syncFields(ctx, c, g, work.tracked, work.want)
	case adopt.ActionCreate, adopt.ActionRecreate:
		r.announceCreate(g, action)
		return r.createGroup(ctx, c, g, work.want, persist)
	case adopt.ActionRecoverOwnCreate:
		logf.FromContext(ctx).Info("Recovered Group from a create whose outcome was lost", "name", name)
		r.event(g, corev1.EventTypeNormal, "Recovered", "Create",
			"Recovered group %q created by an earlier reconcile", name)
		recordGroup(g, authentikiov1alpha1.ProvenanceCreated, work.target)
		return r.syncFields(ctx, c, g, work.target, work.want)
	case adopt.ActionAdopt, adopt.ActionObserve:
		if err := r.adoptGroup(ctx, g, work.target, persist); err != nil {
			return nil, err
		}
		return r.syncFields(ctx, c, g, work.target, work.want)
	case adopt.ActionDryRunAdopt:
		return nil, r.dryRunAdopt(ctx, c, g, work.target, work.want)
	case adopt.ActionDryRunCreate:
		return nil, condErrf(reasonAdoptionDryRun, "dry run: no existing group named %q; a new one would be "+
			"created; nothing was written", name)
	case adopt.ActionConflict:
		return nil, r.conflict(g)
	case adopt.ActionTargetNotFound, adopt.ActionTrackedGone:
		return nil, r.missing(g)
	case adopt.ActionAmbiguous:
		return nil, condErrf(reasonConflict, "several groups named %q exist in Authentik; refusing to pick one. "+
			"Rename the duplicates in Authentik so the name is unique", name)
	}
	return nil, fmt.Errorf("unexpected adoption action %q", action)
}

// announceCreate emits the event that explains why a create is about to be
// sent: the tracked group vanished, or nothing was found to adopt.
func (r *AuthentikGroupReconciler) announceCreate(g *authentikiov1alpha1.AuthentikGroup, action adopt.Action) {
	name := groupName(g)
	switch {
	case action == adopt.ActionRecreate:
		r.event(g, corev1.EventTypeWarning, "GroupMissing", "Recreate",
			"Group %q no longer exists in Authentik; recreating it", name)
	case g.Spec.ManagementPolicy.Resolve() == authentikiov1alpha1.ManagementPolicyAdoptOrCreate:
		r.event(g, corev1.EventTypeNormal, "NothingToAdopt", "Create",
			"Creating a new group because nothing was found to adopt (name %q)", name)
	}
}

// recordGroup records which group g manages and how it came to.
func recordGroup(g *authentikiov1alpha1.AuthentikGroup, prov authentikiov1alpha1.Provenance, group *api.Group) {
	g.Status.Provenance = prov
	g.Status.GroupPK = group.Pk
	g.Status.Name = group.Name
	g.Status.PendingCreate = ""
	// A new claim owns no attribute key yet: adoption keeps every live key, and a create sets its own.
	g.Status.ManagedAttributes = nil
}

// conflict is the Conflict for a group found by name that this resource may
// not take.
func (r *AuthentikGroupReconciler) conflict(g *authentikiov1alpha1.AuthentikGroup) error {
	name := groupName(g)
	if g.Status.PendingCreate == name {
		return condErrf(reasonConflict, "a create of group %q was sent, but the group now named %q does not carry "+
			"this resource's ownership proof, so it is not treated as this resource's group under any "+
			"managementPolicy. If that group should be managed by this resource, clear status.pendingCreate and "+
			"use managementPolicy Adopt (it is then adopted, not created); otherwise choose another spec.name or "+
			"remove that group from Authentik", name, name)
	}
	return condErrf(reasonConflict, "a group named %q already exists in Authentik and was not created by this "+
		"resource; set spec.managementPolicy to Adopt, AdoptOrCreate or ReadOnly to take it over or observe it", name)
}

// missing is the report for a group that is not there and will not be created:
// an adoption or ReadOnly target that does not exist, or a group this resource
// tracked that vanished and is not recreated. The pk stays in status.
func (r *AuthentikGroupReconciler) missing(g *authentikiov1alpha1.AuthentikGroup) error {
	name := groupName(g)
	switch {
	case g.Status.GroupPK != "":
		return condErrf(reasonAdoptionTargetNotFound, "the group %q (pk %s) no longer exists in Authentik; not "+
			"recreating it. Restore it, or clear status.groupPk, status.name and status.provenance and set "+
			"spec.managementPolicy to AdoptOrCreate (or Create) to create a new one", g.Status.Name, g.Status.GroupPK)
	case readOnly(g.Spec.ManagementPolicy):
		return condErrf(reasonAdoptionTargetNotFound, "no group named %q exists in Authentik to observe; "+
			"managementPolicy ReadOnly never writes to Authentik, so nothing is created", name)
	}
	return condErrf(reasonAdoptionTargetNotFound, "no group named %q exists in Authentik to adopt; nothing was "+
		"written. Fix spec.name, or set spec.managementPolicy to AdoptOrCreate to create one", name)
}

// groupClaim is g's own hold on its group.
func groupClaim(g *authentikiov1alpha1.AuthentikGroup) claim {
	return claim{namespace: g.Namespace, name: g.Name, uid: g.UID, provenance: g.Status.Provenance,
		ref: g.Spec.AuthentikRef}
}

// claimants lists the other group resources, terminating ones included, that
// track pk on the same Authentik as g. rd is the cached client for the routine
// check, which lists through the field index (cached true), and the API reader
// where a stale answer is not good enough, which cannot filter on a custom
// field and lists everything.
func (r *AuthentikGroupReconciler) claimants(ctx context.Context, rd client.Reader, cached bool,
	g *authentikiov1alpha1.AuthentikGroup, pk string) ([]claim, error) {
	var opts []client.ListOption
	if cached {
		opts = append(opts, client.MatchingFields{indexGroupPK: pk})
	}
	var list authentikiov1alpha1.AuthentikGroupList
	if err := rd.List(ctx, &list, opts...); err != nil {
		return nil, fmt.Errorf("list AuthentikGroups: %w", err)
	}
	var candidates []claim
	for i := range list.Items {
		if o := &list.Items[i]; o.UID != g.UID && o.Status.GroupPK == pk {
			candidates = append(candidates, groupClaim(o))
		}
	}
	return holders(ctx, r.Connections, groupClaim(g), candidates)
}

// yieldIfOutranked releases this resource's claim and reports a Conflict when
// another resource holding the same group outranks it (see claim.outranks), so
// two resources that passed the pre-adoption check together are settled, and a
// Created resource keeps the group over an adopter. rd is the reader the
// holders are listed with.
func (r *AuthentikGroupReconciler) yieldIfOutranked(ctx context.Context, rd client.Reader, cached bool,
	g *authentikiov1alpha1.AuthentikGroup) error {
	others, err := r.claimants(ctx, rd, cached, g, g.Status.GroupPK)
	if err != nil {
		return err
	}
	winner, lost := outrankedBy(groupClaim(g), others)
	if !lost {
		return nil
	}
	name := g.Status.Name
	g.Status.Provenance, g.Status.GroupPK, g.Status.Name, g.Status.ManagedAttributes = "", "", "", nil
	return condErrf(reasonConflict, "group %q is also claimed by %s, which takes precedence; "+
		"this resource released it", name, winner)
}

// adoptGroup takes over target (or, under ReadOnly, starts observing it): any
// other holder refuses it, then the claim is recorded and persisted before the
// authoritative re-check, and before anything is written to Authentik.
func (r *AuthentikGroupReconciler) adoptGroup(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup,
	target *api.Group, persist func() error) error {
	if err := r.refuseHeld(ctx, g, target); err != nil {
		return err
	}
	recordGroup(g, authentikiov1alpha1.ProvenanceAdopted, target)
	if err := persist(); err != nil {
		return err
	}
	// Re-check after the claim is persisted, reading the API rather than the
	// informer cache, which need not hold another resource's just-written
	// claim; no Authentik write has happened yet.
	if err := r.yieldIfOutranked(ctx, r.APIReader, false, g); err != nil {
		return err
	}
	if readOnly(g.Spec.ManagementPolicy) {
		r.event(g, corev1.EventTypeNormal, "Observing", "Adopt", "Observing existing group %q read only", target.Name)
	} else {
		r.event(g, corev1.EventTypeNormal, "Adopted", "Adopt", "Adopted existing group %q", target.Name)
	}
	return nil
}

// refuseHeld is a Conflict when any other resource, terminating ones included,
// already tracks target. This read may be served by a lagging cache; the
// re-check after the claim is persisted is the authoritative one.
func (r *AuthentikGroupReconciler) refuseHeld(ctx context.Context, g *authentikiov1alpha1.AuthentikGroup,
	target *api.Group) error {
	others, err := r.claimants(ctx, r.Client, true, g, target.Pk)
	if err != nil {
		return err
	}
	if len(others) > 0 {
		return condErrf(reasonConflict, "group %q is already managed by %s; two resources cannot manage one group",
			target.Name, others[0])
	}
	return nil
}

// dryRunAdopt reports what adopting target would change and writes nothing.
func (r *AuthentikGroupReconciler) dryRunAdopt(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup, target *api.Group, want map[string]any) error {
	if err := r.refuseHeld(ctx, g, target); err != nil {
		return err
	}
	// Adoption keeps every live key, so nothing is owned yet, whatever an earlier claim left in status.
	plan := planGroup(g, want, target, nil)
	patch, live := plan.reportView(target)
	changes := dryRunChanges(fmt.Sprintf("group %q", target.Name), patch, live, plan.changed)
	if g.Spec.Members != nil {
		members, _, err := observeMembers(ctx, c, g, target.Pk, g.Status.Members)
		if err != nil {
			return err
		}
		changes += "; members: " + membersReport(members)
	}
	// announce is judged against the whole message, members included.
	msg, announce := dryRunMessage(g.Status.Conditions, changes)
	if announce {
		r.event(g, corev1.EventTypeNormal, reasonAdoptionDryRun, "Adopt", "%s", msg)
	}
	return condErr(reasonAdoptionDryRun, errors.New(msg))
}

// createGroup creates the group, recording status.pendingCreate first so a
// lost response can be recovered by the ownership proof the request carries.
func (r *AuthentikGroupReconciler) createGroup(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup, want map[string]any, persist func() error) (*api.Group, error) {
	req := desiredGroup(g, want)
	g.Status.PendingCreate = req.Name
	if err := persist(); err != nil {
		return nil, err
	}
	created, err := c.CreateGroup(ctx, *req)
	if err != nil {
		if code := authentik.StatusCode(err); code >= 400 && code < 500 {
			g.Status.PendingCreate = "" // definitely not created
		}
		return nil, err
	}
	recordGroup(g, authentikiov1alpha1.ProvenanceCreated, created)
	g.Status.ManagedAttributes = ownedKeys(g, want)
	logf.FromContext(ctx).Info("Created Group", "name", created.Name, "pk", created.Pk)
	r.event(g, corev1.EventTypeNormal, "Created", "Create", "Created group %q", created.Name)
	return created, nil
}

// ownedKeys is status.managedAttributes after a sync that lists want: every
// listed key, or none when the spec leaves attributes out.
func ownedKeys(g *authentikiov1alpha1.AuthentikGroup, want map[string]any) []string {
	if g.Spec.Attributes == nil {
		return nil
	}
	return mergeAttributes(nil, want, nil).Owned
}

// syncFields makes live match the spec: a PATCH of the fields that differ, or
// under ReadOnly a Drifted condition and no write. It returns the group as
// Authentik now has it.
func (r *AuthentikGroupReconciler) syncFields(ctx context.Context, c *authentik.Client,
	g *authentikiov1alpha1.AuthentikGroup, live *api.Group, want map[string]any) (*api.Group, error) {
	plan := planGroup(g, want, live, g.Status.ManagedAttributes)
	if readOnly(g.Spec.ManagementPolicy) {
		if err := recordGroupDrift(ctx, c, g, live, plan); err != nil {
			return nil, err
		}
		return live, nil
	}
	got := live
	if len(plan.changed) > 0 {
		updated, err := c.PatchGroup(ctx, live.Pk, plan.patch)
		if err != nil {
			return nil, err
		}
		got = updated
		logf.FromContext(ctx).Info("Updated Group", "name", live.Name, "pk", live.Pk, "fields", plan.changed)
		r.event(g, corev1.EventTypeNormal, "Updated", "Update", "Updated group fields: %s",
			strings.Join(plan.changed, ", "))
	}
	g.Status.ManagedAttributes = nil
	if g.Spec.Attributes != nil {
		g.Status.ManagedAttributes = plan.owned
	}
	return got, nil
}

// finalize deletes the group this resource is entitled to delete, then
// releases the finalizer. A group is left alone unless the policy and the
// identity selector allow deleting it, and is never deleted while a policy
// binding uses it, or when the operator cannot see every binding.
func (r *AuthentikGroupReconciler) finalize(ctx context.Context,
	g *authentikiov1alpha1.AuthentikGroup) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(g, FinalizerName) {
		return ctrl.Result{}, nil
	}
	orig := g.Status.DeepCopy()
	result, release, err := r.decideDeletion(ctx, g)
	if perr := r.patchStatus(ctx, g, orig); perr != nil {
		err = errors.Join(err, perr)
	}
	if err != nil || !release {
		return result, err
	}
	base := g.DeepCopy()
	controllerutil.RemoveFinalizer(g, FinalizerName)
	return ctrl.Result{}, client.IgnoreNotFound(r.Patch(ctx, g, client.MergeFrom(base)))
}

// decideDeletion runs the deletion flow. release says the finalizer may go.
func (r *AuthentikGroupReconciler) decideDeletion(ctx context.Context,
	g *authentikiov1alpha1.AuthentikGroup) (result ctrl.Result, release bool, err error) {
	if g.Status.GroupPK == "" {
		return ctrl.Result{}, true, nil
	}
	allowed, err := (identityGate{r.APIReader, r.IdentityNamespaceSelector}).allowed(ctx, g.Namespace,
		g.Spec.AuthentikRef)
	if err != nil {
		return ctrl.Result{}, false, err
	}
	fin := adopt.Finalize(adopt.FinalizeFacts{
		Policy: g.Spec.ManagementPolicy, DeletionPolicy: g.Spec.DeletionPolicy, Provenance: g.Status.Provenance,
		IdentityAllowed: allowed,
	})
	name := g.Status.Name
	switch fin {
	case adopt.FinalizeRetain:
		logf.FromContext(ctx).Info("Retaining Group in Authentik", "name", name)
		r.event(g, corev1.EventTypeNormal, "Retained", "Delete", "Group %q left in Authentik", name)
		return ctrl.Result{}, true, nil
	case adopt.FinalizeRetainUndecided:
		r.event(g, corev1.EventTypeWarning, "Retained", "Delete",
			"Group %q left in Authentik: provenance unknown; retaining", name)
		return ctrl.Result{}, true, nil
	case adopt.FinalizeRetainNotAllowed:
		r.event(g, corev1.EventTypeWarning, "Retained", "Delete",
			"Group %q left in Authentik: it was not created by this resource, and this namespace is outside "+
				"--default-connection-identity-namespace-selector, which confines deletion to groups the resource "+
				"created", name)
		return ctrl.Result{}, true, nil
	case adopt.FinalizeDeleteUndecided:
		r.event(g, corev1.EventTypeWarning, "DeletingUndecided", "Delete",
			"Deleting group %q on an explicit deletionPolicy Delete although this resource's provenance is "+
				"undecided: it may not have created it", name)
		return r.deleteGroup(ctx, g)
	case adopt.FinalizeDelete:
		return r.deleteGroup(ctx, g)
	}
	return ctrl.Result{}, false, fmt.Errorf("unexpected finalization %q", fin)
}

// deleteGroup deletes the group in Authentik unless a policy binding uses it.
// used_by lists only what the token may see, so unless the token's user is a
// superuser the operator cannot know the list is complete: it then retains the
// group rather than risk deleting one that is bound.
func (r *AuthentikGroupReconciler) deleteGroup(ctx context.Context,
	g *authentikiov1alpha1.AuthentikGroup) (ctrl.Result, bool, error) {
	log := logf.FromContext(ctx)
	name, pk := g.Status.Name, g.Status.GroupPK
	resolved, err := resolveForDeletion(ctx, r.Connections, g.Namespace, g.Spec.AuthentikRef)
	switch {
	case connectionGone(err):
		log.Info("Could not reach Authentik to delete Group; leaving it in place", "name", name,
			"reason", err.Error())
		r.event(g, corev1.EventTypeWarning, "Orphaned", "Delete", "Group %q left in Authentik: %v", name, err)
		return ctrl.Result{}, true, nil
	case err != nil:
		return r.deletionFailed(g, err)
	}
	c := resolved.Client
	me, err := c.Me(ctx)
	if err != nil {
		return r.deletionFailed(g, err)
	}
	if !me.IsSuperuser {
		log.Info("Retaining Group in Authentik: the token user is not a superuser", "name", name)
		r.event(g, corev1.EventTypeWarning, "RetainedUnverifiable", "Delete",
			"Group %q left in Authentik: the operator's token user is not a superuser, so it cannot see every "+
				"binding that uses the group; delete it in Authentik after checking its bindings", name)
		return ctrl.Result{}, true, nil
	}
	used, err := c.GroupUsedBy(ctx, pk)
	switch {
	case authentik.IsNotFound(err):
		return ctrl.Result{}, true, nil
	case err != nil:
		return r.deletionFailed(g, err)
	}
	if bindings := policyBindings(used); len(bindings) > 0 {
		msg := fmt.Sprintf("deletion blocked: the group is still used by %d policy binding(s): %s; remove the "+
			"binding in Authentik or the group from the application's spec.access.groups, or set "+
			"spec.deletionPolicy: Retain on this resource", len(bindings), strings.Join(bindings, ", "))
		if r.setReady(g, metav1.ConditionFalse, reasonDeletionBlocked, msg) {
			r.event(g, corev1.EventTypeWarning, reasonDeletionBlocked, "Delete", "%s", msg)
		}
		return ctrl.Result{RequeueAfter: userErrorRecheck}, false, nil
	}
	if err := c.DeleteGroup(ctx, pk); err != nil {
		return r.deletionFailed(g, err)
	}
	log.Info("Deleted Group", "name", name, "pk", pk)
	r.event(g, corev1.EventTypeNormal, "Deleted", "Delete", "Deleted group %q", name)
	return ctrl.Result{}, true, nil
}

// policyBindings quotes the name of every policy binding in used.
func policyBindings(used []api.UsedBy) []string {
	var names []string
	for _, u := range used {
		if u.App == policyBindingApp && u.ModelName == policyBindingModel {
			names = append(names, fmt.Sprintf("%q", u.Name))
		}
	}
	return names
}

// deletionFailed keeps the resource terminating and reports why: nothing was
// deleted, and the error returned makes the controller retry with backoff.
func (r *AuthentikGroupReconciler) deletionFailed(g *authentikiov1alpha1.AuthentikGroup,
	err error) (ctrl.Result, bool, error) {
	if r.setReady(g, metav1.ConditionFalse, conditionReason(err, reasonAuthentikError), "Deletion blocked: "+err.Error()) {
		r.event(g, corev1.EventTypeWarning, reasonDeletionBlocked, "Delete",
			"Cannot delete group %q yet: %v", g.Status.Name, err)
	}
	return ctrl.Result{}, false, err
}

// failed records why sync stopped; see classifyFailure.
func (r *AuthentikGroupReconciler) failed(g *authentikiov1alpha1.AuthentikGroup, err error) (ctrl.Result, error) {
	reason, result, retErr, warn := classifyFailure(err)
	if r.setReady(g, metav1.ConditionFalse, reason, err.Error()) && warn {
		r.event(g, corev1.EventTypeWarning, reasonInvalidSpec, "Sync", "Authentik rejected the group: %v", err)
	}
	return result, retErr
}

// setReady records the Ready condition and reports whether it changed.
func (r *AuthentikGroupReconciler) setReady(g *authentikiov1alpha1.AuthentikGroup,
	status metav1.ConditionStatus, reason, msg string) bool {
	return meta.SetStatusCondition(&g.Status.Conditions, metav1.Condition{
		Type: ConditionReady, Status: status, Reason: reason, Message: msg, ObservedGeneration: g.Generation,
	})
}

//nolint:revive // argument-limit: mirrors events.EventRecorder.Eventf, guarded for a nil Recorder.
func (r *AuthentikGroupReconciler) event(g *authentikiov1alpha1.AuthentikGroup,
	eventType, reason, action, note string, args ...any) {
	emit(r.Recorder, g, eventType, reason, action, note, args...)
}

func (r *AuthentikGroupReconciler) patchStatus(ctx context.Context,
	g *authentikiov1alpha1.AuthentikGroup, orig *authentikiov1alpha1.AuthentikGroupStatus) error {
	if equality.Semantic.DeepEqual(orig, &g.Status) {
		return nil
	}
	base := g.DeepCopy()
	base.Status = *orig
	err := r.Status().Patch(ctx, g, client.MergeFrom(base))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// groupPKIndex indexes groups by the pk they track.
func groupPKIndex(o client.Object) []string {
	g, ok := o.(*authentikiov1alpha1.AuthentikGroup)
	if !ok || g.Status.GroupPK == "" {
		return nil
	}
	return []string{g.Status.GroupPK}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AuthentikGroupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &authentikiov1alpha1.AuthentikGroup{},
		indexGroupPK, groupPKIndex); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &authentikiov1alpha1.AuthentikGroup{},
		indexGroupName, groupNameIndex); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&authentikiov1alpha1.AuthentikGroup{}).
		Watches(&authentikiov1alpha1.AuthentikApplication{}, handler.EnqueueRequestsFromMapFunc(r.groupsForApplication)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Named("authentikgroup").
		Complete(r)
}

// groupNameIndex indexes groups by their name in Authentik.
func groupNameIndex(o client.Object) []string {
	g, ok := o.(*authentikiov1alpha1.AuthentikGroup)
	if !ok {
		return nil
	}
	return []string{groupName(g)}
}

// groupsForApplication maps an application to the groups its spec.access
// lists, in any namespace: the group's own reconcile re-checks its deletion
// guard when a binding of it comes or goes.
func (r *AuthentikGroupReconciler) groupsForApplication(ctx context.Context, o client.Object) []reconcile.Request {
	a, ok := o.(*authentikiov1alpha1.AuthentikApplication)
	if !ok || !wantsGroups(a) {
		return nil
	}
	var reqs []reconcile.Request
	for _, name := range a.Spec.Access.Groups {
		var list authentikiov1alpha1.AuthentikGroupList
		if err := r.List(ctx, &list, client.MatchingFields{indexGroupName: name}); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to list AuthentikGroups for application",
				"application", client.ObjectKeyFromObject(o), "group", name)
			continue
		}
		for i := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}
