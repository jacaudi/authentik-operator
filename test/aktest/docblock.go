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

// SetVars replaces the value of each shell assignment NAME=value (value
// single-quoted or unquoted, ended by whitespace or ';') in script, and fails
// if a variable is not assigned in it, so a renamed variable in the runbook
// cannot silently leave the old value in place.
func SetVars(script string, vars map[string]string) (string, error) {
	for name, val := range vars {
		re, err := regexp.Compile(`(^|[\s;])` + regexp.QuoteMeta(name) + `=('[^']*'|[^\s;]*)`)
		if err != nil {
			return "", err
		}
		if !re.MatchString(script) {
			return "", fmt.Errorf("variable %s is not assigned in the script", name)
		}
		quoted := "'" + strings.ReplaceAll(val, "'", `'\''`) + "'"
		script = re.ReplaceAllStringFunc(script, func(m string) string {
			prefix := m[:len(m)-len(re.FindStringSubmatch(m)[2])-len(name)-1]
			return prefix + name + "=" + quoted
		})
	}
	return script, nil
}
