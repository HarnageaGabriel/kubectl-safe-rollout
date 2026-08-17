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

// Package check implementa le verifiche pre-flight statiche di
// `kubectl safe-rollout check`. Ogni verifica vive nel proprio file e
// implementa l'interfaccia Check: questo permette di aggiungere nuove
// verifiche senza toccare le esistenti e di testarle in isolamento con
// client-go/kubernetes/fake.
package check

import (
	"context"

	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// Target raggruppa tutto cio' che una verifica puo' avere bisogno di
// leggere. E' costruito una sola volta per invocazione di `check` e
// condiviso tra tutte le verifiche, cosi' il costo delle List (PDB,
// ResourceQuota, ...) si puo' ammortizzare a monte se necessario.
//
// MetricsClient e' nil quando metrics-server non e' raggiungibile: le
// verifiche che ne hanno bisogno devono degradare a Result.Skipped,
// mai fallire l'intera esecuzione di `check`.
type Target struct {
	Namespace     string
	Workload      workload.Workload
	Client        kubernetes.Interface
	MetricsClient metricsv1beta1.MetricsV1beta1Interface
}

// Result e' l'esito dell'esecuzione di una singola Check. Skipped
// distingue "nessun problema trovato" (Findings vuoto, Skipped false) da
// "non sono riuscito a valutarlo" (Skipped true, SkipReason valorizzato):
// confonderli produrrebbe falsi negativi silenziosi, per esempio quando
// mancano permessi RBAC su ResourceQuota.
type Result struct {
	CheckID    string
	Findings   []model.Finding
	Skipped    bool
	SkipReason string
}

// Check e' l'interfaccia che ogni verifica pre-flight implementa. ID deve
// essere stabile tra versioni: e' la chiave usata nell'output json e nel
// gating in CI su singole regole.
type Check interface {
	ID() string
	Run(ctx context.Context, target Target) (Result, error)
}

// Skip costruisce un Result che dichiara esplicitamente l'impossibilita'
// di valutare la verifica, invece di un Result vuoto che sarebbe
// indistinguibile da "nessun problema".
func Skip(id, reason string) Result {
	return Result{CheckID: id, Skipped: true, SkipReason: reason}
}
