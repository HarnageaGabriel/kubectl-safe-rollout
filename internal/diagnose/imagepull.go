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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ImagePullDiagnoserID e' l'identificativo stabile della categoria, usato
// come Result.DiagnoserID.
const ImagePullDiagnoserID = "imagepull"

// ImagePull classifica i container in ImagePullBackOff/ErrImagePull
// distinguendo tag/digest inesistente, registry non raggiungibile e
// credenziali mancanti. Il segnale strutturato (ContainerState.Waiting.
// Reason) identifica solo che il pull fallisce; il perche' vive nel
// messaggio testuale dell'evento Reason=="Failed" associato, isolato in
// internal/diagnose/pattern.
type ImagePull struct{}

// ID implementa Diagnoser.
func (ImagePull) ID() string { return ImagePullDiagnoserID }

// Diagnose implementa Diagnoser.
func (d ImagePull) Diagnose(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting == nil {
				continue
			}
			reason := cs.State.Waiting.Reason
			if reason != "ImagePullBackOff" && reason != "ErrImagePull" {
				continue
			}
			findings = append(findings, d.classify(pod, cs, target.EventsByUID[pod.UID]))
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func (d ImagePull) classify(pod corev1.Pod, cs corev1.ContainerStatus, events []corev1.Event) model.Finding {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	base := []string{
		fmt.Sprintf("container=%s", cs.Name),
		fmt.Sprintf("image=%s", cs.Image),
		fmt.Sprintf("waitingReason=%s", cs.State.Waiting.Reason),
	}

	for _, e := range events {
		if e.Reason != "Failed" {
			continue
		}
		cause, ok := pattern.ImagePullFailure(e.Message)
		if !ok {
			continue
		}
		evidence := append(append([]string(nil), base...), fmt.Sprintf("evento: %s", e.Message))
		return d.findingForCause(cause, pod, cs, resource, evidence)
	}

	return d.undetermined(resource, base)
}

func (ImagePull) findingForCause(cause string, pod corev1.Pod, cs corev1.ContainerStatus, resource model.ResourceRef, evidence []string) model.Finding {
	image := cs.Image
	switch cause {
	case "tag-not-found":
		return model.Finding{
			CheckID:  string(CauseImagePullTagNotFound),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"il container %q di %s non riesce a tirare l'immagine %q: tag o digest inesistente sul registry",
				cs.Name, resource, image,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"verifica che %q esista con quel tag/digest sul registry: probabile refuso nel tag, o la build non e' ancora stata pubblicata",
					image,
				),
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "unauthorized":
		return model.Finding{
			CheckID:  string(CauseImagePullUnauthorized),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"il container %q di %s non riesce a tirare l'immagine %q: il registry rifiuta il pull per autenticazione o autorizzazione mancante",
				cs.Name, resource, image,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("aggiungi o rinnova imagePullSecrets sul ServiceAccount o sul Pod per il registry di %q", image),
				Commands:         []string{fmt.Sprintf("kubectl get pod %s -n %s -o jsonpath={.spec.imagePullSecrets}", pod.Name, pod.Namespace)},
				ContextDependent: true,
			},
			Resource: resource,
		}
	case "registry-unreachable":
		return model.Finding{
			CheckID:  string(CauseImagePullRegistryUnreachable),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"il container %q di %s non riesce a tirare l'immagine %q: il registry non e' raggiungibile dal cluster (DNS, rete o timeout)",
				cs.Name, resource, image,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary:          fmt.Sprintf("verifica la raggiungibilita' del registry che serve %q dal nodo (DNS, network policy, proxy) o lo stato del registry stesso", image),
				ContextDependent: true,
			},
			Resource: resource,
		}
	default:
		// Non dovrebbe accadere: pattern.ImagePullFailure restituisce
		// solo queste tre stringhe quando ok=true. Se succede e' un bug
		// interno di allineamento tra i due package, non una condizione
		// del cluster: torna undetermined invece di un finding con
		// CheckID inventato.
		return ImagePull{}.undetermined(resource, evidence)
	}
}

func (ImagePull) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CauseImagePullUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s non riesce a tirare l'immagine ma nessun evento riconosciuto ne spiega il motivo", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "raccogli piu' contesto: describe del pod per il testo completo dell'evento di pull",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
