# urunc × Argo Workflows — Verified Integration Status & Remaining Work

This file records only statements verified from (1) the checked-out source, (2) repository
documentation, (3) an actually-executed test/POC, or a combination. Items not verified are marked
explicitly in §5. Environment for all live results: single-node k3s `v1.36.2+k3s1`, urunc
`0.7.0-c6bcc89-dirty` (branch `argo-poc-integration`), monitor `solo5-spt`, no `/dev/kvm`,
Argo Workflows `v3.6.5` (emissary executor).

---

## 1. Current verified implementation

**Argo detection & scoping** — `parseArgoTask` (`pkg/containerd-shim/task_service.go`).
A container is treated as an Argo "main" only if: the `com.urunc.unikernel.sandboxProfile` annotation
is `argo-workflow` (or, when absent, the process argv is `argoexec emissary …` — `isArgoEmissaryArgs`),
AND it carries `com.urunc.unikernel.unikernelType` (i.e. it is a unikernel, not a sidecar), AND a
`/var/run/argo` mount exists. Any other container returns `(nil,false)` and every hook is inert.
Evidence: source `parseArgoTask`; live shim log shows only the `main` container tracked, not the
`wait` sidecar (`urunc(shim): tracking Argo main container … ctr/main`).

**Network behavior** — `getNetworkType` (`pkg/unikontainers/unikontainers.go`) returns `static` for a
Knative `user-container` or when `argoWorkflowContext(spec)` is true, else `dynamic`. Static mode is
the pre-existing `StaticNetwork` path (`pkg/network/network_static.go` `setNATRule`): tap `172.16.1.1`,
guest `172.16.1.2`, iptables `POSTROUTING … MASQUERADE`, `ip_forward=1`, and **no eth0 tc-redirect
steal filter**. Evidence (live): on an `argo-workflow` pod, `tc filter show dev eth0 ingress` = 0 steal
filters; tap = `172.16.1.1/24`; `POSTROUTING -s 172.16.1.0/24 -o eth0 -j MASQUERADE` present;
`ip_forward=1`; the guest (`net-spt-mirage`) was reachable on `172.16.1.2:8080` and the co-located
sidecar reached the API server (`10.43.0.1:443`). Sidecars never reach this code: they hit
`ErrNotUnikernel` and are delegated to runc in `cmd/urunc/create.go` before `SetupNet`/`getNetworkType`.

