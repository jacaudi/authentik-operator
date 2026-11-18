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

// Package connection resolves which Authentik instance an object talks to.
//
// Model (TASK-01, decided): an Authentik API object names an
// AuthentikDeployment in its own namespace via spec.authentikRef; the
// deployment's spec.connection supplies URL, token Secret and CA. When
// authentikRef is omitted, the operator-level default connection (flags /
// environment) is used. A Helm-installed Authentik therefore needs either
// the operator default or a ConnectOnly AuthentikDeployment, never a
// deployment that manages its pods.
package connection

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
	"github.com/jacaudi/authentik-operator/internal/authentik"
)

// Condition reasons reported when a connection cannot be resolved.
const (
	ReasonNoConnection            = "NoConnection"
	ReasonDeploymentNotFound      = "AuthentikDeploymentNotFound"
	ReasonConnectionNotConfigured = "ConnectionNotConfigured"
	ReasonSecretNotFound          = "SecretNotFound"
	ReasonSecretKeyMissing        = "SecretKeyMissing"
	ReasonCABundleNotFound        = "CABundleNotFound"
	ReasonForbidden               = "Forbidden"
	ReasonSecretNotOptedIn        = "SecretNotOptedIn"
	ReasonInvalidConfig           = "InvalidConfig"
)

// defaultKey caches the operator-level default client.
const defaultKey = ""

// Error is a connection that cannot be resolved from the current cluster
// state. Reason is suitable for a status condition.
type Error struct {
	Reason  string
	Message string
	Err     error
}

// Error returns the user-facing message.
func (e *Error) Error() string { return e.Message }

// Unwrap returns the underlying API error, if any.
func (e *Error) Unwrap() error { return e.Err }

// Reason extracts the condition reason from err, or "" if err is not an *Error.
func Reason(err error) string {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Reason
	}
	return ""
}

// Resolver builds (and caches) Authentik clients for API objects.
type Resolver struct {
	// Reader reads AuthentikDeployments, Secrets and ConfigMaps. Use the
	// manager's API reader: Secrets are fetched by name on demand and never
	// listed, watched or cached.
	Reader client.Reader
	// Default is the operator-level connection; nil when not configured.
	Default *authentik.Config
	// DefaultExternalURL is the browser-facing URL of the default
	// connection; empty means Default.URL.
	DefaultExternalURL string
	// NewClient builds a client; defaults to authentik.New.
	NewClient func(authentik.Config) (*authentik.Client, error)

	mu      sync.Mutex
	clients map[string]cachedClient
}

type cachedClient struct {
	fingerprint [sha256.Size]byte
	client      *authentik.Client
}

// Resolved is a ready-to-use client plus its browser-facing URL.
type Resolved struct {
	Client *authentik.Client
	// ExternalURL is the browser-facing base URL of this Authentik (no
	// trailing slash); it equals the API base URL unless configured.
	ExternalURL string
	// ExternalURLSet is true when ExternalURL was configured (the default
	// connection's external URL, or spec.connection.externalURL) rather than
	// derived from the API base URL. That URL is usually an in-cluster address
	// a person cannot open, so a link meant for a person needs this.
	ExternalURLSet bool
}

// ForRef resolves the client for an API object in namespace with the given
// (possibly nil) authentikRef.
func (r *Resolver) ForRef(ctx context.Context, namespace string,
	ref *authentikiov1alpha1.AuthentikRef) (*Resolved, error) {
	if ref == nil {
		if r.Default == nil {
			return nil, &Error{
				Reason:  ReasonNoConnection,
				Message: "spec.authentikRef is not set and the operator has no default Authentik connection configured",
			}
		}
		c, err := r.cached(defaultKey, *r.Default)
		if err != nil {
			return nil, err
		}
		return &Resolved{Client: c, ExternalURL: externalOrBase(r.DefaultExternalURL, c.BaseURL()),
			ExternalURLSet: r.DefaultExternalURL != ""}, nil
	}

	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}
	var d authentikiov1alpha1.AuthentikDeployment
	if err := r.Reader.Get(ctx, key, &d); err != nil {
		return nil, readError("AuthentikDeployment", key.Namespace, key.Name, ReasonDeploymentNotFound, err)
	}
	return r.ForDeployment(ctx, &d)
}

