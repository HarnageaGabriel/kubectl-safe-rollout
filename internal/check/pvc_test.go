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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/check"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func runPVCExistsCheck(t *testing.T, d *appsv1.Deployment, objects ...runtime.Object) check.Result {
	t.Helper()
	client := fake.NewSimpleClientset(objects...)
	result, err := check.PVCExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() returned an unexpected error: %v", err)
	}
	return result
}

func TestPVCExists_NoVolumes_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})

	result := runPVCExistsCheck(t, d)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("no PVC volumes at all: want empty result, got %+v", result)
	}
}

func TestPVCExists_Missing_HighFinding(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-pvc"}},
	}}

	result := runPVCExistsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("want one non-skipped finding for the missing PVC, got %+v", result)
	}
	f := result.Findings[0]
	if f.CheckID != check.PVCExistsCheckID || f.Severity != model.SeverityHigh {
		t.Errorf("unexpected finding: checkID=%q severity=%v, want High", f.CheckID, f.Severity)
	}
	if !strings.Contains(f.Cause, "data-pvc") {
		t.Errorf("cause must name the missing PVC, got %q", f.Cause)
	}
	if !f.Remediation.ContextDependent {
		t.Error("remediation must declare itself context-dependent")
	}
	if f.Resource.Kind != "PersistentVolumeClaim" || f.Resource.Name != "data-pvc" {
		t.Errorf("unexpected resource ref: %+v", f.Resource)
	}
}

func TestPVCExists_Exists_NoFindings(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-pvc"}},
	}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-pvc", Namespace: testNamespace}}

	result := runPVCExistsCheck(t, d, pvc)
	if result.Skipped || len(result.Findings) != 0 {
		t.Fatalf("PVC exists: want empty result, got %+v", result)
	}
}

func TestPVCExists_SameClaimTwoVolumes_OneFindingOnly(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{
		{Name: "data1", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "shared-pvc"}}},
		{Name: "data2", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "shared-pvc"}}},
	}

	result := runPVCExistsCheck(t, d)
	if result.Skipped || len(result.Findings) != 1 {
		t.Fatalf("two volumes naming the same missing PVC must produce one finding, not two, got %+v", result.Findings)
	}
}

func TestPVCExists_ReadFailed_Skipped(t *testing.T) {
	d := deploymentWithContainers(corev1.Container{Name: "app", Image: "nginx:1.27"})
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{
		Name:         "data",
		VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-pvc"}},
	}}
	client := fake.NewSimpleClientset()
	client.PrependReactor("get", "persistentvolumeclaims", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden: RBAC denies reading PersistentVolumeClaims")
	})

	result, err := check.PVCExists{}.Run(context.Background(), check.Target{
		Namespace: testNamespace,
		Workload:  workload.FromDeployment(d),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("Run() must not return an error on a failed read; it must degrade to Skipped: %v", err)
	}
	if !result.Skipped {
		t.Fatal("want Skipped=true when the PersistentVolumeClaim is not accessible")
	}
	if result.SkipReason == "" {
		t.Error("SkipReason must not be empty")
	}
}
