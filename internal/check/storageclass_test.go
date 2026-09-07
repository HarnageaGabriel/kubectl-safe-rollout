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
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func statefulSetWithVolumeClaimTemplates(name string, templates ...corev1.PersistentVolumeClaim) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "example.com/db:v1"}},
				},
			},
			VolumeClaimTemplates: templates,
		},
	}
}

func storageClassNamePtr(name string) *string { return &name }

func runStorageClassExistsCheck(t *testing.T, w workload.Workload, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.StorageClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  w,
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

// 1. A Deployment-backed Target must produce zero findings with zero API
// calls: Deployment has no volumeClaimTemplates field at all, so
// Workload.VolumeClaimTemplates() already returns nil unconditionally.
func TestStorageClassExists_DeploymentBacked_NoFindingsNoAPICalls(t *testing.T) {
	d := deploymentWithSelectorContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	client := fake.NewSimpleClientset()

	var storageClassGot bool
	client.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		storageClassGot = true
		return false, nil, nil
	})

	result, err := check.StorageClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("Deployment has no volumeClaimTemplates: want empty result, got %+v", result)
	}
	if storageClassGot {
		t.Error("StorageClass get must not be attempted for a Deployment-backed target")
	}
}

// 2. A StatefulSet with no volumeClaimTemplates must also produce zero
// findings with zero API calls.
func TestStorageClassExists_NoVolumeClaimTemplates_NoFindingsNoAPICalls(t *testing.T) {
	s := statefulSetWithVolumeClaimTemplates("db")
	client := fake.NewSimpleClientset()

	var storageClassGot bool
	client.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		storageClassGot = true
		return false, nil, nil
	})

	result, err := check.StorageClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromStatefulSet(s),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no volumeClaimTemplates: want empty result, got %+v", result)
	}
	if storageClassGot {
		t.Error("StorageClass get must not be attempted when there are no volumeClaimTemplates")
	}
}

// A DaemonSet-backed Target must also produce zero findings with zero API
// calls: DaemonSetSpec has no volumeClaimTemplates field at all, so
// Workload.VolumeClaimTemplates() already returns nil unconditionally, the
// same hard fact already true for Deployment above.
func TestStorageClassExists_DaemonSetBacked_NoFindingsNoAPICalls(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: testNamespace},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
			},
		},
	}
	client := fake.NewSimpleClientset()

	var storageClassGot bool
	client.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		storageClassGot = true
		return false, nil, nil
	})

	result, err := check.StorageClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDaemonSet(ds),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("DaemonSet has no volumeClaimTemplates: want empty result, got %+v", result)
	}
	if storageClassGot {
		t.Error("StorageClass get must not be attempted for a DaemonSet-backed target")
	}
}

// 3. A nil storageClassName means "use the cluster's default StorageClass":
// a legitimate configuration, not flagged.
func TestStorageClassExists_NilStorageClassName_NoFindings(t *testing.T) {
	tmpl := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: nil},
	}
	s := statefulSetWithVolumeClaimTemplates("db", tmpl)

	result := runStorageClassExistsCheck(t, workload.FromStatefulSet(s))
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("nil storageClassName defers to the cluster default: want empty result, got %+v", result)
	}
}

// 4. An explicit empty-string storageClassName means "no storage class, do
// not dynamically provision": also a legitimate, deliberate configuration,
// not flagged.
func TestStorageClassExists_EmptyStringStorageClassName_NoFindings(t *testing.T) {
	tmpl := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("")},
	}
	s := statefulSetWithVolumeClaimTemplates("db", tmpl)

	result := runStorageClassExistsCheck(t, workload.FromStatefulSet(s))
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("explicit empty storageClassName means pre-provisioned PV: want empty result, got %+v", result)
	}
}

// 5. A volumeClaimTemplate naming a real, existing StorageClass: no finding.
func TestStorageClassExists_StorageClassExists_NoFindings(t *testing.T) {
	tmpl := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("standard")},
	}
	s := statefulSetWithVolumeClaimTemplates("db", tmpl)
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard"}}

	result := runStorageClassExistsCheck(t, workload.FromStatefulSet(s), sc)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("StorageClass exists: want empty result, got %+v", result)
	}
}

