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

package jeffresc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	jeffrescGroup = "goauthentik.io"

	kindV1    = "AuthentikApplication"
	kindV2    = "AuthentikOAuth2Application"
	kindSAML  = "AuthentikSAMLApplication"
	kindGroup = "AuthentikGroup"
)

// jeffProvider is the JeffResc provider block (identical in v0.1.x and
// v0.2.x apart from validation).
type jeffProvider struct {
	AuthorizationFlow      string    `json:"authorizationFlow"`
	InvalidationFlow       string    `json:"invalidationFlow"`
	RedirectURIs           []string  `json:"redirectUris"`
	Scopes                 *[]string `json:"scopes,omitempty"`
	ClientType             string    `json:"clientType,omitempty"`
	AccessCodeValidity     string    `json:"accessCodeValidity,omitempty"`
	AccessTokenValidity    string    `json:"accessTokenValidity,omitempty"`
	RefreshTokenValidity   string    `json:"refreshTokenValidity,omitempty"`
	SubMode                string    `json:"subMode,omitempty"`
	IncludeClaimsInIDToken *bool     `json:"includeClaimsInIdToken,omitempty"`
	GrantTypes             []string  `json:"grantTypes,omitempty"`
	IssuerMode             string    `json:"issuerMode,omitempty"`
	PropertyMappings       []string  `json:"propertyMappings,omitempty"`
	SigningKey             *string   `json:"signingKey,omitempty"`
}

