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
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The generated certificate must verify against the generated CA for the
// requested name, and must NOT verify against the system roots; otherwise the
// custom-CA scenario would pass vacuously.
func TestPKIServesOnlyToClientsThatTrustTheCA(t *testing.T) {
	p, err := NewPKI("authentik-tls.example.test")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(p.ServerCrt, p.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	dial := func(roots *x509.CertPool) error {
		tr := &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: roots, ServerName: "authentik-tls.example.test", MinVersion: tls.VersionTLS12}}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	if err := dial(nil); err == nil || !strings.Contains(err.Error(), "x509") {
		t.Fatalf("system roots must reject the private CA, got %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.CACert) {
		t.Fatal("CA PEM not parsed")
	}
	if err := dial(pool); err != nil {
		t.Fatalf("trusting the CA must work: %v", err)
	}
}
