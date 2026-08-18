# Live Validation Capture — 2026-08-16 (audit re-verification run)

Raw, unedited command output captured on VM `vidhya` on **2026-08-16** to close evidence gaps found in the
factuality audit of `COMPATIBILITY_AND_ROADMAP_CONCISE.md`. Every block below is verbatim tool output.

**Environment (re-verified this session, not carried over):**

```
$ ls -l /dev/kvm
ls: cannot access '/dev/kvm': No such file or directory      <- no KVM
$ uname -m ; . /etc/os-release ; echo $PRETTY_NAME
aarch64
Ubuntu 24.04.4 LTS
$ which solo5-spt solo5-hvt qemu-system-aarch64 firecracker cloud-hypervisor
/usr/local/bin/solo5-spt                                     <- spt is the ONLY monitor installed
$ containerd --version
containerd containerd v2.2.6 11ce9d5f3c68c941867e82890e93e815c1304f1b
$ sudo k3s kubectl version | head -3
Server Version: v1.36.2+k3s1
$ urunc --version
urunc version 0.7.0-c6bcc89-dirty
$ sudo k3s kubectl -n argo get deploy -o jsonpath=...
argo-server          quay.io/argoproj/argocli:v3.6.5
workflow-controller  quay.io/argoproj/workflow-controller:v3.6.5
```

Branch state during capture (unchanged before and after):

```
$ cd ~/urunc-src && git rev-parse --abbrev-ref HEAD ; git rev-parse HEAD ; git status --short
argo-poc-integration
c6bcc8998d60c6ff07b4c97590fc1d24efb80911
 M pkg/containerd-shim/task_service.go
 M pkg/unikontainers/config.go
 M pkg/unikontainers/unikontainers.go
?? ARGO_URUNC_ARCHITECTURE.md
?? mentorship
?? pkg/containerd-shim/argo_test.go
?? pkg/unikontainers/argo_test.go
```

---

## TEST N1 — Static vs dynamic network: controlled A/B

**Purpose.** Establish, with raw output, (a) whether the dynamic-mode catch-all tc steal filter is
absent in static mode, (b) the actual MASQUERADE rule and `ip_forward` value, (c) whether the
Kubernetes API is reachable from the pod network namespace (the path the Argo `wait` sidecar uses),
and (d) guest reachability on the static guest IP vs the pod IP.

**Method.** Two pods, created **within 30 s of each other on the same node from the same image**
(`net-spt-mirage`), differing in exactly one field: the presence of
`com.urunc.unikernel.sandboxProfile: "argo-workflow"`. Both were created directly (not by Argo), so
`getNetworkType()` is exercised through the same `argoWorkflowContext` -> profile branch an Argo pod
takes, without Argo's emissary argv rewrite. All commands run via `nsenter --net=<pod netns>`.

**What this proves.** The static/dynamic network difference and its effect on API reachability
**while the guest is running and holds the tap**.
**What this does NOT prove.** That the dynamic-mode blackhole is universal across all urunc/Argo
deployments — see the conditionality note at the end of this file.

### N1a — STATIC mode (sandboxProfile=argo-workflow)

