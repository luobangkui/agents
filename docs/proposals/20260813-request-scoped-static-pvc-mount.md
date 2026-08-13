---
title: Request-scoped static PVC mounts for E2B sandbox creation
authors:
  - "@luobangkui"
creation-date: 2026-08-13
status: implementable
---

# Request-scoped static PVC mounts for E2B sandbox creation

## Motivation

Some storage systems allocate an existing PersistentVolumeClaim for one
sandbox creation request. The claim name is not known when the SandboxSet or
SandboxTemplate is created, so it cannot be embedded in the warm-pool
template. The request must still use a SandboxSet, including a SandboxSet with
zero warm replicas, and inject the PVC before the cold-created Sandbox is sent
to Kubernetes.

## API contract

The E2B create request may carry the internal metadata key
`e2b.agents.kruise.io/static-pvc-mounts`. Its value is a JSON array:

```json
[
  {
    "claimName": "wenyon-lease-pvc",
    "mountPath": "/mnt/wenyon",
    "subPath": "jobs/current",
    "readOnly": false
  }
]
```

The API layer parses this key into a typed extension and removes it from user
metadata before annotations are propagated. Unknown JSON fields and trailing
JSON values are rejected. Claim names must be DNS subdomains. Mount paths must
be clean absolute paths other than `/`. Optional subpaths must be clean
relative paths and cannot traverse a parent. Duplicate claim names and mount
paths are rejected.

Static PVC mounts require `e2b.agents.kruise.io/create-on-no-stock` to be true.
They are supported only for SandboxSet-backed creation, not checkpoint clone.

## Layering and create flow

The API translates the protocol model into a neutral Infra option carried by
the Manager claim use case. Infra revalidates the option at its trust boundary.
No API model is imported by Manager or introduced into the Kubernetes-facing
implementation.

For a request containing static PVC mounts:

1. Claim is marked `RequireFresh`.
2. Infra bypasses all available and speculative warm-pool candidates.
3. Infra resolves the requested SandboxSet and builds a new Sandbox.
4. For an inline SandboxSet template, Infra deep-copies and mutates the inline
   PodTemplate.
5. For `templateRef`, Infra resolves the SandboxTemplate, materializes its
   PodTemplate into this Sandbox, and clears `templateRef` so the per-request
   PodTemplate can be mutated without changing a shared template.
6. Infra appends one Kubernetes PVC volume per request item and a matching
   VolumeMount to the container named `sandbox`, falling back to the first
   container for compatible templates.
7. The Sandbox is created through the existing claim lifecycle.

The generated volume name is stable and derived from the claim name. Existing
template volume-name, PVC-reference, and mount-path conflicts fail before the
Sandbox create call.

## Recycling

A Sandbox with request-scoped PVCs must not return to its source warm pool.
Infra removes `agents.kruise.io/cleanup-enabled` from the new Sandbox after
mount injection. Consequently the normal delete path deletes it instead of
triggering cleanup/recycle. The PVC remains externally owned; this feature
does not create, adopt, or delete PVCs.

## Compatibility

Requests without the extension follow the existing candidate selection and
template-reference behavior. No Sandbox, SandboxSet, or SandboxTemplate CRD
field is added. Existing runtime CSI mounts, volume claim templates, and
template-defined personal or shared storage behavior are unchanged.

## Failure behavior

Parsing and protocol conflicts return a bad request before claim. Deterministic
backend template and mount conflicts also return a typed bad request without
retrying. A missing referenced SandboxTemplate follows the existing
no-available behavior. A failed Kubernetes create follows the existing claim
admission cleanup and failed-sandbox retention policy.
