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

package connection

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
)

const (
	ns           = "apps"
	tokenKey     = "token"
	defaultAKURL = "https://ak"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := authentikiov1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func deployment(name string, conn *authentikiov1alpha1.ConnectionSpec) *authentikiov1alpha1.AuthentikDeployment {
	return &authentikiov1alpha1.AuthentikDeployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: authentikiov1alpha1.AuthentikDeploymentSpec{
			Mode:       authentikiov1alpha1.DeploymentModeConnectOnly,
			Connection: conn,
		},
	}
}

// secret returns an opted-in (labeled) Secret.
func secret(name string, data map[string]string) *corev1.Secret {
	s := unlabeledSecret(name, data)
	s.Labels = map[string]string{authentikiov1alpha1.LabelConnection: authentikiov1alpha1.LabelConnectionValue}
	return s
}

func unlabeledSecret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func conn(secretName string) *authentikiov1alpha1.ConnectionSpec {
	return &authentikiov1alpha1.ConnectionSpec{
		URL:            "https://ak.example.com",
		TokenSecretRef: authentikiov1alpha1.SecretKeyRef{Name: secretName},
	}
}

func ref(name string) *authentikiov1alpha1.AuthentikRef {
	return &authentikiov1alpha1.AuthentikRef{Name: name}
}

func TestForRef(t *testing.T) {
	scheme := newScheme(t)
	objs := make([]client.Object, 0, 9)
	objs = append(objs,
		deployment("ok", conn("tok")),
		secret("tok", map[string]string{tokenKey: "abc\n"}),
		deployment("no-conn", nil),
		deployment("missing-secret", conn("nope")),
		deployment("missing-key", conn("wrongkey")),
		secret("wrongkey", map[string]string{"other": "x"}),
		deployment("empty-token", conn("empty")),
		secret("empty", map[string]string{tokenKey: "  "}),
	)
	withCA := conn("tok")
	withCA.CABundle = &authentikiov1alpha1.CABundleSource{
		ConfigMapKeyRef: &authentikiov1alpha1.ConfigMapKeyRef{Name: "no-such-cm"},
	}
	unlabeledCA := conn("tok")
	unlabeledCA.CABundle = &authentikiov1alpha1.CABundleSource{
		SecretKeyRef: &authentikiov1alpha1.CASecretKeyRef{Name: "plain-ca"},
	}
	wrongValue := secret("wrong-label", map[string]string{tokenKey: "x"})
	wrongValue.Labels[authentikiov1alpha1.LabelConnection] = "yes"
	objs = append(objs,
		deployment("missing-ca", withCA),
		deployment("unlabeled", conn("plain")),
		unlabeledSecret("plain", map[string]string{tokenKey: "stolen"}),
		deployment("unlabeled-ca", unlabeledCA),
		unlabeledSecret("plain-ca", map[string]string{"ca.crt": "x"}),
		deployment("wrong-label", conn("wrong-label")),
		wrongValue,
	)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	var seen []authentik.Config
	r := &Resolver{Reader: reader, NewClient: func(c authentik.Config) (*authentik.Client, error) {
		seen = append(seen, c)
		return authentik.New(c)
	}}
	ctx := context.Background()

	res, err := r.ForRef(ctx, ns, ref("ok"))
	if err != nil {
		t.Fatalf("ok: %v", err)
	}
	if res.Source != ns+"/ok" || seen[0].Token != "abc" {
		t.Fatalf("ok: source %q token %q", res.Source, seen[0].Token)
	}

	for name, wantReason := range map[string]string{
		"absent":         ReasonDeploymentNotFound,
		"no-conn":        ReasonConnectionNotConfigured,
		"missing-secret": ReasonSecretNotFound,
		"missing-key":    ReasonSecretKeyMissing,
		"empty-token":    ReasonSecretKeyMissing,
		"missing-ca":     ReasonCABundleNotFound,
		"unlabeled":      ReasonSecretNotOptedIn,
		"unlabeled-ca":   ReasonSecretNotOptedIn,
		"wrong-label":    ReasonSecretNotOptedIn,
	} {
		_, err := r.ForRef(ctx, ns, ref(name))
		if got := Reason(err); got != wantReason {
			t.Errorf("%s: reason %q, want %q (err %v)", name, got, wantReason, err)
		}
	}

	// Same namespace only: the name is looked up in the object's namespace.
	if _, err := r.ForRef(ctx, "other", ref("ok")); Reason(err) != ReasonDeploymentNotFound {
		t.Errorf("cross-namespace lookup resolved: %v", err)
	}
}