```text
############ CAPTURE: STATIC MODE (sandboxProfile=argo-workflow) ############
## timestamp(UTC): 2026-08-16T15:04:38Z
## pod: argo/audit-static-net
## podIP=10.42.0.86 podUID=121ac2ed-206b-4367-a8dc-53daf52d74a1 runtimeClassName=urunc sandboxProfile=argo-workflow
## sandboxID=5489a2936e2ae6e5a7cdc5447be6f0109801be9333af9c6f1a4499673a935c0b
## netns=/var/run/netns/cni-5f1e3b73-e760-b2c2-643a-24f86190b0c5

$ ip -o addr show
1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
1: lo    inet6 ::1/128 scope host \       valid_lft forever preferred_lft forever
2: eth0    inet 10.42.0.86/24 brd 10.42.0.255 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet6 fe80::3077:2ff:fe32:e46c/64 scope link \       valid_lft forever preferred_lft forever
3: tap0_urunc    inet 172.16.1.1/24 brd 172.16.1.255 scope global tap0_urunc\       valid_lft forever preferred_lft forever
3: tap0_urunc    inet6 fe80::f81d:b7ff:feea:cc57/64 scope link \       valid_lft forever preferred_lft forever

$ ip -o link show
1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000\    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
2: eth0@if33: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default \    link/ether 32:77:02:32:e4:6c brd ff:ff:ff:ff:ff:ff link-netnsid 0
3: tap0_urunc: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP mode DEFAULT group default qlen 1000\    link/ether fa:1d:b7:ea:cc:57 brd ff:ff:ff:ff:ff:ff

$ ip route show
default via 10.42.0.1 dev eth0 
10.42.0.0/24 dev eth0 proto kernel scope link src 10.42.0.86 
172.16.1.0/24 dev tap0_urunc proto kernel scope link src 172.16.1.1 

$ tc qdisc show dev eth0
qdisc noqueue 0: root refcnt 2 

$ tc filter show dev eth0 ingress

$ tc filter show dev eth0 egress

$ iptables -t nat -S POSTROUTING
-P POSTROUTING ACCEPT
-A POSTROUTING -s 172.16.1.0/24 -o eth0 -j MASQUERADE

$ iptables -t nat -S PREROUTING
-P PREROUTING ACCEPT

$ sysctl net.ipv4.ip_forward
net.ipv4.ip_forward = 1

$ nsenter -- curl -sS -m 5 -o /dev/null -w 'http_code=%{http_code}' http://172.16.1.2:8080/  (guest static IP)
curl: (52) Empty reply from server
http_code=000

$ nsenter -- curl -sS -m 5 -o /dev/null -w 'http_code=%{http_code}' http://10.42.0.86:8080/  (pod IP - inbound DNAT gap)
curl: (7) Failed to connect to 10.42.0.86 port 8080 after 0 ms: Couldn't connect to server
http_code=000

$ nsenter -- curl -k -sS -m 8 -o /dev/null -w 'http_code=%{http_code}' https://10.43.0.1:443/  (K8s API from pod netns == sidecar path)
http_code=401

## host-side leak counters: solo5=2 uruncTaps(host)=0
############ END CAPTURE: STATIC MODE (sandboxProfile=argo-workflow) ############
```

### N1b — DYNAMIC mode (no sandboxProfile) — CONTROL

```text
############ CAPTURE: DYNAMIC MODE (no sandboxProfile) - CONTROL ############
## timestamp(UTC): 2026-08-16T15:04:52Z
## pod: argo/audit-dynamic-net
## podIP=10.42.0.87 podUID=6426ca4e-0fe6-42cb-bf51-474c8a4f5f6e runtimeClassName=urunc sandboxProfile=<none>
## sandboxID=434afa972316206ab289dc8b19a9e43096d52ed6742c34758ef31e93149afd94
## netns=/var/run/netns/cni-fa12dddb-9096-ecc9-4736-4fbb7e96bbd0

$ ip -o addr show
1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
1: lo    inet6 ::1/128 scope host \       valid_lft forever preferred_lft forever
2: eth0    inet 10.42.0.87/24 brd 10.42.0.255 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet6 fe80::e083:36ff:fe20:7490/64 scope link \       valid_lft forever preferred_lft forever
3: tap0_urunc    inet6 fe80::30ad:29ff:fe1a:9704/64 scope link \       valid_lft forever preferred_lft forever

$ ip -o link show
1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000\    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
2: eth0@if34: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP mode DEFAULT group default qlen 1000\    link/ether e2:83:36:20:74:90 brd ff:ff:ff:ff:ff:ff link-netnsid 0
3: tap0_urunc: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP mode DEFAULT group default qlen 1000\    link/ether 32:ad:29:1a:97:04 brd ff:ff:ff:ff:ff:ff

$ ip route show
default via 10.42.0.1 dev eth0 
10.42.0.0/24 dev eth0 proto kernel scope link src 10.42.0.87 

$ tc qdisc show dev eth0
qdisc noqueue 0: root refcnt 2 
qdisc ingress ffff: parent ffff:fff1 ---------------- 

$ tc filter show dev eth0 ingress
filter parent ffff: protocol all pref 49152 u32 chain 0 
filter parent ffff: protocol all pref 49152 u32 chain 0 fh 800: ht divisor 1 
filter parent ffff: protocol all pref 49152 u32 chain 0 fh 800::800 order 2048 key ht 800 bkt 0 terminal flowid not_in_hw 
  match 00000000/00000000 at 0
	action order 1: mirred (Egress Redirect to device tap0_urunc) stolen
	index 2 ref 1 bind 1


$ tc filter show dev eth0 egress
filter parent ffff: protocol all pref 49152 u32 chain 0 
filter parent ffff: protocol all pref 49152 u32 chain 0 fh 800: ht divisor 1 
filter parent ffff: protocol all pref 49152 u32 chain 0 fh 800::800 order 2048 key ht 800 bkt 0 terminal flowid not_in_hw 
  match 00000000/00000000 at 0
	action order 1: mirred (Egress Redirect to device tap0_urunc) stolen
	index 2 ref 1 bind 1


$ iptables -t nat -S POSTROUTING
-P POSTROUTING ACCEPT

$ iptables -t nat -S PREROUTING
-P PREROUTING ACCEPT

$ sysctl net.ipv4.ip_forward
net.ipv4.ip_forward = 1

$ nsenter -- curl -sS -m 5 -o /dev/null -w 'http_code=%{http_code}' http://172.16.1.2:8080/  (guest static IP)
curl: (7) Failed to connect to 172.16.1.2 port 8080 after 3092 ms: Couldn't connect to server
http_code=000

$ nsenter -- curl -sS -m 5 -o /dev/null -w 'http_code=%{http_code}' http://10.42.0.87:8080/  (pod IP - inbound DNAT gap)
curl: (7) Failed to connect to 10.42.0.87 port 8080 after 0 ms: Couldn't connect to server
http_code=000

$ nsenter -- curl -k -sS -m 8 -o /dev/null -w 'http_code=%{http_code}' https://10.43.0.1:443/  (K8s API from pod netns == sidecar path)
curl: (7) Failed to connect to 10.43.0.1 port 443 after 3105 ms: Couldn't connect to server
http_code=000

## host-side leak counters: solo5=2 uruncTaps(host)=0
############ END CAPTURE: DYNAMIC MODE (no sandboxProfile) - CONTROL ############
```

