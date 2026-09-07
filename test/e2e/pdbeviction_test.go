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
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// pollUntilDisruptionsAllowed waits for the real disruption controller to
// converge status.disruptionsAllowed on a PodDisruptionBudget to want,
// rather than assuming it is already correct the instant after creation.
func pollUntilDisruptionsAllowed(ctx context.Context, t *testing.T, client kubernetes.Interface, namespace, name string, want int32) error {
	t.Helper()
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 30*time.Second, true, func(pollCtx context.Context) (bool, error) {
		live, err := client.PolicyV1().PodDisruptionBudgets(namespace).Get(pollCtx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return live.Status.DisruptionsAllowed == want, nil
	})
}

// TestCheckE2E_PDBEvictionBlocked_TwoMatchingPDBs_HighAndRealEvictionRejected
// is the load-bearing verification for pdb-eviction-blocked's Fact A. Its
// doc comment (internal/check/pdbeviction.go) claims, from reading the
// vendored eviction handler source rather than from observed behavior, that
// the API server rejects EVERY eviction of a pod matched by two or more
// PodDisruptionBudgets with a fixed HTTP 500 and an exact message,
// regardless of how generous either budget is. This scenario calls the
// real eviction subresource against a live apiserver to confirm that
// claim, then calls the check directly and confirms it reports the same
// fact as a High finding naming both PodDisruptionBudgets.
func TestCheckE2E_PDBEvictionBlocked_TwoMatchingPDBs_HighAndRealEvictionRejected(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, client, ns, "victim", 2, podSpec, nil)

	watchAndExpectSuccess(t, client, ns, d, 90*time.Second)

	minAvailable := intstr.FromInt32(1)
	for _, name := range []string{"pdb-a", "pdb-b"} {
		pdb := &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: policyv1.PodDisruptionBudgetSpec{
				MinAvailable: &minAvailable,
				Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "victim"}},
			},
		}
		if _, err := client.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, pdb, metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating PodDisruptionBudget %q: %v", name, err)
		}
	}

	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) == 0 {
		t.Fatalf("want at least one pod for %s/%s, got none", ns, d.Name)
	}
	podName := pods.Items[0].Name

	evictErr := client.PolicyV1().Evictions(ns).Evict(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns},
	})
	if evictErr == nil {
		t.Fatalf("want the real eviction subresource to reject a pod matched by two PodDisruptionBudgets, got no error")
	}
	if !strings.Contains(evictErr.Error(), "more than one PodDisruptionBudget") {
		t.Errorf("eviction error = %q, want it to contain the documented \"more than one PodDisruptionBudget\" message", evictErr.Error())
	}

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.PDBEvictionBlocked{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (PodDisruptionBudget list is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	for _, name := range []string{"pdb-a", "pdb-b"} {
		found := false
		for _, ev := range f.Evidence {
			if strings.Contains(ev, name) {
				found = true
			}
		}
		if !found && !strings.Contains(f.Cause, name) {
			t.Errorf("finding must name PodDisruptionBudget %q somewhere in Cause/Evidence, got Cause=%q Evidence=%v", name, f.Cause, f.Evidence)
		}
	}
}

// TestCheckE2E_PDBEvictionBlocked_SinglePDBHealthy_NoFindingRealCluster proves
// the negative path against a real controller: a single, generously-budgeted
// PodDisruptionBudget must never produce a finding, and a real eviction
// against it must succeed.
func TestCheckE2E_PDBEvictionBlocked_SinglePDBHealthy_NoFindingRealCluster(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, client, ns, "healthy", 2, podSpec, nil)

	watchAndExpectSuccess(t, client, ns, d, 90*time.Second)

	minAvailable := intstr.FromInt32(1)
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "pdb-healthy", Namespace: ns},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "healthy"}},
		},
	}
	if _, err := client.PolicyV1().PodDisruptionBudgets(ns).Create(ctx, pdb, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating PodDisruptionBudget: %v", err)
	}

	// disruptionsAllowed is written asynchronously by the disruption
	// controller: poll instead of assuming it is already correct.
	if err := pollUntilDisruptionsAllowed(ctx, t, client, ns, pdb.Name, 1); err != nil {
		t.Fatalf("waiting for PodDisruptionBudget %s/%s to report disruptionsAllowed=1: %v", ns, pdb.Name, err)
	}

	// Check the steady state BEFORE evicting anything: an eviction below
	// perturbs currentHealthy immediately (the disruption controller reacts
	// faster than the Deployment controller's own status update), which
	// would otherwise race this assertion against a real, but here
	// irrelevant, transient dip through Fact B's own live condition.
	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.PDBEvictionBlocked{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a healthy single-PDB configuration must produce no finding, got %+v", res)
	}

	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=healthy"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) == 0 {
		t.Fatalf("want at least one pod for %s/%s, got none", ns, d.Name)
	}
	if err := client.PolicyV1().Evictions(ns).Evict(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Name: pods.Items[0].Name, Namespace: ns},
	}); err != nil {
		t.Errorf("want a healthy single-PDB eviction to succeed, got: %v", err)
	}
}
