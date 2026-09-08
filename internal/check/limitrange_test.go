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
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func limitRangeObject(name string, items ...corev1.LimitRangeItem) *corev1.LimitRange {
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       corev1.LimitRangeSpec{Limits: items},
	}
}

func runLimitRangeCheck(t *testing.T, w workload.Workload, objs ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objs...)
	res, err := check.LimitRangeFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  w,
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return res
}

// TestLimitRangeFeasibility_ExplicitLimitViolatesMax_High covers a manifest
// that declares a limit above a namespace's single, deterministic max: a
// guaranteed pod-creation rejection, verified live on kind for this project
// (see the doc comment on LimitRangeFeasibility).
func TestLimitRangeFeasibility_ExplicitLimitViolatesMax_High(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:      "app",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
	})
	lr := limitRangeObject("caps", corev1.LimitRangeItem{
		Type: corev1.LimitTypeContainer,
		Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, lr)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.CheckID != check.LimitRangeFeasibilityCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "cpu") || !strings.Contains(f.Cause, "app") {
		t.Errorf("cause must name the resource and container: %q", f.Cause)
	}
	if !f.Remediation.ContextDependent || len(f.Remediation.Commands) != 1 || !strings.Contains(f.Remediation.Commands[0], "kubectl get limitrange") {
		t.Errorf("remediation must be context-dependent with a read-only limitrange command, got %+v", f.Remediation)
	}
	if f.Resource.Kind != "Pod" || f.Resource.Namespace != testNamespace || f.Resource.Name != "checkout/app" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

// TestLimitRangeFeasibility_ExplicitLimitViolatesMin_High declares only a
// limit (no request) below the namespace minimum: the check must simulate
// the stage-1 requests-from-limits copy itself (the manifest never declares
// a request at all) to find the violation.
func TestLimitRangeFeasibility_ExplicitLimitViolatesMin_High(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name:      "app",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("50Mi")}},
	})
	lr := limitRangeObject("floors", corev1.LimitRangeItem{
		Type: corev1.LimitTypeContainer,
		Min:  corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("100Mi")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, lr)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !strings.Contains(f.Cause, "memory") {
		t.Errorf("cause must name memory: %q", f.Cause)
	}
}

// TestLimitRangeFeasibility_InitContainerViolatesMax_High_RegularContainerClean
// mirrors the same regular/init distinction already established by
// resource-limits: the kubelet (and here, LimitRanger) enforces an init
// container's limits identically to a regular one's.
func TestLimitRangeFeasibility_InitContainerViolatesMax_High_RegularContainerClean(t *testing.T) {
	// "app" declares both request and limit explicitly, deliberately, so
	// this scenario isolates the init container's violation: the regular
	// container must not also produce the unrelated Low finding for a
	// silently defaulted request (covered by its own test).
	d := deploymentWithContainers(corev1.Container{
		Name: "app",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		},
	})
	d.Spec.Template.Spec.InitContainers = []corev1.Container{{
		Name:      "setup",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
	}}
	lr := limitRangeObject("caps", corev1.LimitRangeItem{
		Type: corev1.LimitTypeContainer,
		Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, lr)

	if len(res.Findings) != 1 {
		t.Fatalf("want exactly 1 finding (the init container only), got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !strings.Contains(f.Cause, "init container") || !strings.Contains(f.Cause, "setup") {
		t.Errorf("cause must identify the init container by name: %q", f.Cause)
	}
	if f.Resource.Name != "checkout/setup" {
		t.Errorf("resource ref = %+v, want name checkout/setup", f.Resource)
	}
}

// TestLimitRangeFeasibility_RatioViolated_High covers maxLimitRequestRatio
// with both request and limit declared explicitly in the manifest.
func TestLimitRangeFeasibility_RatioViolated_High(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name: "app",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")},
		},
	})
	lr := limitRangeObject("ratios", corev1.LimitRangeItem{
		Type:                 corev1.LimitTypeContainer,
		MaxLimitRequestRatio: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, lr)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High", f.Severity)
	}
	if !strings.Contains(f.Cause, "ratio") {
		t.Errorf("cause must mention the ratio violation: %q", f.Cause)
	}
}

// TestLimitRangeFeasibility_CrossObjectDeterministic_High replicates the
// exact scenario verified live on kind for this project (see the doc
// comment on LimitRangeFeasibility): LimitRange A supplies only a max
// (backfilling default/defaultRequest to the max value), LimitRange B
// supplies only a higher min (backfilling defaultRequest to the min value,
// with no default entry at all). A container declaring nothing is rejected
// no matter which object the apiserver lists first.
func TestLimitRangeFeasibility_CrossObjectDeterministic_High(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})
	objectA := limitRangeObject("caps", corev1.LimitRangeItem{
		Type:           corev1.LimitTypeContainer,
		Max:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
		Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
		DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
	})
	objectB := limitRangeObject("floors", corev1.LimitRangeItem{
		Type:           corev1.LimitTypeContainer,
		Min:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
		DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, objectA, objectB)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityHigh {
		t.Errorf("severity = %v, want High: every possible default combination must fail here", f.Severity)
	}
	if !strings.Contains(f.Cause, "caps") || !strings.Contains(f.Cause, "floors") {
		t.Errorf("cause must name both contributing LimitRange objects: %q", f.Cause)
	}
}

