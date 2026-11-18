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
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestConvertNamesFilesFromValidNamesAndSlugsOnly pins that every output file
// name is built from a Kubernetes name and a slug the operator's own CRD
// accepts, so a name is always one plain component of the output directory.
func TestConvertNamesFilesFromValidNamesAndSlugsOnly(t *testing.T) {
	const manifest = `apiVersion: goauthentik.io/v1alpha1
kind: AuthentikApplication
metadata: {name: %q, namespace: ns}
spec:
  name: Demo
  slug: %q
  provider:
    authorizationFlow: a
    invalidationFlow: b
    redirectUris: ["https://demo.example.com/cb"]
`
	for _, tc := range []struct {
		name, metadataName, slug string
		wantErr                  string
	}{
		{name: "plain slug", metadataName: "demo", slug: "my_app-1"},
		{name: "slug with separators", metadataName: "demo", slug: "/../../escape", wantErr: "slug"},
		{name: "slug with a dot", metadataName: "demo", slug: "my.app", wantErr: "slug"},
		{name: "name with a separator", metadataName: "a/b", slug: "ok", wantErr: "metadata.name"},
		{name: "name that climbs", metadataName: "../x", slug: "ok", wantErr: "metadata.name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := fmt.Sprintf(manifest, tc.metadataName, tc.slug)
			res, err := Convert([]Input{{Source: "input.yaml", Data: []byte(in)}}, Options{DryRunAdoption: true})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range res.Files {
				if f.Name != filepath.Base(f.Name) {
					t.Errorf("file name %q is not a single path component", f.Name)
				}
			}
		})
	}
}