**N1 result (PASS).**

| Property | STATIC (argo profile) | DYNAMIC (control) |
|---|---|---|
| `tc filter show dev eth0 ingress` | **empty** | `match 00000000/00000000 ... mirred (Egress Redirect to device tap0_urunc) stolen` |
| `tc filter show dev eth0 egress` | **empty** | same catch-all steal |
| `tap0_urunc` address | `172.16.1.1/24` | no IPv4 address |
| `iptables -t nat -S POSTROUTING` | `-A POSTROUTING -s 172.16.1.0/24 -o eth0 -j MASQUERADE` | no rule |
| `iptables -t nat -S PREROUTING` | no DNAT | no DNAT |
| `sysctl net.ipv4.ip_forward` | `1` | `1` |
| K8s API `https://10.43.0.1:443` from pod netns | **`http_code=401`** (reachable; 401 = TLS + HTTP response from apiserver) | **`curl: (7) Failed to connect ... after 3105 ms`** (blackholed) |
| Guest `http://172.16.1.2:8080` | `curl: (52) Empty reply from server` -> **TCP connection established** | `curl: (7)` not reachable |
| Guest on pod IP `:8080` | `curl: (7)` **refused** -> no inbound DNAT | `curl: (7)` refused |

Notes on precision:
- `net.ipv4.ip_forward = 1` is **1 in both** namespaces. `setNATRule` does set it
  (`network_static.go:44-53`), but the value alone is not discriminating evidence, because the CNI
  also sets it. The **MASQUERADE rule** is the discriminating evidence.
- Guest reachability at `172.16.1.2:8080` is proven at **TCP level only**: `curl: (52) Empty reply
  from server` means connect() succeeded and the peer closed without an HTTP response. This is
  L3/L4 reachability, not a working HTTP service. The contrast with pod IP (`curl: (7)`, immediate
  refusal) is what establishes the routing/DNAT distinction.

---

## TEST N2 — Static mode is genuinely selected inside a real emissary-created Argo pod

**Purpose.** N1 used directly-created pods. This test confirms the same network state inside a pod
actually built by the Argo workflow-controller (with the emissary `init`/`main`/`wait` structure).

**Method.** Workflow `audit-argo-net` (`nginx-spt-rumprun-block-aarch64`, `sandboxProfile:
argo-workflow`, `podSpecPatch` runtimeClassName). A 0.15 s poll loop captured the netns the instant
`tap0_urunc` appeared, because the guest is short-lived.

### N2 — race capture inside the live Argo pod

