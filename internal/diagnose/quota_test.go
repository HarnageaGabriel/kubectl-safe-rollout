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
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose"
)

func replicaSet(name string) appsv1.ReplicaSet {
	return appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, UID: "rs-uid"},
	}
}

func TestQuota_Superata(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	events := []corev1.Event{
		event("rs-uid", "FailedCreate", `Error creating: pods "app-abc123-" is forbidden: exceeded quota: compute-quota, requested: limits.cpu=1, used: limits.cpu=4, limited: limits.cpu=4`),
	}
	target := newTarget(t, nil, events, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseQuotaExceeded) {
		t.Fatalf("atteso 1 finding %q, got %+v", diagnose.CauseQuotaExceeded, res.Findings)
	}
	if res.Findings[0].Resource.Kind != "ReplicaSet" {
		t.Errorf("Resource.Kind = %q, atteso ReplicaSet: il Pod non arriva a esistere quando la quota blocca la creazione", res.Findings[0].Resource.Kind)
	}
}

func TestQuota_Undetermined_FailedCreateNonDiQuota(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	events := []corev1.Event{
		event("rs-uid", "FailedCreate", `Error creating: admission webhook "policy.example.com" denied the request: missing required label`),
	}
	target := newTarget(t, nil, events, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 1 || res.Findings[0].CheckID != string(diagnose.CauseQuotaUndetermined) {
		t.Fatalf("atteso 1 finding %q, got %+v", diagnose.CauseQuotaUndetermined, res.Findings)
	}
	if !res.Findings[0].Undetermined {
		t.Error("un FailedCreate senza menzione di quota deve restare Undetermined, non presumere una causa")
	}
}

func TestQuota_NessunProblema_SenzaFailedCreate(t *testing.T) {
	rs := []appsv1.ReplicaSet{replicaSet("app-abc123")}
	target := newTarget(t, nil, nil, rs)

	res, err := diagnose.Quota{}.Diagnose(t.Context(), target)
	if err != nil {
		t.Fatalf("Diagnose ha restituito un errore inatteso: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("nessun evento FailedCreate non deve produrre finding, got %+v", res.Findings)
	}
}
