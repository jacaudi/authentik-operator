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

// Command convert-jeffresc converts JeffResc/authentik-k8s-operator
// resources (AuthentikApplication, AuthentikOAuth2Application) into this
// operator's AuthentikOAuthProvider, AuthentikApplication and
// AuthentikCredentialsTemplate, with managementPolicy Adopt. See
// docs/migration-from-jeffresc.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jacaudi/authentik-operator/internal/migrate/jeffresc"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main with its dependencies passed in; it returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("convert-jeffresc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		write(fs.Output(), "usage: convert-jeffresc --out DIR [flags] FILE_OR_DIR...\n")
		fs.PrintDefaults()
	}
	var opts jeffresc.Options
	force := fs.Bool("force", false, "write into a non-empty --out directory")
	out := fs.String("out", "", "directory to write the converted objects to (required)")
	fs.BoolVar(&opts.DryRunAdoption, "dry-run-adoption", true,
		"set spec.dryRun on every converted provider, application and group")
	fs.StringVar(&opts.Namespace, "namespace", "",
		"namespace for the converted objects (default: each source's namespace)")
	fs.StringVar(&opts.AuthentikRef, "authentik-ref", "", "name of an AuthentikDeployment to reference as authentikRef")
	fs.StringVar(&opts.TemplateName, "template-name", "",
		"name for the AuthentikCredentialsTemplate (default jeffresc-<hash>)")
	fs.StringVar(&opts.SecretSuffix, "secret-suffix", "",
		"suffix for the rendered Secret names, to run beside the old Secrets")
	fs.StringVar(&opts.ProviderSuffix, "provider-suffix", "", "suffix for the AuthentikOAuthProvider resource names")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	inputs, err := jeffresc.ReadInputs(fs.Args())
	if err != nil {
		return fail(stderr, err)
	}
	res, err := jeffresc.Convert(inputs, opts)
	if err != nil {
		return fail(stderr, err)
	}
	if entries, err := os.ReadDir(*out); err == nil && len(entries) > 0 && !*force {
		return fail(stderr, errors.New(*out+" is not empty; use --force to write into it"))
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return fail(stderr, err)
	}
	paths, err := writeFiles(*out, res.Files)
	for _, p := range paths {
		write(stdout, p+"\n")
	}
	if err != nil {
		return fail(stderr, err)
	}
	write(stderr, res.Report)
	return 0
}

// writeFiles writes each file into dir through an os.Root, so no file name can
// reach outside dir, and returns the paths it wrote.
func writeFiles(dir string, files []jeffresc.File) (paths []string, err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	for _, f := range files {
		if err := root.WriteFile(f.Name, f.Data, 0o600); err != nil {
			return paths, fmt.Errorf("write %s: %w", f.Name, err)
		}
		paths = append(paths, filepath.Join(dir, f.Name))
	}
	return paths, nil
}

// fail reports err and returns the exit code for a failure.
func fail(w io.Writer, err error) int {
	write(w, "error: "+err.Error()+"\n")
	return 1
}

// write prints s to w. A failure to print a message has nowhere to be
// reported, so it is dropped.
func write(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		return
	}
}