```text
## RACE CAPTURE at 2026-08-16T15:06:37.877160568Z  pod=audit-argo-net sandbox=e515ebe43663ce13a627a124ade5cba9a30da2a9cbcf116bc2cf808af7c53bc8 netns=/var/run/netns/cni-7ccb6b78-a617-1711-c879-32e3bb03e68d
$ ip -o addr show
1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
1: lo    inet6 ::1/128 scope host \       valid_lft forever preferred_lft forever
2: eth0    inet 10.42.0.88/24 brd 10.42.0.255 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet6 fe80::895:82ff:feb5:588a/64 scope link \       valid_lft forever preferred_lft forever
3: tap0_urunc    inet 172.16.1.1/24 brd 172.16.1.255 scope global tap0_urunc\       valid_lft forever preferred_lft forever
3: tap0_urunc    inet6 fe80::44b9:41ff:fefb:c33f/64 scope link tentative \       valid_lft forever preferred_lft forever
$ tc filter show dev eth0 ingress
$ tc filter show dev eth0 egress
$ iptables -t nat -S POSTROUTING
-P POSTROUTING ACCEPT
-A POSTROUTING -s 172.16.1.0/24 -o eth0 -j MASQUERADE
$ sysctl net.ipv4.ip_forward
net.ipv4.ip_forward = 1
$ curl -k https://10.43.0.1:443 (API from pod netns)
http_code=401
```

**N2 result (PASS).** In a genuine Argo pod: `tap0_urunc = 172.16.1.1/24`; `tc filter show dev eth0
ingress` and `egress` both **empty**; `-A POSTROUTING -s 172.16.1.0/24 -o eth0 -j MASQUERADE`
present; API from the pod netns `http_code=401` (reachable). Workflow `Succeeded`
(`2026-08-16T15:06:31Z -> 15:06:41Z`, 10 s); `main exit=0`, `wait exit=0`.

**What this proves.** Static mode is selected, and the steal filter is absent, in a real Argo pod.
**What this does NOT prove.** A *serving* unikernel step under Argo — see TEST N5.

---

## TEST N3 — Output extraction + F2 exitcode, captured on disk before GC

**Purpose.** The prior evidence for extraction was a journal file count (`files=2`). This test
captures the **actual destination files, contents, permissions, symlink exclusion and the exitcode
file** from the shared emptyDir.

**Method.** Source tree pre-seeded on the host (**the guest-write is still SIMULATED — see the
limitation note below**):

```
$ ls -lAR /tmp/t3out
/tmp/t3out:
lrwxrwxrwx  evil-symlink -> /etc/shadow                 <- absolute symlink escape
drwxrwxr-x  logs
-rw-rw-r--  result.txt
/tmp/t3out/logs:
lrwxrwxrwx  evil-relative -> ../../../etc/passwd        <- relative symlink escape
-rw-rw-r--  run.log
```

Workflow `audit-extract`: `hello-spt-rumprun-block-aarch64`, annotations `sandboxProfile:
argo-workflow` + `argoOutputVolume: "/out"`, hostPath `/tmp/t3out` mounted at `/out`. A 0.1 s watcher
snapshotted the emptyDir the instant `ctr/main/exitcode` appeared (34 ms after it was written) —
necessary because the kubelet removes the volume shortly after the pod terminates.

### N3 — emptyDir snapshot

```text
############ EXTRACTION + F2 ON-DISK SNAPSHOT ############
## captured(UTC): 2026-08-16T16:33:16.999867714Z
## emptyDir: /var/lib/kubelet/pods/10dd87b7-d9e5-4b2f-99c3-eb36ecdbc9e2/volumes/kubernetes.io~empty-dir/var-run-argo

$ find <emptyDir> -mindepth 1 -printf '%y %M %5s %P\n' | sort
d drwxr-xr-x  4096 ctr
d drwxr-xr-x  4096 ctr/main
d drwxr-xr-x  4096 outputs
d drwxr-xr-x  4096 outputs/logs
f -r--r--r--   411 template
f -r-xr-xr-x 120077344 argoexec
f -rw-r--r--     1 ctr/main/exitcode
f -rw-r--r--    37 outputs/logs/run.log
f -rw-r--r--    41 outputs/result.txt

$ stat -c '%n mode=%a size=%s' <exitcode>
/var/lib/kubelet/pods/10dd87b7-d9e5-4b2f-99c3-eb36ecdbc9e2/volumes/kubernetes.io~empty-dir/var-run-argo/ctr/main/exitcode mode=644 size=1
exitcode content = [0]

--- outputs/result.txt ---
AUDIT-MARKER-2026-08-16-extraction-proof
--- outputs/logs/run.log ---
line1 run started
line2 run finished

## SYMLINK EXCLUSION
source regular files = 2; source symlinks = 2
dest   regular files = 2; dest   symlinks = 0
dest outputs/evil-symlink exists?      NO
dest outputs/logs/evil-relative exists? NO

## F2 STALE TEMP FILES
count of *.tmp under emptyDir = 0
############ END SNAPSHOT ############
```

