#!/usr/bin/env bash
# EVIDENCE: solo5-spt mirage unikernel does real block read/write I/O with NO
# shared-fs and NO KVM. Runs under nerdctl --net none (the §3.1 net-less-tap bug
# forces this outside k8s). Expect: "Total tests passed: 10 / failed: 0", solo5_exit(0).
set -euo pipefail
sudo nerdctl run --rm --net none \
  --runtime io.containerd.urunc.v2 --snapshotter devmapper \
  harbor.nbfc.io/nubificus/urunc/block-test-spt-mirage-aarch64:latest -l '*:info'
# The guest's block device is bundled at /unikernel/disk.img (com.urunc.unikernel.block).
# Host-side OUTPUT COLLECTION of the guest's writes requires urunc's
# getBlockVolumes+restoreBlockVolumes path (rumprun+ext2 only) — NOT demonstrable
# on this VM's overlayfs k8s. See code_audit/AUDIT_VERDICTS.md, Blocker 2/output note.
