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

package check_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func runQuotaObjectCountCheck(t *testing.T, w workload.Workload, objs ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objs...)

	target := check.Target{
		Namespace: testNamespace,
		Workload:  w,
		Client:    client,
	}

	res, err := check.QuotaObjectCount{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

func statefulSetForQuotaObjectCount(name string) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(3),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "example.com/db:v1"}},
				},
			},
		},
	}
}

func daemonSetForQuotaObjectCount(name string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "example.com/logger:v1"}},
				},
			},
		},
	}
}

// TestQuotaObjectCount_Deployment_Exhausted_High covers the Deployment
// mapping: count/replicasets.apps hard=used means the next pod-template
// change cannot create a new ReplicaSet at all.
func TestQuotaObjectCount_Deployment_Exhausted_High(t *testing.T) {
	one := intstr.FromInt(1)
	d := deployment(4, rollingUpdateStrategyWithSurge(&one))
	quota := resourceQuota("rs-quota",
		corev1.ResourceList{corev1.ResourceName("count/replicasets.apps"): resource.MustParse("2")},
		corev1.ResourceList{corev1.ResourceName("count/replicasets.apps"): resource.MustParse("2")},
	)

	res := runQuotaObjectCountCheck(t, workload.FromDeployment(d), d, quota)

	if res.Skipped {
		t.Fatalf("want Skipped=false, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for an exhausted count/replicasets.apps quota, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if f.CheckID != check.QuotaObjectCountCheckID {
		t.Errorf("checkID = %q, want %q", f.CheckID, check.QuotaObjectCountCheckID)
	}
	if !f.Remediation.ContextDependent {
		t.Errorf("remediation must declare itself context-dependent")
	}
	if f.Remediation.Summary == "" {
		t.Errorf("remediation summary must not be empty")
	}
}

// TestQuotaObjectCount_StatefulSet_Exhausted_High proves the kind ->
// GroupResource mapping actually switches for StatefulSet, not merely a
// duplicate of the Deployment case: the relevant key is
// count/controllerrevisions.apps, distinct from count/replicasets.apps.
func TestQuotaObjectCount_StatefulSet_Exhausted_High(t *testing.T) {
	s := statefulSetForQuotaObjectCount("db")
	quota := resourceQuota("cr-quota",
		corev1.ResourceList{corev1.ResourceName("count/controllerrevisions.apps"): resource.MustParse("3")},
		corev1.ResourceList{corev1.ResourceName("count/controllerrevisions.apps"): resource.MustParse("3")},
	)

	res := runQuotaObjectCountCheck(t, workload.FromStatefulSet(s), s, quota)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for an exhausted count/controllerrevisions.apps quota on a StatefulSet, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// TestQuotaObjectCount_DaemonSet_Exhausted_High proves DaemonSet is NOT
// excluded from this check: unlike several other checks in this project
// that deliberately skip DaemonSet (scheduling-constraints-feasibility,
// hpa-quota-headroom) because of DaemonSet-specific topology/scale
// mechanics, the ControllerRevision quota blockage here is structural and
// applies identically to DaemonSet.
func TestQuotaObjectCount_DaemonSet_Exhausted_High(t *testing.T) {
	ds := daemonSetForQuotaObjectCount("logger")
	quota := resourceQuota("cr-quota",
		corev1.ResourceList{corev1.ResourceName("count/controllerrevisions.apps"): resource.MustParse("3")},
		corev1.ResourceList{corev1.ResourceName("count/controllerrevisions.apps"): resource.MustParse("3")},
	)

	res := runQuotaObjectCountCheck(t, workload.FromDaemonSet(ds), ds, quota)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding for an exhausted count/controllerrevisions.apps quota on a DaemonSet, got %+v", res.Findings)
	}
	if res.Findings[0].Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", res.Findings[0].Severity)
	}
}

// TestQuotaObjectCount_HeadroomOfOne_NoFinding fixes the "no Medium"
// decision: Used == Hard-1 must never produce any finding, only
// Used >= Hard does.
func TestQuotaObjectCount_HeadroomOfOne_NoFinding(t *testing.T) {
	d := deployment(4, rollingUpdateStrategy(nil))
	quota := resourceQuota("rs-quota",
		corev1.ResourceList{corev1.ResourceName("count/replicasets.apps"): resource.MustParse("2")},
		corev1.ResourceList{corev1.ResourceName("count/replicasets.apps"): resource.MustParse("1")},
	)

	res := runQuotaObjectCountCheck(t, workload.FromDeployment(d), d, quota)

	if len(res.Findings) != 0 {
		t.Fatalf("headroom of exactly 1 must not produce any finding, got %+v", res.Findings)
	}
	if res.Skipped {
		t.Fatalf("want Skipped=false, got skip reason %q", res.SkipReason)
	}
}

// TestQuotaObjectCount_KeyAbsent_NoFinding covers a quota that constrains
// only unrelated resources (cpu/memory/pods): the relevant object-count key
// is simply absent from Hard, so this check must stay silent.
func TestQuotaObjectCount_KeyAbsent_NoFinding(t *testing.T) {
	d := deployment(4, rollingUpdateStrategy(nil))
	quota := resourceQuota("cpu-mem-quota",
		corev1.ResourceList{
			corev1.ResourceName("requests.cpu"):    resource.MustParse("2"),
			corev1.ResourceName("requests.memory"): resource.MustParse("2Gi"),
			corev1.ResourcePods:                    resource.MustParse("10"),
		},
		corev1.ResourceList{
			corev1.ResourceName("requests.cpu"):    resource.MustParse("2"),
			corev1.ResourceName("requests.memory"): resource.MustParse("2Gi"),
			corev1.ResourcePods:                    resource.MustParse("10"),
		},
	)

	res := runQuotaObjectCountCheck(t, workload.FromDeployment(d), d, quota)

	if len(res.Findings) != 0 {
		t.Fatalf("a quota that never declares count/replicasets.apps must not produce a finding, got %+v", res.Findings)
	}
}

