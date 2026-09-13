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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// The default kind cluster is single-node with a fixed, small allocatable
// (6 cpu / ~7.75Gi memory on this project's pinned image): requesting
// dramatically more than that is a fact that holds on any real machine this
// suite would run on, not a coincidence of this particular node.

// TestCheckE2E_NodeCapacityFeasibility_CPUExceedsAllocatable_High is the
// load-bearing verification: a request far exceeding the node's allocatable
// cpu leaves the pod Pending forever with the scheduler's own Unresolvable
// signature, and the check reports it as High.
func TestCheckE2E_NodeCapacityFeasibility_CPUExceedsAllocatable_High(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")},
			},
		}},
	}
	d := deployWorkload(t, client, ns, "huge-cpu", 1, podSpec, nil)

	deadline := time.Now().Add(30 * time.Second)
	sawInsufficientCPU := false
	for time.Now().Before(deadline) {
		pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=huge-cpu"})
		if err != nil {
			t.Fatalf("listing pods: %v", err)
		}
		for _, p := range pods.Items {
			if p.Status.Phase != corev1.PodPending {
				t.Fatalf("want the pod to stay Pending (cpu request exceeds any node's allocatable), got phase %q", p.Status.Phase)
			}
		}
		events, err := client.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Fatalf("listing events: %v", err)
		}
		for _, ev := range events.Items {
			if ev.Reason == "FailedScheduling" && strings.Contains(ev.Message, "Insufficient cpu") {
				sawInsufficientCPU = true
				break
			}
		}
		if sawInsufficientCPU {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !sawInsufficientCPU {
		t.Errorf("want a FailedScheduling event citing Insufficient cpu within the deadline")
	}

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.NodeCapacityFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (Node list is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// TestCheckE2E_NodeCapacityFeasibility_LimitsOnlyMemory_High confirms the
// stage-1 limits-to-requests copy live: a container declaring only
// limits.memory far above the node's allocatable memory produces a real
// Pod whose requests.memory equals that limit, stays Pending, and the
// check reports High from the template alone (which never carries the
// copy).
func TestCheckE2E_NodeCapacityFeasibility_LimitsOnlyMemory_High(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Gi")},
			},
		}},
	}
	d := deployWorkload(t, client, ns, "huge-mem-limits", 1, podSpec, nil)

	time.Sleep(6 * time.Second)
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=huge-mem-limits"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("want exactly 1 pod, got %d", len(pods.Items))
	}
	if pods.Items[0].Status.Phase != corev1.PodPending {
		t.Fatalf("want the pod Pending, got phase %q", pods.Items[0].Status.Phase)
	}
	gotReq := pods.Items[0].Spec.Containers[0].Resources.Requests[corev1.ResourceMemory]
	wantReq := resource.MustParse("64Gi")
	if gotReq.Cmp(wantReq) != 0 {
		t.Errorf("want the real pod's requests.memory copied from limits (64Gi), got %s", gotReq.String())
	}

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.NodeCapacityFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding from the template alone (which never carries the stage-1 copy), got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// TestCheckE2E_NodeCapacityFeasibility_OversizedInitContainer_High proves
// init containers are included in the aggregate: a tiny regular container
// paired with a hugely oversized init container must still be caught.
func TestCheckE2E_NodeCapacityFeasibility_OversizedInitContainer_High(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		InitContainers: []corev1.Container{{
			Name:    "init",
			Image:   "busybox:1.36",
			Command: []string{"sh", "-c", "true"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")},
			},
		}},
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			},
		}},
	}
	d := deployWorkload(t, client, ns, "huge-init", 1, podSpec, nil)

	time.Sleep(6 * time.Second)
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=huge-init"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 || pods.Items[0].Status.Phase != corev1.PodPending {
		t.Fatalf("want exactly 1 Pending pod (oversized init container), got %+v", pods.Items)
	}

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.NodeCapacityFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding (the init container's request must be included), got %+v", res)
	}
}

// TestCheckE2E_NodeCapacityFeasibility_ComfortableRequest_NoFinding proves
// the positive path: a request well within the node's allocatable rolls
// out and produces no finding.
func TestCheckE2E_NodeCapacityFeasibility_ComfortableRequest_NoFinding(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
			},
		}},
	}
	d := deployWorkload(t, client, ns, "fits", 1, podSpec, nil)
	watchAndExpectSuccess(t, client, ns, d, 60*time.Second)

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.NodeCapacityFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a comfortably-sized request must produce no finding, got %+v", res)
	}
}

// TestCheckE2E_NodeCapacityFeasibility_DaemonSetOversized_High proves
// DaemonSet is in scope: an oversized DaemonSet pod template must also be
// flagged.
func TestCheckE2E_NodeCapacityFeasibility_DaemonSetOversized_High(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")},
			},
		}},
	}
	ds := deployDaemonSet(t, client, ns, "huge-ds", podSpec, nil)

	live, err := client.AppsV1().DaemonSets(ns).Get(ctx, ds.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading DaemonSet %s/%s: %v", ns, ds.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDaemonSet(live), Client: client}
	res, err := check.NodeCapacityFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding for an oversized DaemonSet, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}
