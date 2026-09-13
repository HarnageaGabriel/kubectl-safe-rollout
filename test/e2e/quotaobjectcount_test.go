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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// pollUntilQuotaUsed waits for the ResourceQuota controller to report the
// given used count for key, rather than assuming it is already correct the
// instant after the workload is created.
func pollUntilQuotaUsed(ctx context.Context, t *testing.T, client kubernetes.Interface, ns, quotaName, key, want string) error {
	t.Helper()
	// 90s, not 30s: this quota-used convergence is the slowest wait in the
	// whole suite to observe on this project's WSL2 VM, both under
	// back-to-back load (contention) and as the very first API operation
	// against a freshly created kind cluster (the resourcequota
	// controller's own startup/informer-sync latency) — never in a cluster
	// that has already been running for a while and is otherwise idle. Same
	// class of environment latency already addressed by widening three
	// StatefulSet e2e timeouts elsewhere in this suite.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		q, err := client.CoreV1().ResourceQuotas(ns).Get(ctx, quotaName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if v, ok := q.Status.Used[corev1.ResourceName(key)]; ok && v.String() == want {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return context.DeadlineExceeded
}

// pollUntilDeploymentConditionReason waits for the named Deployment's
// Progressing condition to report the given reason.
func pollUntilDeploymentConditionReason(ctx context.Context, t *testing.T, client kubernetes.Interface, ns, name, reason string) error {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		d, err := client.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing && c.Reason == reason {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return context.DeadlineExceeded
}

// TestCheckE2E_QuotaObjectCount_Deployment_ReplicaSetQuotaExhausted_High is
// the load-bearing verification: a namespace whose count/replicasets.apps
// quota is already fully consumed by an existing Deployment's ReplicaSet
// blocks the next pod-template change from ever creating its replacement.
func TestCheckE2E_QuotaObjectCount_Deployment_ReplicaSetQuotaExhausted_High(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "rs-quota", Namespace: ns},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"count/replicasets.apps": resource.MustParse("1")}},
	}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ResourceQuota: %v", err)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, client, ns, "victim", 1, podSpec, nil)
	watchAndExpectSuccess(t, client, ns, d, 60*time.Second)

	if err := pollUntilQuotaUsed(ctx, t, client, ns, "rs-quota", "count/replicasets.apps", "1"); err != nil {
		t.Fatalf("waiting for ResourceQuota %s/rs-quota to report used=1: %v", ns, err)
	}

	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	live.Spec.Template.Spec.Containers[0].Image = "busybox:1.37"
	if _, err := client.AppsV1().Deployments(ns).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating Deployment image: %v", err)
	}

	// The new ReplicaSet must never be created: confirm the real
	// FailedCreate-shaped rejection independently of the check.
	if err := pollUntilDeploymentConditionReason(ctx, t, client, ns, d.Name, "ReplicaSetCreateError"); err != nil {
		t.Fatalf("waiting for the Deployment's Progressing condition to report ReplicaSetCreateError: %v", err)
	}
	rsList, err := client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing ReplicaSets: %v", err)
	}
	if len(rsList.Items) != 1 {
		t.Errorf("want exactly 1 ReplicaSet (the new one must never be created), got %d", len(rsList.Items))
	}

	live, err = client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: client}
	res, err := check.QuotaObjectCount{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (ResourceQuota list is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// TestCheckE2E_QuotaObjectCount_Deployment_RollbackToExistingTemplate_NoBlock
// is the rollback control: with the same quota still fully exhausted, a
// change that keeps the pod template matching an already-existing
// ReplicaSet must succeed, because the controller reuses that ReplicaSet
// instead of creating a new one. This is why the check's finding never
// claims "every rollout is blocked."
func TestCheckE2E_QuotaObjectCount_Deployment_RollbackToExistingTemplate_NoBlock(t *testing.T) {
	client := newE2EClient(t)
	ns := newE2ENamespace(t, client)
	ctx := context.Background()

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "rs-quota", Namespace: ns},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"count/replicasets.apps": resource.MustParse("1")}},
	}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ResourceQuota: %v", err)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, client, ns, "victim", 1, podSpec, nil)
	watchAndExpectSuccess(t, client, ns, d, 60*time.Second)
	if err := pollUntilQuotaUsed(ctx, t, client, ns, "rs-quota", "count/replicasets.apps", "1"); err != nil {
		t.Fatalf("waiting for ResourceQuota to report used=1: %v", err)
	}

	// Touch only the Deployment's own metadata, leaving the pod template
	// (and therefore the pod-template-hash the existing ReplicaSet already
	// matches) untouched: this must succeed despite the quota being full.
	live, err := client.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations["safe-rollout-e2e/touch"] = "1"
	if _, err := client.AppsV1().Deployments(ns).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating Deployment annotation: %v", err)
	}
	// 60s for the same resource-contention reason documented on
	// pollUntilQuotaUsed above: this scenario is consistently the slowest
	// under back-to-back load on this project's WSL2 VM, never in isolation.
	watchAndExpectSuccess(t, client, ns, d, 60*time.Second)

	rsList, err := client.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing ReplicaSets: %v", err)
	}
	if len(rsList.Items) != 1 {
		t.Errorf("want still exactly 1 ReplicaSet (reused, not recreated), got %d", len(rsList.Items))
	}
}

