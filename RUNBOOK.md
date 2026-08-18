# RUNBOOK_VERIFIED — teardown + from-scratch rebuild of urunc + k3s + Argo (#784)

**Host:** aarch64 Ubuntu 24.04, kernel 6.17-oracle, no `/dev/kvm`, 2 vCPU, ~107 G free, `ssh vidhya`.
**Method:** tore the box down to "fresh Ubuntu + docker/containerd + go", then rebuilt by following
`ARGO_REPRO_RESULTS.md` §6 step by step, capturing real output and every divergence.

**FINAL VERDICT: YES.** The from-scratch runbook reproduces the #784 hang end to end on the clean box.
The runbook is *substantially correct* but needs the corrections below (two of them are blockers on a
box that was previously set up; one is a blocker for the runc baseline). The corrected, copy-pasteable
runbook is in the last section.

---

## Versions actually built/installed (this run)

| Component | Runbook §6 | This rebuild | Note |
|---|---|---|---|
| Go | `/usr/local/go` (unpinned) | go1.26.4 | pre-installed, left untouched |
| Solo5-spt | v0.9.0 | v0.9.0 (Bindings v0.9.1) | exact |
| urunc | `0.7.0-96f992d` | `0.7.0-c6bcc89` | **D1: HEAD moved** (same 0.7.0 base) |
| containerd | v2.x | v2.2.6 (system) | exact |
| nerdctl | v2.1.2 | v2.1.2 | exact |
| CNI plugins | v1.5.1 | v1.5.1 | exact |
| k3s | v1.36.2 | v1.36.2+k3s1 | exact |
| Argo | v3.6.5 | v3.6.5 | exact |

---

## PHASE A — Teardown to clean slate

Original containerd config backup was found at `/etc/containerd/config.toml.bak-preurunc` (886 bytes,
the Docker default with `disabled_plugins=["cri"]`) — **restored it** (did not need to regenerate).

Teardown was not a clean one-shot: the devmapper pool + snap devices were pinned by **orphaned k3s pod
processes** (`/pause`, `argoexec`, and a `containerd-shim-urunc-v2`) still running under the *external*
containerd — `k3s-killall.sh` does not reap them because they are not k3s's own containerd tasks. Had to
unmount the orphaned `k8s.io` task rootfs mounts and `kill -9` the `k8s.io`-namespace shims before the
`dmsetup remove` / `losetup -d` would succeed. (Docker/`moby`-namespace shims were left alone.)

### Proof-of-clean (captured after `systemctl restart containerd`)

```
--- binaries (should be empty) ---
(all empty)                       # solo5-spt urunc containerd-shim-urunc-v2 k3s nerdctl crictl kubectl
--- containerd present (kept) ---
/usr/bin/containerd  ->  containerd v2.2.6
--- dmsetup ls ---                No devices found
--- loop devices (containerd) --- (none)
--- containerd config size ---    886 /etc/containerd/config.toml     # == original
disabled_plugins = ["cri"]
--- devmapper snapshotter dir --- (no devmapper snapshotter dir)
--- /opt/cni/bin ---             (gone)
--- Go still present ---          go version go1.26.4 linux/arm64
```

`~/urunc-fix` and `~/urunc-fuzz` (co-tenant) were never touched; `/usr/local/go` kept; no `go clean`.

> **DIVERGENCE D3 (teardown gap — MAJOR, see GATE 2).** Removing the devmapper pool + snapshotter
> dir + binaries is **not enough** to make the box behave like a fresh OS. containerd's metadata bolt DB
> (`/var/lib/containerd/io.containerd.metadata.v1.bolt`) and the per-namespace image/snapshot records
> **survive** teardown. Those stale records (from the pre-teardown setup) become *dangling* once the pool
> is wiped and later poison every urunc pod/sandbox creation with
> `unable to prepare extraction snapshot: target snapshot "…": already exists` +
> `failed to read snapshotInfo for k8s.io/<id>/…`. The corrected teardown must also purge the
> `k8s.io` (and `default`) namespace snapshots. On a genuinely fresh OS install this never happens.

---

## PHASE B — Rebuild from the runbook

### Step 1 — Solo5-spt v0.9.0 — **PASS**
`git clone -b v0.9.0`, `./configure.sh`, `sed -i 's/-Werror//g' Makefile.common`, `make`,
`install … /usr/local/bin/solo5-spt`. The `-Werror` strip is **required and correct** — build clean.
`configure.sh` reported `Enabled tenders: hvt spt`. No divergence.

