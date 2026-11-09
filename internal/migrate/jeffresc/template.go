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
	"bytes"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"

	yaml "go.yaml.in/yaml/v3"

	"github.com/jacaudi/authentik-operator/internal/credentials"
)

// variableMap maps each variable a JeffResc secret template may use
// (internal/template.SecretData in v0.1.8 and v0.2.5) to the variable of
// internal/credentials.Vars that carries the same value. Every JeffResc
// variable exists unchanged; the map is explicit so that a future
// divergence is a one-line change and anything not listed is rejected.
func variableMap() map[string]string {
	names := []string{
		"ClientID", "ClientSecret", "IssuerURL", "AuthURL", "TokenURL", "UserInfoURL",
		"LogoutURL", "JWKSURL", "ProviderInfoURL", "Slug", "Name",
	}
	m := make(map[string]string, len(names))
	for _, n := range names {
		m[n] = n
	}
	return m
}

// jeffSecretData is the data JeffResc executes secret templates with.
type jeffSecretData struct {
	ClientID, ClientSecret, IssuerURL, AuthURL, TokenURL, UserInfoURL string
	LogoutURL, JWKSURL, ProviderInfoURL, Slug, Name                   string
}

// convertTemplate maps a JeffResc secret template to a credentials template
// body for this operator, or fails: it never returns a template that renders
// differently. Checked, in order: the template parses; it uses only known
// variables; this operator's restricted template subset accepts it; and it
// renders identically (as Secret data) to JeffResc's own rendering for
// sample values.
func convertTemplate(body string) (string, error) {
	t, err := template.New("secret").Option("missingkey=error").Parse(body)
	if err != nil {
		return "", fmt.Errorf("template does not parse: %w", err)
	}
	mapped := variableMap()
	var unknown []string
	for _, f := range fieldsUsed(t.Root) {
		if _, ok := mapped[f]; !ok && !slices.Contains(unknown, f) {
			unknown = append(unknown, f)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return "", fmt.Errorf("template uses unsupported variable(s) .%s; supported: .%s",
			strings.Join(unknown, ", ."), strings.Join(slices.Sorted(maps.Keys(mapped)), ", ."))
	}
	if _, err := credentials.Parse(body); err != nil {
		return "", fmt.Errorf("template uses a construct this operator does not allow: %w", err)
	}
	if err := credentials.Check(body); err != nil {
		return "", fmt.Errorf("template is not accepted by this operator: %w", err)
	}
	if err := sameRendering(t, body); err != nil {
		return "", err
	}
	return body, nil
}

// fieldsUsed returns the top-level field names a parse tree reads.
func fieldsUsed(root parse.Node) []string {
	var out []string
	stack := []parse.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f, ok := n.(*parse.FieldNode); ok {
			out = append(out, f.Ident[0])
		}
		stack = append(stack, children(n)...)
	}
	return out
}

// children returns the nodes directly below n. Typed nils (an absent else
// list or pipe) are returned as they are and yield no children themselves.
func children(n parse.Node) []parse.Node {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		return n.Nodes
	case *parse.ActionNode:
		return []parse.Node{n.Pipe}
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		out := make([]parse.Node, len(n.Cmds))
		for i, c := range n.Cmds {
			out[i] = c
		}
		return out
	case *parse.CommandNode:
		return n.Args
	case *parse.ChainNode:
		return []parse.Node{n.Node}
	case *parse.IfNode:
		return []parse.Node{n.Pipe, n.List, n.ElseList}
	case *parse.RangeNode:
		return []parse.Node{n.Pipe, n.List, n.ElseList}
	case *parse.WithNode:
		return []parse.Node{n.Pipe, n.List, n.ElseList}
	case *parse.TemplateNode:
		return []parse.Node{n.Pipe}
	default:
		return nil
	}
}

// sameRendering renders the template with JeffResc's pipeline (text/template
// on a struct, YAML into map[string]string) and with this operator's, for
// sample values, and requires identical Secret data.
func sameRendering(t *template.Template, body string) error {
	for _, name := range []string{"Example", "GitLab"} {
		const u = "https://authentik.example.com/application/o/example/"
		jd := jeffSecretData{
			ClientID: "client-id-value", ClientSecret: "client-secret-value", IssuerURL: u, AuthURL: u + "authorize/",
			TokenURL: u + "token/", UserInfoURL: u + "userinfo/", LogoutURL: u + "end-session/",
			JWKSURL: u + "jwks/", ProviderInfoURL: u + ".well-known/openid-configuration", Slug: "example", Name: name,
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, jd); err != nil {
			return fmt.Errorf("template does not render with JeffResc's variables: %w", err)
		}
		var old map[string]string
		if err := yaml.Unmarshal(buf.Bytes(), &old); err != nil {
			return fmt.Errorf("template output is not a YAML map: %w", err)
		}
		got, err := credentials.Render(body, credentials.Vars(jd))
		if err != nil {
			return fmt.Errorf("template does not render in this operator: %w", err)
		}
		if len(got) != len(old) {
			return errors.New("template renders different keys in this operator than in JeffResc")
		}
		for k, v := range old {
			if g, ok := got[k]; !ok || string(g) != v {
				return fmt.Errorf("template renders key %q differently in this operator than in JeffResc", k)
			}
		}
	}
	return nil
}
