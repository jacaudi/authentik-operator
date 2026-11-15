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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	sigsyaml "sigs.k8s.io/yaml"

	authentikiov1alpha1 "github.com/jacaudi/authentik-operator/api/v1alpha1"
)

// successCases are the golden cases that produce files.
func successCases() []goldenCase {
	var out []goldenCase
	for _, c := range goldenCases() {
		if c.wantErr == "" {
			out = append(out, c)
		}
	}
	return out
}

func convertCase(t *testing.T, tc goldenCase) *Result {
	t.Helper()
	in, err := os.ReadFile(filepath.Join("testdata", tc.name, "input.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert([]Input{{Source: "input.yaml", Data: in}}, tc.opts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestOutputDecodesStrictly decodes every emitted object into the typed Go
// structs, rejecting unknown fields.
func TestOutputDecodesStrictly(t *testing.T) {
	for _, tc := range successCases() {
		for _, f := range convertCase(t, tc).Files {
			t.Run(tc.name+"/"+f.Name, func(t *testing.T) {
				var head struct {
					Kind string `json:"kind"`
				}
				if err := sigsyaml.Unmarshal(f.Data, &head); err != nil {
					t.Fatal(err)
				}
				var err error
				switch head.Kind {
				case "AuthentikOAuthProvider":
					err = sigsyaml.UnmarshalStrict(f.Data, &authentikiov1alpha1.AuthentikOAuthProvider{})
				case "AuthentikApplication":
					err = sigsyaml.UnmarshalStrict(f.Data, &authentikiov1alpha1.AuthentikApplication{})
				case "AuthentikCredentialsTemplate":
					err = sigsyaml.UnmarshalStrict(f.Data, &authentikiov1alpha1.AuthentikCredentialsTemplate{})
				default:
					t.Fatalf("unexpected kind %q", head.Kind)
				}
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(f.Name, strings.ToLower(head.Kind)+"-") || !strings.HasSuffix(f.Name, ".yaml") {
					t.Fatalf("file %s is not named <kind>-<name>.yaml", f.Name)
				}
			})
		}
	}
}

// TestOutputAdmittedByAPIServer creates every emitted object in an envtest
// apiserver loaded with the real CRDs (schema, defaulting and CEL rules).
func TestOutputAdmittedByAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set (make test sets it)")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Error(err)
		}
	})
	if err := authentikiov1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	made := map[string]bool{}
	ensureNS := func(ns string) {
		if made[ns] {
			return
		}
		made[ns] = true
		n := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		if err := c.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	for i, tc := range successCases() {
		// Each case gets fresh object names: cases share names.
		for _, f := range convertCase(t, tc).Files {
			u := &unstructured.Unstructured{}
			if err := sigsyaml.Unmarshal(f.Data, &u.Object); err != nil {
				t.Fatal(err)
			}
			if ns := u.GetNamespace(); ns != "" {
				ensureNS(ns)
			}
			u.SetName(u.GetName() + "-" + string(rune('a'+i)))
			if err := c.Create(ctx, u); err != nil {
				t.Errorf("%s/%s: %v", tc.name, f.Name, err)
			}
		}
	}
}

// TestReportedDefaultsMatchCRDs keeps the "defaulted by this operator"
// section of the report honest against the generated CRDs.
func TestReportedDefaultsMatchCRDs(t *testing.T) {
	want := map[string]map[string]string{
		"authentik.io_authentikoauthproviders.yaml": {
			"grantTypes":            newGrantTypes,
			"refreshTokenThreshold": newRefreshTokenThreshold,
			"logoutMethod":          newLogoutMethod,
		},
		"authentik.io_authentikapplications.yaml": {"metaHide": newMetaHide},
	}
	for file, props := range want {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "crd", "bases", file))
		if err != nil {
			t.Fatal(err)
		}
		var crd struct {
			Spec struct {
				Versions []struct {
					Schema struct {
						OpenAPIV3Schema struct {
							Properties struct {
								Spec struct {
									Properties map[string]struct {
										Default any `yaml:"default"`
									} `yaml:"properties"`
								} `yaml:"spec"`
							} `yaml:"properties"`
						} `yaml:"openAPIV3Schema"`
					} `yaml:"schema"`
				} `yaml:"versions"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			t.Fatal(err)
		}
		got := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties
		for name, def := range props {
			if render := fmtDefault(got[name].Default); render != def {
				t.Errorf("%s %s: CRD default %q, report says %q", file, name, render, def)
			}
		}
	}
}

func fmtDefault(v any) string {
	if l, ok := v.([]any); ok {
		parts := make([]string, len(l))
		for i, e := range l {
			parts[i] = e.(string) //nolint:forcetypeassert // test: CRD defaults here are strings.
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	if s, ok := v.(string); ok {
		return s
	}
	if b, ok := v.(bool); ok && !b {
		return "false"
	}
	return "?"
}
