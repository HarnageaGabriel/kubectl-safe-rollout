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

//go:build e2e

package e2e_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// These two scenarios are the only scheduling-constraints-feasibility cases
// confirmable against a default single-node kind cluster: a nodeSelector
// that matches zero real nodes (a fact that holds on any cluster topology),
// and the ordinary no-constraints short-circuit against a real clientset
// rather than only client-go/kubernetes/fake.
//
// Deliberately NOT covered here: minDomains shortfall and anti-affinity
// infeasible-across-nodes, both of which need multiple real topology
// domains. kind's default cluster is single-node, so neither scenario is
// confirmable without a custom multi-node kind config, out of scope for
// this file. That gap is already covered by internal/check/scheduling_test.go
// (fake clientset, multiple synthetic Node objects) and by this session's
// manual kind verification recorded in the commit message of 25a6042.

// TestCheckE2E_SchedulingConstraints_ZeroCandidateNodes verifies the
// zero-candidate-nodes finding against a real API server: a nodeSelector
// that cannot match any real node in the cluster.
func TestCheckE2E_SchedulingConstraints_ZeroCandidateNodes(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		NodeSelector: map[string]string{"nonexistent-label": "true"},
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
		TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
			MaxSkew:           1,
			TopologyKey:       "topology.kubernetes.io/zone",
			WhenUnsatisfiable: corev1.DoNotSchedule,
			LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "checkout-nozones"}},
		}},
	}
	d := deployWorkload(t, admin, ns, "checkout-nozones", 2, podSpec, nil)

	target := check.Target{
		Namespace: ns,
		Workload:  workload.FromDeployment(d),
		Client:    admin,
	}
	res, err := check.SchedulingConstraintsFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (List Nodes is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.Resource.Kind != workload.FromDeployment(d).Kind() {
		t.Errorf("Resource.Kind = %q, want %q", f.Resource.Kind, workload.FromDeployment(d).Kind())
	}
}

// TestCheckE2E_SchedulingConstraints_NoConstraints_NoFindings proves the
// check's short-circuit does not accidentally fire against a real cluster:
// an ordinary Deployment with neither topologySpreadConstraints nor
// anti-affinity must produce zero findings.
func TestCheckE2E_SchedulingConstraints_NoConstraints_NoFindings(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, admin, ns, "checkout-plain", 2, podSpec, nil)

	target := check.Target{
		Namespace: ns,
		Workload:  workload.FromDeployment(d),
		Client:    admin,
	}
	res, err := check.SchedulingConstraintsFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("an ordinary workload with no scheduling constraints must produce zero findings, got %+v", res)
	}
}
