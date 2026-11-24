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

package controller

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Samples", func() {
	ctx := context.Background()

	It("accepts every manifest in config/samples", func() {
		const dir = "../../config/samples"
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(files).NotTo(BeEmpty())
		for _, file := range files {
			if filepath.Base(file) == "kustomization.yaml" {
				continue
			}
			raw, err := os.ReadFile(file)
			Expect(err).NotTo(HaveOccurred(), file)
			obj := &unstructured.Unstructured{}
			Expect(yaml.Unmarshal(raw, &obj.Object)).To(Succeed(), file)
			obj.SetNamespace("default")
			Expect(k8sClient.Create(ctx, obj, client.DryRunAll)).To(Succeed(), "sample %s is rejected", file)
		}
	})
})
