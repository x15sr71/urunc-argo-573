# Security / Threat-Model Validation — 2026-08-16

Evidence for a security section covering the urunc ↔ Argo POC (branch `argo-poc-integration`).
All results are source inspection or **non-destructive** live tests on VM `vidhya`. No production code,
test, cluster config or infrastructure was modified. **No Argo Workflow was created** (cluster retention
preserved: 1 workflow before and after). One bare pod (`audit-sec`) was created and gracefully deleted.

Complementary raw evidence: `LIVE_VALIDATION_2026-08-16.md` (network A/B, extraction on disk, exitcode on
disk, `execsnoop` runtime chain).

---

## S0. Test vehicle

A **bare pod** was used instead of a Workflow, so that Argo's retention policy was not consumed. It has the
same shape as an Argo pod (unikernel `main` under the `argo-workflow` sandbox profile + a Linux sidecar in
the shared pod netns) and exercises the identical `getNetworkType → argoWorkflowContext → static` branch.

```yaml
# audit-sec: annotations: com.urunc.unikernel.sandboxProfile: "argo-workflow"
# runtimeClassName: urunc
#   main    = harbor.nbfc.io/nubificus/urunc/net-spt-mirage      (unikernel)
#   sidecar = docker.io/kong/httpbin                             (runc, listens :80)
```
`2026-08-16T17:30:18Z  pod=audit-sec podIP=10.42.0.91 netns=/var/run/netns/cni-47397489-...`