func TestForbiddenAndCAKeyDefault(t *testing.T) {
	scheme := newScheme(t)
	withCA := conn("tok")
	withCA.CABundle = &authentikiov1alpha1.CABundleSource{
		SecretKeyRef: &authentikiov1alpha1.CASecretKeyRef{Name: "ca"},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		deployment("ca-default", withCA),
		secret("tok", map[string]string{tokenKey: "x"}),
		secret("ca", map[string]string{"ca.crt": "not a pem"}),
		deployment("forbidden", conn("locked")),
	).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == "locked" {
				return apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, key.Name, errors.New("rbac"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}).Build()
	r := &Resolver{Reader: reader}
	ctx := context.Background()

	// Empty key in code falls back to ca.crt (not token); the PEM is then
	// rejected by the client, proving the right key was read.
	if _, err := r.ForRef(ctx, ns, ref("ca-default")); Reason(err) != ReasonInvalidConfig {
		t.Fatalf("ca default key: reason %q err %v", Reason(err), err)
	}
	if _, err := r.ForRef(ctx, ns, ref("forbidden")); Reason(err) != ReasonForbidden {
		t.Fatalf("forbidden: reason %q err %v", Reason(err), err)
	}
}

func TestForRefDefault(t *testing.T) {
	reader := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	ctx := context.Background()

	r := &Resolver{Reader: reader}
	if _, err := r.ForRef(ctx, ns, nil); Reason(err) != ReasonNoConnection {
		t.Fatalf("no default: %v", err)
	}

	r.Default = &authentik.Config{URL: "https://default.example.com", Token: "t"}
	res, err := r.ForRef(ctx, ns, nil)
	if err != nil || res.Source != "default" || res.Client.BaseURL() != "https://default.example.com" {
		t.Fatalf("default: %+v, %v", res, err)
	}
}

func TestClientCacheRebuildsOnRotation(t *testing.T) {
	tok := secret("tok", map[string]string{tokenKey: "one"})
	reader := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(deployment("d", conn("tok")), tok).Build()
	builds := 0
	r := &Resolver{Reader: reader, NewClient: func(c authentik.Config) (*authentik.Client, error) {
		builds++
		return authentik.New(c)
	}}
	ctx := context.Background()

	a, _ := r.ForRef(ctx, ns, ref("d"))
	b, _ := r.ForRef(ctx, ns, ref("d"))
	if builds != 1 || a.Client != b.Client {
		t.Fatalf("unchanged config rebuilt client (builds=%d)", builds)
	}

	tok.Data[tokenKey] = []byte("two")
	if err := reader.Update(ctx, tok); err != nil {
		t.Fatal(err)
	}
	c, _ := r.ForRef(ctx, ns, ref("d"))
	if builds != 2 || c.Client == a.Client {
		t.Fatalf("rotated token did not rebuild client (builds=%d)", builds)
	}

	r.Forget(types.NamespacedName{Namespace: ns, Name: "d"})
	if _, err := r.ForRef(ctx, ns, ref("d")); err != nil || builds != 3 {
		t.Fatalf("Forget did not drop cache (builds=%d, err=%v)", builds, err)
	}
}

