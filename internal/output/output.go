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

// Package output rende in due formati (testo leggibile da umano di
// default, JSON stabile per l'uso in CI) l'esito di un'esecuzione di
// `check` o di `watch`. Il renderer non importa ne' internal/check ne'
// internal/diagnose: entrambi convertono il proprio Result verso
// output.Group, il tipo minimo che serve per rendere. E' quello che
// rende vero "un solo Report per entrambi i comandi" senza che questo
// package trascini client-go transitivamente solo per chi consuma il
// JSON.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/HarnageaGabriel/kubectl-safe-rollout/internal/model"
)

// Group e' cio' che check.Result e diagnose.Result hanno in comune:
// l'identificativo di chi ha prodotto l'esito, i Finding, e la
// distinzione tra "nessun problema" e "non sono riuscito a valutare".
// I chiamanti (cmd/) convertono i propri Result verso Group con un
// semplice field-copy: e' un adattatore deliberatamente senza logica,
// non un punto da estendere.
type Group struct {
	ID         string
	Findings   []model.Finding
	Skipped    bool
	SkipReason string
}

// Report e' la forma serializzabile di un insieme di Group,
// indipendente da check e diagnose per non forzare chi consuma solo il
// JSON a importare client-go transitivamente.
type Report struct {
	Status  string        `json:"status,omitempty"`
	Results []CheckReport `json:"results"`
}

// CheckReport e' la forma serializzabile di un singolo Group. Il nome
// del campo json "checkId" resta invariato anche per i Group prodotti
// da `watch` (dove l'ID e' una categoria di diagnose, es. "crashloop"):
// cambiarlo sarebbe una rottura di compatibilita' per chi consuma gia'
// l'output json di `check` senza un guadagno che la giustifichi.
type CheckReport struct {
	CheckID    string          `json:"checkId"`
	Skipped    bool            `json:"skipped"`
	SkipReason string          `json:"skipReason,omitempty"`
	Findings   []model.Finding `json:"findings"`
}

// NewReport converte i Group grezzi in Report. E' qui, non nel
// chiamante, che si decide come ordinare i finding: per severita'
// decrescente, cosi' l'output umano mostra prima cio' che conta di piu'
// indipendentemente dall'ordine di registrazione delle verifiche o
// delle classificazioni.
func NewReport(groups []Group) Report {
	r := Report{Results: make([]CheckReport, 0, len(groups))}
	for _, g := range groups {
		findings := append([]model.Finding(nil), g.Findings...)
		sort.SliceStable(findings, func(i, j int) bool {
			return findings[i].Severity > findings[j].Severity
		})
		r.Results = append(r.Results, CheckReport{
			CheckID:    g.ID,
			Skipped:    g.Skipped,
			SkipReason: g.SkipReason,
			Findings:   findings,
		})
	}
	return r
}

// MaxSeverity restituisce la severita' piu' alta tra tutti i finding del
// report, e ok=false se non ci sono finding. E' quello che il comando
// usa per decidere l'exit code.
func (r Report) MaxSeverity() (sev model.Severity, ok bool) {
	for _, cr := range r.Results {
		for _, f := range cr.Findings {
			if !ok || f.Severity > sev {
				sev = f.Severity
				ok = true
			}
		}
	}
	return sev, ok
}

// RenderJSON scrive il report come JSON indentato e stabile.
func RenderJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// RenderHuman scrive il report in un formato testuale pensato per essere
// letto sotto pressione: severita' piu' alta e causa in prima riga,
// evidenza e remediation indentate sotto.
func RenderHuman(w io.Writer, r Report) error {
	var err error
	// printf accumula il primo errore di scrittura invece di ignorarlo:
	// con un io.Writer (es. un file su disco pieno) fmt.Fprintf puo'
	// fallire, e propagarlo e' quello che rende questa funzione
	// affidabile per --output json e per test che intercettano errori.
	printf := func(format string, a ...any) {
		if err != nil {
			return
		}
		_, err = fmt.Fprintf(w, format, a...)
	}
	if r.Status == "succeeded" {
		printf("SUCCESS rollout completato\n")
		if len(r.Results) == 0 {
			return err
		}
	}

	anyFinding := false
	for _, cr := range r.Results {
		if cr.Skipped {
			printf("SKIP  %-24s %s\n", cr.CheckID, cr.SkipReason)
			continue
		}
		if len(cr.Findings) == 0 {
			printf("OK    %-24s nessun problema rilevato\n", cr.CheckID)
			continue
		}
		anyFinding = true
		for _, f := range cr.Findings {
			printf("%-5s %-24s %s\n", severityLabel(f.Severity), f.CheckID, f.Resource)
			if f.Undetermined {
				printf("      causa (non determinata): %s\n", f.Cause)
			} else {
				printf("      causa: %s\n", f.Cause)
			}
			for _, e := range f.Evidence {
				printf("      evidenza: %s\n", e)
			}
			printf("      rimedio: %s\n", f.Remediation.Summary)
			for _, cmd := range f.Remediation.Commands {
				printf("        $ %s\n", cmd)
			}
			if f.Remediation.ContextDependent {
				printf("      nota: il rimedio dipende dal contesto, valutare prima di applicare\n")
			}
		}
	}
	if !anyFinding {
		printf("\nnessun finding di severita' rilevante\n")
	}
	return err
}

func severityLabel(s model.Severity) string {
	switch s {
	case model.SeverityHigh:
		return "HIGH"
	case model.SeverityMedium:
		return "MED"
	default:
		return "LOW"
	}
}
