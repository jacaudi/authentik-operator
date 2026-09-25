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

package authentik

import (
	"errors"
	"fmt"
	"net/http"
)

var (
	// ErrNotFound is returned (wrapped) when Authentik answers 404, or a
	// lookup by stable identifier matches nothing. Only 404 maps here; a 400
	// is a validation error and must not be treated as absence.
	ErrNotFound = errors.New("authentik: not found")
	// ErrAmbiguous is returned when a lookup by identifier matches more than one object.
	ErrAmbiguous = errors.New("authentik: identifier matches more than one object")

	errNoRewind = errors.New("authentik: request body cannot be replayed for retry")
)

const maxErrorBody = 1024

// APIError is a non-2xx response from Authentik.
type APIError struct {
	StatusCode int
	Method     string
	Path       string
	// Body is the (truncated) response body; Authentik returns field-level
	// validation messages here on 400.
	Body string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("authentik: %s %s: %d %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Is makes errors.Is(err, ErrNotFound) true for 404 responses.
func (e *APIError) Is(target error) bool {
	return target == ErrNotFound && e.StatusCode == http.StatusNotFound
}

// IsNotFound reports whether err denotes a missing object.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsConflict reports whether err is a 409 or a 400 (Authentik reports unique
// constraint violations such as a duplicate slug as 400).
func IsConflict(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok &&
		(apiErr.StatusCode == http.StatusConflict || apiErr.StatusCode == http.StatusBadRequest)
}

// StatusCode returns the HTTP status of err, or 0 if it is not an APIError.
func StatusCode(err error) int {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		return apiErr.StatusCode
	}
	return 0
}

// Check converts the (response, error) pair returned by a generated client
// call into nil, an *APIError, or a wrapped transport error.
func Check(resp *http.Response, err error) error {
	if resp != nil && (resp.StatusCode < 200 || resp.StatusCode > 299) {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		if resp.Request != nil {
			apiErr.Method = resp.Request.Method
			apiErr.Path = resp.Request.URL.Path
		}
		var oaErr interface{ Body() []byte }
		if errors.As(err, &oaErr) {
			body := oaErr.Body()
			if len(body) > maxErrorBody {
				body = append(body[:maxErrorBody:maxErrorBody], "..."...)
			}
			apiErr.Body = string(body)
		}
		return apiErr
	}
	if err != nil {
		return fmt.Errorf("authentik: request failed: %w", err)
	}
	return nil
}
