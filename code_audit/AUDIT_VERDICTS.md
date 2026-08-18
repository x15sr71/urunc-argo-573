# Architectural Audit — 3 Blockers: BUG vs MISSING FEATURE vs INTENDED/HARD LIMIT

Audited against the local urunc clone (`0.7.0`, HEAD `3a70c43`), VM install `0.7.0-c6bcc89`,
official docs (`docs/`), and GitHub issue history. Live evidence in `../vm_logs/vm_evidence_dump.txt`.

Paths below are relative to the `urunc/` source tree.

---

## BLOCKER 1 — tc-redirect catch-all steal blinds co-located sidecars (Blocker B)

**Verdict: INTENDED DESIGN (single-workload-per-netns assumption) + a KNOWN, ALREADY-FILED BUG (the leak).**

**What the code does** — `pkg/network/network.go`:
- `addRedirectFilter()` (~L206-223) installs a `u32` filter with `match 00000000/00000000` (matches
  everything) and a `mirred` action `TC_ACT_STOLEN` + `TCA_EGRESS_REDIR` to the tap.
- `networkSetup(..., addTCRules=true)` (~L249-266) adds it on **both** directions: eth0-ingress→tap and
  tap-ingress→eth0. Live proof: `tc filter show dev eth0 ingress` →
  `match 00000000/00000000 ... mirred (Egress Redirect to device tap0_urunc) stolen`
  (`vm_logs` §C — still present on `urunc-repro` 7 days after main exited).
- Dynamic mode is chosen for everything except a Knative `user-container`
  (`pkg/unikontainers/unikontainers.go:1424-1430`, `getNetworkType()`).

**Why it's INTENDED (not an accident):** `DynamicNetwork.NetworkSetup()`
(`pkg/network/network_dynamic.go:29-45`) carries an explicit design note:
> "network functionality for more than one unikernels is not yet implemented … only one tap device per
> netns can provide functional networking … See: issue #13"

The catch-all steal is a deliberate shortcut for the **one-workload-per-netns** model. Extending it is
tracked open work: **#906** "Enable Multi-Unikernel Network Namespace Coexistence", **#907** "Feat/multi
unikernel network", **#315** "Support multiple block and network devices". (#13 "multiple network
interfaces" is closed.) None of these frame the specific case we measured — a **Linux sidecar
(argoexec) blinded by the steal** — so that consequence is a design GAP not yet explicitly filed.

**The filter LEAK on failed/aborted setup is a KNOWN BUG — already filed as #874** (Anand-240,
2026-07-31): *"Tap device and TC rules are leaked when networkSetup()/DynamicNetwork.NetworkSetup()
fails partway through, permanently blocking future network setup in the same netns."* This is exactly
the persistence we observed. ⚠ Do NOT claim the leak as an original finding — cite #874.
Related: **#417** "SetupNet silently ignores network setup failures" (the swallowed error at
`unikontainers.go:274-283`), **#865** "distinguish missing container iface from setup failures".

**Proposal implication:** the sidecar-blindness *consequence* + causal proof (delete the filter → API
reachable) is the differentiated bit. The mechanism and the leak are known/filed.

---

## BLOCKER 2 — `SupportsSharedfs()=false` on spt/hvt/firecracker/hedge

**Verdict: INTENDED / HARD CAPABILITY LIMITATION (device-model), NOT an un-implemented stub.**

**The values** (`pkg/unikontainers/hypervisors/*.go`, confirmed live in `vm_logs` §E):

| Monitor | `SupportsSharedfs` | `UsesKVM` |
|---|---|---|
| spt (solo5) | **false** (`spt.go:50`) | false |
| hvt (solo5) | **false** (`hvt.go:138`) | **true** |
| firecracker | **false** (`firecracker.go:96`) | **true** |
| hedge | **false** (`hedge.go:50`) | **true** |
| qemu | true (`qemu.go:55`) | true |
| cloud-hypervisor | true (virtio) (`cloud_hypervisor.go:53`) | true |

