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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/workload"
)

// QuotaHeadroomCheckID e' l'identificativo stabile di questa verifica.
const QuotaHeadroomCheckID = "quota-headroom"

// QuotaHeadroom verifica che le ResourceQuota del namespace lascino
// margine sufficiente per il surge del rolling update: numero di pod
// (chiave "pods") e CPU/memoria richieste dai pod aggiuntivi (chiavi
// "cpu"/"requests.cpu", "memory"/"requests.memory"). Se il margine non
// basta, il surge non riesce a partire e il rollout resta bloccato con
// pod Pending, molto prima che si arrivi a diagnosticarlo in `watch`.
//
// Semplificazioni deliberate, coerenti con l'MVP: una ResourceQuota con
// uno scopeSelector (es. limitata ai pod BestEffort) viene trattata come
// se si applicasse comunque a tutti i pod del namespace — un falso
// positivo occasionale costa meno di un margine sovrastimato lasciato
// silenzioso. PodRequests somma solo i container regolari, non gli init
// container (vedi workload.Workload.PodRequests).
type QuotaHeadroom struct{}

// ID implementa check.Check.
func (QuotaHeadroom) ID() string { return QuotaHeadroomCheckID }

// Run implementa check.Check.
func (c QuotaHeadroom) Run(ctx context.Context, target Target) (Result, error) {
	quotaList, err := target.Client.CoreV1().ResourceQuotas(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Skip(c.ID(), fmt.Sprintf("lista ResourceQuota non accessibile: %v", err)), nil
	}
	if len(quotaList.Items) == 0 {
		return Result{CheckID: c.ID()}, nil
	}

	surge, ok := surgeCount(target.Workload)
	if !ok {
		// Recreate, o maxSurge=0: nessun pod aggiuntivo da coprire.
		return Result{CheckID: c.ID()}, nil
	}

	perPod := target.Workload.PodRequests()
	workloadRef := fmt.Sprintf("%s/%s", target.Workload.Kind(), target.Workload.Name())

	var findings []model.Finding
	for _, q := range quotaList.Items {
		resourceRef := model.ResourceRef{Kind: "ResourceQuota", Namespace: q.Namespace, Name: q.Name}

		if f, ok := podCountFinding(q, resourceRef, workloadRef, surge); ok {
			findings = append(findings, f)
		}
		for _, resName := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if f, ok := computeFinding(q, resourceRef, workloadRef, surge, resName, perPod[resName]); ok {
				findings = append(findings, f)
			}
		}
	}
	return Result{CheckID: c.ID(), Findings: findings}, nil
}

// surgeCount calcola quanti pod in piu' del numero di repliche desiderate
// il rolling update puo' creare contemporaneamente. ok=false quando la
// strategia non fa surge (Recreate) o il calcolo risulta a zero: in
// entrambi i casi non c'e' headroom aggiuntivo da verificare.
func surgeCount(w workload.Workload) (int32, bool) {
	strategy := w.UpdateStrategy()
	if strategy.Type != workload.RollingUpdate || strategy.MaxSurge == nil {
		return 0, false
	}
	v, err := intstr.GetScaledValueFromIntOrPercent(strategy.MaxSurge, int(w.Replicas()), true)
	if err != nil || v <= 0 {
		return 0, false
	}
	return int32(v), true
}

func podCountFinding(q corev1.ResourceQuota, resourceRef model.ResourceRef, workloadRef string, surge int32) (model.Finding, bool) {
	hard, used, ok := headroom(q, corev1.ResourcePods, "")
	if !ok {
		return model.Finding{}, false
	}
	available := hard.Value() - used.Value()
	if available >= int64(surge) {
		return model.Finding{}, false
	}
	return model.Finding{
		CheckID:  QuotaHeadroomCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"il rolling update di %s puo' creare fino a %d pod in piu' durante il surge, ma la ResourceQuota %q lascia margine per solo %d pod aggiuntivi (hard=%s, used=%s)",
			workloadRef, surge, q.Name, available, hard.String(), used.String(),
		),
		Evidence: []string{
			fmt.Sprintf("surge=%d", surge),
			fmt.Sprintf("quota pods hard=%s used=%s", hard.String(), used.String()),
		},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("aumenta la quota \"pods\" di %q, riduci maxSurge di %s, o libera capacita' terminando altri workload; la scelta corretta dipende da chi condivide il namespace", q.Name, workloadRef),
			ContextDependent: true,
		},
		Resource: resourceRef,
	}, true
}

func computeFinding(q corev1.ResourceQuota, resourceRef model.ResourceRef, workloadRef string, surge int32, resName corev1.ResourceName, perPod resource.Quantity) (model.Finding, bool) {
	hard, used, ok := headroom(q, resName, "requests.")
	if !ok || perPod.IsZero() {
		return model.Finding{}, false
	}
	available := hard.DeepCopy()
	available.Sub(used)
	needed := *resource.NewMilliQuantity(perPod.MilliValue()*int64(surge), perPod.Format)
	if available.Cmp(needed) >= 0 {
		return model.Finding{}, false
	}
	return model.Finding{
		CheckID:  QuotaHeadroomCheckID,
		Severity: model.SeverityHigh,
		Cause: fmt.Sprintf(
			"il rolling update di %s richiede fino a %s di %s aggiuntivi durante il surge (%d pod), ma la ResourceQuota %q lascia margine per solo %s",
			workloadRef, needed.String(), resName, surge, q.Name, available.String(),
		),
		Evidence: []string{
			fmt.Sprintf("surge=%d perPodRequest.%s=%s", surge, resName, perPod.String()),
			fmt.Sprintf("quota %s hard=%s used=%s", resName, hard.String(), used.String()),
		},
		Remediation: model.Remediation{
			Summary:          fmt.Sprintf("aumenta la quota di %s su %q, riduci maxSurge di %s, o riduci le request del container; la scelta corretta dipende da quanta capacita' extra il namespace puo' offrire durante un rollout", resName, q.Name, workloadRef),
			ContextDependent: true,
		},
		Resource: resourceRef,
	}, true
}

// headroom legge la coppia hard/used di una ResourceQuota per una data
// risorsa, provando prima la chiave con prefisso (es. "requests.cpu")
// e poi l'alias senza prefisso (es. "cpu", equivalente per le quota):
// non tutte le ResourceQuota usano lo stesso stile di chiave. ok=false
// se la quota non vincola affatto questa risorsa.
func headroom(q corev1.ResourceQuota, name corev1.ResourceName, prefix string) (hard, used resource.Quantity, ok bool) {
	keys := []corev1.ResourceName{name}
	if prefix != "" {
		keys = []corev1.ResourceName{corev1.ResourceName(prefix + string(name)), name}
	}
	for _, k := range keys {
		if h, exists := q.Status.Hard[k]; exists {
			u := q.Status.Used[k]
			return h, u, true
		}
	}
	return resource.Quantity{}, resource.Quantity{}, false
}
