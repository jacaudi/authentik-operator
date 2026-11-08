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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/connection"
)

// fakeAuthentik answers users/me for one accepted token.
func fakeAuthentik(token string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/core/users/me/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"Token invalid/expired"}`))
			return
		}
		user := map[string]any{
			"pk": 1, "username": "akadmin", jsonName: "admin", "is_active": true, "is_superuser": true,
			"is_current": true, "groups": []any{}, "roles": []any{}, "avatar": "", "uid": "u",
			"settings": map[string]any{}, "system_permissions": []string{},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user": user, "users": []any{user}})
	}))
}

var _ = Describe("AuthentikDeployment Controller", func() {
	const namespace = "default"
	ctx := context.Background()

	var (
		srv        *httptest.Server
		reconciler *AuthentikDeploymentReconciler
	)

	BeforeEach(func() {
		srv = fakeAuthentik("good-token")
		reconciler = &AuthentikDeploymentReconciler{
			Client:      k8sClient,
			Scheme:      k8sClient.Scheme(),
			Connections: &connection.Resolver{Reader: k8sClient},
		}
	})
	AfterEach(func() { srv.Close() })

	create := func(obj client.Object) {
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed()) })
	}

	connectOnly := func(name, secretName string) *authentikiov1alpha1.AuthentikDeployment {
		return &authentikiov1alpha1.AuthentikDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: authentikiov1alpha1.AuthentikDeploymentSpec{
				Mode: authentikiov1alpha1.DeploymentModeConnectOnly,
				Connection: &authentikiov1alpha1.ConnectionSpec{
					URL:            srv.URL,
					TokenSecretRef: authentikiov1alpha1.SecretKeyRef{Name: secretName},
				},
			},
		}
	}

	tokenSecret := func(name, token string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: namespace,
				Labels: map[string]string{
					authentikiov1alpha1.LabelConnection: authentikiov1alpha1.LabelConnectionValue,
				},
			},
			StringData: map[string]string{"token": token},
		}
	}

	reconcileAndGetCondition := func(name string) (reconcile.Result, *metav1.Condition,
		*authentikiov1alpha1.AuthentikDeployment) {
		key := types.NamespacedName{Name: name, Namespace: namespace}
		res, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		var d authentikiov1alpha1.AuthentikDeployment
		Expect(k8sClient.Get(ctx, key, &d)).To(Succeed())
		return res, meta.FindStatusCondition(d.Status.Conditions, ConditionConnected), &d
	}

	It("reports Connected for a reachable Authentik with a valid token", func() {
		create(tokenSecret("ak-token-good", "good-token\n"))
		create(connectOnly("connect-good", "ak-token-good"))

		res, cond, d := reconcileAndGetCondition("connect-good")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Message).To(ContainSubstring("akadmin"))
		Expect(cond.ObservedGeneration).To(Equal(d.Generation))
		Expect(d.Status.URL).To(Equal(srv.URL))
		Expect(res.RequeueAfter).To(Equal(healthyRecheck))
	})

	It("reports ConnectionFailed without leaking the token when Authentik rejects it", func() {
		create(tokenSecret("ak-token-bad", "wrong-token"))
		create(connectOnly("connect-bad", "ak-token-bad"))

		res, cond, _ := reconcileAndGetCondition("connect-bad")
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("ConnectionFailed"))
		Expect(cond.Message).To(ContainSubstring("403"))
		Expect(cond.Message).NotTo(ContainSubstring("wrong-token"))
		Expect(res.RequeueAfter).To(Equal(unhealthyRecheck))
	})

	It("refuses a token Secret that is not opted in, without contacting Authentik", func() {
		var hits atomic.Int32
		spy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		DeferCleanup(spy.Close)
		s := tokenSecret("ak-token-unlabeled", "some-other-apps-secret")
		s.Labels = nil
		create(s)
		d := connectOnly("connect-unlabeled", "ak-token-unlabeled")
		d.Spec.Connection.URL = spy.URL
		create(d)

		_, cond, _ := reconcileAndGetCondition("connect-unlabeled")
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(connection.ReasonSecretNotOptedIn))
		Expect(cond.Message).To(ContainSubstring(authentikiov1alpha1.LabelConnection))
		Expect(hits.Load()).To(BeZero())
	})

	It("reports SecretNotFound when the token Secret is missing", func() {
		create(connectOnly("connect-nosecret", "does-not-exist"))

		_, cond, d := reconcileAndGetCondition("connect-nosecret")
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(connection.ReasonSecretNotFound))
		Expect(d.Status.URL).To(BeEmpty())
	})

	It("reports Managed mode without a connection as not implemented", func() {
		create(&authentikiov1alpha1.AuthentikDeployment{
			ObjectMeta: metav1.ObjectMeta{Name: "managed", Namespace: namespace},
			Spec:       authentikiov1alpha1.AuthentikDeploymentSpec{Mode: authentikiov1alpha1.DeploymentModeManaged},
		})

		_, cond, _ := reconcileAndGetCondition("managed")
		Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
		Expect(cond.Reason).To(Equal("ManagedModeNotImplemented"))
	})

	It("defaults the ping timeout to 15s", func() {
		Expect((&AuthentikDeploymentReconciler{}).pingTimeout()).To(Equal(15 * time.Second))
	})

	It("reports ConnectionFailed within the ping timeout for an unresponsive Authentik", func() {
		hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		DeferCleanup(hang.Close)
		create(tokenSecret("ak-token-hang", "good-token"))
		d := connectOnly("connect-hang", "ak-token-hang")
		d.Spec.Connection.URL = hang.URL
		create(d)
		reconciler.PingTimeout = 200 * time.Millisecond

		start := time.Now()
		_, cond, _ := reconcileAndGetCondition("connect-hang")
		// Well under the 15s default: the configured timeout is what bounds the ping.
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
		Expect(cond.Reason).To(Equal("ConnectionFailed"))
	})

	Context("admission validation", func() {
		It("rejects ConnectOnly without a connection", func() {
			d := &authentikiov1alpha1.AuthentikDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-noconn", Namespace: namespace},
				Spec:       authentikiov1alpha1.AuthentikDeploymentSpec{Mode: authentikiov1alpha1.DeploymentModeConnectOnly},
			}
			Expect(k8sClient.Create(ctx, d)).To(MatchError(ContainSubstring("connection is required")))
		})

		It("rejects a missing mode", func() {
			d := &authentikiov1alpha1.AuthentikDeployment{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-nomode", Namespace: namespace},
			}
			Expect(k8sClient.Create(ctx, d)).NotTo(Succeed())
		})

		It("rejects a non-http(s) URL", func() {
			d := connectOnly("invalid-url", "x")
			d.Spec.Connection.URL = "ftp://authentik"
			Expect(k8sClient.Create(ctx, d)).NotTo(Succeed())
		})

		It("rejects a CA bundle with both sources set", func() {
			d := connectOnly("invalid-ca", "x")
			d.Spec.Connection.CABundle = &authentikiov1alpha1.CABundleSource{
				SecretKeyRef:    &authentikiov1alpha1.CASecretKeyRef{Name: "a"},
				ConfigMapKeyRef: &authentikiov1alpha1.ConfigMapKeyRef{Name: "b"},
			}
			Expect(k8sClient.Create(ctx, d)).To(MatchError(ContainSubstring("exactly one of")))
		})

		It("rejects caBundle together with insecureSkipVerify", func() {
			d := connectOnly("invalid-insecure", "x")
			d.Spec.Connection.InsecureSkipVerify = true
			d.Spec.Connection.CABundle = &authentikiov1alpha1.CABundleSource{
				SecretKeyRef: &authentikiov1alpha1.CASecretKeyRef{Name: "a"},
			}
			Expect(k8sClient.Create(ctx, d)).To(MatchError(ContainSubstring("mutually exclusive")))
		})

		It("defaults the CA Secret key to ca.crt and the token key to token", func() {
			d := connectOnly("defaults", "tok")
			d.Spec.Connection.CABundle = &authentikiov1alpha1.CABundleSource{
				SecretKeyRef: &authentikiov1alpha1.CASecretKeyRef{Name: "ca"},
			}
			create(d)
			var got authentikiov1alpha1.AuthentikDeployment
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(d), &got)).To(Succeed())
			Expect(got.Spec.Connection.TokenSecretRef.Key).To(Equal("token"))
			Expect(got.Spec.Connection.CABundle.SecretKeyRef.Key).To(Equal("ca.crt"))
		})

		It("accepts changing mode; ConnectOnly still requires a connection", func() {
			d := connectOnly("mutable-mode", "tok")
			create(d)
			d.Spec.Mode = authentikiov1alpha1.DeploymentModeManaged
			Expect(k8sClient.Update(ctx, d)).To(Succeed())
			d.Spec.Mode = authentikiov1alpha1.DeploymentModeConnectOnly
			d.Spec.Connection = nil
			Expect(k8sClient.Update(ctx, d)).To(MatchError(ContainSubstring("connection is required")))
		})
	})
})
