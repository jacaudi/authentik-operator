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
// Templates use Go text/template with missingkey=error and no extra
// function library, like JeffResc/authentik-k8s-operator, but restricted
// to a safe subset (see validateTemplate): text, {{ .Var }}, if/else,
// comparisons and a width-free printf. That subset covers credential
// templates while making it impossible for whoever writes the template to
// hang the operator (unbounded range, recursive define) or exhaust its
// memory. Output is capped and parsed as a YAML mapping of string keys to
// string values.
package credentials

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"text/template/parse"

	yaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/util/validation"
)

// MaxOutput bounds rendered output; Secrets are limited to 1 MiB anyway.
const MaxOutput = 1 << 20

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

func (v Vars) data() map[string]string {
	return map[string]string{
		"ClientID": v.ClientID, "ClientSecret": v.ClientSecret, "IssuerURL": v.IssuerURL,
		"AuthURL": v.AuthURL, "TokenURL": v.TokenURL, "UserInfoURL": v.UserInfoURL,
		"LogoutURL": v.LogoutURL, "JWKSURL": v.JWKSURL, "ProviderInfoURL": v.ProviderInfoURL,
		"Slug": v.Slug, "Name": v.Name,
	}
}

// TemplateError is a user error in the template or its output. Retrying does
// not help until the spec changes. Messages never contain rendered content.
type TemplateError struct{ Err error }

// Error returns the message, prefixed so callers can tell a template
// problem from a transient one.
func (e *TemplateError) Error() string { return "credentials template: " + e.Err.Error() }

// Unwrap returns the underlying validation or rendering error.
func (e *TemplateError) Unwrap() error { return e.Err }

// IsTemplateError reports whether err is a *TemplateError.
func IsTemplateError(err error) bool {
	// errors.As rather than errors.AsType: errcheck reads AsType's first
	// result (a *TemplateError, which is an error) as a discarded error.
	var te *TemplateError
	return errors.As(err, &te)
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

	if err := checkVars(v); err != nil {
		return nil, err
	}
	t, err := Parse(tmpl)
	if err != nil {
		return nil, err
	}
	out := &limitedBuffer{max: MaxOutput}
	// A map (not the struct) so that missingkey=error governs unknown
	// variables, as in the operator this one replaces.
	if err := t.Execute(out, v.data()); err != nil {
		if errors.Is(err, errOutputTooLarge) {
			return nil, &TemplateError{Err: fmt.Errorf("output exceeds %d bytes", MaxOutput)}
		}
		// Execution errors name the template position and key, never values;
		// scrub the secret anyway.
		return nil, &TemplateError{Err: errors.New(scrub(err.Error(), v.ClientSecret))}
	}
	kv, err := parseOutput(out.Bytes())
	if err != nil {
		return nil, err
	}
	return toBytes(kv), nil
}

// checkVars rejects values containing control characters (newlines in
// particular): interpolated unescaped into YAML, they could add keys the
// template author did not write. Values come from the provider spec and from
// Authentik. The message names the variable, never its value.
func checkVars(v Vars) error {
	for k, val := range v.data() {
		if strings.ContainsFunc(val, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return &TemplateError{Err: fmt.Errorf("variable .%s contains control characters", k)}
		}
	}
	return nil
}

// Check validates a template by rendering it with example values, so that
// unknown variables and malformed output are reported before any provider
// uses it. It renders twice: with plain values, then with an application
// name containing YAML syntax (allowed in Authentik), which catches
// unquoted {{ .Name }}. Output that depends on other actual values can
// still fail later.
func Check(tmpl string) error {
	if _, err := Render(tmpl, exampleVars("Example")); err != nil {
		return err
	}
	if _, err := Render(tmpl, exampleVars(`Example: "app" #1, [a]`)); err != nil {
		return &TemplateError{Err: fmt.Errorf("%w (with an application name containing ': ', '#' and quotes; "+
			`quote .Name with {{ printf "%%q" .Name }})`, errors.Unwrap(err))}
	}
	return nil
}

func exampleVars(name string) Vars {
	const ex = "https://authentik.example.com/application/o/example/"
	return Vars{
		ClientID: "example-client-id", ClientSecret: "example-client-secret",
		IssuerURL: ex, AuthURL: ex, TokenURL: ex, UserInfoURL: ex, LogoutURL: ex, JWKSURL: ex, ProviderInfoURL: ex,
		Slug: "example", Name: name,
	}
}

// Parse parses and validates a template without executing it.
func Parse(tmpl string) (*template.Template, error) {
	t, err := template.New("secret").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return nil, &TemplateError{Err: err}
	}
	if err := validateTemplate(t); err != nil {
		return nil, &TemplateError{Err: err}
	}
	return t, nil
}

