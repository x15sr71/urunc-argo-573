# Argo Workflows × urunc — cluster repro results for #784

> **CORRECTION (2026-08-16 factuality audit).** This file describes the `wait` sidecar as detecting
> completion via an **inotify** watch (lines ~9, ~247, ~361). That is **wrong**: emissary v3.6.5
> `isComplete` calls `os.Stat` on `ctr/<name>/exitcode` inside a **1 s poll loop** and never parses the
> content (`workflow/executor/emissary/emissary.go:112,126`). The *conclusion* — the file is never
> written, so `wait` never observes completion — is unaffected. See
> `COMPATIBILITY_AND_ROADMAP_CONCISE.md` §2.

**Headline:** The hang reproduced, cleanly and repeatably. A single-step Argo Workflow whose
`main` container is a urunc unikernel (`runtimeClassName: urunc`, Solo5-spt monitor) **stays
`Running` indefinitely after the unikernel process has completely exited**. The identical Argo
install runs a normal (runc) `hello-world` workflow to `Succeeded` in 10 s. The mechanism from
`ARGO_URUNC_ARCHITECTURE.md` §3 is **measured-confirmed**: urunc `syscall.Exec`s the main
container into the Solo5-spt VMM, so Argo's `argoexec emissary` wrapper never runs, so
`/var/run/argo/ctr/main/exitcode` is **never written**, so the `wait` sidecar's inotify wait
blocks forever and the pod never completes.

Side-by-side, same cluster, same Argo v3.6.5, same emissary executor:

```
NAME                STATUS      AGE   DURATION   MESSAGE
urunc-repro         Running     5m    5m                     <- unikernel main; hung
hello-world-jqlt7   Succeeded   6m    10s                    <- runc main; fine
```

At 5 minutes into the urunc workflow:

```
main: terminated (exit=1)      <- the Solo5-spt VMM ran and exited
wait: running  (exit=-)        <- argoexec wait never unblocked
# host: `ps` shows 0 solo5-spt processes — the VMM is gone
```

---

## 1. Environment (all real, captured on the host)

- Host: aarch64 Ubuntu 24.04, kernel 6.17, **no `/dev/kvm`** (cloud VM, no nested virt).
- Monitor: **Solo5-spt v0.9.0**, built from source, installed as `/usr/local/bin/solo5-spt`.
  Chosen because `SPT.UsesKVM() == false`; every KVM-based monitor (qemu, firecracker,
  cloud-hypervisor, solo5-hvt) is unusable here.
- urunc `v0.7.0-96f992d` built from source → `/usr/local/bin/{urunc,containerd-shim-urunc-v2}`.
- containerd **v2.2.6** (the system/Docker containerd), CRI enabled, devmapper thin-pool
  (`containerd-pool`, 100 GB), `io.containerd.urunc.v2` runtime wired in.
- **k3s v1.36.2** pointed at the *external* system containerd
  (`--container-runtime-endpoint unix:///run/containerd/containerd.sock`), so k8s inherits the
  urunc + devmapper runtime. `CONTAINER-RUNTIME` on the node reads `containerd://2.2.6`.
- Flannel disabled; a plain **bridge CNI** (`/etc/cni/net.d/10-bridge.conflist`, 10.42.0.0/24)
  gives pods an `eth0`.
- Argo Workflows **v3.6.5** (`quick-start-minimal`) in namespace `argo`; default executor is
  **emissary**.

---

## 2. Gate results

### GATE 1 — urunc + Solo5-spt under nerdctl — **PASS**

```
$ sudo nerdctl run --rm --net none --runtime io.containerd.urunc.v2 --snapshotter devmapper \
    harbor.nbfc.io/nubificus/urunc/hello-spt-mirage:latest --hello=Holahola
Solo5: Bindings version v0.9.1
Solo5: Memory map: 268 MB addressable:
2026-...: [INFO] [application] Holahola
2026-...: [INFO] [application] Holahola
2026-...: [INFO] [application] Holahola
2026-...: [INFO] [application] Holahola
Solo5: solo5_exit(0) called
=== EXIT 0 ===
```

