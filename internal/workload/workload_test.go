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

package workload_test

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func int32Ptr(v int32) *int32 { return &v }

func TestFromDeployment_ReplicasDefaultToOne(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
	}
	w := workload.FromDeployment(d)

	if got := w.Replicas(); got != 1 {
		t.Errorf("Replicas() = %d, expected 1 when Spec.Replicas is nil (Kubernetes default)", got)
	}
}

func TestFromDeployment_ExplicitReplicas(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: int32Ptr(5)}}
	if got := workload.FromDeployment(d).Replicas(); got != 5 {
		t.Errorf("Replicas() = %d, expected 5", got)
	}
}

func TestFromDeployment_RollingUpdateStrategyWithDefaults(t *testing.T) {
	d := &appsv1.Deployment{} // Empty Strategy.Type: must resolve to RollingUpdate 25%/25%
	s := workload.FromDeployment(d).UpdateStrategy()

	if s.Type != workload.RollingUpdate {
		t.Fatalf("Type = %q, expected RollingUpdate", s.Type)
	}
	if s.MaxUnavailable == nil || s.MaxUnavailable.StrVal != "25%" {
		t.Errorf("MaxUnavailable = %+v, expected default 25%%", s.MaxUnavailable)
	}
	if s.MaxSurge == nil || s.MaxSurge.StrVal != "25%" {
		t.Errorf("MaxSurge = %+v, expected default 25%%", s.MaxSurge)
	}
}

func TestFromDeployment_ExplicitRollingUpdateStrategy(t *testing.T) {
	one := intstr.FromInt(1)
	d := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Strategy: appsv1.DeploymentStrategy{
				Type:          appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{MaxUnavailable: &one},
			},
		},
	}
	s := workload.FromDeployment(d).UpdateStrategy()

	if s.MaxUnavailable == nil || s.MaxUnavailable.IntVal != 1 {
		t.Errorf("MaxUnavailable = %+v, expected 1 (explicit value, no default)", s.MaxUnavailable)
	}
	// MaxSurge is not specified in RollingUpdate: it must still resolve to the
	// default, not remain nil.
	if s.MaxSurge == nil || s.MaxSurge.StrVal != "25%" {
		t.Errorf("MaxSurge = %+v, expected default 25%% when unspecified", s.MaxSurge)
	}
}

func TestFromDeployment_RecreateStrategy(t *testing.T) {
	d := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}},
	}
	s := workload.FromDeployment(d).UpdateStrategy()

	if s.Type != workload.Recreate {
		t.Fatalf("Type = %q, expected Recreate", s.Type)
	}
	if s.MaxUnavailable != nil || s.MaxSurge != nil {
		t.Errorf("Recreate does not support MaxUnavailable/MaxSurge: expected both nil, got %+v / %+v", s.MaxUnavailable, s.MaxSurge)
	}
}

func TestFromDeployment_PodLabels(t *testing.T) {
	labels := map[string]string{"app": "api"}
	d := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}},
		},
	}
	got := workload.FromDeployment(d).PodLabels()
	if got["app"] != "api" {
		t.Errorf("PodLabels() = %+v, expected app=api", got)
	}
}

func TestFromDeployment_PodContainers_ExcludesInitContainers(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "migrazioni"}},
			Containers: []corev1.Container{
				{Name: "app"},
				{Name: "sidecar"},
			},
		}},
	}}

	got := workload.FromDeployment(d).PodContainers()
	if len(got) != 2 {
		t.Fatalf("PodContainers() returned %d containers, expected 2 regular containers", len(got))
	}
	if got[0].Name != "app" || got[1].Name != "sidecar" {
		t.Fatalf("PodContainers() = %+v, expected only app and sidecar", got)
	}
}

func TestFromDeployment_InitContainers_ExcludesRegularContainers(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "migrations"}},
			Containers: []corev1.Container{
				{Name: "app"},
				{Name: "sidecar"},
			},
		}},
	}}

	got := workload.FromDeployment(d).InitContainers()
	if len(got) != 1 || got[0].Name != "migrations" {
		t.Fatalf("InitContainers() = %+v, expected only migrations", got)
	}
}

func TestFromDeployment_ImagePullSecretsAndServiceAccount(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "registry-uno"}, {Name: "registry-due"}},
			ServiceAccountName: "deployer",
		}},
	}}
	w := workload.FromDeployment(d)

	names := w.ImagePullSecretNames()
	if len(names) != 2 || names[0] != "registry-uno" || names[1] != "registry-due" {
		t.Fatalf("ImagePullSecretNames() = %+v, expected registry-uno and registry-due", names)
	}
	if got := w.ServiceAccountName(); got != "deployer" {
		t.Fatalf("ServiceAccountName() = %q, expected deployer", got)
	}
}

func TestFromDeployment_PodSelectorUsesControllerSelector(t *testing.T) {
	d := &appsv1.Deployment{Spec: appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			"app": "api", "revision-label": "current",
		}}},
	}}
	selector, err := workload.FromDeployment(d).PodSelector()
	if err != nil {
		t.Fatalf("PodSelector: %v", err)
	}
	if got := selector.String(); got != "app=api" {
		t.Fatalf("PodSelector() = %q, expected only immutable selector app=api", got)
	}
}

func TestFromDeployment_Identity(t *testing.T) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "api", Namespace: "payments", UID: "deployment-uid",
	}}
	w := workload.FromDeployment(d)
	if w.Kind() != "Deployment" || w.Name() != "api" || w.Namespace() != "payments" || w.UID() != "deployment-uid" {
		t.Fatalf("unexpected workload identity: kind=%s name=%s namespace=%s uid=%s", w.Kind(), w.Name(), w.Namespace(), w.UID())
	}
}

func TestFromDeployment_RolloutComplete(t *testing.T) {
	base := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(2)},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 2, UpdatedReplicas: 2, Replicas: 2, AvailableReplicas: 2,
		},
	}
	if !workload.FromDeployment(base.DeepCopy()).RolloutComplete() {
		t.Fatal("fully available rollout must be reported as complete")
	}

	tests := []struct {
		name   string
		mutate func(*appsv1.Deployment)
	}{
		{"generation not observed", func(d *appsv1.Deployment) { d.Status.ObservedGeneration = 1 }},
		{"insufficient updated replicas", func(d *appsv1.Deployment) { d.Status.UpdatedReplicas = 1 }},
		{"old replicas present", func(d *appsv1.Deployment) { d.Status.Replicas = 3 }},
		{"unavailable replicas", func(d *appsv1.Deployment) { d.Status.AvailableReplicas = 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := base.DeepCopy()
			test.mutate(d)
			if workload.FromDeployment(d).RolloutComplete() {
				t.Fatal("incomplete rollout reported as complete")
			}
		})
	}
}

func TestFromDeployment_ProgressDeadlineExceeded(t *testing.T) {
	d := &appsv1.Deployment{Status: appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded", Message: "ReplicaSet timed out",
	}}}}
	message, ok := workload.FromDeployment(d).ProgressDeadlineExceeded()
	if !ok || message != "ReplicaSet timed out" {
		t.Fatalf("expected deadline condition, got message=%q ok=%v", message, ok)
	}

	d.Status.Conditions[0].Reason = "NewReplicaSetAvailable"
	if _, ok := workload.FromDeployment(d).ProgressDeadlineExceeded(); ok {
		t.Fatal("unexpired Progressing condition must not be reported as deadline exceeded")
	}
}