**Limitation that applies to every network test below:** there is no client unikernel image available, so
traffic could not be generated *from inside the guest*. Egress tests were sourced from `172.16.1.1` (the
tap's host side, which the MASQUERADE rule `-s 172.16.1.0/24` matches identically). Locally-generated
traffic traverses `OUTPUT`, whereas real guest traffic traverses `FORWARD`/`INPUT`; the chain policies were
therefore inspected directly (S2) and are the authoritative result.

---

## S1. Trust boundaries and privilege — the VMM runs as fully-capable root

Side-by-side, **the same pod**, at `2026-08-16T17:32:52Z`:

```
--- solo5-spt VMM (unikernel "main"), pid 1813589 ---
Uid:        0  0  0  0
CapEff:     000001ffffffffff      <- ALL capabilities
CapBnd:     000001ffffffffff
NoNewPrivs: 1
Seccomp:    2   (Seccomp_filters: 1)
cgroup:     0::/system.slice/containerd.service
userns:     user:[4026531837]     <- host user namespace

--- httpbin sidecar (runc), pid 3109 ---
Uid:        10001  10001  10001  10001
CapEff:     0000000000000400      <- CAP_NET_BIND_SERVICE only
CapBnd:     00000000a80425fb
NoNewPrivs: 0
Seccomp:    0
cgroup:     0::/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod36c0c3bf_.../cri-containerd-f2e1ff94....scope
userns:     user:[4026531837]
```

`capsh --decode=000001ffffffffff` = `cap_sys_admin, cap_sys_module, cap_sys_rawio, cap_sys_ptrace,
cap_dac_override, cap_mknod, cap_bpf, cap_perfmon, …` (full set).

Namespaces of the VMM vs host PID 1: `mnt`, `net`, `pid`, `ipc`, `uts`, `cgroup` **isolated**;
`user` **shared with the host** (no user-namespace remapping).

**Findings**
- **[OBSERVED] S1-a — the process hosting the untrusted guest runs as root with the complete capability
  set, in the host user namespace**, while an ordinary container in the same pod is non-root with one
  capability. Linux capability confinement is therefore **not** the isolation boundary here.
- **[OBSERVED] S1-b — the VMM is not in the pod's kubepods cgroup.** It is accounted to
  `/system.slice/containerd.service` (`memory.max = max`), while the sidecar is in the correct
  `kubepods-besteffort-pod<uid>.slice` scope. Kubernetes CPU limits/shares therefore do not reach the VMM
  process on this path.
- **[CODE] S1-c — guest RAM *is* bounded by the pod memory limit.** `monitorMemoryBytes(defaultMemSizeMB,
  u.Spec.Linux.Resources)` (`unikontainers.go:508`) uses `resources.Memory.Limit` when set
  (`:440-442`), else a default. The observed `--mem=268` is the default because the test pod was
  BestEffort. No equivalent handling of CPU resources exists in `pkg/unikontainers`.

All of S1 is **inherited urunc behaviour, not added by this POC.**

---

## S2. The real containment boundary is solo5-spt's own seccomp sandbox

`Seccomp: 2` on the VMM does **not** come from urunc. urunc installs a seccomp filter only for **hvt**:

```
pkg/unikontainers/hypervisors/hvt.go:94-111   filter{DefaultAction: seccomp.ActionTrap, ...}; seccomp.LoadFilter
pkg/unikontainers/hypervisors/spt.go          func (s *SPT) PreExec(_ types.ExecArgs) error { return nil }   <- no-op
```

The filter is applied by **solo5-spt itself** (`~/solo5/tenders/spt/`), immediately before entering the
guest:

```
spt_core.c:152   spt->sc_ctx = seccomp_init(SCMP_ACT_KILL);      <- default action = KILL
spt_core.c:285   prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0);
spt_core.c:298   syscall(SYS_seccomp, SECCOMP_SET_MODE_FILTER, 0, &prog);
spt_core.c:300   spt_launch(...)   /* does not return */
```

Complete allowlist, argument-scoped where a file descriptor is involved:

| Syscall | Constraint | Source |
|---|---|---|
| `write` | **fd == 1** (stdout only) | `spt_core.c:322-323` |
| `exit_group` | — | `spt_core.c:326` |
| `epoll_pwait` | fd == epollfd | `spt_core.c:329` |
| `timerfd_settime` | fd == timerfd | `spt_core.c:334` |
| `clock_gettime` | — | `spt_core.c:337,342` |
| `arch_prctl` | — | `spt_core.c:348` |
| `read`, `write` | fd == **tap fd** | `spt_module_net.c:130,135` |
| `pread64`, `pwrite64` | fd == **block fd** | `spt_module_block.c:144,153` |

**[CODE] S2-a** — a guest that fully escapes the unikernel into the tender's address space can only write
to stdout, read/write the tap fd, pread/pwrite the block fd, poll, read the clock and exit. There is no
`open`/`openat`, `execve`, `socket`, `ptrace`, `mount` or `clone`. Everything else is `SCMP_ACT_KILL`.
This — not capability dropping — is what makes the root/full-caps posture of S1 tolerable.

**Scope limits:** this applies to **solo5-spt only**. `hvt` uses urunc's own `ActionTrap` filter;
qemu/firecracker/cloud-hypervisor were not examined and are untested here. The filter is installed *after*
tender setup, so the pre-launch setup phase runs unfiltered.

---

## S3. Network: the POC provides NAT and routing, **not** isolation or filtering

Full filter table inside the pod netns, `2026-08-16T17:30:18Z`:

```
$ iptables -S INPUT
-P INPUT ACCEPT
$ iptables -S FORWARD
-P FORWARD ACCEPT
$ iptables -S OUTPUT
-P OUTPUT ACCEPT
$ iptables -t nat -S
-P PREROUTING ACCEPT
-P INPUT ACCEPT
-P OUTPUT ACCEPT
-P POSTROUTING ACCEPT
-A POSTROUTING -s 172.16.1.0/24 -o eth0 -j MASQUERADE
```

```
$ ip route show
default via 10.42.0.1 dev eth0
10.42.0.0/24 dev eth0 proto kernel scope link src 10.42.0.91
172.16.1.0/24 dev tap0_urunc proto kernel scope link src 172.16.1.1
```

**[VERIFIED] S3-a — there is exactly one netfilter rule in the pod namespace, and it is a NAT rule.**
`INPUT`, `FORWARD` and `OUTPUT` are all policy `ACCEPT` with **zero** rules. Nothing restricts what the
guest may send or to whom. The static profile removes the dynamic-mode tc steal (see
`LIVE_VALIDATION_2026-08-16.md` §N1) and replaces it with unrestricted routed NAT.

**[VERIFIED] S3-b — no NetworkPolicy exists and none is enforced.**
`kubectl get networkpolicy -A` → `No resources found`; no kube-router/netpol controller process on the node.

Reachability from `172.16.1.1` (proxy — see S0 limitation), `2026-08-16T17:31:35Z`:

| Target | Result |
|---|---|
| K8s API service `10.43.0.1:443` | **TCP CONNECT OK**, `http_code=401` |
| node kubelet `10.0.0.100:10250` | **TCP CONNECT OK**, `http_code=400` |
| sidecar via pod IP `10.42.0.91:80` | **`http_code=200`** |
| sidecar via tap IP `172.16.1.1:80` | **`http_code=200`** |
| other pods (`10.42.0.66:9000`, `10.42.0.70:2746`, `10.42.0.68:9090`) | no connect |
| flannel gw `10.42.0.1:53` | connection refused |
| external `1.1.1.1:443` | no connect |

The three same-subnet pod targets and external egress did **not** connect from this source address. The
cause was **not isolated** (possible on-link routing/NAT interaction with the bound source address, or
target-side behaviour). **This is not evidence that a guest cannot reach them** — the authoritative result
is S3-a: `FORWARD` is policy ACCEPT with no rules, so nothing in the pod namespace filters forwarded guest
traffic. Treat lateral reachability as **[UNVERIFIED, presumed open]**, not as a demonstrated restriction.

**[VERIFIED] S3-c — MASQUERADE does not apply to pod-internal traffic.** httpbin echoed the client address:

```
from 172.16.1.1 to podIP:80   origin as seen by httpbin: 172.16.1.1
from 172.16.1.1 to tapIP:80   origin as seen by httpbin: 172.16.1.1
```

The rule is `-o eth0`, so only traffic *leaving* the pod is rewritten to the pod IP. Guest→sidecar traffic
arrives with its **guest-subnet source address intact**. Consequences: (i) the guest inherits the pod's
network identity only for egress — any IP-based policy applied to the pod also covers guest egress;
(ii) inside the pod the guest is a *distinct* L3 peer that sidecars will see as `172.16.1.x`.

---

## S4. Cross-container / sidecar exposure

```
$ ss -lntup   (pod netns)
tcp LISTEN 0 2048 0.0.0.0:80 0.0.0.0:*  users:(("gunicorn",pid=1814098,fd=5),("gunicorn",pid=1814078,fd=5))
```

**[VERIFIED] S4-a — a sidecar bound to `0.0.0.0` is bound on `tap0_urunc` too and is reachable from the
guest subnet** (`http_code=200` to both `10.42.0.91:80` and `172.16.1.1:80`), with `INPUT` policy ACCEPT.

**Attribution.** Sharing one network namespace between a pod's containers is **standard Kubernetes**
behaviour: any container can already reach a sibling's listening socket on `127.0.0.1` or the pod IP. What
is **urunc-specific** is that the sandboxed workload is a *separate L3 host on an attached tap* rather than
a process in that namespace — so (i) it reaches sidecars from a foreign source address (S3-c) rather than
loopback, and (ii) it is a routed peer whose traffic is forwarded, not a namespace-local process. The POC
does not add a filter between the two.

For the real Argo pod shape: the `wait` sidecar is an `argoexec` client and was **not** observed listening
on any port; the only listener in this test was the deliberately-added httpbin. Whether a production
`argoexec wait` exposes a listening port is **[UNVERIFIED]** here.

---

## S5. Host filesystem / artifact integrity

### What the guest can touch at all

Observed live VMM command line (the guest's entire host-facing device set):

```
/usr/local/bin/solo5-spt --mem=268 --net:service=tap0_urunc --net-mac:service=8a:30:82:72:fe:5e \
    /.boot/kernel --ipv4=172.16.1.2/24 --ipv4-gateway=172.16.1.1 -l "*:debug"
```

`BuildExecCmd` (`hypervisors/spt.go`) composes exactly `--mem`, optional `--net:<tap>`, zero or more
`--block:<id>=<path>`, and the guest command line. There is **no filesystem passthrough for spt**
(`SupportsSharedfs() == false`).

**[VERIFIED/CODE] S5-a — the guest cannot read or write the shared Argo emptyDir.** Its only host channels
are the tap fd and, when declared, a block fd — enforced additionally by the S2 seccomp allowlist. The
guest therefore **cannot** write `ctr/<name>/exitcode`, cannot plant files under `outputs/`, and cannot
create symlinks anywhere in the destination tree.

### Source-side guards (guest-controlled input)

`copyOutputs` (`pkg/containerd-shim/task_service.go`):

| Guard | Code | Status |
|---|---|---|
| symlinks skipped, never followed | `if d.Type()&fs.ModeSymlink != 0 { return nil }` (`WalkDir`/`ReadDir` = lstat semantics) | **[VERIFIED]** on disk: source 2 files + **2 symlinks** → dest 2 files + **0 symlinks**; both an absolute (`/etc/shadow`) and a relative (`../../../etc/passwd`) escape excluded (`LIVE_VALIDATION_2026-08-16.md` §N3) |
| non-regular files skipped | `if !d.Type().IsRegular() { return nil }` | **[CODE]** — FIFOs, sockets and device nodes on a guest-authored volume are not opened |
| `..` escape rejected | `rel == ".." \|\| strings.HasPrefix(rel, "../")` | **[CODE]** — defence in depth only; `filepath.Rel` over `WalkDir` descendants can never produce `..`, so this branch is unreachable in practice. The **real** traversal protection is that `WalkDir` yields only descendants and symlinks are skipped |
| total byte cap | `total+fi.Size() > maxExtractBytes` (64 MiB), checked **before** each copy | **[CODE]** bytes are bounded |

### Destination-side (F4)

```go
// copyFile
os.MkdirAll(filepath.Dir(dst), 0o755)
out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)   // no O_NOFOLLOW
```

**[CODE] S5-b** — if `dst` already exists as a **symlink**, `open()` follows it and truncates/writes the
target; `MkdirAll` likewise follows a symlinked intermediate component. The shim runs as **root in the host
mount namespace**, so a followed link resolves against the **host** filesystem.

**Exact capability required.** The guest cannot create that symlink (S5-a). It must be planted by a
**co-pod container that mounts `/var/run/argo` and can create entries in `<emptyDir>/outputs`** — i.e. the
Argo `init` or `wait` container, or another container the user adds. Observed directory modes in a real
Argo pod (`LIVE_VALIDATION_2026-08-16.md` §N3):

```
d drwxr-xr-x  ctr
d drwxr-xr-x  ctr/main
d drwxr-xr-x  outputs
d drwxr-xr-x  outputs/logs
f -rw-r--r--  ctr/main/exitcode
```

`outputs/` and `ctr/` are created by the shim with mode **0755**, so a **non-root** co-pod container cannot
plant entries there. Note this is *stricter than upstream Argo*: emissary deliberately creates
`ctr/<name>` mode **0777** (`cmd/argoexec/commands/emissary.go:58`) — that code never runs in a urunc main
container, so the 0777 exposure does not arise on this path. **Whether Argo's `init`/`wait` containers run
as root in this deployment is [UNVERIFIED]** (no `securityContext` is set in the controller configmap;
the argoexec image default was not inspected).

**[CODE] S5-c — `writeArgoExitcode` is symlink-safe on the final component**, unlike `copyFile`:
`os.CreateTemp` uses `O_CREATE|O_EXCL`, and `os.Rename` **replaces** a symlink rather than following it.
Its residual exposure is `os.MkdirAll(dir, 0o755)` following a symlinked `ctr/<name>` component.

**No exploit was attempted or demonstrated.** S5-b/S5-c are code-proven vectors conditional on a
compromised trusted co-pod container.

### Path traversal via container name

`exitDir = filepath.Join(argoSrc, "ctr", name)` where `name = spec.Annotations["io.kubernetes.cri.container-name"]`.
Server-side dry-run (nothing created):

```
$ kubectl apply --dry-run=server   # container name "../../../../evil"
The Pod "audit-badname" is invalid: spec.containers[0].name: Invalid value: "../../../../evil":
a lowercase RFC 1123 label must consist of lower case alphanumeric characters or '-' ...
```

**[VERIFIED] S5-d** — the Kubernetes API server rejects container names that could traverse; the value is
constrained to `[a-z0-9]([-a-z0-9]*[a-z0-9])?`.

---

## S6. Block storage

`getBlockVolumes` (`block.go:196`) / `restoreBlockVolumes` (`block.go:250`) /
`setLoopAutoclear` (`block.go:285`).

- **[CODE] S6-a — ownership is exclusive-by-unmount.** `getBlockVolumes` clears the loop `LO_FLAGS_AUTOCLEAR`
  flag and then **unmounts the host mountpoint**, handing the guest the raw device; `restoreBlockVolumes`
  remounts it at Delete. While the guest runs, the host has no mounted view — so there is no concurrent
  host/guest access window, and equally **no host-side sidecar can read the volume during the run**.
- **[CODE] S6-b — the guest gets raw block access**, not a filesystem view: it can read and write any
  sector of the backing image, including data outside the filesystem structure. Any pre-existing content of
  a reused backing file is readable by the guest.
- **[CODE] S6-c — leak on non-graceful teardown.** The autoclear flag is only restored in the Delete path.
  The source comment states it: *"if delete is never called then the autoclear flag will never get restored"*.
  This matches the previously recorded devmapper force-delete wedge (`GAP_ANALYSIS_AND_PLAN.md` §GAP 3,
  ~34 leaked dm devices, cleared only by reboot) — **[OBSERVED]**, baseline urunc/containerd behaviour.
- **[CODE] S6-d — nothing prevents two pods referencing the same host path.** There is no lock, refcount or
  ownership check in `getBlockVolumes`. Two concurrent guests pointed at the same source would both be
  given the same raw device. `PRE_IMPLEMENTATION_DESIGN_REVIEW.md` already records the channel as
  "exclusive-by-unmount … a concurrent host-sidecar mount is not supported and would corrupt". **Not
  tested** — a deliberate corruption experiment was out of scope.
- **[CODE] S6-e — eligibility is narrow.** A mount becomes a block volume only if it is a bind mount whose
  source is itself a mountpoint *and* `ukernel.SupportsFS(fsType)` is true, which in practice means ext2 +
  rumprun (`code_audit/AUDIT_VERDICTS.md`).

---

## S7. Completion / output integrity

- **[VERIFIED] S7-a — the guest cannot forge the completion token.** It has no access to the emptyDir
  (S5-a); the token is written by the shim from `resp.ExitStatus` after the inner Delete.
- **[CODE] S7-b — forging the token would not forge the result.** Argo's node exit code comes from the
  pod's terminated container status (`getExitCode`, `workflow/controller/operator.go:1528-1531`, used at
  `:1462`), **not** from the file; emissary only `os.Stat`s it (`emissary.go:126`). A forged/premature
  token therefore causes premature completion *signalling*, while the reported exit code remains the
  kubelet-observed container status.
- **[CODE] S7-c — cross-container writes.** Any co-pod container with the emptyDir mounted and sufficient
  privileges could write another container's `ctr/<name>/exitcode`. On the urunc path the shim creates
  those directories 0755 (S5-b), so a non-root sidecar cannot; **upstream Argo is weaker here**, creating
  `ctr/<name>` as 0777 by design. This is an Argo property, not one introduced by the POC.
- **[CODE] S7-d — the shim's gating limits what it will write.** `parseArgoTask` requires the
  `com.urunc.unikernel.unikernelType` annotation, so the shim writes a completion file only for the
  unikernel container, not for `init`/`wait`. Before that gate existed it wrote spurious `ctr/init` and
  `ctr/wait` files (journal, 2026-08-10) — fixed and re-confirmed 2026-08-16 (only `ctr/main` tracked).

---

## S8. Resource exhaustion

| Vector | Bound | Status |
|---|---|---|
| Extracted **bytes** | `maxExtractBytes` = 64 MiB, checked before each copy | **[CODE] bounded** |
| Extracted **file count** | **none** | **[CODE] UNBOUNDED by the cap** — a guest writing many zero-byte files never trips the byte cap; each triggers `MkdirAll` + `Open` + `OpenFile` + `io.Copy`. Bounded only by the inode capacity of the guest's block volume |
| **Directory** count / depth | **none** — directories are created with `MkdirAll` and are not counted against the byte cap | **[CODE] unbounded by the cap** |
| Extraction **time** | none; proportional to inode count, executed **synchronously inside the shim `Delete` hook** | **[CODE]** delays pod teardown |
| Destination medium | the emptyDir is tmpfs → **RAM-backed**; inodes and data consume node memory | **[CODE]** |
| Repeated `Delete` | the `argoTask` is claimed-and-removed under `s.mu`; a second Delete finds nothing | **[CODE] bounded** |
| Guest RAM | VMM `--mem`, honouring the pod memory limit when set (`unikontainers.go:440-442,508`) | **[CODE] bounded** |
| Guest CPU | no CPU handling in `pkg/unikontainers`; VMM outside the kubepods cgroup (S1-b) | **[OBSERVED] unbounded by pod limits** |

Observed extraction cost in the real run was trivial (2 files, ~1 ms). **The unbounded file-count path was
not executed** — doing so would have meant deliberately stressing the node.

---

## S9. Inherited vs POC-added security properties

| Property | Inherited from urunc / K8s / containerd | Added by this POC | Directly tested | Remaining risk |
|---|---|---|---|---|
| VM isolation | solo5-spt seccomp sandbox (`SCMP_ACT_KILL` default, fd-scoped allowlist); VMM = root + all caps, host userns | nothing | S2 [CODE]; S1 [OBSERVED] | Root/full-caps posture relies entirely on solo5's filter; unexamined for qemu/fc/ch |
| Pod networking | CNI-created netns; shared-netns pod model | selects **static** mode for Argo main, removing the dynamic catch-all tc steal | S3-a, §N1 A/B [VERIFIED] | Static mode is NAT + routing, **no filtering** |
| Egress | pod-level routing/identity | MASQUERADE to the pod IP for guest egress | S3-a/S3-c [VERIFIED] | `FORWARD` policy ACCEPT, no NetworkPolicy → guest egress presumed unrestricted [UNVERIFIED] |
| Sidecar interaction | shared pod netns (standard K8s) | guest becomes a routed L3 peer inside that netns | S4-a [VERIFIED] | Sidecars bound to `0.0.0.0` are reachable from the guest, from a non-pod-IP source |
| Artifact copying | — | `copyOutputs` symlink/non-regular/byte-cap guards, ordering before completion | S5 [VERIFIED on disk] | Destination side lacks `O_NOFOLLOW` (F4); file-count unbounded |
| Storage ownership | urunc block model (exclusive-by-unmount, loop autoclear) | shim reads the restored host mount at Delete | S6 [CODE] | Leak on non-graceful teardown; no guard against two pods sharing a source |
| Completion signalling | Argo: token existence only; exit code from container status | shim writes the token; `unikernelType` gate; F1 ExecID guard; F2 atomic publish | S7 [VERIFIED/CODE] | Token is not authenticated (by design in Argo) |
| Resource limits | pod memory limit → VMM `--mem` | 64 MiB extraction byte cap | S8 [CODE] | No file-count/time cap; VMM outside kubepods cgroup → no CPU limit |

---

## S10. Threat model summary

**Assets.** Node root filesystem; the shared Argo emptyDir (completion token + staged outputs); other pods
and cluster services; the Kubernetes API and the pod's service-account identity; block-volume contents;
node CPU/memory; workflow result integrity.

**Attacker capabilities considered.**
1. *Malicious unikernel image / compromised guest* — full control of guest code, of the contents and
   filenames of its block volume, and of packets on the tap. **Cannot** touch the emptyDir or any host path
   (S5-a) and is confined by solo5's seccomp filter (S2).
2. *Malicious workflow author* — controls annotations, mounts, container names, images.
3. *Compromised trusted co-pod container* (`init`/`wait`) — has the emptyDir mounted; required for the F4
   vector (S5-b).
4. Not considered: a compromised kubelet/containerd/shim (above the boundary), or physical/node access.

**Trust boundaries.** Trusted: Argo controller, kubelet, containerd, the urunc shim, the urunc CLI, the
solo5 tender's setup phase, `init`/`wait` sidecars. Untrusted: the unikernel guest, its block-volume
contents, and inbound network traffic. The boundary that actually holds is **the solo5-spt seccomp filter
plus the solo5 device model**, not Linux capabilities or user namespaces.

**Attack surfaces.** The tap fd and the block fd (the only guest→host channels); the shim's `Delete`-time
extraction reading guest-authored filenames; the shared emptyDir as a meeting point between the shim and
the sidecars; the unfiltered pod netns.

**Existing mitigations (inherited).** solo5-spt `SCMP_ACT_KILL` + fd-scoped allowlist; `PR_SET_NO_NEW_PRIVS`;
no filesystem passthrough on spt; mnt/net/pid/ipc/uts/cgroup namespaces; exclusive-by-unmount block
ownership; API-server container-name validation; guest RAM bounded by the pod memory limit.

**POC-added mitigations.** Static network mode (removes the dynamic catch-all steal that blackholes
sidecars); source-side symlink/non-regular/`..`/64 MiB guards in `copyOutputs`; extraction ordered strictly
before completion; the `unikernelType` gate preventing spurious completion files for sidecars; **F1**
(exec-delete cannot consume the main's completion state); **F2** (atomic token publish, and `rename`
happens to be symlink-safe on the final component).

**Verified protections.** Guest cannot reach the emptyDir (S5-a); source-side symlink exclusion proven on
disk incl. relative and absolute escapes (S5); completion token cannot be forged by the guest (S7-a) and
would not alter the reported exit code anyway (S7-b); container-name traversal blocked by the API server
(S5-d); only `ctr/main` is written (S7-d).

**Unverified assumptions.** That the guest's forwarded egress is genuinely unrestricted (inferred from
`FORWARD` policy ACCEPT, not generated from the guest); whether `argoexec init`/`wait` run as root (decides
whether F4 is reachable by a non-root sidecar); the seccomp/isolation posture of qemu/firecracker/
cloud-hypervisor; behaviour of two pods sharing one block source; the unbounded file-count extraction path.