`--net none` is required: this hello unikernel's Solo5 manifest declares **no** network device,
but urunc unconditionally attaches the container's tap and passes `--net:service=…`. With a tap
present the unikernel aborts: `solo5-spt: Resource not declared in manifest: 'service'`. This is a
real urunc-under-networking limitation and it becomes important in k8s (see §5, additional
findings), where a pod always has an `eth0`.

### GATE 2 — single-node k8s wired to this containerd — **PASS (with the net caveat)**

k3s node `Ready`, running the external containerd. A bare Pod with `runtimeClassName: urunc` and
`net-spt-mirage` (a unikernel that *does* declare a `service` net device) boots end to end:

```
NAME        READY   STATUS    IP           NODE
net-urunc   1/1     Running   10.42.0.58   vidhya
# logs: mirage TCP/IP stack up on the pod IP:
[INFO] [tcp.pcb] TCP layer connected on 10.42.0.58/24 ...
```

`crictl inspect` of that container proves the handler and that CRI hands urunc the **unikernel**
command (from image labels, no argoexec — this is a *bare* pod, not an Argo pod):

```
runtimeType: io.containerd.urunc.v2
image:       harbor.nbfc.io/nubificus/urunc/net-spt-mirage:latest
OCI process.args: ['-l', '"*:debug"']
# pod sandbox (pause) also runs under io.containerd.urunc.v2 and is delegated to runc internally
```

Two GATE-2 caveats, both findings in their own right:
- **`hello-spt-mirage` cannot Succeed in a CNI pod**: urunc attaches the pod tap → the net-less
  hello unikernel aborts with `Resource not declared in manifest: 'service'` and exits 1
  (`runtimeType: io.containerd.urunc.v2, state: CONTAINER_EXITED, exit: 1`).
- **`net-spt-mirage` is a server** (never exits); k8s later SIGKILLs it (exit 137) via an early
  `Killing` event. Neither image is both networked *and* short-lived, so the repro (§4) uses
  `hello-spt-mirage`, whose fast VMM exit is exactly what makes the "VMM gone, workflow still
  Running" contrast sharp.

---

## 3. Argo baseline — **PASS**

The stock `hello-world` (normal busybox/runc container) reaches **Succeeded in 10 s**. This proves
Argo + emissary work in this cluster and that the emissary exit-code contract *does* fire for a
normal process:

```
# main container of the baseline pod:
main -> terminated {exitCode: 0, reason: Completed}
# main.log:  "hello world" ... "sub-process exited" argo=true error="<nil>"
# => argoexec emissary wrapped the process, ran it, observed its exit. wait then finalized.
```

(One setup fix was needed and is unrelated to urunc: `quick-start-minimal` enables `archiveLogs`
to a minio S3 endpoint. Cluster-DNS/ClusterIP service routing is imperfect under the plain bridge
CNI, so the log upload blocked. Setting `archiveLogs: false` in the `artifact-repositories`
ConfigMap removed the minio dependency; the baseline then succeeded. The urunc repro hang is a
*different* mechanism and does not involve minio.)

---

## 4. The #784 repro and the 7 measurements

Workflow submitted (single `main` step, `podSpecPatch` sets `runtimeClassName: urunc`, image
`hello-spt-mirage`):

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata: { name: urunc-repro, namespace: argo }
spec:
  entrypoint: main
  podSpecPatch: |
    runtimeClassName: urunc
  templates:
  - name: main
    container:
      image: harbor.nbfc.io/nubificus/urunc/hello-spt-mirage:latest
      command: ["/hello"]
      args: ["--hello=ArgoUrunc"]
