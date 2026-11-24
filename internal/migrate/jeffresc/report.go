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

// objectReport is what the person migrating should know about one object.
type objectReport struct {
	ref   string
	files []string
	notes []string
}

// describe adds the notes about the credentials Secret.
func (b *builder) describe(a source, ns, oldSecret, newSecret string, rep *objectReport) {
	if a.spec.Provider.ClientType == "public" && usesClientSecret(a.spec.Secret.Template) {
		rep.notes = append(rep.notes,
			"public client: the template reads .ClientSecret, which has the same value in both operators "+
				"(Authentik's, normally empty for a public client)")
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
	b.writeIntro(&sb)
	for _, r := range b.reports {
		fmt.Fprintf(&sb, "\n%s\n", r.ref)
		fmt.Fprintf(&sb, "  files: %s\n", strings.Join(r.files, ", "))
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
	if b.opts.AuthentikRef == "" {
		sb.WriteString("\nAdoption gate: these objects use the operator's default connection, where Adopt is refused\n")
		sb.WriteString("(reason AdoptionNotAllowed) unless the operator admin allows their namespaces. Set the flag\n")
		sb.WriteString("--default-connection-adoption-namespace-selector, or the Helm value\n")
		sb.WriteString("adoption.defaultConnectionNamespaceSelector, to:\n")
		namespaces := strings.Join(slices.Sorted(slices.Values(b.namespaces)), ", ")
		allowed := fmt.Sprintf("  %s in (%s)\n", metaNamespaceLabel, namespaces)
		sb.WriteString(allowed)
		if len(b.groupNamespaces) > 0 {
			// Only the groups' namespaces: an identity selector grants power over users and superuser groups.
			groupNamespaces := strings.Join(slices.Sorted(slices.Values(b.groupNamespaces)), ", ")
			sb.WriteString("\nIdentity gate: groups on the default connection are refused (reason IdentityNotAllowed)\n")
			sb.WriteString("unless the operator admin allows their namespaces with\n")
			sb.WriteString("--default-connection-identity-namespace-selector, or the Helm value\n")
			sb.WriteString("identity.defaultConnectionNamespaceSelector, set to:\n")
			fmt.Fprintf(&sb, "  %s in (%s)\n", metaNamespaceLabel, groupNamespaces)
		}
	}
	b.writeAdoptMode(&sb)
	return sb.String()
}

// onlyGroups reports that groups were converted and no provider or application.
func (b *builder) onlyGroups() bool { return len(b.groupNamespaces) > 0 && len(b.providers) == 0 }

// writeIntro says how the converted objects adopt.
func (b *builder) writeIntro(sb *strings.Builder) {
	if b.onlyGroups() {
		sb.WriteString("Every converted object adopts the group JeffResc created (managementPolicy: Adopt).\n")
		sb.WriteString("Adoption writes only the fields in the spec: a field JeffResc left unset is left unset\n")
		sb.WriteString("here, so the live value stays.\n")
	} else {
		sb.WriteString("Every converted object adopts the provider or application JeffResc created\n")
		sb.WriteString("(managementPolicy: Adopt). Adoption writes only the fields in the spec: a field JeffResc\n")
		sb.WriteString("left unset (it applied its own default) is left unset here, so the live value stays.\n")
		sb.WriteString("The one exception is grantTypes, which is always written: copied from JeffResc's\n")
		sb.WriteString("spec.provider.grantTypes (v0.2.7+) when it lists any, else [authorization_code, refresh_token],\n")
		sb.WriteString("JeffResc's own default (it treats an empty list as the pair). Authentik 2026.5+ rejects a login\n")
		sb.WriteString("whose grant type is not listed, and JeffResc v0.2.6 and older never sent the field, so providers\n")
		sb.WriteString("they created on 2026.5+ have none and reject every login (JeffResc issue 279). Providers that\n")
		sb.WriteString("existed before the 2026.5 upgrade were back-filled with all 7 grant types (Authentik migration\n")
		sb.WriteString("0032): the pair narrows those to 2, as JeffResc v0.2.7+ does itself, and drops implicit, hybrid,\n")
		sb.WriteString("password, client_credentials and device_code. The dry run shows the change per provider.\n")
	}
	sb.WriteString("Before turning dryRun off, read each object's Ready condition (reason AdoptionDryRun lists\n")
	sb.WriteString("`field: old -> new`) and edit the spec until the only listed changes are ones you intend.\n")
	if len(b.groupNamespaces) > 0 && !b.onlyGroups() {
		sb.WriteString("Converted groups adopt the group JeffResc created in the same way; their members are never\n")
		sb.WriteString("written (see each group's notes).\n")
	}
}

// writeAdoptMode ends the report with how to leave adopt mode.
func (b *builder) writeAdoptMode(sb *strings.Builder) {
	if b.onlyGroups() {
		sb.WriteString("\nAdopt mode: a missing group is reported as AdoptionTargetNotFound and nothing is created.\n")
		sb.WriteString("Once every group is migrated, leave adopt mode: remove managementPolicy and dryRun from\n")
		sb.WriteString("every converted group (final runbook step in docs/migration-from-jeffresc.md).\n")
		return
	}
	sb.WriteString("\nAdopt mode: a missing match target is reported as AdoptionTargetNotFound and nothing is\n")
	sb.WriteString("created. Once every app is migrated and SSO login is verified, leave adopt mode: remove\n")
	sb.WriteString("managementPolicy, match and dryRun from every converted provider and application (final\n")
	sb.WriteString("runbook step in docs/migration-from-jeffresc.md).\n")
	if len(b.groupNamespaces) > 0 {
		sb.WriteString("Converted groups have no match: remove managementPolicy and dryRun from them as well.\n")
	}
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