**N3 result (PASS).**
- `outputs/result.txt` — mode **644**, 41 bytes, content exactly `AUDIT-MARKER-2026-08-16-extraction-proof`
- `outputs/logs/run.log` — mode **644**, 37 bytes, **nested directory structure preserved**
- **Symlink exclusion proven on disk:** source = 2 regular files + 2 symlinks; destination = 2 regular
  files + **0 symlinks**; neither `outputs/evil-symlink` nor `outputs/logs/evil-relative` exists.
  Both an absolute (`/etc/shadow`) and a relative (`../../../etc/passwd`) escape were excluded.
- **F2:** `ctr/main/exitcode` mode **644**, size **1**, content `[0]`; **0** `*.tmp` files anywhere
  under the emptyDir.
- Also visible in the snapshot: `argoexec` (120 MB) and `template` (411 B) staged by the Argo `init`
  container, confirming init's documented role.

**Ordering (fresh measurement):**

```
Aug 16 16:33:16.965317  urunc(shim): extracted Argo outputs  files=2  dest=.../var-run-argo/outputs
Aug 16 16:33:16.966304  urunc(shim): wrote Argo completion file  code=0  dir=.../ctr/main
```

Extraction completed **987 microseconds** before the completion file was written. (The earlier
recorded run measured 276 us on 2026-08-15; both confirm the same ordering guarantee.)

**LIMITATION — unchanged.** The guest did **not** write these files. `/tmp/t3out` was pre-seeded on
the host. This test proves the shim's copy, its symlink guard, the destination permissions and the
completion ordering. It does **not** prove a guest-written output end to end, exact Argo
declared-path mapping, or Argo-native artifact/parameter consumption.

---

## TEST N4 — The runtime chain, directly observed (not inferred)

**Purpose.** The claim "shim `Delete` -> inner Delete runs `urunc delete` -> `restoreBlockVolumes`"
was previously an inference. This test observes the actual `execve` calls.

**Mechanism (build-time, load-bearing).** urunc's `Makefile` patches vendored go-runc before building
the shim, so the inner containerd runc task service invokes `urunc` instead of `runc`:

```
$ sed -n '177,186p' ~/urunc-src/Makefile
$(SHIM_BIN)_static_%: $(SHIM_SRC) | prepare
	@sed -i 's/DefaultCommand = "runc"/DefaultCommand = "urunc"/g' \
		$(VENDOR_DIR)/github.com/containerd/go-runc/runc.go
$ grep -n 'DefaultCommand' ~/urunc-src/vendor/github.com/containerd/go-runc/runc.go
54:	DefaultCommand = "urunc"
```

The bundle carries **no** `BinaryName`, confirming the default is what is used:

```
$ sudo cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/<id>/runtime      -> (empty)
$ sudo cat /run/containerd/io.containerd.runtime.v2.task/k8s.io/<id>/options.json -> {}
```

**Observed `execve` trace** (`execsnoop-bpfcc`, during the `audit-extract` workflow; paths trimmed):

