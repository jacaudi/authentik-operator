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
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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

func (e *Error) Error() string { return e.Message }
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
	// manager's API reader so that Secrets are not cached cluster-wide.
	Reader client.Reader
	// Default is the operator-level connection; nil when not configured.
	Default *authentik.Config
	// NewClient builds a client; defaults to authentik.New.
	NewClient func(authentik.Config) (*authentik.Client, error)

	mu      sync.Mutex
	clients map[string]cachedClient
}

type cachedClient struct {
	fingerprint [sha256.Size]byte
	client      *authentik.Client
}

// Resolved is a ready-to-use client plus where it came from.
type Resolved struct {
	Client *authentik.Client
	// Source is "default" or "<namespace>/<name>" of the AuthentikDeployment.
	Source string
}

// ForRef resolves the client for an API object in namespace with the given
// (possibly nil) authentikRef.
func (r *Resolver) ForRef(ctx context.Context, namespace string, ref *authentikiov1alpha1.AuthentikRef) (*Resolved, error) {
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
		return &Resolved{Client: c, Source: "default"}, nil
	}

	key := types.NamespacedName{Namespace: namespace, Name: ref.Name}
	var d authentikiov1alpha1.AuthentikDeployment
	if err := r.Reader.Get(ctx, key, &d); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &Error{
				Reason:  ReasonDeploymentNotFound,
				Message: fmt.Sprintf("AuthentikDeployment %s not found", key),
				Err:     err,
			}
		}
		return nil, fmt.Errorf("get AuthentikDeployment %s: %w", key, err)
	}
	return r.ForDeployment(ctx, &d)
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
	return &Resolved{Client: c, Source: key.String()}, nil
}

// Forget drops the cached client of a deleted AuthentikDeployment.
func (r *Resolver) Forget(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, key.String())
}

func (r *Resolver) configFor(ctx context.Context, d *authentikiov1alpha1.AuthentikDeployment) (authentik.Config, error) {
	conn := d.Spec.Connection
	if conn == nil {
		return authentik.Config{}, &Error{
			Reason: ReasonConnectionNotConfigured,
			Message: fmt.Sprintf("AuthentikDeployment %s/%s has no spec.connection "+
				"(deriving it in Managed mode is not implemented yet)", d.Namespace, d.Name),
		}
	}

	token, err := r.secretValue(ctx, d.Namespace, conn.TokenSecretRef, "token", ReasonSecretNotFound)
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
			cfg.CABundle, err = r.secretValue(ctx, d.Namespace, *ca.SecretKeyRef, "ca.crt", ReasonCABundleNotFound)
		case ca.ConfigMapKeyRef != nil:
			cfg.CABundle, err = r.configMapValue(ctx, d.Namespace, *ca.ConfigMapKeyRef)
		}
		if err != nil {
			return authentik.Config{}, err
		}
	}
	return cfg, nil
}

func (r *Resolver) secretValue(ctx context.Context, namespace string, ref authentikiov1alpha1.SecretKeyRef,
	defaultKey, notFoundReason string) ([]byte, error) {
	k := ref.Key
	if k == "" {
		k = defaultKey
	}
	var s corev1.Secret
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &Error{
				Reason:  notFoundReason,
				Message: fmt.Sprintf("Secret %s/%s not found", namespace, ref.Name),
				Err:     err,
			}
		}
		return nil, fmt.Errorf("get Secret %s/%s: %w", namespace, ref.Name, err)
	}
	v, ok := s.Data[k]
	if !ok {
		return nil, &Error{
			Reason:  ReasonSecretKeyMissing,
			Message: fmt.Sprintf("Secret %s/%s has no key %q", namespace, ref.Name, k),
		}
	}
	return v, nil
}

func (r *Resolver) configMapValue(ctx context.Context, namespace string, ref authentikiov1alpha1.ConfigMapKeyRef) ([]byte, error) {
	k := ref.Key
	if k == "" {
		k = "ca.crt"
	}
	var cm corev1.ConfigMap
	if err := r.Reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &Error{
				Reason:  ReasonCABundleNotFound,
				Message: fmt.Sprintf("ConfigMap %s/%s not found", namespace, ref.Name),
				Err:     err,
			}
		}
		return nil, fmt.Errorf("get ConfigMap %s/%s: %w", namespace, ref.Name, err)
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
		_, _ = fmt.Fprintf(h, "%d:", len(part))
		_, _ = h.Write(part)
	}
	if cfg.InsecureSkipVerify {
		_, _ = h.Write([]byte{1})
	}
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}
