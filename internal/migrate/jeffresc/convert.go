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

// Package jeffresc converts JeffResc/authentik-k8s-operator custom resources
// (goauthentik.io) into the resources of this operator (authentik.io).
package jeffresc

import (
	"cmp"
	"errors"
	"maps"
	"slices"
)

// Options control the conversion.
type Options struct {
	// DryRunAdoption sets spec.dryRun on every emitted provider and application.
	DryRunAdoption bool
	// Namespace is the namespace of the emitted namespaced objects; empty
	// keeps each source object's namespace.
	Namespace string
	// AuthentikRef names an AuthentikDeployment for authentikRef.
	AuthentikRef string
	// TemplateName names the AuthentikCredentialsTemplate.
	TemplateName string
	// SecretSuffix is appended to every credentials Secret name.
	SecretSuffix string
	// ProviderSuffix is appended to the provider resource names.
	ProviderSuffix string
	// AcceptUnmanagedGroupBindings converts objects that list groupBindings.
	// This operator does not manage group bindings, and Authentik lets every
	// user in when an application has none, so without it such an object is an
	// error.
	AcceptUnmanagedGroupBindings bool
}

// Input is one YAML file's contents.
type Input struct {
	// Source names the file, for messages.
	Source string
	// Data is the YAML, possibly several documents.
	Data []byte
}

// File is one emitted object.
type File struct {
	// Name is the file name, <kind-lowercase>-<name>.yaml.
	Name string
	// Data is the YAML.
	Data []byte
}

// Result is the output of Convert.
type Result struct {
	// Files are the emitted objects, sorted by name.
	Files []File
	// Report is a human-readable account of the conversion (how adoption
	// treats unset fields, the Secret names, the adoption-gate selector to
	// set), for the person running the migration.
	Report string
}

// Convert converts the JeffResc objects found in inputs. Conversion is
// all-or-nothing: if any object cannot be converted faithfully, no files are
// returned and the error names every offending object.
func Convert(inputs []Input, opts Options) (*Result, error) {
	apps, skipped, err := parseInputs(inputs)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(apps, func(a, b source) int {
		return cmp.Or(cmp.Compare(a.namespace, b.namespace), cmp.Compare(a.name, b.name), cmp.Compare(a.kind, b.kind))
	})
	b := &builder{opts: opts, templates: map[string]*templateGroup{}, files: map[string]File{}}
	var errs []error
	for i := range apps {
		if err := b.add(apps[i]); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if len(apps) == 0 {
		return nil, errors.New("no AuthentikApplication or AuthentikOAuth2Application (goauthentik.io) found in the input")
	}
	if err := b.finish(); err != nil {
		return nil, err
	}
	files := slices.Collect(maps.Values(b.files))
	slices.SortFunc(files, func(x, y File) int { return cmp.Compare(x.Name, y.Name) })
	return &Result{Files: files, Report: b.report(skipped)}, nil
}