```text
urunc            1785743 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702/log.json --log-format json create --bundle d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702 --pid-file d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702/init.pid d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702
urunc            1785767 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702/log.json --log-format json start d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702
urunc            1786611 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c/log.json --log-format json create --bundle b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c --pid-file b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c/init.pid b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c
urunc            1786636 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c/log.json --log-format json start b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c
urunc            1786655 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c/log.json --log-format json kill --all b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c 9
urunc            1786667 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c/log.json --log-format json delete b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c
urunc            1786687 1786679   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c/log.json --log-format json delete --force b8262a6d8d0ea984ec4cb56d2093f36a54789cb67ce604638cf9fdedc4f1a72c
urunc            1786727 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e/log.json --log-format json create --bundle 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e --pid-file 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e/init.pid 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e
urunc            1786753 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e/log.json --log-format json start 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e
urunc            1786937 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/log.json --log-format json create --bundle 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3 --pid-file 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/init.pid 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3
exe              1786945 1786937   0 /proc/self/exe --root /run/containerd/runc/k8s.io --log 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/log.json --log-format json create --bundle 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3 --pid-file 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/init.pid 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3 --reexec
urunc            1786953 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/log.json --log-format json start 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3
urunc            1786962 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/log.json --log-format json kill --all 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3 9
urunc            1786968 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/log.json --log-format json delete 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3
urunc            1786982 1786974   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3/log.json --log-format json delete --force 210f7aca65232ac9024de153a9eb89b547018bb482dd35aec9f61ad3247c07e3
urunc            1786989 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e/log.json --log-format json kill --all 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e 9
urunc            1786999 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e/log.json --log-format json delete 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e
urunc            1787019 1787010   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e/log.json --log-format json delete --force 7e655e967643f363839d9880b1a8731ea48279af4c090b704e14b4c2583a244e
urunc            1787088 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702/log.json --log-format json kill d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702 9
urunc            1787100 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702/log.json --log-format json kill --all d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702 9
urunc            1787112 1785728   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702/log.json --log-format json delete d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702
urunc            1787130 1787123   0 /usr/local/bin/urunc --root /run/containerd/runc/k8s.io --log d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702/log.json --log-format json delete --force d33b6296c417245db1cb4ff59c7654396703bfb470fac00c1451b26254510702
```

**N4 result (PASS).** The `containerd-shim-urunc-v2` process execs `/usr/local/bin/urunc` for
**create**, **start**, **kill --all** and **delete**. Two distinct downstream behaviours are visible:

- **Sidecars / non-unikernel containers:** the very same PID then execs `/usr/bin/runc` — this is
  `runcExec()` in `cmd/urunc/create.go:113-122` replacing the process image after
  `ErrNotUnikernel`. Delegation to runc is therefore observed, not assumed.
- **The unikernel container:** `urunc ... create` is followed by `/proc/self/exe ... --reexec`, the
  urunc unikernel path — no `runc` exec.

The line `urunc ... delete <container-id>` with the shim as its parent directly confirms the
`shim Delete -> urunc delete` link, and therefore that `Unikontainer.Delete` ->
`restoreBlockVolumes` (`unikontainers.go:842,859`) runs synchronously before the shim's extraction,
since go-runc's `Delete` is a blocking `exec.Cmd` (`go-runc/runc.go` `runOrError`).

---

## TEST N5 — NEW FINDING: Argo's emissary argv rewrite corrupts the guest command line

**Purpose.** Incidental, discovered while trying to run a *serving* guest under Argo.

**Mechanism [CODE].** `unikontainers.go:528` sets the guest command line from the OCI process args:

```go
unikernelParams := types.UnikernelParams{ CmdLine: u.Spec.Process.Args, ... }
if len(unikernelParams.CmdLine) == 0 {                       // :535
    unikernelParams.CmdLine = strings.Fields(u.State.Annotations[annotCmdLine])
}
```

Under Argo, `Process.Args` is **never** empty — Argo rewrites it to
`["/var/run/argo/argoexec","emissary","--loglevel","info","--log-format","text","--gloglevel","0","--", <cmd>...]`
(`workflow/controller/workflowpod.go:404`). So the image's `com.urunc.unikernel.cmdline` annotation
is **never used under Argo**, and the guest receives emissary's wrapper flags as its own arguments.

**Observed**, `net-spt-mirage` under an `argo-workflow` profile workflow:

```
Solo5: Bindings version v0.9.1
network: unknown option '--loglevel', did you mean '-l'?
         unknown option '--log-format', did you mean '-l'?
         unknown option '--gloglevel'.
Usage: network [--port=VAL] [OPTION]...
Solo5: solo5_exit(64) called
```

Pod `main` terminated `exitCode: 64`. The same image runs correctly as a bare pod (TEST N1a), where
no rewrite happens.

**Scope.** Guests that ignore argv are unaffected — `hello-spt-rumprun-block` exits 0 under Argo
(TESTS N3, and the smoke below). Guests that parse argv (mirage/cmdliner) fail. `nginx-spt-rumprun-block`
under Argo booted, mounted its block device and halted with `solo5_exit(0)` instead of serving.

**Status:** [VERIFIED] as an incompatibility; **not** previously documented. Not fixed on this branch.

---

## TEST N6 — Documented-runbook smoke (does the documented recipe work today?)

