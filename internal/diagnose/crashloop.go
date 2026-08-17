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

// CrashLoopDiagnoserID e' l'identificativo stabile della categoria,
// usato come Result.DiagnoserID (non come model.Finding.CheckID: quello
// porta la CauseID specifica, vedi cause.go).
const CrashLoopDiagnoserID = "crashloop"

// CrashLoop classifica i container in CrashLoopBackOff distinguendo
// OOMKill, terminazione da liveness probe ed errore applicativo.
//
// Ordine di precedenza deliberato: l'evento di liveness probe si
// controlla prima di ContainerStatus.LastTerminationState, perche'
// quando e' la probe a uccidere il container il Reason del Terminated
// e' spesso il generico "Error", indistinguibile altrimenti da un crash
// applicativo. L'evento "Killing" con menzione della liveness probe e'
// il segnale piu' specifico disponibile e va verificato per primo.
type CrashLoop struct{}

// ID implementa Diagnoser.
func (CrashLoop) ID() string { return CrashLoopDiagnoserID }

// Diagnose implementa Diagnoser.
func (d CrashLoop) Diagnose(ctx context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, pod := range target.Pods {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting == nil || cs.State.Waiting.Reason != "CrashLoopBackOff" {
				continue
			}
			findings = append(findings, d.classify(ctx, target, pod, cs))
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}

func (d CrashLoop) classify(ctx context.Context, target Target, pod corev1.Pod, cs corev1.ContainerStatus) model.Finding {
	resource := model.ResourceRef{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}
	base := []string{
		fmt.Sprintf("container=%s", cs.Name),
		fmt.Sprintf("restartCount=%d", cs.RestartCount),
	}

	if f, ok := d.classifyLivenessKilling(target, pod, cs, resource, base); ok {
		return f
	}

	term := cs.LastTerminationState.Terminated
	switch {
	case term == nil:
		evidence := append(append([]string(nil), base...), fmt.Sprintf("container=%s in CrashLoopBackOff, LastTerminationState.Terminated non popolato", cs.Name))
		return d.undetermined(resource, evidence)
	case term.Reason == "OOMKilled":
		return d.classifyOOMKilled(pod, cs, term, resource, base)
	case term.Reason != "":
		return d.classifyAppError(ctx, target, pod, cs, term, resource, base)
	default:
		evidence := append(append([]string(nil), base...), fmt.Sprintf("container=%s in CrashLoopBackOff, Terminated.Reason vuoto", cs.Name))
		return d.undetermined(resource, evidence)
	}
}

func (CrashLoop) classifyLivenessKilling(target Target, pod corev1.Pod, cs corev1.ContainerStatus, resource model.ResourceRef, base []string) (model.Finding, bool) {
	for _, e := range target.EventsByUID[pod.UID] {
		if !pattern.LivenessKilling(e.Reason, e.Message, cs.Name) {
			continue
		}
		evidence := append(append([]string(nil), base...), fmt.Sprintf("evento: %s", e.Message))
		return model.Finding{
			CheckID:  string(CauseCrashLoopLivenessProbe),
			Severity: model.SeverityHigh,
			Cause: fmt.Sprintf(
				"il container %q di %s viene terminato dal kubelet perche' la liveness probe fallisce ripetutamente, non per un crash dell'applicazione",
				cs.Name, resource,
			),
			Evidence: evidence,
			Remediation: model.Remediation{
				Summary: fmt.Sprintf(
					"la liveness probe di %q termina il container prima che l'app sia pronta: aumenta initialDelaySeconds/periodSeconds/failureThreshold, o verifica che l'endpoint di liveness risponda correttamente durante l'avvio (valuta una startupProbe se l'avvio e' lento)",
					cs.Name,
				),
				ContextDependent: true,
			},
			Resource: resource,
		}, true
	}
	return model.Finding{}, false
}

func (CrashLoop) classifyOOMKilled(pod corev1.Pod, cs corev1.ContainerStatus, term *corev1.ContainerStateTerminated, resource model.ResourceRef, base []string) model.Finding {
	evidence := append(append([]string(nil), base...), fmt.Sprintf("exitCode=%d reason=OOMKilled", term.ExitCode))
	return model.Finding{
		CheckID:  string(CauseCrashLoopOOMKilled),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"il container %q di %s viene ucciso dal kernel (OOMKilled) per superamento del limite di memoria",
			cs.Name, resource,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary: fmt.Sprintf(
				"aumenta limits.memory del container %q o riduci il consumo di memoria dell'app; il valore corretto dipende dal profilo reale di utilizzo, non e' un numero sicuro da suggerire alla cieca",
				cs.Name,
			),
			ContextDependent: true,
		},
		Resource: resource,
	}
}

// classifyAppError e' l'unico ramo che recupera i log del container
// precedente come evidenza supplementare: e' il caso in cui la causa e'
// applicativa e non c'e' altro segnale strutturato oltre all'exit code,
// quindi e' dove i log aggiungono davvero valore. Il fetch e' best
// effort: se fallisce o LogTailer e' nil, il Finding resta valido senza
// quella riga.
func (CrashLoop) classifyAppError(ctx context.Context, target Target, pod corev1.Pod, cs corev1.ContainerStatus, term *corev1.ContainerStateTerminated, resource model.ResourceRef, base []string) model.Finding {
	evidence := append(append([]string(nil), base...), fmt.Sprintf("exitCode=%d reason=%s", term.ExitCode, term.Reason))
	if target.LogTailer != nil {
		if tail, err := target.LogTailer.PreviousLogTail(ctx, pod.Namespace, pod.Name, cs.Name, 3); err == nil && tail != "" {
			evidence = append(evidence, fmt.Sprintf("log --previous (ultime righe): %s", tail))
		}
	}
	return model.Finding{
		CheckID:  string(CauseCrashLoopAppError),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"il container %q di %s termina con causa applicativa (exit code %d), ne' OOMKill ne' liveness probe",
			cs.Name, resource, term.ExitCode,
		),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("controlla i log del container %q per la causa applicativa dell'uscita", cs.Name),
			Commands:         []string{fmt.Sprintf("kubectl logs %s -c %s -n %s --previous", pod.Name, cs.Name, pod.Namespace)},
			ContextDependent: true,
		},
		Resource: resource,
	}
}

func (CrashLoop) undetermined(resource model.ResourceRef, evidence []string) model.Finding {
	return model.Finding{
		CheckID:  string(CauseCrashLoopUndetermined),
		Severity: model.SeverityHigh,
		Cause:    fmt.Sprintf("%s e' in CrashLoopBackOff ma l'evidenza raccolta non distingue OOMKill, liveness probe o errore applicativo", resource),
		Evidence: evidence,
		Remediation: model.Remediation{
			Summary:          "raccogli piu' contesto prima di intervenire: describe del pod e log del container precedente",
			Commands:         []string{fmt.Sprintf("kubectl describe pod %s -n %s", resource.Name, resource.Namespace)},
			ContextDependent: true,
		},
		Resource:     resource,
		Undetermined: true,
	}
}
