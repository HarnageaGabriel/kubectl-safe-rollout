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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

func daemonSetBase() *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec: appsv1.DaemonSetSpec{
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type: appsv1.RollingUpdateDaemonSetStrategyType,
			},
		},
		Status: appsv1.DaemonSetStatus{
			ObservedGeneration:     2,
			DesiredNumberScheduled: 3,
			UpdatedNumberScheduled: 3,
			NumberAvailable:        3,
		},
	}
}

func TestFromDaemonSet_Identity(t *testing.T) {
	d := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
		Name: "logger", Namespace: "monitoring", UID: "daemonset-uid",
	}}
	w := workload.FromDaemonSet(d)
	if w.Kind() != "DaemonSet" || w.Name() != "logger" || w.Namespace() != "monitoring" || w.UID() != "daemonset-uid" {
		t.Fatalf("unexpected workload identity: kind=%s name=%s namespace=%s uid=%s", w.Kind(), w.Name(), w.Namespace(), w.UID())
	}
}

// Replicas has no spec.replicas field to default: it must reflect the
// controller's own count of eligible nodes, not a user-set value.
func TestFromDaemonSet_Replicas_FromDesiredNumberScheduled(t *testing.T) {
	d := &appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 7}}
	if got := workload.FromDaemonSet(d).Replicas(); got != 7 {
		t.Errorf("Replicas() = %d, expected 7 (status.desiredNumberScheduled)", got)
	}
}

func TestFromDaemonSet_UpdateStrategy_RollingUpdateWithDefaults(t *testing.T) {
	d := &appsv1.DaemonSet{} // Empty Type: must resolve to RollingUpdate 1/0
	s := workload.FromDaemonSet(d).UpdateStrategy()

	if s.Type != workload.RollingUpdate {
		t.Fatalf("Type = %q, expected RollingUpdate", s.Type)
	}
	if s.MaxUnavailable == nil || s.MaxUnavailable.IntVal != 1 {
		t.Errorf("MaxUnavailable = %+v, expected default 1", s.MaxUnavailable)
	}
	if s.MaxSurge == nil || s.MaxSurge.IntVal != 0 {
		t.Errorf("MaxSurge = %+v, expected default 0", s.MaxSurge)
	}
	if s.Partition != nil {
		t.Errorf("Partition = %+v, expected nil: the field does not exist on DaemonSet", s.Partition)
	}
}

func TestFromDaemonSet_UpdateStrategy_ExplicitRollingUpdate(t *testing.T) {
	maxUnavailable := intstr.FromString("20%")
	maxSurge := intstr.FromInt32(2)
	d := &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{
		UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
			Type: appsv1.RollingUpdateDaemonSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{
				MaxUnavailable: &maxUnavailable,
				MaxSurge:       &maxSurge,
			},
		},
	}}
	s := workload.FromDaemonSet(d).UpdateStrategy()
	if s.MaxUnavailable == nil || s.MaxUnavailable.StrVal != "20%" {
		t.Errorf("MaxUnavailable = %+v, expected explicit 20%%", s.MaxUnavailable)
	}
	if s.MaxSurge == nil || s.MaxSurge.IntVal != 2 {
		t.Errorf("MaxSurge = %+v, expected explicit 2", s.MaxSurge)
	}
}

func TestFromDaemonSet_UpdateStrategy_OnDelete(t *testing.T) {
	d := &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{
		UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType},
	}}
	s := workload.FromDaemonSet(d).UpdateStrategy()
	if s.Type != workload.OnDelete {
		t.Fatalf("Type = %q, expected OnDelete", s.Type)
	}
	if s.MaxUnavailable != nil || s.MaxSurge != nil {
		t.Errorf("OnDelete must not carry RollingUpdate fields, got %+v / %+v", s.MaxUnavailable, s.MaxSurge)
	}
}