### Step 2 — urunc `make && sudo make install` — **PASS**
Cloned fresh into `~/urunc-src` (per instructions; not `~/urunc-fix`). Built
`urunc version 0.7.0-c6bcc89`, installed `/usr/local/bin/{urunc,containerd-shim-urunc-v2}`.
> **D1 (minor):** `git clone --depth 1 https://github.com/urunc-dev/urunc` gives **HEAD of main**, so the
> commit differs from the runbook's `96f992d`. Pin a tag/commit if exact reproducibility is wanted.

### Step 3 — devmapper pool + nerdctl + CNI — **PASS**
`sudo bash ~/urunc-src/script/dm_create.sh` → pool `containerd-pool` (100 G data + 10 G meta, loops 4/5).
nerdctl v2.1.2 → `/usr/local/bin`; CNI plugins v1.5.1 → `/opt/cni/bin`. (Runbook names the versions but
not the download commands — supplied in corrected runbook below.) No divergence.

### Step 4 — containerd config — **PASS (with warnings)**
Applied §6's TOML verbatim (version=3, global CRI snapshotter overlayfs, urunc-only devmapper, CRI/CNI
stanzas). After restart: devmapper snapshotter `ok`, CRI runtime `ok`, no plugin errors.
> **D2 (minor):** containerd v2.2.6 warns `bin_dir … is deprecated since v2.1 … use bin_dirs`. Works, but
> the corrected config uses `bin_dirs = ["/opt/cni/bin"]`.
> **D5 (minor):** §6 omits `[plugins.'io.containerd.cri.v1.images'.pinned_images] sandbox=".../pause:3.10"`
> that the original working config carried. Not required here — k3s supplies its own sandbox image.

### GATE 1 — nerdctl + Solo5-spt — **PASS**
```
$ sudo nerdctl run --rm --net none --runtime io.containerd.urunc.v2 --snapshotter devmapper \
    harbor.nbfc.io/nubificus/urunc/hello-spt-mirage:latest --hello=Holahola
Solo5: Bindings version v0.9.1
Solo5: Memory map: 268 MB addressable:
2026-08-03…: [INFO] [application] Holahola   (x4)
Solo5: solo5_exit(0) called
=== exit code: 0 ===
```
Benign warnings each run: `cannot set cgroup manager to "systemd" for runtime io.containerd.urunc.v2`
(**D6**) and `Failed to load … /etc/urunc/config.toml … Using default configuration` (**D7**).
> On this (previously-used) box GATE 1 first failed once with `target snapshot … already exists` from a
> leftover pre-teardown image record in the `default` namespace; `nerdctl … rmi` of the stale image then
> re-pull fixed it. Same D3 root cause — moot on a fresh OS.

### Step 5 — k3s on external containerd + bridge CNI + RuntimeClass — **PASS**
`get.k3s.io` installed **v1.36.2+k3s1**; node `Ready`, `CONTAINER-RUNTIME = containerd://2.2.6` (external
containerd confirmed). Wrote `/etc/cni/net.d/10-bridge.conflist` (bridge `cni0` + host-local 10.42.0.0/24
+ portmap). `RuntimeClass urunc` (handler `urunc`) created. No divergence.

### GATE 2 — bare Pod `runtimeClassName: urunc` + `net-spt-mirage` — **PASS (after D3 cleanup)**
```
NAME        READY   STATUS    IP           NODE
net-urunc   1/1     Running   10.42.0.3    vidhya
# logs: Solo5 Bindings v0.9.1; mirage netif plugged, gratuitous ARP for 10.42.0.3, IP6 up
# crictl inspect: "runtimeType": "io.containerd.urunc.v2"
```
> **DIVERGENCE D3 in full (MAJOR — the hardest part of the whole rebuild).**
> With the runbook's config (global **overlayfs** for CRI images + **devmapper** only for the urunc
> runtime), a urunc pod exercises **both** snapshotters: CRI's `PullImage` unpacks the image into
> overlayfs, then the urunc container/sandbox re-unpacks the *same chainID* into devmapper. On **healthy**
> containerd metadata this second unpack is a graceful diff-reuse and succeeds (as proven — GATE 2 passed).
> But the pre-teardown setup had left **dangling snapshot records** in containerd's bolt DB for the
> `k8s.io` namespace (e.g. pause `78750a…`, net layer `7f98e1ac…`), whose overlayfs backing dirs were gone
> (`failed to read snapshotInfo for k8s.io/<id>/…`). Every unpack that touched those chainIDs then failed
> hard with `target snapshot … already exists`, blocking **sandbox** creation (`FailedCreatePodSandBox`)
> and **container** creation (`CreateContainerError`) for *every* urunc pod. `ctr snapshot rm` alone does
> not fix it (the dangling record is namespace/backend-split). **The fix that finally worked:** stop k3s,
> purge all `k8s.io` snapshots + containers + images, reset the devmapper pool fresh, restart. After that
> the *pure runbook path* (create pod, no manual pre-load) reached `Running` on the first try. **Action:**
> fold this purge into teardown (see corrected teardown). Net-less-unikernel and net-server caveats from
> §2 still hold; `net-spt-mirage` is used here precisely because it declares a `service` net device.

