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
	"slices"
	"strings"
)

// Values the new CRDs default for fields JeffResc has no equivalent of.
// Adoption writes them onto the live object (the API server fills a CRD
// default before the operator sees the spec), so the report lists them. A
// test checks them against config/crd/bases.
const (
	newGrantTypes            = "[authorization_code, refresh_token]"
	newRefreshTokenThreshold = "seconds=0"
	newLogoutMethod          = "backchannel"
	newRedirectMatchingMode  = "strict"
	newRedirectType          = "authorization"
	newMetaHide              = "false"
)

// objectReport is what the person migrating should know about one object.
type objectReport struct {
	ref    string
	files  []string
	filled []string
	notes  []string
}

// describe adds the notes that do not depend on defaults.
func (b *builder) describe(a source, ns, oldSecret, newSecret string, rep *objectReport) {
	if a.spec.Provider.ClientType == "public" && usesClientSecret(a.spec.Secret.Template) {
		rep.notes = append(rep.notes,
			"public client: the template reads .ClientSecret, which is empty for a public client in both operators")
	}
	switch {
	case ns != a.namespace:
		rep.notes = append(rep.notes, fmt.Sprintf(
			"moves from namespace %s to %s: consumers must be pointed at Secret %s/%s", a.namespace, ns, ns, newSecret))
	case newSecret == oldSecret:
		rep.notes = append(rep.notes, fmt.Sprintf(
			"Secret %s keeps its name: this operator refuses to overwrite the Secret JeffResc owns (SecretConflict) "+
				"until the old one is deleted with the old CR; see the runbook, or convert with --secret-suffix", oldSecret))
	default:
		rep.notes = append(rep.notes, fmt.Sprintf(
			"Secret %s is rendered as %s alongside the old one: compare, then point consumers at it", oldSecret, newSecret))
	}
	rep.notes = append(rep.notes,
		"JeffResc labels its Secret goauthentik.io/application=<name>; this operator does not set that label")
}

func usesClientSecret(tmpl string) bool { return strings.Contains(tmpl, ".ClientSecret") }

// report renders the report. It is deterministic.
func (b *builder) report(skipped []string) string {
	var sb strings.Builder
	sb.WriteString("convert-jeffresc report\n=======================\n\n")
	sb.WriteString("Adoption will WRITE every field below onto the live objects in Authentik. Before turning\n")
	sb.WriteString("dryRun off, read each object's Ready condition (reason AdoptionDryRun lists `field: old -> new`)\n")
	sb.WriteString("and edit the spec until the only listed changes are ones you intend.\n")
	for _, r := range b.reports {
		fmt.Fprintf(&sb, "\n%s\n", r.ref)
		fmt.Fprintf(&sb, "  files: %s\n", strings.Join(r.files, ", "))
		sb.WriteString("  unset in JeffResc, written explicitly with JeffResc's default (keeps the live value):\n")
		writeList(&sb, r.filled)
		sb.WriteString("  defaulted by this operator's CRDs (no JeffResc equivalent; adoption writes them):\n")
		writeList(&sb, []string{
			"provider.grantTypes = " + newGrantTypes,
			"provider.refreshTokenThreshold = " + newRefreshTokenThreshold,
			"provider.logoutMethod = " + newLogoutMethod,
			fmt.Sprintf("provider.redirectUris[*].matchingMode = %s, .type = %s (what JeffResc sent for plain URIs)",
				newRedirectMatchingMode, newRedirectType),
			"application.metaHide = " + newMetaHide,
		})
		if len(r.notes) > 0 {
			sb.WriteString("  notes:\n")
			writeList(&sb, r.notes)
		}
	}
	if len(b.templates) > 0 {
		sb.WriteString("\nAuthentikCredentialsTemplates (cluster-scoped; a platform admin applies them)\n")
		for _, k := range sortedKeys(b.templateNames) {
			g := b.templates[k]
			fmt.Fprintf(&sb, "  %s: namespaces %s\n", b.templateNames[k], strings.Join(g.namespaces, ", "))
		}
	}
	if len(skipped) > 0 {
		sb.WriteString("\nSkipped (not goauthentik.io):\n")
		slices.Sort(skipped)
		writeList(&sb, skipped)
	}
	return sb.String()
}

func writeList(sb *strings.Builder, items []string) {
	if len(items) == 0 {
		sb.WriteString("    (none)\n")
		return
	}
	for _, it := range items {
		fmt.Fprintf(sb, "    - %s\n", it)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