func TestFromDaemonSet_PodRequests_InitContainers_Volumes_PodSelector(t *testing.T) {
	d := &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "logger"}},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "logger", "extra": "label"}},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "setup"}},
				Containers: []corev1.Container{{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("50m"),
						},
					},
				}},
				Volumes: []corev1.Volume{{Name: "varlog"}},
			},
		},
	}}
	w := workload.FromDaemonSet(d)

	reqs := w.PodRequests()
	if reqs.Cpu().String() != "50m" {
		t.Errorf("PodRequests() cpu = %s, expected 50m", reqs.Cpu().String())
	}
	if init := w.InitContainers(); len(init) != 1 || init[0].Name != "setup" {
		t.Errorf("InitContainers() = %+v, expected only setup", init)
	}
	if vols := w.Volumes(); len(vols) != 1 || vols[0].Name != "varlog" {
		t.Errorf("Volumes() = %+v, expected only varlog", vols)
	}
	selector, err := w.PodSelector()
	if err != nil {
		t.Fatalf("PodSelector: %v", err)
	}
	if got := selector.String(); got != "app=logger" {
		t.Fatalf("PodSelector() = %q, expected only immutable selector app=logger", got)
	}
}

func TestFromDaemonSet_RolloutComplete(t *testing.T) {
	if !workload.FromDaemonSet(daemonSetBase()).RolloutComplete() {
		t.Fatal("fully available rollout must be reported as complete")
	}

	tests := []struct {
		name   string
		mutate func(*appsv1.DaemonSet)
	}{
		{"generation not observed", func(d *appsv1.DaemonSet) { d.Status.ObservedGeneration = 1 }},
		{"insufficient updated pods", func(d *appsv1.DaemonSet) { d.Status.UpdatedNumberScheduled = 2 }},
		{"insufficient available pods", func(d *appsv1.DaemonSet) { d.Status.NumberAvailable = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := daemonSetBase()
			test.mutate(d)
			if workload.FromDaemonSet(d).RolloutComplete() {
				t.Fatal("incomplete rollout reported as complete")
			}
		})
	}
}

func TestFromDaemonSet_RolloutComplete_OnDelete_NotFalselyBlocked(t *testing.T) {
	d := daemonSetBase()
	d.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}
	if !workload.FromDaemonSet(d).RolloutComplete() {
		t.Fatal("OnDelete must not be blocked forever once generation/readiness checks pass")
	}
}

func TestFromDaemonSet_ProgressDeadlineExceeded_AlwaysNotApplicable(t *testing.T) {
	d := &appsv1.DaemonSet{}
	if _, ok := workload.FromDaemonSet(d).ProgressDeadlineExceeded(); ok {
		t.Fatal("DaemonSet has no progressDeadlineSeconds mechanism: ok must always be false")
	}
}

func TestFromDaemonSet_Paused_AlwaysFalse(t *testing.T) {
	d := &appsv1.DaemonSet{}
	if workload.FromDaemonSet(d).Paused() {
		t.Fatal("DaemonSet has no spec.paused field: must always report false")
	}
}

func TestFromDaemonSet_PendingRevisionUpdate_AlwaysNotApplicable(t *testing.T) {
	d := &appsv1.DaemonSet{}
	if _, _, ok := workload.FromDaemonSet(d).PendingRevisionUpdate(); ok {
		t.Fatal("DaemonSetStatus has no revision-hash pair: ok must always be false")
	}
}

func TestFromDaemonSet_VolumeClaimTemplates_AlwaysNil(t *testing.T) {
	d := &appsv1.DaemonSet{}
	if got := workload.FromDaemonSet(d).VolumeClaimTemplates(); got != nil {
		t.Fatalf("VolumeClaimTemplates() = %+v, expected nil: DaemonSetSpec has no such field", got)
	}
}

func TestFromDaemonSet_DesiredCount(t *testing.T) {
	d := daemonSetBase()
	count, observed := workload.FromDaemonSet(d).DesiredCount()
	if count != 3 || !observed {
		t.Fatalf("DesiredCount() = (%d, %v), expected (3, true)", count, observed)
	}

	d.Status.ObservedGeneration = 1
	count, observed = workload.FromDaemonSet(d).DesiredCount()
	if count != 3 || observed {
		t.Fatalf("DesiredCount() = (%d, %v), expected (3, false) when generation is not yet observed", count, observed)
	}
}
