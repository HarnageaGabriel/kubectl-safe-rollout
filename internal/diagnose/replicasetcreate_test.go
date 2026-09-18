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

package diagnose_test

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

const replicaSetCreateDeployUID types.UID = "deploy-uid"

// warningEvent builds a Warning-typed Event, unlike the shared event()
// helper in diagnose_test.go (which leaves Type as the empty string): the
// fallback path in ReplicaSetCreate.Diagnose deliberately requires
// Type==Warning, matching the real Deployment controller's
// eventRecorder.Eventf(d, v1.EventTypeWarning, FailedRSCreateReason, ...)
// call this Diagnoser's doc comment cites.
func warningEvent(uid types.UID, reason, message string, count int32) corev1.Event {
	return corev1.Event{
		InvolvedObject: corev1.ObjectReference{UID: uid},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        message,
		Count:          count,
	}
}

func replicaSetCreateDeploymentWithConditions(conditions ...appsv1.DeploymentCondition) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace, Generation: 1, UID: replicaSetCreateDeployUID},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(3)},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1,
			Conditions:         conditions,
		},
	}
}

const quotaMessage = `Failed to create new replica set "app-75fff74496": replicasets.apps "app-75fff74496" is forbidden: exceeded quota: rs-quota, requested: count/replicasets.apps=1, used: count/replicasets.apps=1, limited: count/replicasets.apps=1`

const vapDenialMessage = `Failed to create new replica set "app-b8bf678cf": replicasets.apps "app-b8bf678cf" is forbidden: ValidatingAdmissionPolicy 'deny-rs' with binding 'deny-rs-binding' denied request: no new replicasets allowed by policy`

// (a) condition present, quota message -> quota-exceeded, deterministic.
func TestReplicaSetCreate_ConditionQuotaExceeded(t *testing.T) {
	d := replicaSetCreateDeploymentWithConditions(appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Reason: "ReplicaSetCreateError", Message: quotaMessage,
	})
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseReplicaSetCreateQuotaExceeded) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseReplicaSetCreateQuotaExceeded, res.Findings)
	}
	f := res.Findings[0]
	if f.Undetermined {
		t.Error("a quota-exceeded message is deterministic: must not be Undetermined")
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("expected SeverityHigh, got %v", f.Severity)
	}
	if len(f.Evidence) != 1 || !strings.Contains(f.Evidence[0], quotaMessage) {
		t.Errorf("expected the verbatim message in Evidence, got %+v", f.Evidence)
	}
}

// (b) condition present, non-quota message -> undetermined; proven reachable
// live on kind this session via a ValidatingAdmissionPolicy denial.
func TestReplicaSetCreate_ConditionUndetermined_ValidatingAdmissionPolicyDenial(t *testing.T) {
	d := replicaSetCreateDeploymentWithConditions(appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Reason: "ReplicaSetCreateError", Message: vapDenialMessage,
	})
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseReplicaSetCreateUndetermined) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseReplicaSetCreateUndetermined, res.Findings)
	}
	f := res.Findings[0]
	if !f.Undetermined {
		t.Error("a non-quota rejection message must remain Undetermined")
	}
	if f.Severity != model.SeverityHigh {
		t.Errorf("expected SeverityHigh even for the undetermined cause: the rollout is genuinely stalled either way, got %v", f.Severity)
	}
}

// (c) no condition at all, but a Warning event on the Deployment's own UID
// carries the same Reason: the fallback path, covering
// spec.progressDeadlineSeconds disabled via the MaxInt32 sentinel.
func TestReplicaSetCreate_FallbackEvent_NoCondition(t *testing.T) {
	d := replicaSetCreateDeploymentWithConditions() // no conditions at all
	events := []corev1.Event{warningEvent(replicaSetCreateDeployUID, "ReplicaSetCreateError", quotaMessage, 0)}
	target := newTarget(t, nil, events, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseReplicaSetCreateQuotaExceeded) {
		t.Fatalf("expected 1 finding %q via the event fallback, got %+v", diagnose.CauseReplicaSetCreateQuotaExceeded, res.Findings)
	}
}