// BaseURL is the normalized base URL (see authentik.NormalizeURL) of the
// Authentik an object in namespace with the given (possibly nil) authentikRef
// talks to, without building a client or reading any Secret. It lets the
// controllers tell whether two objects on different connections reach the same
// instance. Only the URL is compared: a service DNS name and an ingress host
// that reach one instance look different here.
func (r *Resolver) BaseURL(ctx context.Context, namespace string,
	ref *authentikiov1alpha1.AuthentikRef) (string, error) {
	if ref == nil {
		if r.Default == nil {
			return "", &Error{
				Reason:  ReasonNoConnection,
				Message: "spec.authentikRef is not set and the operator has no default Authentik connection configured",
			}
		}
		return authentik.NormalizeURL(r.Default.URL)
	}
	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}
	var d authentikiov1alpha1.AuthentikDeployment
	if err := r.Reader.Get(ctx, key, &d); err != nil {
		return "", readError("AuthentikDeployment", key.Namespace, key.Name, ReasonDeploymentNotFound, err)
	}
	if d.Spec.Connection == nil {
		return "", &Error{
			Reason:  ReasonConnectionNotConfigured,
			Message: fmt.Sprintf("AuthentikDeployment %s/%s has no spec.connection", d.Namespace, d.Name),
		}
	}
	return authentik.NormalizeURL(d.Spec.Connection.URL)
}

// ForDeployment resolves the client described by an AuthentikDeployment's
// spec.connection.
func (r *Resolver) ForDeployment(ctx context.Context, d *authentikiov1alpha1.AuthentikDeployment) (*Resolved, error) {
	key := types.NamespacedName{Namespace: d.Namespace, Name: d.Name}
	cfg, err := r.configFor(ctx, d)
	if err != nil {
		return nil, err
	}
	c, err := r.cached(key.String(), cfg)
	if err != nil {
		return nil, err
	}
	return &Resolved{Client: c, ExternalURL: externalOrBase(d.Spec.Connection.ExternalURL, c.BaseURL()),
		ExternalURLSet: d.Spec.Connection.ExternalURL != ""}, nil
}

func externalOrBase(external, base string) string {
	if external == "" {
		return base
	}
	return strings.TrimRight(external, "/")
}

// LastKnown returns the most recently built client for an object's
// connection without reading any Secrets. Finalizers use it when the
// connection can no longer be resolved -- typically during namespace
// deletion, when the token Secret is removed before the objects that use it
// -- so cleanup can still reach Authentik. The cache is in memory only, so
// this does not survive an operator restart.
func (r *Resolver) LastKnown(namespace string, ref *authentikiov1alpha1.AuthentikRef) (*Resolved, bool) {
	key := defaultKey
	if ref != nil {
		key = types.NamespacedName{Namespace: namespace, Name: ref.Name}.String()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[key]
	if !ok {
		return nil, false
	}
	return &Resolved{Client: c.client}, true
}

// Forget drops the cached client of a deleted AuthentikDeployment.
func (r *Resolver) Forget(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, key.String())
}

func (r *Resolver) configFor(ctx context.Context,
	d *authentikiov1alpha1.AuthentikDeployment) (authentik.Config, error) {
	conn := d.Spec.Connection
	if conn == nil {
		return authentik.Config{}, &Error{
			Reason: ReasonConnectionNotConfigured,
			Message: fmt.Sprintf("AuthentikDeployment %s/%s has no spec.connection "+
				"(deriving it in Managed mode is not implemented yet)", d.Namespace, d.Name),
		}
	}

	token, err := r.secretValue(ctx, d.Namespace, secretKey{
		name: conn.TokenSecretRef.Name, key: cmp.Or(conn.TokenSecretRef.Key, "token"),
		notFoundReason: ReasonSecretNotFound, keyMissingReason: ReasonSecretKeyMissing,
	})
	if err != nil {
		return authentik.Config{}, err
	}
	cfg := authentik.Config{
		URL:                conn.URL,
		Token:              string(bytes.TrimSpace(token)),
		InsecureSkipVerify: conn.InsecureSkipVerify,
	}
	if cfg.Token == "" {
		return authentik.Config{}, &Error{
			Reason:  ReasonSecretKeyMissing,
			Message: fmt.Sprintf("token in Secret %s/%s is empty", d.Namespace, conn.TokenSecretRef.Name),
		}
	}

	if ca := conn.CABundle; ca != nil {
		switch {
		case ca.SecretKeyRef != nil:
			cfg.CABundle, err = r.secretValue(ctx, d.Namespace, secretKey{
				name: ca.SecretKeyRef.Name, key: cmp.Or(ca.SecretKeyRef.Key, "ca.crt"),
				notFoundReason: ReasonCABundleNotFound, keyMissingReason: ReasonCABundleNotFound,
			})
		case ca.ConfigMapKeyRef != nil:
			cfg.CABundle, err = r.configMapValue(ctx, d.Namespace, *ca.ConfigMapKeyRef)
		}
		if err != nil {
			return authentik.Config{}, err
		}
	}
	return cfg, nil
}