```

Result: workflow node `urunc-repro/main` **stays `Running` for 5+ minutes** while `main` is
`terminated` and no `solo5-spt` process remains. The 7 measurements:

### 4.1 — Which container runs under what
All three containers report CRI `runtimeType: io.containerd.urunc.v2` (RuntimeClass is pod-wide),
and urunc then delegates internally:

```
init -> runtimeType io.containerd.urunc.v2, EXITED exit 0   OCI args: ['argoexec','init', ...]
wait -> runtimeType io.containerd.urunc.v2, RUNNING         OCI args: ['argoexec','wait', ...]
main -> runtimeType io.containerd.urunc.v2, EXITED exit 1   OCI args: ['/var/run/argo/argoexec','emissary', ...]
```

`init`/`wait` are ordinary `argoexec` processes (they executed, logged, `init` exited 0) → urunc's
`ErrNotUnikernel` path delegated them to runc (confirms `ARGO_URUNC_ARCHITECTURE.md` §1). `main`
carries the unikernel image labels → urunc ran the VMM (proven in 4.2).

### 4.2 — The exact main command, and which §3 branch fires  ← **the pivotal measurement**
CRI hands urunc the **argoexec emissary wrapper** as `process.args`:

```
main OCI process.args:
  ['/var/run/argo/argoexec', 'emissary', '--loglevel', 'info', '--log-format', 'text',
   '--gloglevel', '0', '--', '/hello', '--hello=ArgoUrunc']
```

…but the `main` container's **logs show Solo5-spt ran, not argoexec**:

```
solo5-spt: Resource not declared in manifest: 'service'
solo5-spt: Invalid option: `--net:service=tap0_urunc'
usage: solo5-spt [ CORE OPTIONS ] [ -- ] KERNEL [ ARGS ]  ...
```

**Branch A of §3's "command wrapping" row fires.** urunc reads the `com.urunc.unikernel.*` image
labels (`binary=/.boot/kernel`, `hypervisor=spt`, `unikernelType=mirage`), `syscall.Exec`s
`solo5-spt`, and **discards the `argoexec emissary -- …` command entirely**. argoexec never runs in
the main container. (The `exit 1` here is the incidental net-attach abort; even had the unikernel
exited 0, argoexec still would not have run, so nothing downstream changes.)

### 4.3 — Does `/var/run/argo/ctr/main/exitcode` ever appear?  **NO** ← **root cause**
The shared `/var/run/argo` emptyDir on the host contains only the staged binary and template — no
`ctr/` dir, no exitcode file, ever:

```
$ ls -la .../kubernetes.io~empty-dir/var-run-argo/
-r-xr-xr-x 1 root root 120077344  argoexec     # staged by init
-r--r--r-- 1 root root       208  template
# no  ctr/  directory;  find ... -name exitcode  ->  (nothing)
```

Because `argoexec emissary` (which is what creates `ctr/main/exitcode`) never executed in the main
container (4.2), the exit-code file is never written. This is the mechanism the hypothesis named.

### 4.4 — Where the wait container is stuck
`argoexec wait` starts, initialises, launches its deadline monitor, and then never reports a
container completion:

```
"Starting Workflow Executor" version=v3.6.5
"Executor initialized" ... podName=urunc-repro templateName=main
"Starting deadline monitor"
# ...then only, once a minute, progress-report failures (see 4.6). No "main completed" line, ever.
```

It is blocked in the emissary wait loop (`file.WaitForCreate` on the exitcode path from 4.3). Since
wait never returns, the pod never completes (1/2 `NotReady`), so the workflow node never leaves
`Running`. This is network-independent: it holds even though the controller has full API access.

### 4.5 — Guest vs host filesystem for outputs
Not exercisable with this image: `hello-spt-mirage` writes no output artifacts (and aborts at net
setup). Architecturally the guest rootfs is a VM boundary (`unikontainers.go` `ChooseRootfs`), so a
guest that *did* write outputs would write inside the VM, not the pod emptyDir — but that specific
claim is **inconclusive from this repro** for lack of an output-producing spt image. The
exit-code-file half of the same emptyDir contract is confirmed (4.3).

### 4.6 — Pod-local networking (sidecar ↔ main)
Best-effort. Two concrete observations under urunc that do not occur for the runc baseline:
- The `wait` sidecar loses API-server connectivity: `dial tcp 10.43.0.1:443: connect: no route to
  host` (repeated). The baseline runc workflow's wait reached the API fine (it Succeeded). This is
  consistent with urunc reconfiguring the pod netns `eth0` into the guest tap (§3 shared-netns
  row), though a plain-bridge cluster's imperfect service routing is a partial confound, so this is
  **observed, mechanism strongly-supported rather than exec-verified**.
- `kubectl exec` into the sidecars fails: `OCI runtime exec failed: urunc did not terminate
  successfully: exit status 1` — urunc's exec path does not support these delegated containers,
  which also blocked direct in-netns inspection.