**The clincher that it is NOT a KVM gate and NOT a lazy stub:** hvt, firecracker and hedge all
**use KVM yet still return false.** Shared-fs tracks the monitor's *device model*, not virtualization:
- **solo5 (spt/hvt):** the solo5 ABI exposes only **net + block** devices — no shared-fs channel exists.
  Live proof: the solo5-spt usage banner lists `Compiled-in modules: core net block` (no fs module).
  urunc cannot "implement" virtio-fs for solo5; the guest/monitor ABI has no such device.
- **firecracker:** minimal device model by design; no virtio-fs. (`docs/hypervisor-support.md` L81-86:
  Firecracker "aims to provide a smaller set of devices".)
- The gate is **two-sided** (`pkg/unikontainers/rootfs.go:234-259`): shared-fs is used only if BOTH
  `unikernel.SupportsFS(...)` AND `vmm.SupportsSharedfs(...)` are true.

No open issue proposes adding shared-fs to solo5/firecracker (searched: only test/doc issues #723/#815/
#732). **A proposal cannot "just write" shared-fs for spt** — it would require changing solo5 itself.
The correct output path for these monitors is **block device or network**, which is exactly what
cmainas means by "don't rely solely on shared-fs" (#573, to HARSHRAJ2789).

**Output corollary (proven):** the shared-fs-less storage primitive that DOES exist — a raw block
device — works: `block-test-spt-mirage` → 10/10 block read/write, `solo5_exit(0)` (`vm_logs` §B). Host
collection of guest writes needs `getBlockVolumes`+`restoreBlockVolumes` (`pkg/unikontainers/block.go:196,
250`), which requires an **ext2** volume and a guest with `SupportsFS("ext2")` — **rumprun only**
(`rumprun.go:131-135`; mirage `SupportsFS=false`, `mirage.go:54`).

---

## BLOCKER 3 — net-less / block-only guest aborts in a CNI pod (§3.1)

**Verdict: BUG / MISSING FEATURE (urunc-fixable), NOT a hardware limit.**

**Root cause** — urunc attaches a guest net device based on the **pod** having eth0, never on the
**guest** declaring a net device:
- `SetupNet()` (`pkg/unikontainers/unikontainers.go:265-296`) runs `NetworkSetup()` (creates tap + tc
  rules) and populates `netArgs.TapDev` whenever eth0 exists.
- `spt.go:70-72`: `if args.Net.TapDev != "" { cmdString += ukernel.MonitorNetCli(...) }` — the net flag
  is appended purely on **tap presence**, with **no check of the guest's declared devices**.
- `mirage.go:58-68` `MonitorNetCli` emits `--net:<devName>=<tap>`; devName defaults to `service`.
- Design comment `rootfs.go:400-405`: the tun device "is always included because in urunc create we can
  not know if there will be a virtual ethernet device or not … the decision … is decided in Exec, which
  checks the **network configuration**." → it reconciles with the **pod's** network, never the guest's.

**Consequence (live, `vm_logs` §D):** a block-only mirage guest (manifest declares `block`, not
`service`) in a CNI pod is force-fed `--net:service=tap0_urunc` →
`solo5-spt: Resource not declared in manifest: 'service'` → `Invalid option: --net:service=...` → abort.
Runs fine under `nerdctl --net none`.

**Why it's a fixable bug, not hardware:** the fix lives entirely in urunc — gate net attachment on the
guest actually having a net device. The hooks already exist:
- the Solo5 manifest (embedded in the binary) declares the guest's devices; urunc already *contemplates*
  reading it (`mirage.go:80`, `rumprun.go:159`: "Either we read the Solo5 manifest or …");
- an annotation precedent exists — **#690** added `com.urunc.unikernel.netDev` for the net device *name*
  (hardcoded "service" was already a known problem). A parallel "no-net" signal is the obvious fix.
Not found explicitly filed for the block-only/net-less case → a clean small issue to open.

---

## Cross-cutting proposal note
- Blocker 2 (spt/solo5 shared-fs) is a **hard boundary** → design the output path around **block +
  network**, not shared PVCs, for the lightweight monitors.
- Blockers 1 (leak part) and 3 are **urunc-fixable** and partly filed (#874; #690/#417 adjacent) → good
  scoped deliverables, but check existing issues before claiming novelty.
- The differentiated, unfiled contributions remain: the **sidecar-blindness causal proof** (Blocker 1
  consequence) and the **resource-template working path** (§2c) with its **block-primitive** backing.
