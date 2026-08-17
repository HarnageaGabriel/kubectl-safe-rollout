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

package diagnose

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// PendingDiagnoserID e' l'identificativo stabile della categoria, usato
// come Result.DiagnoserID.
const PendingDiagnoserID = "pending"

// Pending classifica i pod fermi in fase Pending distinguendo risorse
// insufficienti, vincoli di scheduling e PersistentVolumeClaim non
// bound.
//
// Ordine di precedenza deliberato: la PVC referenziata dal pod si
// verifica per prima, leggendo direttamente il suo Status.Phase (segnale
// strutturato, nessun parsing di testo), prima di guardare l'evento
// FailedScheduling. Solo se la lettura della PVC non e' possibile o non
// spiega il Pending si passa al pattern testuale sull'evento.
type Pending struct{}

// ID implementa Diagnoser.
func (Pending) ID() string { return PendingDiagnoserID }

// Diagnose implementa Diagnoser.
func (d Pending) Diagnose(ctx context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		if pod.Status.Phase != corev1.PodPending {
			continue
		}
		if finding, ok := d.classify(ctx, target, pod); ok {
			findings = append(findings, finding)
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func (d Pending) classify(ctx context.Context, target Target, pod corev1.Pod) (model.Finding, bool) {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}

	if f, ok := d.classifyUnboundPVC(ctx, target, pod, resource); ok {
		return f, true
	}

	for _, e := range target.EventsByUID[pod.UID] {
		if e.Reason != "FailedScheduling" {
			continue
		}
		cause, ok := pattern.FailedScheduling(e.Message)
		if !ok {
			return d.undetermined(resource, []string{fmt.Sprintf("evento FailedScheduling non riconosciuto: %s", e.Message)}), true
		}
		evidence := []string{fmt.Sprintf("evento: %s", e.Message)}
		return d.findingForCause(cause, resource, evidence), true
	}

	// Pending senza FailedScheduling e senza PVC non Bound e' normale
	// durante startup/scheduling. Non deve fermare watch immediatamente.
	return model.Finding{}, false
}

// classifyUnboundPVC legge direttamente lo Status.Phase di ogni
// PersistentVolumeClaim referenziata dal pod: e' un campo strutturato,
// non un pattern su testo libero, quindi ha precedenza su tutto il
// resto. Se la lettura di una PVC fallisce (RBAC insufficiente, o
// altro), questa funzione non la considera prova di nulla e lascia che
// classify() prosegua con l'evento FailedScheduling: un errore di
// lettura puntuale su una singola PVC non deve bloccare la
// classificazione dell'intero pod.
func (Pending) classifyUnboundPVC(ctx context.Context, target Target, pod corev1.Pod, resource model.ResourceRef) (model.Finding, bool) {
	for _, vol := range pod.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		claimName := vol.PersistentVolumeClaim.ClaimName
		pvc, err := target.Client.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, claimName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if pvc.Status.Phase == corev1.ClaimBound {
			continue
		}
		evidence := []string{
			fmt.Sprintf("volume=%s claim=%s", vol.Name, claimName),
			fmt.Sprintf("pvcPhase=%s", pvc.Status.Phase),
		}
		return model.Finding{
			CheckID:  string(CausePendingUnboundPVC),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"%s resta Pending perche' la PersistentVolumeClaim %q non e' Bound (fase attuale: %s)",
				resource, claimName, pvc.Status.Phase,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"verifica perche' %q non si lega a un PersistentVolume: storageClass senza provisioner disponibile, capacita' insufficiente, o vincoli di zona/nodo incompatibili con dove il pod puo' schedulare",
					claimName,
				),
				ContextDependent: true,
			},
			Resource: resource,
		}, true
	}
	return model.Finding{}, false
}

func (Pending) findingForCause(cause string, resource model.ResourceRef, evidence []string) model.Finding {
	switch cause {
	case "insufficient-resources":
		return model.Finding{
			CheckID:  string(CausePendingInsufficientResources),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("%s resta Pending: nessun nodo ha CPU, memoria o storage effimero sufficienti per lo scheduling", resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          "riduci le request del pod se sovradimensionate, oppure aggiungi capacita' al cluster (nuovi nodi o cluster-autoscaler); il valore corretto dipende dal carico reale atteso",
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "scheduling-constraints":
		return model.Finding{
			CheckID:  string(CausePendingSchedulingConstraints),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("%s resta Pending: nodeSelector, affinity o taint/toleration escludono tutti i nodi disponibili", resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          "verifica nodeSelector/affinity del pod contro le label dei nodi disponibili e le toleration contro i taint: il vincolo corretto dipende da dove il workload deve effettivamente girare",
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "unbound-pvc":
		// Lo scheduler segnala un problema di PVC che la lettura
		// strutturata in classifyUnboundPVC non ha confermato (es.
		// RBAC insufficiente sulla PVC): il segnale resta valido anche
		// senza la conferma diretta, non va scartato.
		return model.Finding{
			CheckID:  string(CausePendingUnboundPVC),
			Severity: model.SeverityHigh,
			Cause:    fmt.Sprintf("%s resta Pending: lo scheduler segnala una PersistentVolumeClaim non bound", resource),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          "verifica lo stato delle PersistentVolumeClaim referenziate dal pod",
				Commands:         []string{fmt.Sprintf("kubectl get pvc -n %s", resource.Namespace)},
				ContextDependent: true,
			},
			Resource: resource,
		}
	default:
		return Pending{}.undetermined(resource, evidence)
	}
}

func (Pending) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CausePendingUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s resta Pending ma l'evidenza raccolta non distingue tra risorse insufficienti, vincoli di scheduling e PersistentVolumeClaim non bound", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "raccogli piu' contesto: describe del pod per il testo completo degli eventi di scheduling",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
