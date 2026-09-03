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

func TestFromStatefulSet_ReplicasDefaultToOne(t *testing.T) {
	s := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "default"}}
	w := workload.FromStatefulSet(s)

	if got := w.Replicas(); got != 1 {
		t.Errorf("Replicas() = %d, expected 1 when Spec.Replicas is nil (Kubernetes default)", got)
	}
}

func TestFromStatefulSet_ExplicitReplicas(t *testing.T) {
	s := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: int32Ptr(5)}}
	if got := workload.FromStatefulSet(s).Replicas(); got != 5 {
		t.Errorf("Replicas() = %d, expected 5", got)
	}
}

func TestFromStatefulSet_Identity(t *testing.T) {
	s := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{
		Name: "db", Namespace: "payments", UID: "statefulset-uid",
	}}
	w := workload.FromStatefulSet(s)
	if w.Kind() != "StatefulSet" || w.Name() != "db" || w.Namespace() != "payments" || w.UID() != "statefulset-uid" {
		t.Fatalf("unexpected workload identity: kind=%s name=%s namespace=%s uid=%s", w.Kind(), w.Name(), w.Namespace(), w.UID())
	}
}

func TestFromStatefulSet_PriorityClassName(t *testing.T) {
	s := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
		Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			PriorityClassName: "high-priority",
		}},
	}}
	if got := workload.FromStatefulSet(s).PriorityClassName(); got != "high-priority" {
		t.Fatalf("PriorityClassName() = %q, expected high-priority", got)
	}
}

func TestFromStatefulSet_UpdateStrategy_OnDelete(t *testing.T) {
	s := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
	}}
	strategy := workload.FromStatefulSet(s).UpdateStrategy()

	if strategy.Type != workload.OnDelete {
		t.Fatalf("Type = %q, expected OnDelete", strategy.Type)
	}
	if strategy.MaxUnavailable != nil || strategy.MaxSurge != nil || strategy.Partition != nil {
		t.Errorf("OnDelete does not carry MaxUnavailable/MaxSurge/Partition, got %+v", strategy)
	}
}

func TestFromStatefulSet_UpdateStrategy_RollingUpdateNoPartition(t *testing.T) {
	s := &appsv1.StatefulSet{} // Empty Type must resolve to RollingUpdate, mirroring Deployment's default.
	strategy := workload.FromStatefulSet(s).UpdateStrategy()

	if strategy.Type != workload.RollingUpdate {
		t.Fatalf("Type = %q, expected RollingUpdate", strategy.Type)
	}
	if strategy.Partition != nil {
		t.Errorf("Partition = %v, expected nil when unset", strategy.Partition)
	}
	// StatefulSet has no MaxSurge field at all: must always be nil, never defaulted.
	if strategy.MaxSurge != nil {
		t.Errorf("MaxSurge = %v, expected nil: StatefulSet has no MaxSurge field", strategy.MaxSurge)
	}
}

func TestFromStatefulSet_UpdateStrategy_RollingUpdateWithPartitionAndMaxUnavailable(t *testing.T) {
	partition := int32(2)
	maxUnavailable := intstr.FromInt(1)
	s := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
		UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
			Type: appsv1.RollingUpdateStatefulSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
				Partition:      &partition,
				MaxUnavailable: &maxUnavailable,
			},
		},
	}}
	strategy := workload.FromStatefulSet(s).UpdateStrategy()

	if strategy.Type != workload.RollingUpdate {
		t.Fatalf("Type = %q, expected RollingUpdate", strategy.Type)
	}
	if strategy.Partition == nil || *strategy.Partition != 2 {
		t.Errorf("Partition = %v, expected 2", strategy.Partition)
	}
	if strategy.MaxUnavailable == nil || strategy.MaxUnavailable.IntVal != 1 {
		t.Errorf("MaxUnavailable = %+v, expected 1", strategy.MaxUnavailable)
	}
	if strategy.MaxSurge != nil {
		t.Errorf("MaxSurge = %v, expected nil: StatefulSet has no MaxSurge field", strategy.MaxSurge)
	}
}

func TestFromStatefulSet_Paused_AlwaysFalse(t *testing.T) {
	s := &appsv1.StatefulSet{}
	if workload.FromStatefulSet(s).Paused() {
		t.Fatal("StatefulSet has no pause mechanism: Paused() must always be false")
	}
}

func TestFromStatefulSet_ProgressDeadlineExceeded_AlwaysNotApplicable(t *testing.T) {
	s := &appsv1.StatefulSet{}
	if _, ok := workload.FromStatefulSet(s).ProgressDeadlineExceeded(); ok {
		t.Fatal("StatefulSet has no progressDeadlineSeconds/Progressing condition: ok must always be false")
	}
}

