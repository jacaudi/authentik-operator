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
	"slices"
	"strconv"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// testIndexes are the field indexes the reconcilers register, by name.
var testIndexes = map[string]client.IndexerFunc{
	indexProviderPK:     providerPKIndex,
	indexApplicationUID: applicationUIDIndex,
	indexGroupPK:        groupPKIndex,
	indexAccessGroup:    accessGroupIndex,
	indexGroupName:      groupNameIndex,
	indexUserPK:         userPKIndex,
}

// indexedClient serves List with client.MatchingFields the way the manager's
// cache does for a registered field index: it lists everything and keeps the
// objects whose index function yields the requested value, so the production
// index functions and keys are what the specs exercise. The envtest API server
// cannot filter CRDs on custom fields, and the reconcilers' lists need a
// cache. The real registration against an informer cache is proven by the
// "field index" spec below.
type indexedClient struct{ client.Client }

func (c indexedClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	var lo client.ListOptions
	lo.ApplyOptions(opts)
	if lo.FieldSelector == nil {
		return c.Client.List(ctx, list, opts...)
	}
	reqs := lo.FieldSelector.Requirements()
	lo.FieldSelector = nil
	if err := c.Client.List(ctx, list, &lo); err != nil {
		return err
	}
	items, err := meta.ExtractList(list)
	if err != nil {
		return err
	}
	kept := make([]runtime.Object, 0, len(items))
	for _, item := range items {
		obj, ok := item.(client.Object)
		if !ok {
			continue
		}
		match := true
		for _, req := range reqs {
			fn, known := testIndexes[req.Field]
			if !known {
				return &unknownIndexError{req.Field}
			}
			match = match && slices.Contains(fn(obj), req.Value)
		}
		if match {
			kept = append(kept, item)
		}
	}
	return meta.SetList(list, kept)
}

type unknownIndexError struct{ field string }

func (e *unknownIndexError) Error() string { return "no field index registered for " + e.field }

func TestIndexFuncs(t *testing.T) {
	p := &authentikiov1alpha1.AuthentikOAuthProvider{}
	if got := providerPKIndex(p); got != nil {
		t.Errorf("a provider that tracks nothing must not be indexed, got %v", got)
	}
	p.Status.ProviderPK = 42
	if got := providerPKIndex(p); len(got) != 1 || got[0] != "42" {
		t.Errorf("providerPKIndex = %v, want [42]", got)
	}
	a := &authentikiov1alpha1.AuthentikApplication{}
	if got := applicationUIDIndex(a); got != nil {
		t.Errorf("an application that tracks nothing must not be indexed, got %v", got)
	}
	a.Status.ApplicationUID = "uid-1"
	if got := applicationUIDIndex(a); len(got) != 1 || got[0] != "uid-1" {
		t.Errorf("applicationUIDIndex = %v, want [uid-1]", got)
	}
	g := &authentikiov1alpha1.AuthentikGroup{}
	if got := groupPKIndex(g); got != nil {
		t.Errorf("a group that tracks nothing must not be indexed, got %v", got)
	}
	g.Status.GroupPK = "pk-1"
	if got := groupPKIndex(g); len(got) != 1 || got[0] != "pk-1" {
		t.Errorf("groupPKIndex = %v, want [pk-1]", got)
	}
	u := &authentikiov1alpha1.AuthentikUser{}
	if got := userPKIndex(u); got != nil {
		t.Errorf("a user that tracks nothing must not be indexed, got %v", got)
	}
	u.Status.UserPK = 7
	if got := userPKIndex(u); len(got) != 1 || got[0] != "7" {
		t.Errorf("userPKIndex = %v, want [7]", got)
	}
}

var _ = Describe("Claim field indexes", func() {
	const namespace = "default"
	ctx := context.Background()

	It("are registered on a real informer cache and list only the holders of one object", func() {
		c, err := cache.New(cfg, cache.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		Expect(indexProviderPKs(ctx, c)).To(Succeed())
		Expect(indexApplicationUIDs(ctx, c)).To(Succeed())

		mk := func(name string, pk int32) {
			p := &authentikiov1alpha1.AuthentikOAuthProvider{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: authentikiov1alpha1.AuthentikOAuthProviderSpec{AuthorizationFlow: "a", InvalidationFlow: "b",
					RedirectURIs: []authentikiov1alpha1.RedirectURI{{URL: "https://index.example.com/cb"}}},
			}
			Expect(k8sClient.Create(ctx, p)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, p))).To(Succeed()) })
			if pk != 0 {
				p.Status.ProviderPK = pk
				Expect(k8sClient.Status().Update(ctx, p)).To(Succeed())
			}
		}
		mk("index-a", 5)
		mk("index-b", 5)
		mk("index-c", 6)
		mk("index-d", 0)

		cacheCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		go func() { _ = c.Start(cacheCtx) }()
		Expect(c.WaitForCacheSync(cacheCtx)).To(BeTrue())

		Eventually(func(g Gomega) {
			var list authentikiov1alpha1.AuthentikOAuthProviderList
			g.Expect(c.List(ctx, &list, client.MatchingFields{indexProviderPK: strconv.Itoa(5)})).To(Succeed())
			names := make([]string, 0, len(list.Items))
			for _, it := range list.Items {
				names = append(names, it.Name)
			}
			g.Expect(names).To(ConsistOf("index-a", "index-b"))
		}).Should(Succeed())
	})
})