### Argo v3.6.5 install — **PASS**, baseline `hello-world` — **PASS (after D4)**
`quick-start-minimal.yaml` applied; `archiveLogs:false` patch applied; controller restarted. All pods up.
First baseline **errored**:
```
Error (exit code 64): workflowtaskresults.argoproj.io is forbidden:
User "system:serviceaccount:argo:argo" cannot create resource "workflowtaskresults" … in namespace "argo"
```
> **DIVERGENCE D4 (MAJOR — missing from §6).** `quick-start-minimal` binds the `executor` role (which
> grants `workflowtaskresults`) via rolebinding `executor-default` to the **`default`** SA only — **not**
> to the `argo` SA. A Workflow that runs as `serviceAccountName: argo` (or is submitted with the argo CLI
> default) has the emissary sidecar rejected and the workflow **Errors**. **Fix:** either run workflows as
> `serviceAccountName: default`, or bind the `executor` role to the `argo` SA. After binding it, the
> baseline reached **Succeeded in ~15 s**. (Without D4 the runc baseline cannot pass; the urunc repro would
> hang regardless, but you could not show the runc side-by-side control.)

### REPRO — `urunc-repro` Workflow (§4) — **HANG REPRODUCED**
Submitted the §4 workflow (`podSpecPatch: runtimeClassName: urunc`, image `hello-spt-mirage`,
`command:[/hello] args:[--hello=ArgoUrunc]`, `serviceAccountName: argo`).

```
NAME          STATUS    AGE      MESSAGE
urunc-repro   Running   3m28s              <- node never leaves Running

# container states:
main:  terminated  exitCode 1  reason Error  (started == finished 12:57:38)
wait:  running     (started 12:57:33)

# main container logs — Solo5-spt ran, NOT argoexec:
solo5-spt: Resource not declared in manifest: 'service'
solo5-spt: Invalid option: `--net:service=tap0_urunc'
usage: solo5-spt [ CORE OPTIONS ] [ -- ] KERNEL [ ARGS ] ...

# wait sidecar — initialised then never observes completion:
"Starting Workflow Executor" version=v3.6.5
"Executor initialized" … podName=urunc-repro templateName=main
"Starting deadline monitor"
# (no "main completed" line, ever)

