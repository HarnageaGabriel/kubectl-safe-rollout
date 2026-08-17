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

package check

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// PDBCheckID e' l'identificativo stabile di questa verifica, usato
// nell'output json e per il gating in CI su singole regole.
const PDBCheckID = "pdb-consistency"

// PDBConsistency verifica che i PodDisruptionBudget che coprono un
// workload lascino effettivamente margine di disruption.
//
// Nota tecnica deliberata: un PDB e' applicato dalla Eviction API (usata
// da `kubectl drain`, dal cluster-autoscaler, dal descheduler), non dal
// controller del Deployment quando sostituisce i pod durante un rolling
// update. Questa verifica non afferma quindi che il PDB blocchi
// `kubectl rollout` di per se': afferma che, se un drenaggio di nodo o
// una manutenzione capitano durante la finestra del rollout, restano
// bloccati finche' il budget non si libera. E' lo scenario descritto nel
// brief del progetto ed e' la causa reale piu' comune di rollout che
// "sembrano" incastrati.
type PDBConsistency struct{}

// ID implementa check.Check.
func (PDBConsistency) ID() string { return PDBCheckID }

// Run implementa check.Check.
func (c PDBConsistency) Run(ctx context.Context, target Target) (Result, error) {
	pdbList, err := target.Client.PolicyV1().PodDisruptionBudgets(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("lista PodDisruptionBudget non accessibile: %v", err)), nil
	}

	replicas := target.Workload.Replicas()
	podLabels := labels.Set(target.Workload.PodLabels())
	strategy := target.Workload.UpdateStrategy()

	var findings []model.Finding
	for _, pdb := range pdbList.Items {
		selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil || selector.Empty() || !selector.Matches(podLabels) {
			continue
		}

		allowed, mode, err := allowedDisruptions(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable, replicas)
		if err != nil {
			// PDB con Spec priva sia di MinAvailable che di
			// MaxUnavailable non e' un oggetto valido lato apiserver:
			// non dovrebbe succedere su un cluster reale, ma se
			// succede non e' compito di questa verifica segnalarlo.
			continue
		}

		resource := model.ResourceRef{Kind: "PodDisruptionBudget", Namespace: pdb.Namespace, Name: pdb.Name}
		workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

		if allowed <= 0 {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityHigh,
				Cause: fmt.Sprintf(
					"il PodDisruptionBudget %q non lascia margine di disruption (%s calcolato su %d repliche): un drenaggio di nodo concorrente al rollout di %s restera' bloccato finche' i pod non sono di nuovo pronti",
					pdb.Name, mode, replicas, workloadRef,
				),
				Evidence: []string{
					fmt.Sprintf("%s=%s", mode, pdbSpecValue(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable)),
					fmt.Sprintf("replicas=%d", replicas),
					fmt.Sprintf("disruptionsAllowed=%d", allowed),
				},
				Remediation: model.Remediation{
					Summary: fmt.Sprintf(
						"aumenta il margine del PDB (es. maxUnavailable: 1 o minAvailable: %d) oppure aumenta le repliche di %s; il valore corretto dipende da quante repliche puoi permetterti di perdere in produzione",
						replicas-1, workloadRef,
					),
					ContextDependent: true,
				},
				Resource: resource,
			})
			continue
		}

		if strategy.Type == workload.Recreate && allowed < replicas {
			findings = append(findings, model.Finding{
				CheckID:  c.ID(),
				Severity: model.SeverityHigh,
				Cause: fmt.Sprintf(
					"%s usa strategia Recreate (tutti i pod vengono terminati insieme) ma il PodDisruptionBudget %q permette solo %d disruption su %d repliche: una manutenzione di nodo durante il rollout si bloccherebbe comunque",
					workloadRef, pdb.Name, allowed, replicas,
				),
				Evidence: []string{
					"strategy=Recreate",
					fmt.Sprintf("%s=%s", mode, pdbSpecValue(pdb.Spec.MinAvailable, pdb.Spec.MaxUnavailable)),
					fmt.Sprintf("disruptionsAllowed=%d", allowed),
				},
				Remediation: model.Remediation{
					Summary:          fmt.Sprintf("passa %s a strategia RollingUpdate, oppure allarga il PDB a maxUnavailable pari alle repliche totali se Recreate e' una scelta deliberata", workloadRef),
					ContextDependent: true,
				},
				Resource: resource,
			})
		}
	}

	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// allowedDisruptions calcola quanti pod il PDB permette di rendere
// indisponibili contemporaneamente. Il roundUp riflette il comportamento
// del controller PDB in-tree: minAvailable arrotonda per eccesso (piu'
// prudente sul numero di pod da mantenere disponibili), maxUnavailable
// arrotonda per difetto (piu' prudente sul numero di disruption concesse).
func allowedDisruptions(minAvailable, maxUnavailable *intstr.IntOrString, replicas int32) (allowed int32, mode string, err error) {
	switch {
	case minAvailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(minAvailable, int(replicas), true)
		if err != nil {
			return 0, "", err
		}
		allowed = replicas - int32(v)
		mode = "minAvailable"
	case maxUnavailable != nil:
		v, err := intstr.GetScaledValueFromIntOrPercent(maxUnavailable, int(replicas), false)
		if err != nil {
			return 0, "", err
		}
		allowed = int32(v)
		mode = "maxUnavailable"
	default:
		return 0, "", fmt.Errorf("PDB privo sia di minAvailable che di maxUnavailable")
	}
	if allowed < 0 {
		allowed = 0
	}
	return allowed, mode, nil
}

func pdbSpecValue(minAvailable, maxUnavailable *intstr.IntOrString) string {
	if minAvailable != nil {
		return minAvailable.String()
	}
	if maxUnavailable != nil {
		return maxUnavailable.String()
	}
	return "<unset>"
}