// jeffSecret is the JeffResc output Secret block.
type jeffSecret struct {
	Name        string            `json:"name,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Template    string            `json:"template,omitempty"`
}

// jeffSpec is the JeffResc application spec.
type jeffSpec struct {
	Name             string       `json:"name"`
	Slug             string       `json:"slug,omitempty"`
	Group            string       `json:"group,omitempty"`
	PolicyEngineMode string       `json:"policyEngineMode,omitempty"`
	MetaLaunchURL    string       `json:"metaLaunchUrl,omitempty"`
	MetaDescription  string       `json:"metaDescription,omitempty"`
	MetaPublisher    string       `json:"metaPublisher,omitempty"`
	OpenInNewTab     *bool        `json:"openInNewTab,omitempty"`
	GroupBindings    []string     `json:"groupBindings,omitempty"`
	Provider         jeffProvider `json:"provider"`
	Secret           jeffSecret   `json:"secret,omitzero"`
}

// jeffGroupSpec is the JeffResc group spec (v0.2.10 AuthentikGroupSpec),
// decoded strictly so a field the converter does not know is an error.
type jeffGroupSpec struct {
	Name        string                     `json:"name,omitempty"`
	IsSuperuser *bool                      `json:"isSuperuser,omitempty"`
	Parents     []string                   `json:"parents,omitempty"`
	Roles       []string                   `json:"roles,omitempty"`
	Attributes  map[string]json.RawMessage `json:"attributes,omitempty"`
	// DeletionPolicy is read and ignored: a converted group is always Retain.
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// source is one JeffResc application or group found in the input.
type source struct {
	from      string
	kind      string
	name      string
	namespace string
	spec      jeffSpec
	// group is set, and spec is zero, for an AuthentikGroup.
	group *jeffGroupSpec
}

func (s source) ref() string { return s.kind + " " + s.namespace + "/" + s.name }

// envelope is the part of any object read before its kind is known.
type envelope struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
	Spec       json.RawMessage `json:"spec,omitempty"`
	Status     json.RawMessage `json:"status,omitempty"`
	Items      []envelope      `json:"items,omitempty"`
}

type objectMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// ReadInputs reads YAML files, or every .yaml/.yml/.json file in the
// directories (not recursive), in sorted order.
func ReadInputs(paths []string) ([]Input, error) {
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("read input: %w", err)
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil, fmt.Errorf("read input dir: %w", err)
		}
		for _, e := range entries {
			switch strings.ToLower(filepath.Ext(e.Name())) {
			case ".yaml", ".yml", ".json":
				if !e.IsDir() {
					files = append(files, filepath.Join(p, e.Name()))
				}
			}
		}
	}
	slices.Sort(files)
	var out []Input
	for _, f := range files {
		data, err := os.ReadFile(f) //nolint:gosec // the operator of the tool names these paths.
		if err != nil {
			return nil, fmt.Errorf("read input: %w", err)
		}
		out = append(out, Input{Source: f, Data: data})
	}
	return out, nil
}

// parseInputs extracts the JeffResc applications and groups from inputs. Objects of
// other groups are skipped (and named in skipped); unsupported or malformed
// JeffResc objects are errors, all reported together.
func parseInputs(inputs []Input) (apps []source, skipped []string, err error) {
	var errs []error
	for _, in := range inputs {
		r := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(in.Data)))
		for {
			doc, rerr := r.Read()
			if errors.Is(rerr, io.EOF) {
				break
			}
			if rerr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", in.Source, rerr))
				break
			}
			var env envelope
			if uerr := yaml.Unmarshal(doc, &env); uerr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", in.Source, uerr))
				continue
			}
			a, s, perr := parseEnvelope(in.Source, env)
			apps, skipped, errs = append(apps, a...), append(skipped, s...), append(errs, perr...)
		}
	}
	return apps, skipped, errors.Join(errs...)
}

func parseEnvelope(from string, env envelope) (apps []source, skipped []string, errs []error) {
	if env.Kind == "" && env.APIVersion == "" {
		return nil, nil, nil // an empty document
	}
	if env.Kind == "List" {
		for i := range env.Items {
			a, s, e := parseEnvelope(from, env.Items[i])
			apps, skipped, errs = append(apps, a...), append(skipped, s...), append(errs, e...)
		}
		return apps, skipped, errs
	}
	var meta objectMeta
	if len(env.Metadata) > 0 {
		if err := yaml.Unmarshal(env.Metadata, &meta); err != nil {
			return nil, nil, []error{fmt.Errorf("%s: %s: metadata: %w", from, env.Kind, err)}
		}
	}
	group, version, _ := strings.Cut(env.APIVersion, "/")
	if group != jeffrescGroup {
		return nil, []string{fmt.Sprintf("%s %s/%s (%s)", env.Kind, meta.Namespace, meta.Name, from)}, nil
	}
	s := source{from: from, kind: env.Kind, name: meta.Name, namespace: meta.Namespace}
	switch {
	case version != "v1alpha1":
		return nil, nil, []error{fmt.Errorf("%s: %s: unsupported apiVersion %q (expected %s/v1alpha1)",
			from, s.ref(), env.APIVersion, jeffrescGroup)}
	case env.Kind == kindSAML:
		return nil, nil, []error{fmt.Errorf("%s: %s: unsupported: SAML applications have no equivalent "+
			"(this operator has no SAML provider kind); leave it on the JeffResc operator", from, s.ref())}
	case env.Kind != kindV1 && env.Kind != kindV2 && env.Kind != kindGroup:
		return nil, nil, []error{fmt.Errorf("%s: %s: unsupported kind %q", from, s.ref(), env.Kind)}
	case meta.Name == "":
		return nil, nil, []error{fmt.Errorf("%s: %s has no metadata.name", from, env.Kind)}
	}
	if err := decodeSpec(env.Spec, &s); err != nil {
		return nil, nil, []error{fmt.Errorf("%s: %s: spec: %w", from, s.ref(), err)}
	}
	return []source{s}, nil, nil
}

// decodeSpec strictly decodes spec into s: s.group for an AuthentikGroup, else
// s.spec.
func decodeSpec(spec json.RawMessage, s *source) error {
	if s.kind == kindGroup {
		s.group = &jeffGroupSpec{}
		return yaml.UnmarshalStrict(spec, s.group)
	}
	return yaml.UnmarshalStrict(spec, &s.spec)
}