func TestInvalidConfigReason(t *testing.T) {
	bad := conn("tok")
	bad.URL = "ftp://nope"
	reader := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(deployment("bad", bad), secret("tok", map[string]string{tokenKey: "x"})).Build()
	r := &Resolver{Reader: reader}
	_, err := r.ForRef(context.Background(), ns, ref("bad"))
	if Reason(err) != ReasonInvalidConfig {
		t.Fatalf("reason %q, err %v", Reason(err), err)
	}
	var e *Error
	if !errors.As(err, &e) || e.Err == nil {
		t.Fatal("cause not wrapped")
	}
}

func TestDefaultConfig(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	cfg, err := DefaultConfig(DefaultOptions{}, getenv)
	if cfg != nil || err != nil {
		t.Fatalf("empty: %v, %v", cfg, err)
	}
	if _, err := DefaultConfig(DefaultOptions{TokenFile: "/x"}, getenv); err == nil {
		t.Fatal("token without URL accepted")
	}
	if _, err := DefaultConfig(DefaultOptions{URL: defaultAKURL}, getenv); err == nil {
		t.Fatal("URL without token accepted")
	}

	env[EnvToken] = " envtoken\n"
	cfg, err = DefaultConfig(DefaultOptions{URL: defaultAKURL}, getenv)
	if err != nil || cfg.Token != "envtoken" {
		t.Fatalf("env token: %+v, %v", cfg, err)
	}

	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte("filetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = DefaultConfig(DefaultOptions{URL: defaultAKURL, TokenFile: f}, getenv)
	if err != nil || cfg.Token != "filetoken" {
		t.Fatalf("file token: %+v, %v", cfg, err)
	}

	if _, err := DefaultConfig(DefaultOptions{URL: "ftp://ak"}, getenv); err == nil {
		t.Fatal("invalid URL accepted")
	}
}

const ssoURL = "https://sso.example.com"

func TestExternalURL(t *testing.T) {
	ext := conn("tok")
	ext.ExternalURL = ssoURL + "/"
	reader := fake.NewClientBuilder().WithScheme(newScheme(t)).
		WithObjects(deployment("ext", ext), deployment("plain", conn("tok")), secret("tok", map[string]string{tokenKey: "x"})).Build()
	r := &Resolver{Reader: reader, Default: &authentik.Config{URL: "https://internal.example.com", Token: "t"}}
	ctx := context.Background()

	for name, want := range map[string]string{"ext": ssoURL, "plain": "https://ak.example.com"} {
		res, err := r.ForRef(ctx, ns, ref(name))
		if err != nil || res.ExternalURL != want {
			t.Errorf("%s: ExternalURL %q, err %v; want %q", name, res.ExternalURL, err, want)
		}
	}
	res, _ := r.ForRef(ctx, ns, nil)
	if res.ExternalURL != "https://internal.example.com" {
		t.Errorf("default without external: %q", res.ExternalURL)
	}
	r.DefaultExternalURL = ssoURL
	res, _ = r.ForRef(ctx, ns, nil)
	if res.ExternalURL != ssoURL {
		t.Errorf("default with external: %q", res.ExternalURL)
	}
}

func TestDefaultConfigExternalURL(t *testing.T) {
	env := map[string]string{EnvToken: "t"}
	getenv := func(k string) string { return env[k] }
	if _, err := DefaultConfig(DefaultOptions{URL: defaultAKURL, ExternalURL: ssoURL}, getenv); err != nil {
		t.Fatalf("valid external URL rejected: %v", err)
	}
	if _, err := DefaultConfig(DefaultOptions{URL: defaultAKURL, ExternalURL: "sso.example.com"}, getenv); err == nil {
		t.Fatal("external URL without scheme accepted")
	}
	if _, err := DefaultConfig(DefaultOptions{ExternalURL: ssoURL}, getenv); err == nil {
		t.Fatal("external URL without URL accepted")
	}
}
