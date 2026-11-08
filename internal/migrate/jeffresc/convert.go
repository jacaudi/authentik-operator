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

import "errors"

// Options control the conversion.
type Options struct {
	// DryRunAdoption sets adoption.dryRun on every emitted object.
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
	// Report is a human-readable account of what the new operator will
	// default, for the person running the migration.
	Report string
}

// Convert converts the JeffResc objects found in inputs.
func Convert(_ []Input, _ Options) (*Result, error) {
	return nil, errors.New("not implemented")
}
