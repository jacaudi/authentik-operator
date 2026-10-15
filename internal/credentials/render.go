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

// Package credentials renders an OAuth2 provider's client credentials and
// endpoint URLs into Secret data.
//
// Semantics follow JeffResc/authentik-k8s-operator so migrated templates
// render the same: Go text/template with missingkey=error and no extra
// function library; the output is parsed as a YAML map of string keys to
// string values, one Secret key per entry; without a template a fixed set
// of keys is written.
package credentials

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"text/template"

	yaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Vars are the values available to templates.
type Vars struct {
	ClientID        string
	ClientSecret    string
	IssuerURL       string
	AuthURL         string
	TokenURL        string
	UserInfoURL     string
	LogoutURL       string
	JWKSURL         string
	ProviderInfoURL string
	// Slug and Name are those of the application the provider is assigned to.
	Slug string
	Name string
}

// TemplateError is a user error in the template or its output. Retrying does
// not help until the spec changes.
type TemplateError struct{ Err error }

func (e *TemplateError) Error() string { return "credentials template: " + e.Err.Error() }
func (e *TemplateError) Unwrap() error { return e.Err }

// IsTemplateError reports whether err is a *TemplateError.
func IsTemplateError(err error) bool {
	_, ok := errors.AsType[*TemplateError](err)
	return ok
}

// Render returns the Secret data for vars, using tmpl if set and the
// default keys otherwise.
func Render(tmpl string, v Vars) (map[string][]byte, error) {
	if tmpl == "" {
		return toBytes(map[string]string{
			"client-id":         v.ClientID,
			"client-secret":     v.ClientSecret,
			"issuer-url":        v.IssuerURL,
			"authorization-url": v.AuthURL,
			"token-url":         v.TokenURL,
			"userinfo-url":      v.UserInfoURL,
		}), nil
	}

	t, err := template.New("secret").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return nil, &TemplateError{Err: err}
	}
	// A map (not the struct) so that missingkey=error governs unknown
	// variables, as in the operator this one replaces.
	data := map[string]string{
		"ClientID":        v.ClientID,
		"ClientSecret":    v.ClientSecret,
		"IssuerURL":       v.IssuerURL,
		"AuthURL":         v.AuthURL,
		"TokenURL":        v.TokenURL,
		"UserInfoURL":     v.UserInfoURL,
		"LogoutURL":       v.LogoutURL,
		"JWKSURL":         v.JWKSURL,
		"ProviderInfoURL": v.ProviderInfoURL,
		"Slug":            v.Slug,
		"Name":            v.Name,
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		// The error text names the template position and key, never values.
		return nil, &TemplateError{Err: err}
	}

	var kv map[string]string
	if err := yaml.Unmarshal(out.Bytes(), &kv); err != nil {
		// Deliberately not wrapping err: yaml errors can quote the
		// offending line, which may contain the client secret.
		return nil, &TemplateError{Err: errors.New("output is not a YAML map of string keys to string values")}
	}
	if len(kv) == 0 {
		return nil, &TemplateError{Err: errors.New("output has no keys")}
	}
	for k := range kv {
		if errs := validation.IsConfigMapKey(k); len(errs) > 0 {
			return nil, &TemplateError{Err: fmt.Errorf("output key %q is not a valid Secret key: %s", k, strings.Join(errs, "; "))}
		}
	}
	return toBytes(kv), nil
}

// Rewrite replaces the internalBase prefix of u (the operator's API base URL,
// which Authentik uses to build endpoint URLs) with externalBase. URLs that
// do not start with internalBase are returned unchanged: Authentik already
// produced a different (configured) host for them.
func Rewrite(u, internalBase, externalBase string) string {
	internalBase = strings.TrimRight(internalBase, "/")
	externalBase = strings.TrimRight(externalBase, "/")
	if externalBase == "" || internalBase == externalBase {
		return u
	}
	if u == internalBase || strings.HasPrefix(u, internalBase+"/") {
		return externalBase + strings.TrimPrefix(u, internalBase)
	}
	return u
}

func toBytes(m map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		out[k] = []byte(v)
	}
	return out
}