### 4.7 — Timing
```
urunc-repro   Running   5m   ...      main: terminated(exit 1)   wait: running   solo5-spt procs: 0
```
The workflow node remains `Running` indefinitely after the VMM PID is gone. This indefinite
`Running` **is** the #784 fingerprint, and it ties directly to 4.3.

---

## 5. Hypothesis ledger (§3 of ARGO_URUNC_ARCHITECTURE.md)

| # | §3 broken assumption | Verdict | Deciding evidence |
|---|---|---|---|
| 1 | **Process / exit visibility** — argoexec forks main and writes `ctr/main/exitcode`; wait inotify-waits it | **MEASURED-CONFIRMED** | 4.2 (main ran solo5-spt, not argoexec) + 4.3 (exitcode file never created) + 4.4 (wait never detects completion) + 4.7 (node `Running` forever, VMM gone). This is the primary hang cause and needs nothing else. |
| 2 | **Command wrapping** — main is `argoexec emissary -- <cmd>`; either urunc runs the unikernel (emissary bypassed) or delegates argoexec to runc (unikernel never runs) | **MEASURED-CONFIRMED — Branch A** | 4.2: OCI `process.args` *are* the `argoexec emissary --` wrapper, yet main's logs are Solo5-spt. urunc selects the unikernel from image labels and ignores the emissary command. The wrapper is bypassed; emissary's contract is silently void. |
| 3 | **Shared `/var/run/argo` emptyDir** — exit-code + artifacts shared main↔wait | **MEASURED-CONFIRMED for the exit-code file; INCONCLUSIVE for guest-artifact staging** | 4.3: the guest/VMM wrote nothing into the shared emptyDir (`ctr/` never created). The stronger "guest writes land in VM rootfs, not emptyDir" needs an output-producing spt image (4.5) and was not exercised. |
| 4 | **Shared network namespace** — sidecars reach main over pod localhost | **OBSERVED / strongly-supported** | 4.6: under urunc the `wait` sidecar cannot reach the API server ("no route to host") while the runc baseline can; consistent with urunc moving the pod `eth0` into the guest tap. Not the primary hang cause (4.4 is network-independent), and not exec-verified (urunc exec into sidecars fails). |

**Additional findings surfaced by the repro (not in the original §3 list):**
- **Net-less unikernels can't run in a CNI pod.** urunc unconditionally attaches the pod tap and
  passes `--net:service=…`; a unikernel whose manifest declares no net device aborts at Solo5
  startup. This blocks `hello-spt-mirage` (and any net-less image) in k8s independent of Argo.
- **urunc `exec` into delegated sidecars fails** (`urunc did not terminate successfully: exit
  status 1`), breaking `kubectl exec` / debugging of the argoexec containers.
- **Pod-wide RuntimeClass runs the sandbox and sidecars through urunc.** They work only because
  urunc's `ErrNotUnikernel` path delegates them to runc; the sandbox `pause` is delegated the same
  way.

---

## 6. Appendix — reproduce from scratch (proposal-grade)

Assumes aarch64 Ubuntu 24.04, no `/dev/kvm`, Go at `/usr/local/go`, and a system containerd v2.x
active on `/run/containerd/containerd.sock`.

**1. Build & install Solo5-spt v0.9.0** (drop `-Werror` so the newer GCC builds the hvt gdb module;
only spt is needed):
```bash
sudo apt-get install -y build-essential pkg-config libseccomp-dev
git clone -b v0.9.0 --depth 1 https://github.com/Solo5/solo5 && cd solo5
./configure.sh && sed -i 's/-Werror//g' Makefile.common && make
sudo install -m0755 tenders/spt/solo5-spt /usr/local/bin/solo5-spt   # must be named solo5-spt on PATH
```

**2. Build & install urunc:**
```bash
git clone --depth 1 https://github.com/urunc-dev/urunc && cd urunc
make && sudo make install   # -> /usr/local/bin/{urunc,containerd-shim-urunc-v2}
```

**3. devmapper thin-pool + nerdctl + CNI:**
```bash
sudo bash urunc/script/dm_create.sh                                   # pool "containerd-pool", 100G
# nerdctl v2.1.2 -> /usr/local/bin ; CNI plugins v1.5.1 -> /opt/cni/bin
```