// secretKey names one key of a Secret and the condition reasons to report
// when the Secret or the key is missing.
type secretKey struct {
	name, key                        string
	notFoundReason, keyMissingReason string
}

func (r *Resolver) secretValue(ctx context.Context, namespace string, ref secretKey) ([]byte, error) {
	name, key := ref.name, ref.key
	var s corev1.Secret
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
		return nil, readError("Secret", namespace, name, ref.notFoundReason, err)
	}
	// Opt-in check before touching any data, so an unlabeled Secret reveals
	// nothing about its keys.
	if s.Labels[authentikiov1alpha1.LabelConnection] != authentikiov1alpha1.LabelConnectionValue {
		return nil, &Error{
			Reason: ReasonSecretNotOptedIn,
			Message: fmt.Sprintf("Secret %s/%s is not labeled %s=%s; label it to allow the operator to use it",
				namespace, name, authentikiov1alpha1.LabelConnection, authentikiov1alpha1.LabelConnectionValue),
		}
	}
	v, ok := s.Data[key]
	if !ok {
		return nil, &Error{
			Reason:  ref.keyMissingReason,
			Message: fmt.Sprintf("Secret %s/%s has no key %q", namespace, name, key),
		}
	}
	return v, nil
}

// readError maps a failed Get to a condition-bearing *Error where the cause
// is actionable by the user (missing object, operator not granted access).
func readError(kind, namespace, name, notFoundReason string, err error) error {
	switch {
	case apierrors.IsNotFound(err):
		return &Error{
			Reason:  notFoundReason,
			Message: fmt.Sprintf("%s %s/%s not found", kind, namespace, name),
			Err:     err,
		}
	case apierrors.IsForbidden(err):
		return &Error{
			Reason: ReasonForbidden,
			Message: fmt.Sprintf("operator is not permitted to get %s %s/%s; grant its ServiceAccount "+
				"get on this resource in namespace %s", kind, namespace, name, namespace),
			Err: err,
		}
	default:
		return fmt.Errorf("get %s %s/%s: %w", kind, namespace, name, err)
	}
}

func (r *Resolver) configMapValue(ctx context.Context, namespace string,
	ref authentikiov1alpha1.ConfigMapKeyRef) ([]byte, error) {
	k := ref.Key
	if k == "" {
		k = "ca.crt"
	}
	var cm corev1.ConfigMap
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &cm); err != nil {
		return nil, readError("ConfigMap", namespace, ref.Name, ReasonCABundleNotFound, err)
	}
	if v, ok := cm.Data[k]; ok {
		return []byte(v), nil
	}
	if v, ok := cm.BinaryData[k]; ok {
		return v, nil
	}
	return nil, &Error{
		Reason:  ReasonCABundleNotFound,
		Message: fmt.Sprintf("ConfigMap %s/%s has no key %q", namespace, ref.Name, k),
	}
}

// cached returns the client for key, rebuilding it only when the effective
// configuration (URL, token, CA, TLS mode) changed, e.g. on token rotation.
func (r *Resolver) cached(key string, cfg authentik.Config) (*authentik.Client, error) {
	fp := fingerprint(cfg)
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok && c.fingerprint == fp {
		return c.client, nil
	}
	newClient := r.NewClient
	if newClient == nil {
		newClient = authentik.New
	}
	c, err := newClient(cfg)
	if err != nil {
		return nil, &Error{Reason: ReasonInvalidConfig, Message: err.Error(), Err: err}
	}
	if r.clients == nil {
		r.clients = map[string]cachedClient{}
	}
	r.clients[key] = cachedClient{fingerprint: fp, client: c}
	return c, nil
}

func fingerprint(cfg authentik.Config) [sha256.Size]byte {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(cfg.URL), []byte(cfg.Token), cfg.CABundle} {
		h.Write(fmt.Appendf(nil, "%d:", len(part)))
		h.Write(part)
	}
	if cfg.InsecureSkipVerify {
		h.Write([]byte{1})
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}
