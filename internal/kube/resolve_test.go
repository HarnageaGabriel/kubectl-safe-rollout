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

func TestResolveWorkload_UnsupportedKind(t *testing.T) {
	client := fake.NewSimpleClientset()

	_, err := ResolveWorkload(context.Background(), client, "default", "statefulset/checkout")
	if err == nil {
		t.Fatal("expected error for kind unsupported in MVP, got nil")
	}
}

func TestResolveWorkload_NotFound(t *testing.T) {
	client := fake.NewSimpleClientset()

	_, err := ResolveWorkload(context.Background(), client, "default", "deployment/assente")
	if err == nil {
		t.Fatal("expected error for nonexistent Deployment, got nil")
	}
}
