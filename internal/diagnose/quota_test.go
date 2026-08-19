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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

func replicaSet(name string) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: "rs-uid"},
	}
}

func TestQuota_Exceeded(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	events := []corev1.Event{
		event("rs-uid", "FailedCreate", `Error creating: pods "app-abc123-" is forbidden: exceeded quota: compute-quota, requested: limits.cpu=1, used: limits.cpu=4, limited: limits.cpu=4`),
	}
	target := newTarget(t, nil, events, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseQuotaExceeded) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseQuotaExceeded, res.Findings)
	}
	if res.Findings[0].Resource.Kind != "ReplicaSet" {
		t.Errorf("Resource.Kind = %q, expected ReplicaSet: the Pod is never created when quota blocks creation", res.Findings[0].Resource.Kind)
	}
}

func TestQuota_Undetermined_FailedCreateNotQuotaRelated(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	events := []corev1.Event{
		event("rs-uid", "FailedCreate", `Error creating: admission webhook "policy.example.com" denied the request: missing required label`),
	}
	target := newTarget(t, nil, events, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseQuotaUndetermined) {
		t.Fatalf("expected 1 finding %q, got %+v", diagnose.CauseQuotaUndetermined, res.Findings)
	}
	if !res.Findings[0].Undetermined {
		t.Error("a FailedCreate without a quota reference must remain Undetermined; do not assume a cause")
	}
}

func TestQuota_NoIssue_WithoutFailedCreate(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	target := newTarget(t, nil, nil, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("the absence of FailedCreate events must produce no findings, got %+v", res.Findings)
	}
}

// A rollout of N replicas blocked by quota produces N FailedCreate events
// describing the same failure. Observed on kind: a 3-replica Deployment in a
// namespace with `pods: "1"` printed the identical cause and remediation three
// times, which is noise exactly where the output is read under pressure.
func TestQuota_ManyRejectedPods_ProduceOneFinding(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	events := []corev1.Event{
		event("rs-uid", "FailedCreate", `Error creating: pods "app-abc123-hwx76" is forbidden: exceeded quota: tight, requested: pods=1, used: pods=1, limited: pods=1`),
		event("rs-uid", "FailedCreate", `Error creating: pods "app-abc123-brllb" is forbidden: exceeded quota: tight, requested: pods=1, used: pods=1, limited: pods=1`),
		event("rs-uid", "FailedCreate", `Error creating: pods "app-abc123-lxvxq" is forbidden: exceeded quota: tight, requested: pods=1, used: pods=1, limited: pods=1`),
	}
	target := newTarget(t, nil, events, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected the three events to collapse into 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	// The individual messages are kept: they name which pods were rejected.
	if len(res.Findings[0].Evidence) != 3 {
		t.Fatalf("expected all 3 event messages as evidence, got %+v", res.Findings[0].Evidence)
	}
	for _, e := range res.Findings[0].Evidence {
		if strings.Contains(e, "more rejections") {
			t.Errorf("three messages fit under the cap and must not be summarised: %q", e)
		}
	}
	for _, want := range []string{"hwx76", "brllb", "lxvxq"} {
		found := false
		for _, e := range res.Findings[0].Evidence {
			if strings.Contains(e, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("evidence lost the message naming pod %q: %+v", want, res.Findings[0].Evidence)
		}
	}
}

// A ReplicaSet can be rejected for two different reasons at once; collapsing
// per cause must not merge a quota rejection with an unrelated one.
func TestQuota_MixedRejections_ProduceOneFindingPerCause(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	events := []corev1.Event{
		event("rs-uid", "FailedCreate", `Error creating: pods "app-abc123-hwx76" is forbidden: exceeded quota: tight, requested: pods=1, used: pods=1, limited: pods=1`),
		event("rs-uid", "FailedCreate", `Error creating: admission webhook "policy.example.com" denied the request: missing required label`),
	}
	target := newTarget(t, nil, events, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("expected one finding per distinct cause, got %d: %+v", len(res.Findings), res.Findings)
	}
	byID := map[string]model.Finding{}
	for _, f := range res.Findings {
		byID[f.CheckID] = f
	}
	if _, ok := byID[string(diagnose.CauseQuotaExceeded)]; !ok {
		t.Errorf("missing %q: %+v", diagnose.CauseQuotaExceeded, res.Findings)
	}
	undetermined, ok := byID[string(diagnose.CauseQuotaUndetermined)]
	if !ok {
		t.Fatalf("missing %q: %+v", diagnose.CauseQuotaUndetermined, res.Findings)
	}
	if !undetermined.Undetermined {
		t.Error("the non-quota rejection must be marked Undetermined")
	}
}

// The ReplicaSet controller retries for as long as the quota stays exhausted,
// so the evidence list would otherwise grow without bound with messages that
// differ only by generated pod name. Truncation must announce itself.
func TestQuota_ManyRejections_EvidenceIsCappedAndSaysSo(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	var events []corev1.Event
	for _, suffix := range []string{"aaa", "bbb", "ccc", "ddd", "eee", "fff"} {
		events = append(events, event("rs-uid", "FailedCreate",
			`Error creating: pods "app-abc123-`+suffix+`" is forbidden: exceeded quota: tight, requested: pods=1, used: pods=1, limited: pods=1`))
	}
	target := newTarget(t, nil, events, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose returned an unexpected error: %v", err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(res.Findings), res.Findings)
	}
	evidence := res.Findings[0].Evidence
	if len(evidence) != 4 {
		t.Fatalf("expected 3 messages plus one summary line, got %d: %+v", len(evidence), evidence)
	}
	last := evidence[len(evidence)-1]
	if !strings.Contains(last, "3 more") {
		t.Errorf("the summary line must state how many rejections were omitted, got %q", last)
	}
}