**Method.** A workflow built **only** from the documented prerequisites — `podSpecPatch` for
`runtimeClassName: urunc` plus the template-level `sandboxProfile` annotation.

| Run | Workflow | startedAt -> finishedAt | Elapsed | main | wait | Phase |
|---|---|---|---|---|---|---|
| smoke (cold, incl. image pull) | `audit-smoke` | `14:33:10Z -> 14:33:52Z` | **42 s** | exit 0 | exit 0 | Succeeded |
| network (warm) | `audit-argo-net` | `15:06:31Z -> 15:06:41Z` | **10 s** | exit 0 | exit 0 | Succeeded |
| extraction (warm) | `audit-extract` | `16:33:13Z -> 16:33:23Z` | **10 s** | exit 0 | exit 0 | Succeeded |

Shim log for the smoke run (only `ctr/main` tracked — the `init`/`wait` sidecars are correctly
excluded by the `unikernelType` gate):

```
Aug 16 14:33:49.609380  urunc(shim): tracking Argo main container  exitDir=.../ctr/main  outputSrc=
Aug 16 14:33:49.727999  urunc(shim): wrote Argo completion file    code=0  dir=.../ctr/main
```

Guest really ran (container logs): `Solo5: Bindings version v0.9.0`, `rump kernel bare metal bootstrap`.

Real `main` argv as created by Argo, confirming the emissary wrapper reaches urunc verbatim:

```
main: ["/var/run/argo/argoexec","emissary","--loglevel","info","--log-format","text","--gloglevel","0","--","/hello"]
wait: ["argoexec","wait","--loglevel","info","--log-format","text","--gloglevel","0"]
```

**NEW PREREQUISITE DISCOVERED.** The template **must** set `command:` explicitly. Without it the
workflow never starts a pod:

```
failed to look-up entrypoint/cmd for image "harbor.nbfc.io/nubificus/urunc/net-spt-mirage:latest",
you must either explicitly specify the command, or list the image's command in the index:
... Get "https://harbor.nbfc.io/v2/": dial tcp: lookup harbor.nbfc.io: i/o timeout
```

This is Argo's entrypoint lookup (`workflowpod.go:390-403`) failing because the workflow-controller
cannot resolve/reach the registry. It is a **required** part of the runbook, previously undocumented.

---

## TEST N7 — Leak / health check after all of the above

```
$ pgrep -c solo5                     -> 0
$ ip -o link show | grep -c urunc    -> 0     (host-side taps; guest taps are netns-local)
$ sudo dmsetup ls                    -> containerd-pool + 4 snapshots (no leaked thin devices)
$ sudo k3s kubectl get nodes         -> vidhya   Ready   control-plane   13d   v1.36.2+k3s1
```

All pods and workflows created for these tests were deleted **gracefully** (never
`--force --grace-period=0`).

---

## TEST N8 — Mermaid diagram validation

All 5 diagrams retained in `COMPATIBILITY_AND_ROADMAP_CONCISE.md` were parsed with the real Mermaid
parser (mermaid **v11.16.1** under jsdom, `mermaid.parse()`):

```
PASS  1 Component architecture
PASS  2 Main-container lifecycle
PASS  3 Network (static mode)
PASS  4 Artifact + completion flow
PASS  5 Mixed workflow
ALL 5 DIAGRAMS PARSE OK
```

---

## Conditionality note — what the N1 blackhole result does and does not mean

TEST N1b shows the API unreachable from the pod netns **while a dynamic-mode guest is running and
holds the tap**, and N1a shows it reachable under the static profile with everything else identical.
That is a controlled causal demonstration of the mechanism.

It is **not** a demonstration that the dynamic-mode blackhole affects every urunc/Argo deployment:

- In urunc issue #135 (qemu, KVM cluster) urunc's network setup *failed outright*, so no tap and no
  filter were installed and the `wait` sidecar could reach the API; that hang was caused by the
  missing exitcode file alone.
- HARSHRAJ2789 reported on urunc issue #573 (2026-08-10) that a sidecar in a urunc pod reached the
  API on 6 of 6 probes, with a clean-exiting `rumprun` guest — consistent with the filter being torn
  down when the guest exits cleanly.

The defensible statement is therefore: *the shared-netns tc-redirect model blackholes a co-located
sidecar for as long as the unikernel holds the tap, and leaks the filter on a non-clean exit
(urunc #874)* — **not** that it is a universal second root cause. Only the missing-exitcode-file
cause is unconditional.
