# Sandbox service API: execution, files and individual stop

Status: experimental implementation, verified through an isolated full sandbox-manager in the Kata test environment. Both the native container profile and the opt-in private-runtime strict profile are covered. Existing deployments are unchanged.

## API contract

The native paths below also exist under `/kruise/api` through RegisterE2BRoute. They are extensions to the Sandbox control API; the E2B envd protocol is unchanged.

| Method | Path | Input / output |
|---|---|---|
| GET | /sandboxes/{sandboxID}/services | Current podUID and declared services/state/stopSupported |
| POST | /sandboxes/{sandboxID}/services/{service}/exec | JSON podUID, args[], timeoutSeconds (default30, 1..60); stdout/stderr base64, exit_code |
| GET | /sandboxes/{sandboxID}/services/{service}/files?path=... | If-Match: podUID; raw bytes with SHA256, length, attachment filename, no-store |
| POST | /sandboxes/{sandboxID}/services/{service}/stop | JSON podUID, timeoutSeconds; termination observation receipt; no whole-Pod fallback |

All routes use the existing API-key middleware, public Sandbox ID resolution and ownership lookup. Manager rechecks owner and validates operation inputs. Unknown fields such as user/container/namespace are rejected. Commands run as the container's configured UID. The optional neutral Infra capability keeps other backends compatible; no existing interface implementations or production routing defaults are expanded.

## Trusted template opt-in

The Pod template annotation `sandbox.agents.kruise.io/services` declares a JSON map, for example:

```json
{"main":{"container":"sandbox","exec":true,"downloadRoots":["/app"],"stop":true},"observer":{"container":"observer","exec":true,"downloadRoots":["/audit"],"stop":true}}
```

Each declared regular container must include downward-API env `SANDBOX_SERVICE_POD_UID` from `metadata.uid`. API clients cannot choose arbitrary container names or UID overrides. No annotation means no service access. Images currently require Python at the `python` executable. Declarations belong to the trusted template; API callers receive only the service operations their API key owns. This does not grant envd/agent credentials access to the control API. An audit workload should keep the platform API key outside the solving container.

The backend uses informer-backed observations and validates Sandbox UID/owner, controller ownerReference, Pod UID, container ID and zero restart count. The remotely executed helper rechecks the downward-API Pod UID before spawning commands, reading files or sending TERM, preventing the stale-cache/name-reuse race from targeting a replacement Pod.

## Execution and data handling

Foreground exec has a process-group supervisor with independent deadline, bounded1MiB stdout/stderr and cleanup on timeout/output overflow/stdin disconnect. Command descendants in its process group are terminated. Deliberately daemonized processes escaping the group are not a containment guarantee. The Sandbox's container lifetime remains the final resource boundary. Per-backend service operations are bounded to16 concurrent calls.

Downloads are bounded8MiB regular files under declared roots. Final symlinks, special files, traversal and resolved paths outside those roots are rejected; checks use the opened fd. Downloads require a running container. A new explicit RawBody type preserves binary bytes through the existing web framework; ordinary JSON responses are unchanged. Bohr's existing GET/POST wildcard proxy preserves these paths, If-Match and binary responses; its compatibility tests passed without production proxy changes.

## Individual stop semantics

Stop requires declaration permission, restartPolicy Never, separate PID namespaces and no hostPID. It sends TERM only to PID1 of that container, then waits for the same observed container ID to terminate. Other services keep running. A repeated request for an already terminated incarnation returns its same observation. A failed/unconfirmed stop returns an error with Should-Retry:false; no Pod deletion/restart is used as a substitute. The observation explicitly has targetProcessesEmpty:null and is not a Kata guest cgroup proof.

The native container profile does not provide non-cooperative force-stop or PID32 guest proof. It does not support streaming/PTY, arbitrary-image helpers or post-stop filesystem extraction. The strict profile below has separate proof and persistence semantics.

## Validation and deployment boundary

Full regressions of web/E2B/Manager/Sandbox CR packages passed; focused race tests cover binary fidelity, service policy/identity, request/auth guards and process supervision. A pre-existing claim_test.go missing sync import was repaired to compile that regression suite. The first live driver expected200 for API-key creation, while the real API correctly returned201; the driver was fixed and the failed namespace/key store was reclaimed. Successful runs verified native/custom routes, a proven-valid nonowner key denial, nonzero exits, wrong UID/service, binary hash, symlink denial, timeout parent/child listener removal, separate stop/observer continuity, reward1 verifier and ignored TERM -> Unknown without deleting the Pod.

The test used a new full sandbox-manager process on the development host against dedicated namespaces in kata-poc. It did not replace the cluster's existing manager/controller or trigger release CI. All owned resources, temporary reader Pod and credentials were reclaimed.

## Opt-in strict Kata profile

The default-empty `--kata-service-bindings` flag loads an operator-owned JSON array of exact namespace, Sandbox UID, outer Pod UID, control-container name, execution ID and control token bindings. The token-bearing file must be 0600 and kept outside workloads and source control. A tenant cannot choose a runtime URL, token, container ID or alternate execution. Only the configured incarnation activates this profile; other services retain native behavior.

A Sandbox CR/controller owns an outer privileged supervisor. An independent patched containerd/Kata runtime inside it owns main and observer in a single Kata PodSandbox/VM. The existing node RuntimeClass, handler and host configuration remain unchanged. This is a frozen synthetic test profile, not a production rollout or a general provider.

Inventory includes `runtimeProfile=kata-pids32-guest-proof-v1`, `policyReceipt` and per-service `pidLimit`. Before forwarding an operation, the manager checks the trusted guest proofs showing observer requested/effective PID32 before startup and while running. It uses Kubernetes port-forward to the pinned supervisor, authenticates with the operator token, validates the immutable journal target and rechecks the outer Pod after the response. The supervisor rechecks the binding and downward-API Pod UID. These guarantees trust the operator/supervisor and authenticated guest-agent channel; workload self-reported JSON is insufficient.

Strict stop journals intent before `containerd.Task.Kill(SIGKILL,all)`. The formal API requires the same guest boot, service/container and cgroup inode/device, two empty samples, current0/populated0/no processes, confirmed cgroup removal and, for main, continuous observer identity/PID32. Receipt validation is shared with the independent runtime. Incomplete/ambiguous evidence returns an error with no success fallback. Repeated requests recover the existing receipt without another kill; restarting the API process preserves it. Supervisor/journal loss remains unknown and is outside this recovery guarantee.

The strict formal-API test passed startup PID32, fork rejection at32, TERM-ignoring main/child exit137, guest cgroup clearance/removal, surviving observer collect/download and original synthetic fixture reward1. It also rejected a one-empty-sample receipt, returned the identical original receipt after API restart and denied wrong key/service/Pod UID. This round ran an independent host verifier process; no additional Harbor, B01, node reboot or network fault run is claimed. All owned runtime tasks/containers/active snapshots/processes/cgroups, Sandbox/Pod/namespace, reader Pod and credentials were reclaimed; the original host runtime configuration was unchanged.

Deployment, trust boundaries and source patches: [Kata profile](../../integrations/kata/README.md). Persistent cross-supervisor storage, multi-replica recovery, formal runtime builds/releases, concurrency/long-duration validation and broader fault recovery remain future work.
