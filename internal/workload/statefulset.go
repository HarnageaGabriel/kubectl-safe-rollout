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

package workload

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

type statefulSetWorkload struct {
	s *appsv1.StatefulSet
}

// FromStatefulSet builds a Workload from a live StatefulSet.
func FromStatefulSet(s *appsv1.StatefulSet) Workload {
	return &statefulSetWorkload{s: s}
}

func (w *statefulSetWorkload) Kind() string      { return "StatefulSet" }
func (w *statefulSetWorkload) Name() string      { return w.s.Name }
func (w *statefulSetWorkload) Namespace() string { return w.s.Namespace }
func (w *statefulSetWorkload) UID() types.UID    { return w.s.UID }

func (w *statefulSetWorkload) Replicas() int32 {
	if w.s.Spec.Replicas == nil {
		return 1
	}
	return *w.s.Spec.Replicas
}

func (w *statefulSetWorkload) PodLabels() map[string]string {
	return w.s.Spec.Template.Labels
}

func (w *statefulSetWorkload) PodSelector() (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(w.s.Spec.Selector)
}

// UpdateStrategy normalizes StatefulSet's update strategy into the shared
// UpdateStrategy shape. Unlike Deployment, StatefulSet has no MaxSurge field
// at all (a rolling update replaces one pod at a time, deleting the old one
// before creating its replacement, so it never exceeds Replicas): MaxSurge is
// always nil here, not defaulted, so surgeCount-style callers correctly treat
// StatefulSet as never surging. MaxUnavailable does exist on
// RollingUpdateStatefulSetStrategy (gated by the MaxUnavailableStatefulSet
// feature gate, beta and enabled by default as of the k8s.io/api version this
// module depends on) but, unlike Deployment's MaxUnavailable, Kubernetes does
// not guarantee server-side defaulting to a fixed value in every version:
// this method populates it only when the RollingUpdate struct sets it
// explicitly, leaving it nil (not "1") when absent, to avoid asserting a
// default this project has not verified against every supported server
// version.
func (w *statefulSetWorkload) UpdateStrategy() UpdateStrategy {
	strategyType := w.s.Spec.UpdateStrategy.Type
	if strategyType == "" {
		strategyType = RollingUpdate
	}
	if strategyType == appsv1.OnDeleteStatefulSetStrategyType {
		return UpdateStrategy{Type: OnDelete}
	}

	ru := w.s.Spec.UpdateStrategy.RollingUpdate
	strategy := UpdateStrategy{Type: RollingUpdate}
	if ru != nil {
		if ru.MaxUnavailable != nil {
			maxUnavailable := *ru.MaxUnavailable
			strategy.MaxUnavailable = &maxUnavailable
		}
		if ru.Partition != nil {
			partition := *ru.Partition
			strategy.Partition = &partition
		}
	}
	return strategy
}

// PodRequests implements Workload.
func (w *statefulSetWorkload) PodRequests() corev1.ResourceList {
	total := corev1.ResourceList{}
	for _, c := range w.s.Spec.Template.Spec.Containers {
		for name, qty := range c.Resources.Requests {
			sum := total[name]
			sum.Add(qty)
			total[name] = sum
		}
	}
	return total
}

// PodContainers implements Workload.
func (w *statefulSetWorkload) PodContainers() []corev1.Container {
	return w.s.Spec.Template.Spec.Containers
}

// InitContainers implements Workload.
func (w *statefulSetWorkload) InitContainers() []corev1.Container {
	return w.s.Spec.Template.Spec.InitContainers
}

// Volumes implements Workload.
func (w *statefulSetWorkload) Volumes() []corev1.Volume {
	return w.s.Spec.Template.Spec.Volumes
}