func statefulSetBase() *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(3),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2,
			ReadyReplicas:      3,
			UpdatedReplicas:    3,
			CurrentReplicas:    3,
			UpdateRevision:     "rev-2",
			CurrentRevision:    "rev-2",
		},
	}
}

func TestFromStatefulSet_RolloutComplete_StaleGeneration(t *testing.T) {
	s := statefulSetBase()
	s.Status.ObservedGeneration = 1
	if workload.FromStatefulSet(s).RolloutComplete() {
		t.Fatal("stale ObservedGeneration must not be reported as complete")
	}
}

func TestFromStatefulSet_RolloutComplete_ReadyReplicasShort(t *testing.T) {
	s := statefulSetBase()
	s.Status.ReadyReplicas = 2
	if workload.FromStatefulSet(s).RolloutComplete() {
		t.Fatal("readyReplicas short of desired replicas must not be reported as complete")
	}
}

func TestFromStatefulSet_RolloutComplete_RollingUpdateNoPartition_RevisionsMatch(t *testing.T) {
	s := statefulSetBase()
	if !workload.FromStatefulSet(s).RolloutComplete() {
		t.Fatal("matching current/update revisions with full readiness must be reported as complete")
	}
}

func TestFromStatefulSet_RolloutComplete_RollingUpdateNoPartition_RevisionsDiffer(t *testing.T) {
	s := statefulSetBase()
	s.Status.UpdateRevision = "rev-3"
	if workload.FromStatefulSet(s).RolloutComplete() {
		t.Fatal("mismatched current/update revisions must not be reported as complete")
	}
}

func TestFromStatefulSet_RolloutComplete_PartitionSet_UpdatedReplicasCoverAbovePartition(t *testing.T) {
	s := statefulSetBase()
	partition := int32(1)
	s.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition}
	// Only ordinals >= partition (2 pods out of 3) need to be updated.
	s.Status.UpdatedReplicas = 2
	// Revisions intentionally left mismatched: partitioned rollouts do not
	// wait for full revision convergence, only for updatedReplicas to cover
	// the pods above the partition.
	s.Status.UpdateRevision = "rev-3"
	s.Status.CurrentRevision = "rev-2"

	if !workload.FromStatefulSet(s).RolloutComplete() {
		t.Fatal("updatedReplicas covering all pods above the partition must be reported as complete")
	}
}

func TestFromStatefulSet_RolloutComplete_PartitionSet_UpdatedReplicasShort(t *testing.T) {
	s := statefulSetBase()
	partition := int32(1)
	s.Spec.UpdateStrategy.RollingUpdate = &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition}
	s.Status.UpdatedReplicas = 1 // need 2 (replicas 3 - partition 1)

	if workload.FromStatefulSet(s).RolloutComplete() {
		t.Fatal("updatedReplicas short of replicas-partition must not be reported as complete")
	}
}

func TestFromStatefulSet_RolloutComplete_OnDelete_NotFalselyBlocked(t *testing.T) {
	s := statefulSetBase()
	s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	// Revisions intentionally mismatched: under OnDelete this project does not
	// gate completion on revision convergence (see RolloutComplete's doc
	// comment) — a dedicated Phase B diagnoser owns "pending OnDelete update".
	s.Status.UpdateRevision = "rev-3"
	s.Status.CurrentRevision = "rev-2"

	if !workload.FromStatefulSet(s).RolloutComplete() {
		t.Fatal("OnDelete must not be blocked forever once generation/readiness checks pass")
	}
}

func TestFromStatefulSet_PodRequests_InitContainers_Volumes_PodSelector(t *testing.T) {
	s := &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db", "extra": "label"}},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "migrate"}},
				Containers: []corev1.Container{{
					Name: "app",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("100m"),
						},
					},
				}},
				Volumes: []corev1.Volume{{Name: "data"}},
			},
		},
	}}
	w := workload.FromStatefulSet(s)

	reqs := w.PodRequests()
	if reqs.Cpu().String() != "100m" {
		t.Errorf("PodRequests() cpu = %s, expected 100m", reqs.Cpu().String())
	}
	if init := w.InitContainers(); len(init) != 1 || init[0].Name != "migrate" {
		t.Errorf("InitContainers() = %+v, expected only migrate", init)
	}
	if vols := w.Volumes(); len(vols) != 1 || vols[0].Name != "data" {
		t.Errorf("Volumes() = %+v, expected only data", vols)
	}
	selector, err := w.PodSelector()
	if err != nil {
		t.Fatalf("PodSelector: %v", err)
	}
	if got := selector.String(); got != "app=db" {
		t.Fatalf("PodSelector() = %q, expected only immutable selector app=db", got)
	}
}
