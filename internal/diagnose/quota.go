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

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/diagnose/pattern"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// QuotaDiagnoserID e' l'identificativo stabile della categoria, usato
// come Result.DiagnoserID.
const QuotaDiagnoserID = "quota"

// Quota classifica i fallimenti di creazione dei pod bloccati
// dall'admission plugin di ResourceQuota. E' l'unico Diagnoser che non
// opera sui Pod del workload: quando la quota blocca la creazione, il
// Pod non arriva mai a esistere, quindi non c'e' Pod da ispezionare. Il
// segnale vive sull'evento Reason=="FailedCreate" del ReplicaSet.
type Quota struct{}

// ID implementa Diagnoser.
func (Quota) ID() string { return QuotaDiagnoserID }

// Diagnose implementa Diagnoser.
func (d Quota) Diagnose(_ context.Context, target Target) (Result, error) {
	var findings []model.Finding
	for _, rs := range target.ReplicaSets {
		resource := model.ResourceRef{Kind: "ReplicaSet", Namespace: rs.Namespace, Name: rs.Name}
		for _, e := range target.EventsByUID[rs.UID] {
			if e.Reason != "FailedCreate" {
				continue
			}
			evidence := []string{fmt.Sprintf("evento: %s", e.Message)}
			if pattern.QuotaExceeded(e.Message) {
				findings = append(findings, model.Finding{
					CheckID:  string(CauseQuotaExceeded),
					Severity: model.SeverityHigh,
					Cause:    fmt.Sprintf("%s non riesce a creare pod: la ResourceQuota del namespace e' esaurita", resource),
					Evidence: evidence,
					Remediation: model.Remediation{
						Summary:          "aumenta la ResourceQuota del namespace, riduci le request del workload, o libera capacita' terminando altri workload; la scelta corretta dipende da cosa il namespace ospita e da chi ne ha autorita'",
						Commands:         []string{fmt.Sprintf("kubectl describe resourcequota -n %s", rs.Namespace)},
						ContextDependent: true,
					},
					Resource: resource,
				})
				continue
			}
			findings = append(findings, model.Finding{
				CheckID:  string(CauseQuotaUndetermined),
				Severity: model.SeverityHigh,
				Cause:    fmt.Sprintf("%s non riesce a creare pod, ma il messaggio dell'evento non menziona una ResourceQuota superata", resource),
				Evidence: evidence,
				Remediation: model.Remediation{
					Summary:          "leggi il messaggio completo dell'evento FailedCreate: potrebbe essere un webhook di ammissione o un altro vincolo, non necessariamente una quota",
					Commands:         []string{fmt.Sprintf("kubectl describe replicaset %s -n %s", rs.Name, rs.Namespace)},
					ContextDependent: true,
				},
				Resource:     resource,
				Undetermined: true,
			})
		}
	}
	return Result{DiagnoserID: d.ID(), Findings: findings}, nil
}