// predicateFunc reports whether name is an allowed function whose result
// reveals something about its arguments without printing them;
// .ClientSecret may not be an argument of one.
func predicateFunc(name string) bool {
	switch name {
	case "eq", "ne", "lt", "le", "gt", "ge", "and", "or", "not":
		return true
	default:
		return false
	}
}

// allowedFunc reports whether templates may call name.
func allowedFunc(name string) bool {
	return predicateFunc(name) || name == "print" || name == "printf"
}

// safePrintf matches printf formats without width or precision (no
// %0999999999d).
var safePrintf = regexp.MustCompile(`^([^%]|%[sqv%])*$`)

// validateTemplate allows only constructs whose cost is linear in the
// template size: text, comments, actions, if/else. No range, with,
// template/define/block, break/continue or variable declarations.
func validateTemplate(t *template.Template) error {
	if len(t.Templates()) > 1 {
		return errors.New("define/block is not allowed")
	}
	if t.Tree == nil || t.Root == nil {
		return nil
	}
	return (&validator{tree: t.Tree}).walkList(t.Root)
}

// validator walks a parse tree; tree is used for error locations.
type validator struct{ tree *parse.Tree }

// at returns " (line:col)" for n; the location refers to the template
// source, never to rendered values.
func (v *validator) at(n parse.Node) string {
	loc, _ := v.tree.ErrorContext(n)
	if _, pos, ok := strings.Cut(loc, ":"); ok {
		return " (at " + pos + ")"
	}
	return ""
}

