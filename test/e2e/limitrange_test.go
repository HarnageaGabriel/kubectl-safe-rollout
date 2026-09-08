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

// TestCheckE2E_LimitRangeFeasibility_MaxOnlyBackfillsDefault_NoRejection
// reproduces the correction this check's design made to its own original
// plan: a LimitRange declaring only `max` does NOT leave "no default" for a
// container that omits a limit. The API server backfills default (and
// defaultRequest) to max on the LimitRange object itself, before any pod is
// ever evaluated. A container declaring nothing must therefore roll out
// clean, with the real pod carrying limits/requests equal to max.
func TestCheckE2E_LimitRangeFeasibility_MaxOnlyBackfillsDefault_NoRejection(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	lr := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "lr-max-only", Namespace: ns},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type: corev1.LimitTypeContainer,
				Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
			}},
		},
	}
	if _, err := admin.CoreV1().LimitRanges(ns).Create(ctx, lr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating LimitRange: %v", err)
	}

	// Confirm the backfill directly on the live object before trusting the
	// rest of the scenario on it.
	live, err := admin.CoreV1().LimitRanges(ns).Get(ctx, lr.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading back LimitRange: %v", err)
	}
	item := live.Spec.Limits[0]
	if item.Default == nil || item.Default.Cpu().String() != "200m" {
		t.Fatalf("want default.cpu backfilled to 200m from max, got %+v", item.Default)
	}
	if item.DefaultRequest == nil || item.DefaultRequest.Cpu().String() != "200m" {
		t.Fatalf("want defaultRequest.cpu backfilled to 200m from max, got %+v", item.DefaultRequest)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, admin, ns, "victim", 1, podSpec, nil)
	watchAndExpectSuccess(t, admin, ns, d, 60*time.Second)

	pods, err := admin.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("want exactly 1 pod, got %d", len(pods.Items))
	}
	got := pods.Items[0].Spec.Containers[0].Resources
	if got.Limits.Cpu().String() != "200m" || got.Requests.Cpu().String() != "200m" {
		t.Errorf("want the real pod's cpu limit and request both backfilled to 200m, got limits=%v requests=%v", got.Limits, got.Requests)
	}

	live2, err := admin.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live2), Client: admin}
	res, err := check.LimitRangeFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result (LimitRange list is unrestricted here), got Skipped: %s", res.SkipReason)
	}
	for _, f := range res.Findings {
		if f.Severity == model.SeverityHigh || f.Severity == model.SeverityMedium {
			t.Errorf("want no High/Medium finding for a container backfilled cleanly within max, got %+v", f)
		}
	}
}

// TestCheckE2E_LimitRangeFeasibility_CrossObjectContradiction_HighAndRealRejection
// is the load-bearing verification for this check's cross-object claim: two
// LimitRange objects, neither alone a problem, together permanently reject
// pod creation. LimitRange A supplies only max.cpu (backfilling its own
// default), LimitRange B supplies only min.cpu higher than that default —
// additive validation across both objects blocks every pod attempt,
// regardless of which object the API server lists first.
func TestCheckE2E_LimitRangeFeasibility_CrossObjectContradiction_HighAndRealRejection(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	lrMax := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "lr-max", Namespace: ns},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type: corev1.LimitTypeContainer,
				Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
			}},
		},
	}
	lrMin := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "lr-min", Namespace: ns},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type: corev1.LimitTypeContainer,
				Min:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
			}},
		},
	}
	if _, err := admin.CoreV1().LimitRanges(ns).Create(ctx, lrMax, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating LimitRange %q: %v", lrMax.Name, err)
	}
	if _, err := admin.CoreV1().LimitRanges(ns).Create(ctx, lrMin, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating LimitRange %q: %v", lrMin.Name, err)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
		}},
	}
	d := deployWorkload(t, admin, ns, "victim", 1, podSpec, nil)

	// No pod must ever come up: the two objects' constraints are mutually
	// contradictory for any container declaring nothing, regardless of
	// which object's default wins the mutating-admission race.
	time.Sleep(5 * time.Second)
	pods, err := admin.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=victim"})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Errorf("want zero pods created (the two LimitRange objects contradict each other for any default the container could receive), got %d", len(pods.Items))
	}

	events, err := admin.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing events: %v", err)
	}
	foundRejection := false
	for _, ev := range events.Items {
		if ev.Reason == "FailedCreate" && (strings.Contains(ev.Message, "minimum cpu usage per Container") || strings.Contains(ev.Message, "maximum cpu usage per Container")) {
			foundRejection = true
			break
		}
	}
	if !foundRejection {
		t.Errorf("want a FailedCreate event citing the LimitRange min/max violation, got events: %+v", events.Items)
	}

	live, err := admin.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: admin}
	res, err := check.LimitRangeFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped {
		t.Fatalf("want an evaluated result, got Skipped: %s", res.SkipReason)
	}
	var high *model.Finding
	for i := range res.Findings {
		if res.Findings[i].Severity == model.SeverityHigh {
			high = &res.Findings[i]
			break
		}
	}
	if high == nil {
		t.Fatalf("want exactly one High finding for the cross-object contradiction, got %+v", res.Findings)
	}
}

// TestCheckE2E_LimitRangeFeasibility_ExplicitWithinBounds_NoFinding proves
// the positive path: a container whose declared requests/limits already
// satisfy every LimitRange constraint must produce no finding at all.
func TestCheckE2E_LimitRangeFeasibility_ExplicitWithinBounds_NoFinding(t *testing.T) {
	admin := newE2EClient(t)
	ns := newE2ENamespace(t, admin)
	ctx := context.Background()

	lr := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "lr-ok", Namespace: ns},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{{
				Type: corev1.LimitTypeContainer,
				Min:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
				Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
			}},
		},
	}
	if _, err := admin.CoreV1().LimitRanges(ns).Create(ctx, lr, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating LimitRange: %v", err)
	}

	podSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:    "app",
			Image:   "busybox:1.36",
			Command: []string{"sleep", "3600"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
			},
		}},
	}
	d := deployWorkload(t, admin, ns, "victim", 1, podSpec, nil)
	watchAndExpectSuccess(t, admin, ns, d, 60*time.Second)

	live, err := admin.AppsV1().Deployments(ns).Get(ctx, d.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("reading Deployment %s/%s: %v", ns, d.Name, err)
	}
	target := check.Target{Namespace: ns, Workload: workload.FromDeployment(live), Client: admin}
	res, err := check.LimitRangeFeasibility{}.Run(ctx, target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if res.Skipped || len(res.Findings) != 0 {
		t.Fatalf("a container fully within bounds with explicit requests/limits must produce no finding, got %+v", res)
	}
}
