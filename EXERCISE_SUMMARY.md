# Pre-application exercise — urunc × Argo Workflows (issue #573)

**Environment:** single-node aarch64 Ubuntu VM on Oracle Cloud, no `/dev/kvm`, so the only usable
monitor is Solo5-spt. k3s v1.36.2+k3s1 pointed at an external containerd v2.2.6, with a devmapper
thin-pool snapshotter wired in for the urunc runtime. Argo Workflows v3.6.5 running in the `argo`
namespace with the emissary executor (the only executor in current Argo releases).

This is the exercise the maintainer asked for in the mentorship posting on #573: set up urunc,
set up Argo, and try to replicate the issue. I'd actually done a version of this once before, a
couple of weeks earlier, but I wanted a clean, fully reproducible run on a genuinely untouched
urunc checkout rather than relying on that older result — so I cloned urunc fresh from upstream
`main` for this exercise (commit `f513ece`) into its own directory, built it, and ran everything against that
build specifically. Nothing from any of my own branch work touched this run.

## 1. urunc on its own

Built `urunc`/`containerd-shim-urunc-v2` from the fresh clone and installed them. Deployed a single
Pod with `runtimeClassName: urunc` running `hello-spt-rumprun-block-aarch64` (a small rumprun
unikernel that prints "Hello world" and exits). It went straight to `Completed`, exit code 0, clean
Solo5 boot/halt log. That confirms the baseline: urunc itself, freshly built, works correctly as a
Kubernetes runtime for a unikernel workload.

## 2. Argo on its own

Argo Workflows v3.6.5 was already running and healthy in the cluster. I submitted a plain workflow
with a `busybox` step (no urunc involved) to confirm the baseline: it went `Succeeded` in about 15
seconds once its step ran (roughly 40s including image-pull overhead the first time). Normal Argo
behavior, nothing unusual.

## 3. Replicating the issue

I then submitted a single-step workflow whose `main` container used
`runtimeClassName: urunc` (via `podSpecPatch`) and the same `hello-spt-rumprun-block-aarch64`
image. One unrelated hiccup on the first attempt: without an explicit `command:` in the step,
Argo's emissary executor tries to look up the image's entrypoint from the registry before it
creates the pod, and that lookup to `harbor.nbfc.io` timed out — nothing to do with urunc, just a
registry-reachability issue on my cluster. Adding an explicit `command:` sidesteps that lookup, and
the resubmitted workflow (`urunc-hang-repro-vxjq4`) ran as expected from there.

What I saw:

- `main` container: `terminated`, `exitCode: 0` — the unikernel booted, printed "Hello world", and
  exited cleanly, about a second after the pod started.
- `wait` sidecar: still `running` when I checked again — nearly 6.5 minutes after `main` had
  already exited — and I only stopped watching there, not because it recovered.
- The workflow's phase stayed `Running` the entire time; it never reached `Succeeded` or `Failed`
  on its own.

That's the hang described in #573 and #135, and it matches what other people on the issue thread
have independently reported: `main` finishes, but the workflow node never does.

Looking at the `wait` sidecar's own logs gave me two separate findings, not one:

1. **No exit-code file.** urunc replaces the container process with the unikernel VMM directly
   (it `exec`s straight into Solo5-spt); Argo's `argoexec emissary` wrapper — the thing that would
   normally run the command, watch it, and write `/var/run/argo/ctr/main/exitcode` when it's done —
   never actually runs. `wait` is watching for a file that nothing is ever going to create.
2. **`wait` also can't reach the Kubernetes API.** Independent of the exit-code problem, the sidecar
   logged this every ~60 seconds, repeating for several minutes straight:
   ```
   level=warning msg="Non-transient error: Post \"https://10.43.0.1:443/apis/argoproj.io/v1alpha1/namespaces/argo/workflowtaskresults\": dial tcp 10.43.0.1:443: connect: no route to host"
   ```
   `10.43.0.1` is the cluster's API service IP. urunc's default networking mode installs a shared
   traffic-redirect on the pod's network namespace for its own tap device, and with a plain/dynamic
   urunc pod that redirect isn't scoped to just the unikernel's traffic — it affects the whole
   netns, including the `wait` sidecar sharing that pod. So even task-result reporting fails, on top
   of the exit-code problem. These are two separate, independently-triggering bugs, not one.

## 4. Cleanup

I deleted the hung workflow normally (`kubectl delete workflow`, no `--force`/`--grace-period=0` —
force-deleting a urunc pod mid-teardown is known to leak devmapper thin-device mounts and wedge the
snapshotter). The pod terminated within about 15 seconds of the delete request. Afterward: no
leftover Solo5 processes, no leftover tap interfaces, the devmapper pool was intact, and the node
stayed `Ready` throughout.

## Summary

All three pre-application tasks are done: urunc deploys a container correctly on its own, Argo
runs a normal workflow correctly on its own, and putting a urunc unikernel behind
`runtimeClassName: urunc` as an Argo step's `main` container reproduces the hang described in
#573 — cleanly, repeatably, and (this run specifically) on an untouched, freshly built urunc.
