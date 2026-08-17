# Convenzioni Go

- **Modulo**: `github.com/HarnageaGabriel/kubectl-safe-rollout`. Import
  path assoluti sempre da qui, mai relativi.
- **`internal/` e' un confine architetturale, non solo una convenzione
  Go**: `internal/check`, `internal/diagnose`, `internal/remediate`,
  `internal/workload`, `internal/model` non importano `github.com/
  spf13/cobra`, non chiamano `os.Exit`, non scrivono su stdout/stderr
  direttamente. Solo `cmd/kubectl-safe_rollout/` e `internal/output/`
  possono farlo: il confine e' pulito apposta, cosi' una futura
  promozione a `pkg/` (per un Action o un admission controller) resta
  un mechanical move, non un refactor.
- **Interfacce sui client**, non tipi concreti: le funzioni che
  parlano con il cluster accettano `kubernetes.Interface` /
  `metricsv1beta1.MetricsV1beta1Interface`, mai `*kubernetes.Clientset`.
  E' quello che rende `client-go/kubernetes/fake` utilizzabile nei test
  senza wrapper aggiuntivi.
- **Degradazione, non panico**: se una risorsa non e' accessibile
  (RBAC insufficiente, metrics-server assente), la verifica restituisce
  `check.Skip(id, motivo)`, non un errore che interrompe l'intera
  esecuzione di `check`. Un errore da `Check.Run` e' riservato a bug
  interni (es. costruzione di un selector fallita per un bug nostro),
  non a condizioni attese del cluster.
- **Parsing Event isolato**: tutto lo string matching su `Event.Message`
  vive in `internal/diagnose/pattern`. I diagnoser usano prima campi
  strutturati (`Reason`, `ExitCode`, `PVC.Status.Phase`) e ricadono su
  `*-undetermined` se nessun pattern combacia.
- **Nessun default silenzioso su valori che il brief richiede di
  rendere visibili**: se un default Kubernetes viene applicato
  esplicitamente nel codice (es. `Replicas` nil -> 1, `MaxUnavailable`
  non impostato -> 25%), commentare perche', come in
  [internal/workload/workload.go](../internal/workload/workload.go).
- **Errori**: `fmt.Errorf("azione: %w", err)` con l'azione in italiano
  minuscolo, coerente col resto dei messaggi utente (vedi
  `rules/output.md`). Wrappare sempre, mai perdere l'errore originale.
- **`gofmt` e `go vet` puliti prima di ogni commit**: la CI fallisce
  altrimenti (vedi `.github/workflows/ci.yml`).
- **Niente `pkg/` finche' non serve davvero**: non anticipare la
  promozione a libreria pubblica spostando codice prematuramente. Il
  vincolo di non importare `cobra` in `internal/` e' sufficiente a
  tenersi pronti.