// ImagePullSecretNames implements Workload.
func (w *statefulSetWorkload) ImagePullSecretNames() []string {
	refs := w.s.Spec.Template.Spec.ImagePullSecrets
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

// ServiceAccountName implements Workload.
func (w *statefulSetWorkload) ServiceAccountName() string {
	return w.s.Spec.Template.Spec.ServiceAccountName
}

// PriorityClassName implements Workload.
func (w *statefulSetWorkload) PriorityClassName() string {
	return w.s.Spec.Template.Spec.PriorityClassName
}

// TopologySpreadConstraints implements Workload.
func (w *statefulSetWorkload) TopologySpreadConstraints() []corev1.TopologySpreadConstraint {
	return w.s.Spec.Template.Spec.TopologySpreadConstraints
}

// Affinity implements Workload.
func (w *statefulSetWorkload) Affinity() *corev1.Affinity {
	return w.s.Spec.Template.Spec.Affinity
}

// NodeSelector implements Workload.
func (w *statefulSetWorkload) NodeSelector() map[string]string {
	return w.s.Spec.Template.Spec.NodeSelector
}

// Tolerations implements Workload.
func (w *statefulSetWorkload) Tolerations() []corev1.Toleration {
	return w.s.Spec.Template.Spec.Tolerations
}

// SchedulerName implements Workload.
func (w *statefulSetWorkload) SchedulerName() string {
	return w.s.Spec.Template.Spec.SchedulerName
}

// RolloutComplete has now been checked line by line against the real
// StatefulSetStatusViewer.Status implementation in
// k8s.io/kubectl@v0.36.3/pkg/polymorphichelpers/rollout_status.go, lines
// 120-152 (present in this module's build cache even though k8s.io/kubectl
// is not a go.mod dependency of this project; it is not imported here).
// That method: rejects any strategy type other than RollingUpdate outright
// (line 127-129); waits for ObservedGeneration to catch up (line 130-132);
// waits for ReadyReplicas to reach the desired count (line 133-135); then,
// because line 127 has already guaranteed the strategy type is
// RollingUpdate, the "if ... Type == RollingUpdate" guard at line 136 is
// always true, so it always returns done=true at line 143-144 as soon as
// the (optional) Partition threshold on UpdatedReplicas is satisfied. The
// UpdateRevision/CurrentRevision comparison at lines 146-150 is dead code
// in the real controller: that branch can never execute, because the only
// way past line 136 without returning is a strategy type that line 127-129
// already rejected.
//
// This method is deliberately stricter than that real, shipped behavior:
// when Partition is unset, it still requires
// Status.UpdateRevision == Status.CurrentRevision before declaring the
// rollout complete, a comparison `kubectl rollout status` itself never
// actually performs. This is a conscious choice, not an oversight: for a
// pre-flight/diagnosis tool, a false negative (reporting "not complete yet"
// for a StatefulSet that `kubectl rollout status` would already call done)
// is a far cheaper mistake than a false positive (`watch` declaring success
// while old-revision pods are still present). Revision convergence is the
// more precise definition of "done" for a rolling update; the fact that the
// upstream CLI does not check it is closer to a historical accident of that
// code path than a deliberate contract this project should replicate
// exactly.
//
// Divergence from kubectl also deliberately kept for OnDelete: kubectl
// refuses to report status at all for OnDeleteStatefulSetStrategyType (line
// 127-129, same guard). This method does not refuse: once the generation
// and readiness checks pass, it reports the rollout complete under OnDelete
// too, because a dedicated diagnoser for "pending OnDelete update" is
// planned for `watch` (Phase B of StatefulSet support) and RolloutComplete
// must not block `check`/future `watch` forever on a strategy type it is
// allowed to not track precisely.
func (w *statefulSetWorkload) RolloutComplete() bool {
	s := w.s
	if s.Generation > s.Status.ObservedGeneration {
		return false
	}
	desired := w.Replicas()
	if s.Status.ReadyReplicas < desired {
		return false
	}

	strategy := s.Spec.UpdateStrategy
	strategyType := strategy.Type
	if strategyType == "" {
		strategyType = appsv1.RollingUpdateStatefulSetStrategyType
	}
	if strategyType == appsv1.OnDeleteStatefulSetStrategyType {
		return true
	}

	if strategy.RollingUpdate != nil && strategy.RollingUpdate.Partition != nil {
		threshold := desired - *strategy.RollingUpdate.Partition
		return s.Status.UpdatedReplicas >= threshold
	}

	return s.Status.UpdateRevision == s.Status.CurrentRevision
}

// ProgressDeadlineExceeded implements Workload. StatefulSet has no
// progressDeadlineSeconds field and no controller-computed "Progressing"
// condition: there is nothing for this method to read, so it always reports
// ok=false ("not applicable to this controller type"), matching the
// documented contract on the Workload interface itself.
func (w *statefulSetWorkload) ProgressDeadlineExceeded() (message string, ok bool) {
	return "", false
}

// Paused implements Workload. StatefulSet has no spec.paused field at all:
// this method exists on the interface only because Deployment has pause
// semantics that watch must account for. false here is a hard fact about the
// StatefulSet API (there is no pause mechanism to report on), not a
// placeholder default standing in for "unknown".
func (w *statefulSetWorkload) Paused() bool {
	return false
}

// PendingRevisionUpdate implements Workload: the two revision hashes come
// straight from Status, no derivation involved.
func (w *statefulSetWorkload) PendingRevisionUpdate() (updateRevision, currentRevision string, ok bool) {
	return w.s.Status.UpdateRevision, w.s.Status.CurrentRevision, true
}

// VolumeClaimTemplates implements Workload: a direct passthrough of
// spec.volumeClaimTemplates, the mechanism the controller uses to create one
// real PersistentVolumeClaim per pod ordinal from each template.
func (w *statefulSetWorkload) VolumeClaimTemplates() []corev1.PersistentVolumeClaim {
	return w.s.Spec.VolumeClaimTemplates
}