**4. containerd config** (`/etc/containerd/config.toml`, backup first; containerd 2.x, `version=3`):
enable CRI; keep the global CRI snapshotter **overlayfs** (a global `devmapper` breaks the `pause`
unpack: "no unpack platforms defined"); add the devmapper snapshotter plugin and a `urunc` runtime
that alone uses devmapper:
```toml
version = 3
[plugins.'io.containerd.snapshotter.v1.devmapper']
  pool_name = "containerd-pool"
  root_path = "/var/lib/containerd/io.containerd.snapshotter.v1.devmapper"
  base_image_size = "10GB"
  discard_blocks = true
[plugins.'io.containerd.cri.v1.images']
  snapshotter = "overlayfs"
[plugins.'io.containerd.cri.v1.runtime'.cni]
  bin_dir = "/opt/cni/bin"
  conf_dir = "/etc/cni/net.d"
[plugins.'io.containerd.cri.v1.runtime'.containerd]
  default_runtime_name = "runc"
  snapshotter = "overlayfs"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc]
  runtime_type = "io.containerd.runc.v2"
  [plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runc.options]
    SystemdCgroup = true
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.urunc]
  runtime_type = "io.containerd.urunc.v2"
  snapshotter = "devmapper"
  container_annotations = ["com.urunc.unikernel.*"]
  pod_annotations = ["com.urunc.unikernel.*"]
```
`sudo systemctl restart containerd`

GATE 1: `sudo nerdctl run --rm --net none --runtime io.containerd.urunc.v2 --snapshotter devmapper \
harbor.nbfc.io/nubificus/urunc/hello-spt-mirage:latest --hello=Holahola` → prints Holahola, exit 0.

**5. k3s on the external containerd + bridge CNI:**
```bash
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="\
  --container-runtime-endpoint unix:///run/containerd/containerd.sock \
  --flannel-backend=none --disable-network-policy \
  --disable=traefik --disable=servicelb --disable=metrics-server --disable=local-storage \
  --write-kubeconfig-mode 644" sh -
# write /etc/cni/net.d/10-bridge.conflist: bridge cni0 + host-local 10.42.0.0/24 + portmap
kubectl apply -f - <<< 'apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: urunc}
handler: urunc'
```
GATE 2: a bare pod with `runtimeClassName: urunc` + image `net-spt-mirage` reaches `Running` with
the unikernel on the pod IP.

**6. Argo Workflows + repro:**
```bash
kubectl create ns argo
kubectl apply -n argo -f https://github.com/argoproj/argo-workflows/releases/download/v3.6.5/quick-start-minimal.yaml
# remove the minio dependency so the runc baseline can complete:
kubectl patch cm -n argo artifact-repositories --type merge -p '{"data":{"default-v1":"archiveLogs: false\n"}}'
kubectl rollout restart deploy/workflow-controller -n argo
# baseline (Succeeds): argo submit -n argo --wait .../examples/hello-world.yaml
# repro (Hangs): the urunc-repro Workflow from §4 -> node stays Running after the VMM exits
```

**What you will see:** `urunc-repro` `Running` forever with `main` terminated and `wait` running;
`/var/run/argo/ctr/main/exitcode` never created; `main` logs show `solo5-spt`, not `argoexec`.

---

## 7. One-paragraph synthesis for the proposal

urunc's `create` correctly delegates Argo's non-annotated `init`/`wait` sidecars to runc, so the
incompatibility is entirely about the `main` container. Argo's emissary contract is "a host process
(`argoexec emissary`) forks the user command, waits, and writes `/var/run/argo/ctr/main/exitcode`,
which `wait` inotify-watches." urunc deliberately `syscall.Exec`s `main` into the Solo5 VMM using
the unikernel image labels, so on this cluster the `argoexec emissary --` command that CRI actually
hands urunc is **discarded**, argoexec never runs, the exit-code file is **never written**, and the
`wait` sidecar blocks forever — the workflow node stays `Running` minutes after the VMM has exited
and disappeared. That single root cause (main is a VM, not a pod process) is measured-confirmed here
end to end, and it is the concrete mechanism behind #784's "workflow hangs" report.
