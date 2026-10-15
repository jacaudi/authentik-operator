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

package connection

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jacaudi/authentik-operator/internal/authentik"
)

// DefaultOptions is the operator-level fallback connection, used by objects
// that omit spec.authentikRef. The token is read from a file (a mounted
// Secret) or the AUTHENTIK_TOKEN environment variable (populated from a
// Secret via valueFrom), never from a command-line flag.
type DefaultOptions struct {
	URL                string
	ExternalURL        string
	TokenFile          string
	CAFile             string
	InsecureSkipVerify bool
}

// Env names, compatible with JeffResc/authentik-k8s-operator.
const (
	EnvURL         = "AUTHENTIK_URL"
	EnvExternalURL = "AUTHENTIK_EXTERNAL_URL"
	EnvToken       = "AUTHENTIK_TOKEN"
)

// DefaultConfig builds the default connection config, or returns nil when
// no default is configured (URL empty).
func DefaultConfig(o DefaultOptions, getenv func(string) string) (*authentik.Config, error) {
	if o.URL == "" {
		if o.TokenFile != "" || o.CAFile != "" || o.ExternalURL != "" {
			return nil, errors.New("default Authentik connection: token, CA or external URL configured without a URL")
		}
		return nil, nil
	}
	if o.ExternalURL != "" {
		u, err := url.Parse(o.ExternalURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("default Authentik connection: external URL %q must be an http(s) URL "+
				"without userinfo, query or fragment", o.ExternalURL)
		}
	}

	var token string
	if o.TokenFile != "" {
		b, err := os.ReadFile(o.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("default Authentik connection: read token file: %w", err)
		}
		token = strings.TrimSpace(string(b))
	} else {
		token = strings.TrimSpace(getenv(EnvToken))
	}
	if token == "" {
		return nil, fmt.Errorf("default Authentik connection: URL set but no token (set %s or the token file)", EnvToken)
	}

	cfg := &authentik.Config{URL: o.URL, Token: token, InsecureSkipVerify: o.InsecureSkipVerify}
	if o.CAFile != "" {
		b, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("default Authentik connection: read CA file: %w", err)
		}
		cfg.CABundle = b
	}
	// Validate eagerly so a bad flag fails at startup, not on first reconcile.
	if _, err := authentik.New(*cfg); err != nil {
		return nil, fmt.Errorf("default Authentik connection: %w", err)
	}
	return cfg, nil
}
