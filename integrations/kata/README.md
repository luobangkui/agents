# Kata strict service profile (experimental)

This opt-in profile connects the normal Sandbox API to an independent, patched Kata runtime. The existing Sandbox controller owns an outer privileged supervisor Pod. Its private containerd owns **main and observer inside one Kata PodSandbox/VM**; these inner containers are not independently managed by kubelet. No existing node RuntimeClass or handler is upgraded.

## Boundaries

Use only an explicitly authorized test cluster/node. The supervisor has a 90-minute deadline, 8 CPU/8 GiB limit and a 16 GiB work volume. Host runtime executables and `/opt/kata` are read-only; no host runtime socket, hostPID or hostNetwork is used. This privileged supervisor remains inside the operator trust boundary and is not a production isolation guarantee.

The workload is a frozen, externally supplied Python UDP fixture with exactly main/observer services. Observer has only NET_RAW, a read-only root, a private audit tmpfs and a guest PID limit of 32 applied before startup. This is not a general Compose builder. B01, TLS/HTTP auditing, GPU/NAS, supervisor loss recovery, concurrent production operation and node reboot/network faults are outside this validation.

## Build

From the repository root:

```bash
go test ./pkg/auditruntime/kataproof ./pkg/sandbox-manager/config ./pkg/sandbox-manager/infra/sandboxcr ./pkg/servers/e2b ./pkg/servers/web
go build -o /tmp/sandbox-manager ./cmd/sandbox-manager
cd integrations/kata
go test ./pkg/...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/kata-service-test-api ./cmd/kata-service-test-api
```

The nested module pins containerd 1.7.34 and replaces the parent module with this checkout. Runtime source patches, source revision and artifact identity are in [runtime-patches](runtime-patches/README.md). Runtime binaries and fixture data are supplied by the operator, not published here. The tested archive SHA256 is `3461193e6d35f8c76f1409403fd7f882be9c8d3790a50b49ddd294e0f3a4f50a`; a rebuilt archive is not assumed byte-identical or automatically admitted.

## Prepare an isolated test

Prerequisites: an authorized test kubeconfig (0600), an existing Sandbox CR controller, a Linux amd64 KVM-capable test node with the pinned Kata host assets, kubectl/Python, the runtime archive and synthetic fixture. Inspect the generated supervisor specification before using a new environment. Every run uses a new run directory.

```bash
LAB=integrations/kata/lab/labctl.py
RUN=/tmp/unique-kata-test
# Supply TEST_CLUSTER_UID, TEST_NODE_NAME, TEST_NODE_UID and SANDBOX_API_OWNER_ID.
python3 "$LAB" deploy --run-dir "$RUN" --kubeconfig "$TEST_KUBECONFIG" --cluster-uid "$TEST_CLUSTER_UID" --node-name "$TEST_NODE_NAME" --node-uid "$TEST_NODE_UID" --owner-id "$SANDBOX_API_OWNER_ID"
python3 "$LAB" bootstrap --run-dir "$RUN" --kubeconfig "$TEST_KUBECONFIG" --cluster-uid "$TEST_CLUSTER_UID" --node-name "$TEST_NODE_NAME" --node-uid "$TEST_NODE_UID" --runtime-archive "$RUNTIME_ARCHIVE" --fixture "$SYNTHETIC_FIXTURE" --binary /tmp/kata-service-test-api
```

The script checks immutable cluster/node identity, controller ownership and Pod UID. Bootstrap warms the digest-pinned image, creates the private runtime, writes the immutable binding and prepares the frozen workload. `--fault ignore-term` is the default; `--fault ''` selects the ordinary main. `resume` accepts the same bootstrap arguments, verifies uploaded hashes and requires the existing private runtime to remain alive. It does not restart an exited runtime.

Bootstrap produces a **0600 `api-bindings.json`**. Add `--kata-service-bindings "$RUN/api-bindings.json"` to the otherwise normally configured sandbox-manager. Use its ordinary API-key/owner mechanism; the configured owner must match the Sandbox CR. Do not mount this binding or any API/control credentials into main/observer. It contains a secret and must never be logged, committed or published.

## Formal API contract and proof

See [the proposal](../../docs/proposals/sandbox-service-api.md) for routes. Inventory reports `kata-pids32-guest-proof-v1`, the startup policy receipt and PID32. Exec, raw file download and individual stop use existing API-key ownership checks. Runtime URLs, tokens, execution IDs and container IDs are operator/journal inputs, never tenant request parameters. The manager connects through authenticated Kubernetes port-forward and rechecks the outer Pod incarnation.

Strict stop records an intent, sends containerd `SIGKILL/all`, then requires a durable receipt with the same guest boot/container/cgroup identity, two empty samples and confirmed cgroup removal. Main stop also requires observer identity and PID32 continuity. The formal API independently validates the receipt. An incomplete proof is an error, not success. Retrying an existing intent only recovers its original receipt; it does not repeat the kill. API-process restart preserves that receipt; losing the supervisor or its journal remains unknown.

The test control API listens only on supervisor loopback and is reached through bounded local forwarding. It is an internal implementation interface, not a task-submission protocol. The runtime journal lives in supervisor `/work/api-state`; long-term shared storage and HA are not provided.

## Acceptance and cleanup

The live formal-API run verified PID32 at startup, kernel fork rejection at 32, main/child ignoring TERM followed by exit137, two guest empty samples/removal, observer continuity and original fixture reward1. An intentionally incomplete receipt was rejected; retry and API restart recovered the identical original receipt. This round used the fixture's independent host verifier process, not a new Harbor run or new verifier VM. The portable lab wrapper was subsequently syntax-checked; its packaging changes were not a second live run.

Before deleting the supervisor, download audit/result files, runtime journal and stop receipts. Using the private control token, POST the execution ID to `/v1/runtime/reclaim` and require `reclaimed:true`. Confirm no owned tasks, containers, active/view snapshots, private runtime processes or private cgroups remain; a task disappearing alone is insufficient. Keep any failed/unknown reclaim result for diagnosis.

Then delete only resources recorded in this run (UID preconditions):

```bash
python3 "$LAB" cleanup --run-dir "$RUN" --kubeconfig "$TEST_KUBECONFIG" --cluster-uid "$TEST_CLUSTER_UID" --node-name "$TEST_NODE_NAME" --node-uid "$TEST_NODE_UID"
```

Stop this run's local API/port-forward processes after checking ownership, and delete `executor-token`, `service-token`, `api-bindings.json`, temporary API credentials and kubeconfig. `cleanup` deletes cluster resources; it does not automatically delete local credentials or replace private-runtime reclamation. Do not rely only on the Pod deadline.