# host: solo5-spt processes = 0        (VMM gone)
# host: find …var-run-argo… -name exitcode  -> 0 files ; ctr/ dirs -> 0
```

This is the #784 fingerprint, identical to `ARGO_REPRO_RESULTS.md` §2/§4: **Branch A** fires (urunc
`syscall.Exec`s Solo5-spt from the image labels and discards the `argoexec emissary --` wrapper CRI handed
it), `/var/run/argo/ctr/main/exitcode` is **never written**, the emissary `wait` sidecar blocks forever,
and the workflow node stays `Running` minutes after the VMM has exited and disappeared.

---

## Divergence ledger (corrections to fold into the runbook)

| # | Sev | Where | Divergence | Correction |
|---|-----|-------|-----------|-----------|
| D1 | minor | Step 2 | `--depth 1` clone gives HEAD (`c6bcc89`), not `96f992d` | pin a tag/commit for exact repro |
| D2 | minor | Step 4 | `bin_dir` deprecated on containerd 2.1+ (warns) | use `bin_dirs = ["/opt/cni/bin"]` |
| **D3** | **MAJOR** | teardown / GATE 2 | wiping the pool leaves **dangling containerd snapshot records** → all urunc pods fail `target snapshot already exists` | teardown must also purge `k8s.io`(+`default`) snapshots/images and reset the pool; fresh OS unaffected |
| **D4** | **MAJOR** | Argo baseline | `executor` role (workflowtaskresults) bound only to `default` SA, not `argo` → runc baseline Errors | run workflows as `default` SA **or** bind `executor` to the `argo` SA |
| D5 | minor | Step 4 | §6 omits `pinned_images.sandbox` | not required with k3s; add if using pure CRI |
| D6 | benign | GATE 1/2 | `cannot set cgroup manager to systemd for io.containerd.urunc.v2` | ignore (or drop `SystemdCgroup` for urunc) |
| D7 | benign | GATE 1/2 | urunc warns missing `/etc/urunc/config.toml` | ignore (defaults used) |

---

## VERIFIED runbook (copy-paste, corrections folded in)

Assumes aarch64 Ubuntu 24.04, no `/dev/kvm`, Go at `/usr/local/go`, system containerd v2.x on
`/run/containerd/containerd.sock`.

### 0. (Only if the box was set up before — teardown → clean slate) — corrects **D3**
```bash
# argo + k3s
sudo k3s kubectl delete ns argo --wait=false 2>/dev/null || true
sudo /usr/local/bin/k3s-killall.sh 2>/dev/null || true
sudo /usr/local/bin/k3s-uninstall.sh 2>/dev/null || true
# reap ORPHANED external-containerd k8s pods that pin the pool (killall misses these):
for m in $(mount | grep '/run/containerd/io.containerd.runtime.v2.task/k8s.io' | awk '{print $3}'); do sudo umount -l "$m"; done
for p in $(ps -eo pid,cmd | grep -E 'containerd-shim.*namespace k8s.io' | grep -v grep | awk '{print $1}'); do sudo kill -9 "$p"; done
# restore original containerd config (886-byte Docker default), if backed up:
sudo cp /etc/containerd/config.toml.bak-preurunc /etc/containerd/config.toml 2>/dev/null || true
# PURGE containerd's stale image/snapshot metadata (the D3 fix) — leave the moby namespace alone:
sudo systemctl stop containerd
for sn in overlayfs devmapper native; do
  for ns in k8s.io default; do
    sudo ctr -n "$ns" containers ls -q 2>/dev/null | xargs -r -n1 sudo ctr -n "$ns" containers rm 2>/dev/null
    sudo ctr -n "$ns" images ls -q 2>/dev/null | xargs -r -n1 sudo ctr -n "$ns" images rm 2>/dev/null
    for pass in 1 2 3 4 5; do sudo ctr -n "$ns" snapshot --snapshotter $sn ls 2>/dev/null | awk 'NR>1{print $1}' | xargs -r -n1 sudo ctr -n "$ns" snapshot --snapshotter $sn rm 2>/dev/null; done
  done
done
# tear down devmapper pool + loops + backing files:
for d in $(sudo dmsetup ls | awk '/containerd-pool/{print $1}'); do sudo dmsetup remove --retry "$d"; done
sudo losetup -a | grep containerd | sed 's/:.*//' | xargs -r sudo losetup -d
sudo rm -rf /var/lib/containerd/io.containerd.snapshotter.v1.devmapper
sudo rm -f /usr/local/bin/{solo5-spt,urunc,containerd-shim-urunc-v2,nerdctl,crictl}
sudo rm -rf /opt/cni/bin /etc/cni/net.d /etc/rancher /var/lib/rancher
sudo systemctl start containerd
```

### 1. Solo5-spt v0.9.0
```bash
sudo apt-get install -y build-essential pkg-config libseccomp-dev
git clone -b v0.9.0 --depth 1 https://github.com/Solo5/solo5 && cd solo5
./configure.sh && sed -i 's/-Werror//g' Makefile.common && make
sudo install -m0755 tenders/spt/solo5-spt /usr/local/bin/solo5-spt
cd ~
```

### 2. urunc
```bash
git clone --depth 1 https://github.com/urunc-dev/urunc urunc-src && cd urunc-src
# (optional, D1) git checkout <tag-or-commit>
export PATH=$PATH:/usr/local/go/bin
make && sudo make install                 # -> /usr/local/bin/{urunc,containerd-shim-urunc-v2}
cd ~
```

### 3. devmapper pool + nerdctl + CNI
```bash
sudo bash ~/urunc-src/script/dm_create.sh                                    # pool containerd-pool, 100G
curl -sSL -o /tmp/n.tgz https://github.com/containerd/nerdctl/releases/download/v2.1.2/nerdctl-2.1.2-linux-arm64.tar.gz
sudo tar -C /usr/local/bin -xzf /tmp/n.tgz nerdctl
sudo mkdir -p /opt/cni/bin
curl -sSL -o /tmp/cni.tgz https://github.com/containernetworking/plugins/releases/download/v1.5.1/cni-plugins-linux-arm64-v1.5.1.tgz
sudo tar -C /opt/cni/bin -xzf /tmp/cni.tgz
```

### 4. containerd config  (`/etc/containerd/config.toml`, back up first) — corrects **D2**
```toml
version = 3
[plugins.'io.containerd.snapshotter.v1.devmapper']
  pool_name = "containerd-pool"
  root_path = "/var/lib/containerd/io.containerd.snapshotter.v1.devmapper"
  base_image_size = "10GB"
  discard_blocks = true
