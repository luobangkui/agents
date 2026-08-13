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

package job

import (
	"regexp"
	"strings"
)

// deliveryTagRe matches mid-lbg-image NYDUS delivery tags:
//   <repository>:delivery-<sha256-hex>-v<N>
var deliveryTagRe = regexp.MustCompile(`^(.+):delivery-([a-f0-9]{64})-v[0-9]+$`)

// InferOCIBaseImageFromDeliveryRef converts a NYDUS delivery image reference into
// the corresponding OCI digest-pinned base image reference.
//
// Example:
//
//	registry/repo:delivery-e133…bf39-v3
//	→ registry/repo@sha256:e133…bf39
func InferOCIBaseImageFromDeliveryRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	// Strip optional digest suffix on the tag form (rare).
	if at := strings.LastIndex(ref, "@"); at > 0 && strings.Contains(ref[at:], ":delivery-") {
		ref = ref[:at]
	}
	m := deliveryTagRe.FindStringSubmatch(ref)
	if len(m) != 3 {
		return "", false
	}
	return m[1] + "@sha256:" + m[2], true
}

// ResolveBaseImage picks an explicit base image when provided; otherwise tries to
// infer one from the running container's source image reference.
func ResolveBaseImage(explicit, sourceImage string) string {
	if base := strings.TrimSpace(explicit); base != "" {
		return base
	}
	if inferred, ok := InferOCIBaseImageFromDeliveryRef(sourceImage); ok {
		return inferred
	}
	return ""
}
