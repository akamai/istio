// Copyright 2018 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kuberesource

import (
	"istio.io/istio/galley/pkg/config/schema"
	"istio.io/istio/galley/pkg/config/source/kube/rt"
	"istio.io/istio/galley/pkg/source/kube/builtin"
)

// DisableExcludedKubeResources is a helper that filters a KubeResources list to disable some resources
// Behaves in the same way as existing logic:
// - Builtin types are excluded by default.
// - If ServiceDiscovery is enabled, any built-in type should be readded.
func DisableExcludedKubeResources(input schema.KubeResources, excludedResourceKinds []string, enableServiceDiscovery bool) schema.KubeResources {

	var result schema.KubeResources
	for _, r := range input {

		if isKindExcluded(excludedResourceKinds, r.Kind) {
			// Found a matching exclude directive for this KubeResource. Disable the resource.
			r.Disabled = true

			// Check and see if this is needed for Service Discovery. If needed, we will need to re-enable.
			if enableServiceDiscovery {
				// IsBuiltIn is a proxy for types needed for service discovery
				a := rt.DefaultProvider().GetAdapter(r)
				if a.IsBuiltIn() {
					// This is needed for service discovery. Re-enable.
					r.Disabled = false
				}
			}
		}

		result = append(result, r)
	}

	return result
}

func isKindExcluded(excludedResourceKinds []string, kind string) bool {
	for _, excludedKind := range excludedResourceKinds {
		if kind == excludedKind {
			return true
		}
	}

	return false
}

// DefaultExcludedResourceKinds returns the default list of resource kinds to exclude, which is the builtin types.
func DefaultExcludedResourceKinds() []string {
	resources := make([]string, 0)
	for _, spec := range builtin.GetSchema().All() {
		resources = append(resources, spec.Kind)
	}
	return resources
}