// (d) the critical guard: a DIFFERENT Progressing reason is present
// (ProgressDeadlineExceeded) alongside a stale Warning event carrying
// ReplicaSetCreateError. The fallback must not fire: this is not the "no
// condition at all" case it exists for.
func TestReplicaSetCreate_DifferentReasonPresent_StaleEventIgnored(t *testing.T) {
	d := replicaSetCreateDeploymentWithConditions(appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded", Message: "ReplicaSet \"app-abc123\" has timed out progressing.",
	})
	events := []corev1.Event{warningEvent(replicaSetCreateDeployUID, "ReplicaSetCreateError", quotaMessage, 0)}
	target := newTarget(t, nil, events, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a stale ReplicaSetCreateError event must not fire once the condition has moved to a different, known reason, got %+v", res.Findings)
	}
}

// (e) condition present and healthy -> no finding, no event involved.
func TestReplicaSetCreate_HealthyCondition_NoFinding(t *testing.T) {
	d := replicaSetCreateDeploymentWithConditions(appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Reason: "NewReplicaSetAvailable", Status: corev1.ConditionTrue,
	})
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a healthy rollout must produce no findings, got %+v", res.Findings)
	}
}

// (f) no condition and no relevant event -> no finding.
func TestReplicaSetCreate_NoConditionNoEvent_NoFinding(t *testing.T) {
	d := replicaSetCreateDeploymentWithConditions()
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("no signal at all must produce no findings, got %+v", res.Findings)
	}
}

// (g) StatefulSet has no ReplicaSet or Progressing condition: Skip, not a
// silent empty Result.
func TestReplicaSetCreate_StatefulSet_Skipped(t *testing.T) {
	s := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: testNamespace, Generation: 1},
		Status:     appsv1.StatefulSetStatus{ObservedGeneration: 1},
	}
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromStatefulSet(s)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if !res.Skipped {
		t.Fatal("StatefulSet has no ReplicaSet-mediated pod creation: this must be Skipped, not silently empty")
	}
	if !strings.Contains(res.SkipReason, "StatefulSet") {
		t.Errorf("SkipReason must name the kind, got %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a Skipped result must carry no Findings, got %+v", res.Findings)
	}
}

// (h) DaemonSet mirrors the StatefulSet case.
func TestReplicaSetCreate_DaemonSet_Skipped(t *testing.T) {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "logger", Namespace: testNamespace, Generation: 1},
		Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1},
	}
	target := newTarget(t, nil, nil, nil)
	target.Workload = workload.FromDaemonSet(ds)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if !res.Skipped {
		t.Fatal("DaemonSet has no ReplicaSet-mediated pod creation: this must be Skipped, not silently empty")
	}
	if !strings.Contains(res.SkipReason, "DaemonSet") {
		t.Errorf("SkipReason must name the kind, got %q", res.SkipReason)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a Skipped result must carry no Findings, got %+v", res.Findings)
	}
}

// (i) an aggregated event (Count>1, the real apiserver's behavior for a
// repeatedly identical message) still classifies correctly, and the count
// is surfaced in Evidence.
func TestReplicaSetCreate_FallbackEvent_AggregatedCountSurfacedInEvidence(t *testing.T) {
	d := replicaSetCreateDeploymentWithConditions()
	events := []corev1.Event{warningEvent(replicaSetCreateDeployUID, "ReplicaSetCreateError", quotaMessage, 5)}
	target := newTarget(t, nil, events, nil)
	target.Workload = workload.FromDeployment(d)

	res, err := diagnose.ReplicaSetCreate{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseReplicaSetCreateQuotaExceeded) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseReplicaSetCreateQuotaExceeded, res.Findings)
	}
	evidence := res.Findings[0].Evidence
	if len(evidence) != 1 || !strings.Contains(evidence[0], "5") {
		t.Errorf("expected the aggregated Count to appear in Evidence, got %+v", evidence)
	}
}