// TestLimitRangeFeasibility_CompetingDefaults_Medium covers two objects that
// each supply a different explicit default/defaultRequest for the same
// key, where a third constraint (a max on only one of the two objects)
// makes some, but not all, of the possible outcomes violate.
func TestLimitRangeFeasibility_CompetingDefaults_Medium(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})
	objectA := limitRangeObject("a", corev1.LimitRangeItem{
		Type:           corev1.LimitTypeContainer,
		Max:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("150m")},
		Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
	})
	objectB := limitRangeObject("b", corev1.LimitRangeItem{
		Type:           corev1.LimitTypeContainer,
		Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
		DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("300m")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, objectA, objectB)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityMedium {
		t.Fatalf("severity = %v, want Medium: only some default combinations violate the max here", f.Severity)
	}
	if !strings.Contains(f.Cause, "non-deterministic") {
		t.Errorf("cause must state that the outcome is non-deterministic, got %q", f.Cause)
	}
	if !strings.Contains(f.Cause, "a") || !strings.Contains(f.Cause, "b") {
		t.Errorf("cause must name the competing LimitRange objects: %q", f.Cause)
	}
}

// TestLimitRangeFeasibility_SilentDefault_Low covers a container that
// declares nothing at all under a single, healthy LimitRange: no violation,
// but the effective values come entirely from defaulting, worth flagging at
// Low severity so the values are not a silent surprise.
func TestLimitRangeFeasibility_SilentDefault_Low(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})
	lr := limitRangeObject("caps", corev1.LimitRangeItem{
		Type:           corev1.LimitTypeContainer,
		Max:            corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
		Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
		DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, lr)

	if len(res.Findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != model.SeverityLow {
		t.Errorf("severity = %v, want Low", f.Severity)
	}
	if !strings.Contains(f.Cause, "cpu") {
		t.Errorf("cause must name the defaulted resource: %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must be context-dependent")
	}
}

// TestLimitRangeFeasibility_ExplicitWithinBounds_NoFindings covers values
// declared explicitly in the manifest that already satisfy every bound.
func TestLimitRangeFeasibility_ExplicitWithinBounds_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{
		Name: "app",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("150m")},
			Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("180m")},
		},
	})
	lr := limitRangeObject("caps", corev1.LimitRangeItem{
		Type: corev1.LimitTypeContainer,
		Min:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
	})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, lr)

	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings, got %+v", res.Findings)
	}
}

// TestLimitRangeFeasibility_NoLimitRangeObjects_NoFindingsNotSkipped
// verifies the same "clean, not skipped" convention already used by
// quota-headroom when the namespace does not restrict anything at all.
func TestLimitRangeFeasibility_NoLimitRangeObjects_NoFindingsNotSkipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d)

	if res.Skipped {
		t.Fatalf("must not be skipped when no LimitRange exists, got skip reason %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("want 0 findings without any LimitRange in the namespace, got %+v", res.Findings)
	}
}

// TestLimitRangeFeasibility_OnlyPodAndPVCTypeItems_Ignored covers the
// declared v1 scope boundary: Type=Pod and Type=PersistentVolumeClaim items
// are not evaluated at all, even though they are present in the namespace.
func TestLimitRangeFeasibility_OnlyPodAndPVCTypeItems_Ignored(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})
	lr := limitRangeObject("mixed",
		corev1.LimitRangeItem{
			Type: corev1.LimitTypePod,
			Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		},
		corev1.LimitRangeItem{
			Type: corev1.LimitTypePersistentVolumeClaim,
			Max:  corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
	)

	res := runLimitRangeCheck(t, workload.FromDeployment(d), d, lr)

	if len(res.Findings) != 0 {
		t.Fatalf("Pod/PVC-type items are out of scope for v1 and must produce 0 findings, got %+v", res.Findings)
	}
}

func TestLimitRangeFeasibility_ListDenied_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app"})
	client := fake.NewSimpleClientset(d)
	client.PrependReactor("list", "limitranges", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading LimitRanges")
	})

	res, err := check.LimitRangeFeasibility{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed list; it must degrade to Skipped: %v", err)
	}
	if !res.Skipped {
		t.Fatal("want Skipped=true when the LimitRange list is not accessible")
	}
	if res.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}

// TestLimitRangeFeasibility_AllThreeWorkloadKinds_SameFinding verifies no
// workload kind is silently excluded: LimitRange applies to any Pod in the
// namespace regardless of which controller owns it.
func TestLimitRangeFeasibility_AllThreeWorkloadKinds_SameFinding(t *testing.T) {
	container := corev1.Container{
		Name:      "app",
		Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
	}
	lr := limitRangeObject("caps", corev1.LimitRangeItem{
		Type: corev1.LimitTypeContainer,
		Max:  corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("200m")},
	})

	d := deploymentWithContainers(container)
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{container}}},
		},
	}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: testNamespace},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{container}}},
		},
	}

	for _, tc := range []struct {
		name string
		w    workload.Workload
	}{
		{"Deployment", workload.FromDeployment(d)},
		{"StatefulSet", workload.FromStatefulSet(s)},
		{"DaemonSet", workload.FromDaemonSet(ds)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := runLimitRangeCheck(t, tc.w, lr)
			if len(res.Findings) != 1 {
				t.Fatalf("want 1 finding for %s, got %+v", tc.name, res.Findings)
			}
			if res.Findings[0].Severity != model.SeverityHigh {
				t.Errorf("severity = %v, want High for %s", res.Findings[0].Severity, tc.name)
			}
		})
	}
}