**Argo completion signaling** — the urunc monitor `syscall.Exec`s the VMM
(`pkg/unikontainers/unikontainers.go`), discarding Argo's `argoexec` wrapper, so the emissary exitcode
file is never written by the guest. The shim writes it instead: `taskService.Delete`
(`pkg/containerd-shim/task_service.go`) runs the inner `urunc delete`, then `writeArgoExitcode` writes
`<argoEmptyDirSource>/ctr/<name>/exitcode`. emissary's wait sidecar keys completion on that file's
existence (upstream `workflow/executor/emissary`, referenced by the maintainer in issue #135).

**Artifact extraction** — optional. If the main container declares
`com.urunc.unikernel.argoOutputVolume=<guest mount>`, `copyOutputs` copies regular files from the
block volume's restored host mount into `<argoEmptyDirSource>/outputs` during Delete, before the
exitcode file. Guards: symlinks and non-regular files skipped (never followed), `..`-escaping relative
paths rejected, total bytes capped at `maxExtractBytes` (64 MiB). Evidence: source `copyOutputs`;
tests below.

**Argo/non-Argo isolation** — non-Argo urunc pods and normal runc containers are unaffected:
`parseArgoTask` returns false (no argo mount / no profile / not a unikernel), leaving `argoTasks` empty
and the Delete hook a no-op. Evidence (live): a bare `net-spt-mirage` pod runs in `dynamic` mode (eth0
steal filter present) with the F1/F2 binary installed.

---

## 2. Verified validation already completed (concrete results)

All executed this work stream on the environment above.

- **Mixed Argo DAG:** `normal(runc) → urunc(unikernel) → normal(runc)` DAG **Succeeded ~35s**; all three
  nodes Succeeded; per-pod runtime classes correct (normal = "", urunc step = `urunc`); 0 solo5/tap
  leaks after.
- **Failure semantics:** non-zero-exit/crash → workflow **Failed**; cancellation (`shutdown: Terminate`)
  → **Failed**, 0 solo5/tap leaks; `activeDeadlineSeconds` timeout → **Failed**, 0 leaks.
- **Concurrent workflows:** 4 `argo-workflow` workflows launched simultaneously → distinct pod IPs
  (`10.42.0.45–48`), all **Succeeded ~18s**; each guest's `172.16.1.1/172.16.1.2` isolated in its own
  netns; distinct exitcode paths; 0 host-tap leaks; a bare non-Argo urunc pod stayed `dynamic`.
- **Artifact extraction primitive (unit / loop-mount level):** a real `mkfs.ext2` loop image with
  `result.txt` + `logs/run.log` and a symlink-escape entry → `copyOutputs` copied **2 files** with
  correct content, preserved directory structure, and did **not** copy the symlink. (Go test
  `TestLoopExtractDemo` against the live loop mount, plus the unit tests below.)
- **Artifact extraction in the real Argo/Kubernetes pod lifecycle (shim mechanism):** an
  `argo-workflow` Workflow with `com.urunc.unikernel.argoOutputVolume: /data` and a pre-seeded host
  directory (2 regular files + one symlink) mounted at `/data`. Verified in a real pod Delete:
  - shim journal: `urunc(shim): extracted Argo outputs … files=2` — the 2 regular files were copied,
    the symlink was **not** (`files=2`, not 3);
  - **extraction-before-completion ordering:** the `extracted Argo outputs` log line preceded the
    `wrote Argo completion file` line by ~276 µs (journal timestamps 06:18:25.289871 vs 06:18:25.290148);
  - on-disk capture before pod GC (`TEST 3b`): `<argoEmptyDir>/outputs/result.txt` contained the exact
    pre-seeded marker, `outputs/logs/run.log` preserved the nested path, permission `0644`, and
    `outputs/evil-symlink` was absent.
  Caveats (still mentorship scope, see §4/§5): the guest-side write was **simulated** by pre-seeding a
  hostPath (the `solo5-spt` monitor has no shared-fs and no block-writing unikernel image was
  available), and Argo-native output-*parameter/artifact consumption* (done by `argoexec`) is **not**
  wired — this test proves the shim extraction + symlink guard + ordering, not that a downstream step
  consumed the output as an Argo parameter.
- **Reboot / thin-pool recovery:** after an Oracle-console reboot, `containerd-thinpool.service`
  reattached the loopback-backed devmapper pool (`Result=success`, journal `containerd-pool
  reattached`) at 17:14:48, **before** containerd main start (17:14:49); containerd CRI loaded with
  **0 pool-query-failures**; node reached `Ready`; NRestarts=0 for containerd/k3s/thinpool.
- **Argo/urunc post-reboot smoke test:** an `argo-workflow` workflow **Succeeded in 13s** on the
  freshly-rebooted cluster; 0 solo5/tap leaks.
- **Unit tests (this change):** `go test ./pkg/containerd-shim/` — **8/8 PASS**; `-race` — **PASS**.
  `go vet ./pkg/containerd-shim/...` clean; `go build ./...` clean; `gofmt` clean.
- **POC smoke (F1/F2 binary):** `argo-workflow` workflow **Succeeded in 19s**; non-Argo `net-spt-mirage`
  pod **Running in 5s**, `dynamic` mode, reachable on the pod IP.

---

## 3. Verified issues fixed in this change (F1, F2)

**F1 — exec-process deletes must not consume the main container's argoTask.**
Verified contract: the inner containerd runc task service finalizes the container **only when
`r.ExecID == ""`** and returns `DeleteResponse.ExitStatus = p.ExitStatus()` of whatever the request
targeted (`containerd@v1.7.33 runtime/v2/runc/task/service.go` `Delete`); `DeleteRequest` has an
`ExecID` field (`containerd/api@v1.10.0 runtime/task/v2/shim.pb.go`). Previously `taskService.Delete`
did not check `r.ExecID`, so an exec-process delete for a tracked container ID would remove the
`argoTask` and publish the exec's exit status as the container's. Fix: `taskService.Delete` now returns
early when `r.ExecID != ""`, before touching `argoTasks`. Evidence: source
`pkg/containerd-shim/task_service.go` `Delete`; test `TestDeleteExecIDDoesNotConsumeArgoTask` — **PASS**
(asserts an `ExecID`-set delete preserves the `argoTask` and writes no exitcode; the following
`ExecID==""` delete consumes it and writes `"7"`).

**F2 — atomic exitcode publication.**
Previously `writeArgoExitcode` used `os.WriteFile` (`open(O_CREATE|O_TRUNC)` → write), which makes the
file exist empty before content lands; emissary keys completion on file existence. Fix: write the full
contents to `exitcode.*.tmp` in the same directory, `Chmod(0o644)`, then `os.Rename` into place
(atomic on the same filesystem); a deferred `os.Remove` prevents stale temp files on any failure path.
Format (decimal string) and permission (0644) preserved. Evidence: source
`pkg/containerd-shim/task_service.go` `writeArgoExitcode`; test
`TestWriteArgoExitcodeAtomicNoTempLeftover` — **PASS** (content `"255"` then overwrite `"0"`, perm
`0644`, no `.tmp` left); live: on a running workflow the published file was `exitcode=0`, perm `644`,
0 stale `.tmp`.

Diff scope: F1 = an 11-line guard in `Delete`; F2 = rewrite of `writeArgoExitcode`; plus two tests. No
other production behavior changed.

---

## 4. Remaining mentorship work (code-supported)

- **End-to-end Argo artifact chaining.** The shim extraction step is now proven in a real pod Delete
  (§2, TEST 3/3b): declared output-volume files are copied into `<argoEmptyDir>/outputs` (symlinks
  excluded), strictly before the completion file. What is **still not** proven: (a) the guest itself
  writing those files — in §2 the output volume was a pre-seeded hostPath, because `solo5-spt` has no
  shared-fs and no block-writing unikernel image was available; (b) full Argo `outputs.parameters/
  artifacts` resolution (done by `argoexec`, which never runs in the urunc main), i.e. an output
  produced by a urunc step and *consumed* by a later step. What remains: a block-backed pod volume, a
  unikernel image that writes output, and a mapping from `/var/run/argo/outputs` into Argo's
  parameter/artifact collection. Evidence of the gap: no block-writing unikernel image exists in the
  tested registry (available block images ship read-only ISOs); k3s local-path PVCs are
  filesystem-backed, not block.
- **F3 — retryable/idempotent exitcode publication.** `taskService.Delete` removes the `argoTask`
  before extraction+exitcode and treats the exitcode write as best-effort (logs on failure), so a
  transient write failure is unrecoverable. Not proven to fail in practice (all observed writes to the
  tmpfs emptyDir succeeded). A fix must preserve the current claim-under-lock concurrency property and
  handle a containerd Delete retry where the inner container is already deleted. Code: `Delete`,
  `writeArgoExitcode`.
- **F4 — destination-side symlink hardening.** `copyFile` and `writeArgoExitcode` open destination
  files with `O_CREATE|O_TRUNC`, following symlinks in the destination path. The guest itself does not
  directly control the host-side output destination in the tested path (it writes to its block device,
  not the host emptyDir — the guest→host source path in `copyOutputs` is symlink-safe, verified by
  `TestCopyOutputsSkipsSymlink`); however, destination-side symlink following remains a hardening gap
  if another co-pod container can write into the shared Argo output directory. Not proven exploitable.
  Candidate: `O_NOFOLLOW`/`openat2` on the destination. Code: `copyFile`, `writeArgoExitcode`.
- **Guest inbound on the pod IP.** In static/routed mode the guest owns `172.16.1.2`, not the pod IP;
  serving on the pod IP would need an inbound DNAT rule. Evidence (live): tcp/8080 on the pod IP was
  closed while open on `172.16.1.2`. Not implemented.
- **devmapper thin-device cleanup under ungraceful deletion.** `kubectl delete --force
  --grace-period=0` of urunc pods was observed to leak thin-device mounts, after which the devmapper
  snapshotter looped `failed to deactivate device: device or resource busy` and new urunc pod creation
  stalled (runc pods unaffected); recovered only by a VM reboot. This is baseline
  urunc/containerd/devmapper behavior (F1/F2 do not touch devmapper). Candidate: ensure guest mounts
  are released before thin-device deactivation in the Delete path (`pkg/unikontainers/unikontainers.go`
  `Delete`, `pkg/unikontainers/block.go`). Not yet root-caused to a specific urunc ordering bug.
- **shim tests not wired into `make unittest`.** `make unittest` runs `test_unikontainers test_metrics
  test_network test_hypervisors test_unikernels` (`Makefile`); the `pkg/containerd-shim` package is not
  included. The Argo tests here run via `go test ./pkg/containerd-shim/`. A `test_shim` target would add
  CI coverage.

---

## 5. Explicitly unverified / not implemented

- **F3 and F4 are not implemented** (intentionally out of scope for this change). They are described in
  §4 as candidates, not as fixed bugs.
- **KVM monitors not tested.** All results are `solo5-spt` only; the test VM has no `/dev/kvm`, so
  `qemu`/`firecracker`/`cloud-hypervisor` (and shared-fs, which `SupportsSharedfs()` reports false for
  spt/hvt/firecracker/hedge — `pkg/unikontainers/hypervisors/*.go`) were not exercised for Argo.
- **Multi-node behavior not tested** (single-node k3s only).
- **Full artifact chaining not demonstrated end-to-end** (see §4). The shim copy/ordering/symlink-guard
  is proven in a real pod (§2, TEST 3/3b), but the guest writing the output (simulated via pre-seeded
  hostPath) and Argo-native consumption of the output as a parameter/artifact are **not** demonstrated.
- **containerd-restart (not reboot) safety of the thin-pool unit** was validated by the idempotency
  no-op on an active pool, not by an actual containerd restart in the reboot-validation run.

---

## 6. Project / LFX outcome mapping

- **Architecture + incompatibility documentation:** substantially done — `ARGO_URUNC_ARCHITECTURE.md`,
  `ARGO_REPRO_RESULTS.md`, and the gap analysis capture the emissary completion mechanism, the
  network-model incompatibility, and the shared-fs constraint, with live evidence.
- **Working integration:** completion signaling + routed networking + sidecar coexistence are verified
  for spt batch steps (§2); F1/F2 harden the completion path. Artifact chaining is partial (§4).
- **Tutorial / deployment documentation:** not written — mentorship deliverable.
- **Production hardening / maintainer-directed work:** F3, F4, guest inbound DNAT, devmapper
  cleanup-under-force-delete, KVM-monitor and multi-node validation, and CI wiring of the shim tests
  (§4). Design-level items (retry idempotency, symlink threat model) need maintainer input.
