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

package unikontainers

import (
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestIsArgoEmissaryMain(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"argo emissary abs path", []string{"/var/run/argo/argoexec", "emissary", "--", "/hello"}, true},
		{"argo emissary bare name", []string{"argoexec", "emissary", "--loglevel", "info"}, true},
		{"plain unikernel command", []string{"/hello", "--hello=hi"}, false},
		{"argoexec but not emissary", []string{"argoexec", "wait"}, false},
		{"emissary not argv0", []string{"/bin/sh", "emissary"}, false},
		{"single arg", []string{"argoexec"}, false},
		{"empty args", []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &specs.Spec{Process: &specs.Process{Args: tt.args}}
			if got := isArgoEmissaryMain(spec); got != tt.want {
				t.Fatalf("isArgoEmissaryMain(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestIsArgoEmissaryMainNilSafe(t *testing.T) {
	if isArgoEmissaryMain(nil) {
		t.Fatal("nil spec must be false")
	}
	if isArgoEmissaryMain(&specs.Spec{}) {
		t.Fatal("nil process must be false")
	}
}

// TestArgoWorkflowContext covers the Milestone 1 profile resolution: the
// sandboxProfile annotation is authoritative; absence falls back to argv.
func TestArgoWorkflowContext(t *testing.T) {
	emissary := []string{"/var/run/argo/argoexec", "emissary", "--"}
	plain := []string{"/hello"}
	tests := []struct {
		name    string
		profile string // "" means annotation absent
		args    []string
		want    bool
	}{
		{"profile argo-workflow, plain args -> true (annotation wins)", "argo-workflow", plain, true},
		{"profile none, emissary args -> false (explicit off)", "none", emissary, false},
		{"profile knative, emissary args -> false (not argo)", "knative", emissary, false},
		{"absent, emissary args -> true (argv fallback)", "", emissary, true},
		{"absent, plain args -> false (fallback)", "", plain, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ann := map[string]string{}
			if tt.profile != "" {
				ann[annotSandboxProfile] = tt.profile
			}
			spec := &specs.Spec{Process: &specs.Process{Args: tt.args}, Annotations: ann}
			if got := argoWorkflowContext(spec); got != tt.want {
				t.Fatalf("argoWorkflowContext(profile=%q,args=%v) = %v, want %v", tt.profile, tt.args, got, tt.want)
			}
		})
	}
	if argoWorkflowContext(nil) {
		t.Fatal("nil spec must be false")
	}
}

func TestGetNetworkType(t *testing.T) {
	tests := []struct {
		name    string
		ctrName string
		profile string
		args    []string
		want    string
	}{
		{"knative user-container", "user-container", "", []string{"/app"}, "static"},
		{"argo profile annotation", "main", "argo-workflow", []string{"/hello"}, "static"},
		{"argo emissary argv fallback", "main", "", []string{"/var/run/argo/argoexec", "emissary", "--"}, "static"},
		{"profile none stays dynamic", "main", "none", []string{"/var/run/argo/argoexec", "emissary"}, "dynamic"},
		{"plain unikernel stays dynamic", "main", "", []string{"/hello"}, "dynamic"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ann := map[string]string{"io.kubernetes.cri.container-name": tt.ctrName}
			if tt.profile != "" {
				ann[annotSandboxProfile] = tt.profile
			}
			spec := &specs.Spec{Process: &specs.Process{Args: tt.args}, Annotations: ann}
			u := Unikontainer{Spec: spec}
			if got := u.getNetworkType(); got != tt.want {
				t.Fatalf("getNetworkType() = %q, want %q", got, tt.want)
			}
		})
	}
}