[plugins.'io.containerd.cri.v1.images']
  snapshotter = "overlayfs"                      # keep GLOBAL overlayfs (global devmapper breaks pause)
[plugins.'io.containerd.cri.v1.runtime'.cni]
  bin_dirs = ["/opt/cni/bin"]                    # D2: bin_dirs, not bin_dir
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
```bash
sudo systemctl restart containerd
# GATE 1:
sudo nerdctl run --rm --net none --runtime io.containerd.urunc.v2 --snapshotter devmapper \
  harbor.nbfc.io/nubificus/urunc/hello-spt-mirage:latest --hello=Holahola      # -> Holahola x4, exit 0
```

### 5. k3s on external containerd + bridge CNI + RuntimeClass
```bash
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="\
  --container-runtime-endpoint unix:///run/containerd/containerd.sock \
  --flannel-backend=none --disable-network-policy \
  --disable=traefik --disable=servicelb --disable=metrics-server --disable=local-storage \
  --write-kubeconfig-mode 644" sh -
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
sudo tee /etc/cni/net.d/10-bridge.conflist >/dev/null <<'JSON'
{ "cniVersion":"1.0.0","name":"bridge","plugins":[
  {"type":"bridge","bridge":"cni0","isGateway":true,"ipMasq":true,
   "ipam":{"type":"host-local","ranges":[[{"subnet":"10.42.0.0/24"}]],"routes":[{"dst":"0.0.0.0/0"}]}},
  {"type":"portmap","capabilities":{"portMappings":true}} ] }
JSON
kubectl apply -f - <<'YAML'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: urunc}
handler: urunc
YAML
# GATE 2: bare pod runtimeClassName urunc + net-spt-mirage (annotation carries the mirage cmdline)
kubectl apply -f - <<'YAML'
apiVersion: v1
kind: Pod
metadata: {name: net-urunc, annotations: {com.urunc.unikernel.cmdline: "-l *:debug"}}
spec:
  runtimeClassName: urunc
  containers: [{name: net-urunc, image: harbor.nbfc.io/nubificus/urunc/net-spt-mirage:latest}]
YAML
# -> net-urunc 1/1 Running on 10.42.0.x ; logs show mirage TCP/IP up on the pod IP
```

### 6. Argo Workflows v3.6.5 + repro — corrects **D4**
```bash
kubectl create ns argo
kubectl apply -n argo -f https://github.com/argoproj/argo-workflows/releases/download/v3.6.5/quick-start-minimal.yaml
kubectl patch cm -n argo artifact-repositories --type merge -p '{"data":{"default-v1":"archiveLogs: false\n"}}'
kubectl rollout restart deploy/workflow-controller -n argo
# D4: give the argo SA the emissary (workflowtaskresults) permission (or run workflows as SA `default`):
kubectl apply -f - <<'YAML'
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: argo-executor, namespace: argo}
rules: [{apiGroups: ["argoproj.io"], resources: ["workflowtaskresults"], verbs: ["create","patch","get","list","watch","update","delete"]}]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: argo-executor, namespace: argo}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: argo-executor}
subjects: [{kind: ServiceAccount, name: argo, namespace: argo}]
YAML

# BASELINE (Succeeds in ~15s):
kubectl apply -f - <<'YAML'
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata: {name: hello-world, namespace: argo}
spec:
  serviceAccountName: argo
  entrypoint: main
  templates: [{name: main, container: {image: busybox, command: [echo], args: ["hello world"]}}]
YAML

# REPRO (Hangs — node stays Running after the VMM exits):
kubectl apply -f - <<'YAML'
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata: {name: urunc-repro, namespace: argo}
spec:
  serviceAccountName: argo
  entrypoint: main
  podSpecPatch: |
    runtimeClassName: urunc
  templates:
  - name: main
    container:
      image: harbor.nbfc.io/nubificus/urunc/hello-spt-mirage:latest
      command: ["/hello"]
      args: ["--hello=ArgoUrunc"]
YAML
```

**What you will see (confirmed this run):** `urunc-repro` `Running` indefinitely with `main` terminated
(exit 1, Solo5-spt net-attach abort) and `wait` running; host has **0** `solo5-spt` processes;
`/var/run/argo/ctr/main/exitcode` and the `ctr/` dir are **never created**; `main` logs show `solo5-spt`,
not `argoexec`.