// 6. A volumeClaimTemplate naming a nonexistent StorageClass: exactly one
// High finding naming the template and the missing class.
func TestStorageClassExists_StorageClassMissing_HighFinding(t *testing.T) {
	tmpl := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("does-not-exist")},
	}
	s := statefulSetWithVolumeClaimTemplates("db", tmpl)

	result := runStorageClassExistsCheck(t, workload.FromStatefulSet(s))
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing StorageClass, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.StorageClassExistsCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "StatefulSet/db") || !strings.Contains(f.Cause, "data") || !strings.Contains(f.Cause, "does-not-exist") {
		t.Errorf("cause must name the workload, the volumeClaimTemplate, and the missing StorageClass, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent: creating the StorageClass vs. fixing a typo are different actions this tool cannot choose between")
	}
	if len(f.Remediation.Commands) == 0 {
		t.Error("remediation must include a read-only inspection command")
	}
	for _, cmd := range f.Remediation.Commands {
		if strings.Contains(cmd, "delete") || strings.Contains(cmd, "apply") || strings.Contains(cmd, "create") {
			t.Errorf("remediation command must be read-only, got %q", cmd)
		}
	}
	if f.Resource.Kind != "StorageClass" || f.Resource.Name != "does-not-exist" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
	if f.Resource.Namespace != "" {
		t.Errorf("StorageClass is cluster-scoped: resource ref must not carry a namespace, got %+v", f.Resource)
	}
}

// 7. Two volumeClaimTemplates naming the SAME nonexistent StorageClass:
// the Get is memoized (issued once), but each template is its own
// independent PVC-creation family, so each gets its own finding — two
// findings, not one deduplicated finding, unlike config-references-exist's
// per-object dedup (which is appropriate there because multiple refs are
// just different ways of reading the *same* object, not two independent
// resources).
func TestStorageClassExists_TwoTemplatesSameMissingClass_MemoizedGetTwoFindings(t *testing.T) {
	data := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("does-not-exist")},
	}
	logs := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "logs"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("does-not-exist")},
	}
	s := statefulSetWithVolumeClaimTemplates("db", data, logs)
	client := fake.NewSimpleClientset()

	var getCount int
	client.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getCount++
		return false, nil, nil
	})

	result, err := check.StorageClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromStatefulSet(s),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	if result.Skipped || len(result.Findings) != 2 {
		t.Fatalf("want two findings, one per template, got %+v", result)
	}
	if getCount != 1 {
		t.Errorf("want exactly one Get call to the memoized StorageClass, got %d", getCount)
	}
	var sawData, sawLogs bool
	for _, f := range result.Findings {
		if f.Cause == "" {
			t.Error("finding must have a non-empty cause")
		}
		if strings.Contains(f.Evidence[0], "volumeClaimTemplate=data") {
			sawData = true
		}
		if strings.Contains(f.Evidence[0], "volumeClaimTemplate=logs") {
			sawLogs = true
		}
	}
	if !sawData || !sawLogs {
		t.Errorf("want a finding for both templates, got %+v", result.Findings)
	}
}

// 8. Two volumeClaimTemplates, one naming a real class and one naming a
// missing class: exactly one finding, for the right template.
func TestStorageClassExists_OneGoodOneMissing_OnlyBadOneReported(t *testing.T) {
	good := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("standard")},
	}
	bad := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "logs"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("does-not-exist")},
	}
	s := statefulSetWithVolumeClaimTemplates("db", good, bad)
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard"}}

	result := runStorageClassExistsCheck(t, workload.FromStatefulSet(s), sc)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want exactly one finding for the bad template, got %+v", result)
	}
	if !strings.Contains(result.Findings[0].Evidence[0], "volumeClaimTemplate=logs") {
		t.Errorf("finding must name the template with the bad storageClassName, got %+v", result.Findings[0].Evidence)
	}
}

// 9. Degradation: StorageClass Get forbidden must Skip the whole check,
// not fail the run and not silently report clean.
func TestStorageClassExists_StorageClassGetForbidden_Skipped(t *testing.T) {
	tmpl := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: storageClassNamePtr("standard")},
	}
	s := statefulSetWithVolumeClaimTemplates("db", tmpl)
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "storageclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies getting StorageClasses")
	})

	result, err := check.StorageClassExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromStatefulSet(s),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the StorageClass get is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
