// Copyright 2026 Gabriel Harnagea
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

package kube

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSplitRef(t *testing.T) {
	cases := []struct {
		ref      string
		wantKind string
		wantName string
		wantErr  bool
	}{
		{ref: "deployment/api", wantKind: "deployment", wantName: "api"},
		{ref: "deploy/checkout", wantKind: "deploy", wantName: "checkout"},
		{ref: "without-slash", wantErr: true},
		{ref: "deployment/", wantErr: true},
		{ref: "/api", wantErr: true},
		{ref: "foo/bar/baz", wantErr: true},
		{ref: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			kind, name, err := splitRef(tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("splitRef(%q) expected error, got nil", tc.ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitRef(%q) unexpected error: %v", tc.ref, err)
			}
			if kind != tc.wantKind || name != tc.wantName {
				t.Errorf("splitRef(%q) = (%q, %q), expected (%q, %q)", tc.ref, kind, name, tc.wantKind, tc.wantName)
			}
		})
	}
}

func deploymentFixture(name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: func() *int32 { r := int32(3); return &r }()},
	}
}

func TestResolveWorkload_Deployment(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: "default"},
		Spec:       appsv1.DeploymentSpec{Replicas: func() *int32 { r := int32(3); return &r }()},
	}
	client := fake.NewSimpleClientset(d)

	w, err := ResolveWorkload(context.Background(), client, "default", "deployment/checkout")
	if err != nil {
		t.Fatalf("ResolveWorkload: %v", err)
	}
	if w.Name() != "checkout" || w.Replicas() != 3 {
		t.Errorf("resolved workload = {Name: %q, Replicas: %d}, expected {checkout, 3}", w.Name(), w.Replicas())
	}
}

// The kinds a user will plausibly type, including the short forms kubectl
// accepts. Each must fail with a message that names both the kind it rejected
// and what is supported: a bare "not found" would send someone looking for a
// missing object instead of an unsupported feature.
func TestResolveWorkload_UnsupportedKind(t *testing.T) {
	unsupported := []string{
		"statefulset/checkout",
		"statefulsets/checkout",
		"sts/checkout",
		"daemonset/checkout",
		"daemonsets/checkout",
		"ds/checkout",
		"pod/checkout",
		"job/checkout",
		"cronjob/checkout",
		"rollout/checkout",
		"nonsense/checkout",
	}

	for _, ref := range unsupported {
		t.Run(ref, func(t *testing.T) {
			client := fake.NewSimpleClientset()

			_, err := ResolveWorkload(context.Background(), client, "default", ref)
			if err == nil {
				t.Fatalf("expected an error for unsupported reference %q, got nil", ref)
			}
			kind := strings.SplitN(ref, "/", 2)[0]
			if !strings.Contains(err.Error(), kind) {
				t.Errorf("error must name the rejected kind %q, got: %v", kind, err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "deployment") {
				t.Errorf("error must name what is supported, got: %v", err)
			}
		})
	}
}

// Case and the short/plural forms kubectl itself accepts must all resolve, so
// that a habit formed with kubectl does not fail here for no reason.
func TestResolveWorkload_AcceptedDeploymentAliases(t *testing.T) {
	for _, ref := range []string{
		"deployment/checkout",
		"deployments/checkout",
		"deploy/checkout",
		"Deployment/checkout",
		"DEPLOY/checkout",
		"deployment.apps/checkout",
	} {
		t.Run(ref, func(t *testing.T) {
			client := fake.NewSimpleClientset(deploymentFixture("checkout"))

			w, err := ResolveWorkload(context.Background(), client, "default", ref)
			if err != nil {
				t.Fatalf("reference %q must resolve, got: %v", ref, err)
			}
			if w.Name() != "checkout" {
				t.Errorf("resolved workload name = %q, expected checkout", w.Name())
			}
		})
	}
}

func TestResolveWorkload_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()

	_, err := ResolveWorkload(context.Background(), client, "default", "deployment/assente")
	if err == nil {
		t.Fatal("expected error for nonexistent Deployment, got nil")
	}
}
