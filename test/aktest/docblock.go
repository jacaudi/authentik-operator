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

package aktest

import (
	"fmt"
	"regexp"
	"strings"
)

// DocBlock returns the body of the single fenced code block between the
// HTML-comment markers <!-- e2e:NAME:begin --> and <!-- e2e:NAME:end --> in a
// Markdown document. The end-to-end suite runs the runbook's own scripts, so
// a runbook edit that breaks them fails the suite instead of going stale.
func DocBlock(markdown, name string) (string, error) {
	begin, end := "<!-- e2e:"+name+":begin -->", "<!-- e2e:"+name+":end -->"
	_, rest, found := strings.Cut(markdown, begin)
	if !found {
		return "", fmt.Errorf("marker %q not found", begin)
	}
	region, _, found := strings.Cut(rest, end)
	if !found {
		return "", fmt.Errorf("marker %q not found", end)
	}
	open := strings.Index(region, "```")
	if open < 0 {
		return "", fmt.Errorf("no fenced block between the %s markers", name)
	}
	body := region[open:]
	nl := strings.Index(body, "\n")
	if nl < 0 {
		return "", fmt.Errorf("unterminated fence in %s", name)
	}
	body = body[nl+1:]
	closeIdx := strings.Index(body, "\n```")
	if closeIdx < 0 {
		return "", fmt.Errorf("no closing fence in %s", name)
	}
	if strings.Contains(body[closeIdx+4:], "```") {
		return "", fmt.Errorf("more than one fenced block between the %s markers", name)
	}
	return body[:closeIdx+1], nil
}

var (
	headerAssign = regexp.MustCompile(`(?:^|;\s*)([A-Za-z_][A-Za-z0-9_]*)=('[^']*'|[^\s;]*)`)
	assignLine   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

// headerVars returns the names assigned in the script's header: the lines
// before the first command, ignoring blank lines, comments and a leading
// `bash ... <<'EOF'` wrapper. A value computed with $( ) is not configuration
// and is not a header assignment.
func headerVars(script string) []string {
	var names []string
	for line := range strings.SplitSeq(script, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "", strings.HasPrefix(t, "#"), strings.HasPrefix(t, "bash "):
			continue
		case !assignLine.MatchString(t):
			return names
		}
		ms := headerAssign.FindAllStringSubmatch(t, -1)
		if len(ms) > 0 && strings.HasPrefix(ms[0][2], "$(") {
			return names
		}
		for _, m := range ms {
			names = append(names, m[1])
		}
	}
	return names
}

// SetVars replaces the value of each header assignment NAME=value (value
// single-quoted or unquoted) in script. The set of header variables must equal
// the keys of vars: a variable missing from the script, or one the runbook
// gained that the caller does not set, is an error, so no variable silently
// keeps its documentation default.
func SetVars(script string, vars map[string]string) (string, error) {
	have := map[string]bool{}
	for _, n := range headerVars(script) {
		have[n] = true
	}
	for n := range have {
		if _, ok := vars[n]; !ok {
			return "", fmt.Errorf("the script assigns %s in its header but no value was given", n)
		}
	}
	for n := range vars {
		if !have[n] {
			return "", fmt.Errorf("variable %s is not assigned in the script header", n)
		}
	}
	for name, val := range vars {
		re := regexp.MustCompile(`(^|[\s;])` + regexp.QuoteMeta(name) + `=('[^']*'|[^\s;]*)`)
		quoted := "'" + strings.ReplaceAll(val, "'", `'\''`) + "'"
		done := false
		script = re.ReplaceAllStringFunc(script, func(m string) string {
			if done {
				return m // only the header assignment, never a later use
			}
			done = true
			sub := re.FindStringSubmatch(m)
			return sub[1] + name + "=" + quoted
		})
	}
	return script, nil
}

// ReplaceAll substitutes each old/new pair in text and fails if an old token
// does not occur, so a renamed placeholder in the runbook is an error rather
// than a silently unsubstituted script. Pairs are applied in order.
func ReplaceAll(text string, pairs ...string) (string, error) {
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(text, pairs[i]) {
			return "", fmt.Errorf("placeholder %q not found", pairs[i])
		}
		text = strings.ReplaceAll(text, pairs[i], pairs[i+1])
	}
	return text, nil
}
