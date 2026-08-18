// Copyright (c) 2023-2026, Nubificus LTD
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package containerdshim

import (
	"os"
	"path/filepath"
	"testing"

	"encoding/json"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func writeArgoBundle(t *testing.T, args []string, argoSrc, name string, ann map[string]string, mounts []specs.Mount) string {
	t.Helper()
	bundle := t.TempDir()
	a := map[string]string{"io.kubernetes.cri.container-name": name}
	for k, v := range ann {
		a[k] = v
	}
	// Default to a unikernel container unless a test overrides unikernelType;
	// parseArgoTask requires it to scope to the main container (not sidecars).
	if _, set := a["com.urunc.unikernel.unikernelType"]; !set {
		a["com.urunc.unikernel.unikernelType"] = "mirage"
	}
	sp := specs.Spec{Process: &specs.Process{Args: args}, Annotations: a}
	if argoSrc != "" {
		sp.Mounts = append(sp.Mounts, specs.Mount{Destination: "/var/run/argo", Source: argoSrc})
	}
	sp.Mounts = append(sp.Mounts, mounts...)
	data, err := json.Marshal(sp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestParseArgoTask(t *testing.T) {
	src := t.TempDir()

	// Argo emissary main container with the shared /var/run/argo mount.
	b := writeArgoBundle(t, []string{"/var/run/argo/argoexec", "emissary", "--", "/hello"}, src, "main", nil, nil)
	at, ok := parseArgoTask(b)
	if !ok || at.exitDir != filepath.Join(src, "ctr", "main") || at.outputDest != filepath.Join(src, "outputs") {
		t.Fatalf("argo main: ok=%v at=%+v", ok, at)
	}
	if at.outputSrc != "" {
		t.Fatalf("no output volume declared, want empty outputSrc, got %q", at.outputSrc)
	}

	// With a declared output volume annotation resolving to its host source.
	volSrc := t.TempDir()
	b2 := writeArgoBundle(t, []string{"/hello"}, src, "main",
		map[string]string{"com.urunc.unikernel.sandboxProfile": "argo-workflow", "com.urunc.unikernel.argoOutputVolume": "/data"},
		[]specs.Mount{{Destination: "/data", Source: volSrc}})
	at2, ok := parseArgoTask(b2)
	if !ok || at2.outputSrc != volSrc {
		t.Fatalf("output volume: ok=%v outputSrc=%q want %q", ok, at2.outputSrc, volSrc)
	}

	// Plain (non-Argo) container must not match.
	if _, ok := parseArgoTask(writeArgoBundle(t, []string{"/hello"}, src, "main", nil, nil)); ok {
		t.Fatal("plain container must not match")
	}
	// Explicit non-argo profile must not match even with emissary argv.
	if _, ok := parseArgoTask(writeArgoBundle(t, []string{"argoexec", "emissary"}, src, "main",
		map[string]string{"com.urunc.unikernel.sandboxProfile": "none"}, nil)); ok {
		t.Fatal("profile none must not match")
	}
	// A pod-scoped argo-workflow profile must NOT match the runc-delegated
	// init/wait sidecars: they lack the unikernel annotation.
	sidecar := writeArgoBundle(t, []string{"argoexec", "wait"}, src, "wait",
		map[string]string{"com.urunc.unikernel.sandboxProfile": "argo-workflow", "com.urunc.unikernel.unikernelType": ""}, nil)
	if _, ok := parseArgoTask(sidecar); ok {
		t.Fatal("argo sidecar (no unikernel annotation) must not be tracked")
	}

	// Missing bundle must not match (and must not panic).
	if _, ok := parseArgoTask(filepath.Join(t.TempDir(), "nope")); ok {
		t.Fatal("missing bundle must not match")
	}
}

func TestWriteArgoExitcode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ctr", "main")
	if err := writeArgoExitcode(dir, 1); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "exitcode"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1" {
		t.Fatalf("exitcode content = %q, want %q", got, "1")
	}
}

func TestCopyOutputs(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "outputs")
	// normal file + nested dir file
	if err := os.WriteFile(filepath.Join(src, "result.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "nested.bin"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := copyOutputs(src, dst)
	if err != nil || n != 2 {
		t.Fatalf("copyOutputs n=%d err=%v want 2,nil", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "result.txt")); string(b) != "hello" {
		t.Fatalf("result.txt not copied: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "nested.bin")); string(b) != "data" {
		t.Fatalf("nested.bin not copied: %q", b)
	}
}

func TestCopyOutputsSkipsSymlink(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "outputs")
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	// a symlink pointing outside src must be skipped, never followed
	if err := os.Symlink(secret, filepath.Join(src, "escape")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := copyOutputs(src, dst)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 1 {
		t.Fatalf("want 1 file copied (symlink skipped), got %d", n)
	}
	if _, err := os.Lstat(filepath.Join(dst, "escape")); !os.IsNotExist(err) {
		t.Fatal("symlink must not be copied")
	}
}

func TestCopyOutputsEnforcesSizeCap(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "outputs")
	big := make([]byte, maxExtractBytes+1)
	if err := os.WriteFile(filepath.Join(src, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := copyOutputs(src, dst); err == nil {
		t.Fatal("oversize copy must be rejected")
	}
}

func TestCopyOutputsMissingSrcIsNoop(t *testing.T) {
	n, err := copyOutputs(filepath.Join(t.TempDir(), "absent"), t.TempDir())
	if err != nil || n != 0 {
		t.Fatalf("missing src: n=%d err=%v want 0,nil", n, err)
	}
}