// TestQuotaObjectCount_WrongKeySyntax_NoFinding covers two invalid spellings
// the real apiserver would never enforce for a Deployment's ReplicaSet:
// missing the "count/" prefix, and missing the ".apps" group suffix. Neither
// is a valid ObjectCountQuotaResourceNameFor(...) output, so this check must
// never match on them, exactly as the apiserver itself would not.
func TestQuotaObjectCount_WrongKeySyntax_NoFinding(t *testing.T) {
	d := deployment(4, rollingUpdateStrategy(nil))
	quotaNoPrefix := resourceQuota("no-prefix",
		corev1.ResourceList{corev1.ResourceName("replicasets.apps"): resource.MustParse("1")},
		corev1.ResourceList{corev1.ResourceName("replicasets.apps"): resource.MustParse("1")},
	)
	quotaNoGroup := resourceQuota("no-group",
		corev1.ResourceList{corev1.ResourceName("count/replicasets"): resource.MustParse("1")},
		corev1.ResourceList{corev1.ResourceName("count/replicasets"): resource.MustParse("1")},
	)

	res := runQuotaObjectCountCheck(t, workload.FromDeployment(d), d, quotaNoPrefix, quotaNoGroup)

	if len(res.Findings) != 0 {
		t.Fatalf("an invalid quota key spelling must never match, got %+v", res.Findings)
	}
}

// TestQuotaObjectCount_NoQuota_EmptyNotSkipped covers the "zero
// ResourceQuota in the namespace" path: an empty Result, never Skipped.
func TestQuotaObjectCount_NoQuota_EmptyNotSkipped(t *testing.T) {
	d := deployment(4, rollingUpdateStrategy(nil))

	res := runQuotaObjectCountCheck(t, workload.FromDeployment(d), d)

	if res.Skipped {
		t.Fatalf("want Skipped=false when there is no ResourceQuota at all, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings when there is no ResourceQuota at all, got %+v", res.Findings)
	}
}

// TestQuotaObjectCount_ListFailed_Skipped covers RBAC degradation, in the
// same style as quota-headroom's own ListFailed test.
func TestQuotaObjectCount_ListFailed_Skipped(t *testing.T) {
	d := deployment(4, rollingUpdateStrategy(nil))
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("list", "resourcequotas", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading ResourceQuotas")
	})

	target := check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	}

	res, err := check.QuotaObjectCount{}.Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatalf("want Skipped=true when the ResourceQuota list is not accessible")
	}
	if res.SkipReason == "" {
		t.Errorf("SkipReason must not be empty")
	}
}

// TestQuotaObjectCount_UsedNotYetSynced_Skipped covers the status-staleness
// trap: the key is declared in Hard but absent from Used entirely (not the
// same as present-and-zero). Treating it as zero would silently assume full
// headroom; this must instead degrade to Skipped, explicitly naming the
// quota.
func TestQuotaObjectCount_UsedNotYetSynced_Skipped(t *testing.T) {
	d := deployment(4, rollingUpdateStrategy(nil))
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "not-synced", Namespace: testNamespace},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{corev1.ResourceName("count/replicasets.apps"): resource.MustParse("2")},
			Used: corev1.ResourceList{},
		},
	}

	res := runQuotaObjectCountCheck(t, workload.FromDeployment(d), d, quota)

	if !res.Skipped {
		t.Fatalf("want Skipped=true when Used has not reported the declared key yet, got %+v", res)
	}
	if res.SkipReason == "" {
		t.Errorf("SkipReason must not be empty")
	}
	if !strings.Contains(res.SkipReason, "not-synced") {
		t.Errorf("SkipReason must name the specific quota, got %q", res.SkipReason)
	}
}

// TestQuotaObjectCount_ScopedQuota_Ignored covers the decision taken from
// reading generic.Matches/objectCountEvaluator.Matches in
// k8s.io/apiserver@v0.36.1/pkg/quota/v1/generic/evaluator.go: a ResourceQuota
// with a non-empty scopeSelector is NEVER applied by the apiserver to an
// object-count evaluation (MatchesNoScopeFunc always returns false, and
// Matches ANDs every scope's match result), so this check must ignore it
// entirely even though its hard/used pair alone looks fully exhausted —
// unlike quota-headroom, which deliberately treats a scoped quota as if it
// still applied (a different evaluator; see the doc comment on
// QuotaObjectCount for why the two checks are not inconsistent).
func TestQuotaObjectCount_ScopedQuota_Ignored(t *testing.T) {
	d := deployment(4, rollingUpdateStrategy(nil))
	quota := resourceQuota("scoped",
		corev1.ResourceList{corev1.ResourceName("count/replicasets.apps"): resource.MustParse("1")},
		corev1.ResourceList{corev1.ResourceName("count/replicasets.apps"): resource.MustParse("1")},
	)
	quota.Spec.ScopeSelector = &corev1.ScopeSelector{
		MatchExpressions: []corev1.ScopedResourceSelectorRequirement{{
			ScopeName: corev1.ResourceQuotaScopeBestEffort,
			Operator:  corev1.ScopeSelectorOpExists,
		}},
	}

	res := runQuotaObjectCountCheck(t, workload.FromDeployment(d), d, quota)

	if len(res.Findings) != 0 {
		t.Fatalf("a scoped ResourceQuota must never be evaluated for object count, got %+v", res.Findings)
	}
	if res.Skipped {
		t.Fatalf("want Skipped=false, got skip reason %q", res.SkipReason)
	}
}
