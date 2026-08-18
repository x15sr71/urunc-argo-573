# Tutorial — running an Argo Workflows step on urunc

How to deploy and run an Argo Workflow whose `main` container is a unikernel executed by urunc,
using the prototype on the [`argo-poc-integration`](https://github.com/urunc-dev/urunc/compare/main...x15sr71:urunc:argo-poc-integration?expand=1)
branch.

## Scope, and what this does not cover

This is verified for a **single-node cluster running the `solo5-spt` monitor with a short-lived
guest**. It is not a tutorial for KVM-backed monitors (qemu, firecracker, cloud-hypervisor), for
multi-node clusters, or for Argo artifact/parameter chaining — none of those are proven, and
[`STATUS.md`](STATUS.md) records exactly where the evidence stops.

## 1. Prerequisites

**containerd** needs a runtime block for urunc. The one used here:

```toml
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.urunc]
  runtime_type = "io.containerd.urunc.v2"
  snapshotter = "devmapper"
  container_annotations = ["com.urunc.unikernel.*"]
  pod_annotations = ["com.urunc.unikernel.*"]
```

Both `container_annotations` **and** `pod_annotations` matter: Argo sets the sandbox-profile
annotation at the pod level, so without `pod_annotations` it never reaches the runtime.

A **block-capable snapshotter** (`devmapper` or `blockfile`) is required for urunc; the global CRI
snapshotter can stay `overlayfs`.

**Kubernetes** needs a matching RuntimeClass:

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: urunc
handler: urunc
```

**Argo Workflows** installed as normal (v3.6.5 here, emissary executor).

## 2. Build and install urunc

```bash
git clone https://github.com/x15sr71/urunc.git
cd urunc && git checkout argo-poc-integration
make && sudo make install
urunc --version
```

Build the shim through `make` rather than `go build` directly — the Makefile rewrites vendored
go-runc's `DefaultCommand` from `runc` to `urunc`, and a shim built outside `make` will not have it.

Reference environment for the results below: Go 1.26.4, aarch64 Ubuntu 24.04.4, no `/dev/kvm`,
k3s v1.36.2+k3s1 against an external containerd v2.2.6, Argo Workflows v3.6.5, monitor `solo5-spt`.

## 3. Run the tests

```bash
go test ./pkg/containerd-shim/
go test ./pkg/unikontainers/ -run 'Argo|NetworkType'
```

12 tests, and they pass under `-race`. Note `pkg/containerd-shim` is not part of `make unittest`,
so run it directly.

## 4. Run a workflow

[`manifests/urunc-argo-example.yaml`](manifests/urunc-argo-example.yaml):

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: urunc-smoke-
  namespace: argo
spec:
  serviceAccountName: argo
  entrypoint: uni
  templates:
  - name: uni
    podSpecPatch: '{"runtimeClassName":"urunc"}'
    metadata:
      annotations:
        com.urunc.unikernel.sandboxProfile: "argo-workflow"
    container:
      image: harbor.nbfc.io/nubificus/urunc/hello-spt-rumprun-block-aarch64:latest
      command: ["/hello"]
```

```bash
kubectl create -f manifests/urunc-argo-example.yaml -n argo
```

Three settings are doing real work:

- **`runtimeClassName: urunc`** via `podSpecPatch` — Argo has no first-class field for it.
- **`com.urunc.unikernel.sandboxProfile: "argo-workflow"`** — selects static networking and the
  shim-side completion path. Without it the prototype falls back to detecting `argoexec emissary`
  in the container's argv, which also works; the annotation is the explicit form.
- **An explicit `command:`** — required. Without one, Argo's controller tries to look up the image's
  entrypoint from the registry *before* creating the pod. If it cannot reach the registry the
  workflow fails with `failed to look-up entrypoint/cmd for image …` and no pod is ever created.
  This is Argo behaviour, unrelated to urunc.

## 5. What success looks like

```
$ kubectl get wf -n argo
NAME                STATUS      AGE
urunc-smoke-4zdbl   Succeeded   60s
```

Verified run: `Succeeded`, start 23:38:04 → finish 23:38:44 (40 s including image pull).

Both containers should reach `Completed` — the `wait` sidecar exiting rather than running forever is
the whole point. On unmodified urunc this same workflow hangs indefinitely: `main` terminates with
exit 0 while `wait` stays `Running` (see [`EXERCISE_SUMMARY.md`](EXERCISE_SUMMARY.md)).

To watch what the shim did:

```bash
journalctl -t containerd-shim-urunc-v2 | grep -i argo
```

which logs the tracked container, any extracted outputs, and the completion file it wrote.

## 6. Optional — extracting output files

Declare a mount and name it:

```yaml
    metadata:
      annotations:
        com.urunc.unikernel.sandboxProfile: "argo-workflow"
        com.urunc.unikernel.argoOutputVolume: "/data"
```

Regular files under that mount are copied into the Argo `emptyDir` at `outputs/` during `Delete`,
strictly before the completion file is written. Symlinks are skipped, `..`-escaping paths rejected,
and the total is capped at 64 MiB.

**This path is only partly proven.** The copy, ordering and symlink guards are verified in a real
pod, but the case where the *guest itself* writes those files was simulated with a pre-seeded host
directory, and Argo-native consumption of them as parameters or artifacts is not implemented.

## 7. Known limitations

- **argv-sensitive guests abort.** Argo rewrites the container's argv to invoke emissary, and urunc
  passes that argv to the guest as its command line. Guests that parse argv (e.g. `net-spt-mirage`)
  fail on boot; guests that ignore it are unaffected. Unresolved — it needs an argv-precedence
  decision with the maintainers.
- **The guest owns `172.16.1.2`, not the pod IP.** Serving on the pod IP would need an inbound DNAT
  rule, which is not implemented.
- **Untested:** KVM monitors, multi-node, and shared-filesystem configurations.