func (v *validator) walkList(l *parse.ListNode) error {
	if l == nil {
		return nil
	}
	for _, n := range l.Nodes {
		if err := v.walkNode(n); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) walkNode(n parse.Node) error {
	switch n := n.(type) {
	case *parse.TextNode, *parse.CommentNode:
		return nil
	case *parse.ActionNode:
		return v.walkPipe(n.Pipe, false)
	case *parse.IfNode:
		// The condition must not depend on the client secret: success vs
		// failure of the render would otherwise leak it bit by bit.
		if err := v.walkPipe(n.Pipe, true); err != nil {
			return err
		}
		if err := v.walkList(n.List); err != nil {
			return err
		}
		return v.walkList(n.ElseList)
	case *parse.RangeNode:
		return fmt.Errorf("range is not allowed%s", v.at(n))
	case *parse.WithNode:
		return fmt.Errorf("with is not allowed%s", v.at(n))
	case *parse.TemplateNode:
		return fmt.Errorf("template is not allowed%s", v.at(n))
	default:
		return fmt.Errorf("%T is not allowed%s", n, v.at(n))
	}
}

func (v *validator) walkPipe(p *parse.PipeNode, predicate bool) error {
	if p == nil {
		return nil
	}
	if len(p.Decl) > 0 || p.IsAssign {
		return fmt.Errorf("variables are not allowed%s", v.at(p))
	}
	for _, cmd := range p.Cmds {
		if err := v.walkCommand(cmd, predicate); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) walkCommand(cmd *parse.CommandNode, predicate bool) error {
	if len(cmd.Args) == 0 {
		return nil
	}
	if id, ok := cmd.Args[0].(*parse.IdentifierNode); ok {
		if !allowedFunc(id.Ident) {
			return fmt.Errorf("function %q is not allowed%s", id.Ident, v.at(cmd))
		}
		if id.Ident == "printf" {
			if len(cmd.Args) < 2 {
				return fmt.Errorf("printf needs a format%s", v.at(cmd))
			}
			f, ok := cmd.Args[1].(*parse.StringNode)
			if !ok || !safePrintf.MatchString(f.Text) {
				return fmt.Errorf("printf format must be a literal using only %%s, %%q, %%v%s", v.at(cmd))
			}
		}
		predicate = predicate || predicateFunc(id.Ident)
		for _, a := range cmd.Args[1:] {
			if err := v.walkArg(a, predicate); err != nil {
				return err
			}
		}
		return nil
	}
	for _, a := range cmd.Args {
		if err := v.walkArg(a, predicate); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) walkArg(a parse.Node, predicate bool) error {
	switch a := a.(type) {
	case *parse.FieldNode:
		if len(a.Ident) != 1 {
			return fmt.Errorf("nested fields are not allowed%s", v.at(a))
		}
		if predicate && a.Ident[0] == "ClientSecret" {
			return fmt.Errorf(".ClientSecret may only be printed, not compared or tested%s", v.at(a))
		}
		return nil
	case *parse.StringNode, *parse.NumberNode, *parse.BoolNode:
		return nil
	case *parse.PipeNode:
		return v.walkPipe(a, predicate)
	default:
		return fmt.Errorf("%T is not allowed here%s", a, v.at(a))
	}
}

// parseOutput parses the rendered output as a single YAML mapping of scalar
// keys to scalar values. Errors describe the problem and position only.
func parseOutput(out []byte) (map[string]string, error) {
	dec := yaml.NewDecoder(bytes.NewReader(out))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, &TemplateError{Err: errors.New("output has no keys")}
		}
		return nil, &TemplateError{Err: fmt.Errorf("output is not valid YAML%s", yamlLine(err))}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &TemplateError{Err: errors.New("output must be a single YAML document")}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, &TemplateError{Err: errors.New("output must be a YAML mapping of keys to values")}
	}
	m := doc.Content[0]
	kv := make(map[string]string, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		entry := i/2 + 1
		if k.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode {
			return nil, &TemplateError{Err: fmt.Errorf("entry %d (line %d): keys and values must be plain strings",
				entry, k.Line)}
		}
		if _, dup := kv[k.Value]; dup {
			return nil, &TemplateError{Err: fmt.Errorf("entry %d (line %d): duplicate key", entry, k.Line)}
		}
		if errs := validation.IsConfigMapKey(k.Value); len(errs) > 0 {
			// The key is not echoed: it may contain rendered values.
			return nil, &TemplateError{Err: fmt.Errorf("entry %d (line %d): key is not a valid Secret key "+
				"(alphanumerics, '-', '_' or '.')", entry, k.Line)}
		}
		value := v.Value
		if v.Tag == "!!null" {
			value = ""
		}
		kv[k.Value] = value
	}
	if len(kv) == 0 {
		return nil, &TemplateError{Err: errors.New("output has no keys")}
	}
	return kv, nil
}

var yamlLineRe = regexp.MustCompile(`line (\d+)`)

// yamlLine extracts only the line number from a YAML error (the rest of the
// message can quote content).
func yamlLine(err error) string {
	if m := yamlLineRe.FindStringSubmatch(err.Error()); m != nil {
		return " (line " + m[1] + ")"
	}
	return ""
}

// ErrURLMismatch means an Authentik endpoint URL did not start with the
// operator's API base URL, so it could not be rewritten to the external URL.
var ErrURLMismatch = errors.New("endpoint URL does not match the Authentik API base URL")

// Rewrite replaces the internalBase prefix (scheme, host and path) of u --
// Authentik builds endpoint URLs from the host the operator's request was
// sent to -- with externalBase. Scheme and host compare case-insensitively
// and default ports are ignored. With no externalBase (or one equal to
// internalBase) u is returned unchanged. A URL outside internalBase is an
// error rather than silently left internal.
func Rewrite(u, internalBase, externalBase string) (string, error) {
	internalBase = strings.TrimRight(internalBase, "/")
	externalBase = strings.TrimRight(externalBase, "/")
	if externalBase == "" || strings.EqualFold(internalBase, externalBase) {
		return u, nil
	}
	pu, err1 := url.Parse(u)
	pi, err2 := url.Parse(internalBase)
	if err1 != nil || err2 != nil {
		return "", ErrURLMismatch
	}
	if !strings.EqualFold(pu.Scheme, pi.Scheme) || hostKey(pu) != hostKey(pi) {
		return "", fmt.Errorf("%w: %s", ErrURLMismatch, pu.Scheme+"://"+pu.Host)
	}
	prefix := strings.TrimRight(pi.EscapedPath(), "/")
	path := pu.EscapedPath()
	if path != prefix && !strings.HasPrefix(path, prefix+"/") {
		return "", fmt.Errorf("%w: path %s", ErrURLMismatch, path)
	}
	rest := strings.TrimPrefix(path, prefix)
	if pu.RawQuery != "" {
		rest += "?" + pu.RawQuery
	}
	return externalBase + rest, nil
}

func hostKey(u *url.URL) string {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (port == "80" && strings.EqualFold(u.Scheme, "http")) || (port == "443" && strings.EqualFold(u.Scheme, "https")) {
		port = ""
	}
	return host + ":" + port
}

var errOutputTooLarge = errors.New("output too large")

type limitedBuffer struct {
	bytes.Buffer

	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errOutputTooLarge
	}
	return b.Buffer.Write(p)
}

func scrub(msg, secret string) string {
	if secret == "" {
		return msg
	}
	return strings.ReplaceAll(msg, secret, "<redacted>")
}

func toBytes(m map[string]string) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		out[k] = []byte(v)
	}
	return out
}