// TestCheckE2E_QuotaObjectCount_StatefulSet_ControllerRevisionQuotaExhausted_High
// confirms the StatefulSet case, which is more invisible than Deployment's:
// no event is ever emitted, the update simply never registers.
func TestCheckE2E_QuotaObjectCount_StatefulSet_ControllerRevisionQuotaExhausted_High(t *testing.T) {
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
	sts := deployStatefulSet(t, client, ns, "sts-victim", 1, podSpec, nil)
	time.Sleep(8 * time.Second)

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "cr-quota", Namespace: ns},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"count/controllerrevisions.apps": resource.MustParse("1")}},
	}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ResourceQuota: %v", err)
	}
	if err := pollUntilQuotaUsed(ctx, t, client, ns, "cr-quota", "count/controllerrevisions.apps", "1"); err != nil {
		t.Fatalf("waiting for ResourceQuota to report used=1: %v", err)
	}

	live, err := client.AppsV1().StatefulSets(ns).Get(ctx, sts.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading StatefulSet %s/%s: %v", ns, sts.Name, err)
	}
	currentRevision := live.Status.CurrentRevision
	live.Spec.Template.Spec.Containers[0].Image = "busybox:1.37"
	if _, err := client.AppsV1().StatefulSets(ns).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating StatefulSet image: %v", err)
	}

	// The update must never register: no new revision, old image stays.
	time.Sleep(8 * time.Second)
	live, err = client.AppsV1().StatefulSets(ns).Get(ctx, sts.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading StatefulSet %s/%s: %v", ns, sts.Name, err)
	}
	if live.Status.CurrentRevision != currentRevision || live.Status.UpdateRevision != currentRevision {
		t.Errorf("want the StatefulSet's revision unchanged (update must never register), got current=%s update=%s (was %s)", live.Status.CurrentRevision, live.Status.UpdateRevision, currentRevision)
	}
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=sts-victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 || pods.Items[0].Spec.Containers[0].Image != "busybox:1.36" {
		t.Errorf("want the pod still running the old image, got %+v", pods.Items)
	}

	target := check.Target{Namespace: ns, Workload: workload.FromStatefulSet(live), Client: client}
	res, err := check.QuotaObjectCount{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// TestCheckE2E_QuotaObjectCount_DaemonSet_ControllerRevisionQuotaExhausted_High
// confirms DaemonSet behaves identically to StatefulSet: silent, total
// block, no event.
func TestCheckE2E_QuotaObjectCount_DaemonSet_ControllerRevisionQuotaExhausted_High(t *testing.T) {
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
	ds := deployDaemonSet(t, client, ns, "ds-victim", podSpec, nil)
	time.Sleep(6 * time.Second)

	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "cr-quota", Namespace: ns},
		Spec:       corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{"count/controllerrevisions.apps": resource.MustParse("1")}},
	}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating ResourceQuota: %v", err)
	}
	if err := pollUntilQuotaUsed(ctx, t, client, ns, "cr-quota", "count/controllerrevisions.apps", "1"); err != nil {
		t.Fatalf("waiting for ResourceQuota to report used=1: %v", err)
	}

	live, err := client.AppsV1().DaemonSets(ns).Get(ctx, ds.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading DaemonSet %s/%s: %v", ns, ds.Name, err)
	}
	live.Spec.Template.Spec.Containers[0].Image = "busybox:1.37"
	if _, err := client.AppsV1().DaemonSets(ns).Update(ctx, live, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("updating DaemonSet image: %v", err)
	}

	time.Sleep(8 * time.Second)
	revs, err := client.AppsV1().ControllerRevisions(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing ControllerRevisions: %v", err)
	}
	if len(revs.Items) != 1 {
		t.Errorf("want still exactly 1 ControllerRevision (the update must never register), got %d", len(revs.Items))
	}
	pods, err := client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=ds-victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 || pods.Items[0].Spec.Containers[0].Image != "busybox:1.36" {
		t.Errorf("want the pod still running the old image, got %+v", pods.Items)
	}

	target := check.Target{Namespace: ns, Workload: workload.FromDaemonSet(live), Client: client}
	res, err := check.QuotaObjectCount{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding, got %+v", res)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}
