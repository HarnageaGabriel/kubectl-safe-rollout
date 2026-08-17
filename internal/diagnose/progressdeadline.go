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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// ProgressDeadlineDiagnoserID e' l'identificativo stabile della
// categoria, usato come Result.DiagnoserID.
const ProgressDeadlineDiagnoserID = "progress-deadline"

// ProgressDeadline riporta quando il controller del workload ha gia'
// concluso, per conto proprio, che il rollout non progredisce entro il
// proprio deadline. A differenza degli altri Diagnoser non c'e' un
// pattern da riconoscere: la condition letta da
// Workload.ProgressDeadlineExceeded() e' gia' la causa, non un sintomo
// da interpretare, quindi non esiste una variante "-undetermined" per
// questa categoria.
type ProgressDeadline struct{}

// ID implementa Diagnoser.
func (ProgressDeadline) ID() string { return ProgressDeadlineDiagnoserID }

// Diagnose implementa Diagnoser.
func (ProgressDeadline) Diagnose(_ context.Context, target Target) (Result, error) {
	message, exceeded := target.Workload.ProgressDeadlineExceeded()
	if !exceeded {
		return Result{DiagnoserID: ProgressDeadlineDiagnoserID}, nil
	}

	resource := model.ResourceRef{
		Kind:      target.Workload.Kind(),
		Namespace: target.Workload.Namespace(),
		Name:      target.Workload.Name(),
	}
	finding := model.Finding{
		CheckID:  string(CauseProgressDeadlineExceeded),
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"%s ha superato il proprio progressDeadlineSeconds: il controller ha concluso che il rollout non sta progredendo",
			resource,
		),
		Evidence: []string{fmt.Sprintf("condition Progressing/ProgressDeadlineExceeded: %s", message)},
		Remediation: model.Remediation{
			// Nessun Commands: un rollback (`kubectl rollout undo`) e'
			// un'azione con conseguenze reali, non un comando di sola
			// lettura come negli altri Diagnoser. Suggerirlo come
			// comando pronto normalizzerebbe un'azione che deve restare
			// una decisione esplicita di chi opera, non un copia-incolla.
			Summary:          "esamina i pod del rollout (probabilmente in CrashLoopBackOff, ImagePullBackOff o Pending: gli altri Diagnoser di questo comando li classificano separatamente) o valuta un rollback con `kubectl rollout undo` se il rilascio precedente era stabile",
			ContextDependent: true,
		},
		Resource: resource,
	}
	return Result{DiagnoserID: ProgressDeadlineDiagnoserID, Findings: []model.Finding{finding}}, nil
}
