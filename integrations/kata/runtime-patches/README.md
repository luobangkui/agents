# Pinned Kata test-runtime source

Base: kata-containers commit ddcb1ad8d23cbb4323f86c209f132b89592902df (Apache-2.0). Apply patches in this order: upstream-13826.patch, taskarena-guest-proof.patch, taskarena-agent-ctl.patch, taskarena-removal-proof.patch. These are test-only PID-resource propagation and trusted host-to-agent cgroup evidence changes. They do not constitute a node upgrade or production agent-policy review.

The tested archive SHA256 is3461193e6d35f8c76f1409403fd7f882be9c8d3790a50b49ddd294e0f3a4f50a. It contains the patched shim, agent, agent-ctl and guest image. It is operator-supplied; no binary image is committed. The earlier build used Rust1.95.0 and the musl target; agent feature unification affects reproducibility, so a rebuild must not be assumed byte-identical or admitted under this digest without verification. Obtain an approved matching artifact or review a newly built profile/digest explicitly. No upstream merge/release status is inferred from the patch name.

The extra guest evidence RPC is intended only for the trusted Kata host control channel. Full production policy, malicious-guest hardening and arbitrary workload coverage are outside this profile. The root neutral validator and runtime tests reject malformed attribution/removal evidence; the formal API performs the same validation.