**Remaining hardening work.** F4 destination-side `O_NOFOLLOW`/`openat2(RESOLVE_BENEATH)`; a file-count and
wall-clock bound on extraction; placing the VMM in the pod's kubepods cgroup so CPU limits apply; an
optional egress-filtering or NetworkPolicy story for the guest subnet; block-source exclusivity checking;
restoring loop autoclear on non-graceful teardown.

---

## S11. Tests deliberately not performed

| Test | Why not |
|---|---|
| Generating traffic from inside the guest | No client unikernel image available. Substituted with chain-policy inspection (authoritative) plus a same-subnet source proxy, with the difference stated. |
| Exploiting the F4 destination symlink | Would require planting a symlink in a live pod's emptyDir and writing to a host path as root — a destructive host-filesystem experiment. Reported as code-proven, not demonstrated. |
| Extraction file-count exhaustion | Would deliberately consume node RAM/inodes on the single shared VM. Reported from code. |
| Two pods sharing one block source | Expected outcome is data corruption. Out of scope. |
| Anything against the API server, credentials, kubelet or unrelated workloads | Explicitly out of scope; only read-only reachability probes were made, and the kubelet/API were merely connected to, never authenticated against or exercised. |
| Additional Argo Workflows | Cluster retention policy (3 Failed / 10 Succeeded) already consumed capacity in earlier validation; a bare pod exercised the same code path. |
