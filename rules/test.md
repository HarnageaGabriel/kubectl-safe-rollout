# Convenzioni di test

- **Ogni verifica in `internal/check` ha test unitari con
  `client-go/kubernetes/fake`**, non negoziabile (criterio di qualita'
  del progetto). Nessuna verifica si merge senza.
- **Package `_test` esterno** (`package check_test`, non `package
  check`): i test devono usare l'API pubblica del package come farebbe
  un chiamante reale (`cmd/kubectl-safe_rollout`), cosi' un refactor
  interno che rompe l'ergonomia esterna si vede subito.
- **Nomi dei test in italiano, descrittivi del comportamento atteso**:
  `TestPDBConsistency_MaxUnavailableZero_High`, non `TestCase1`. Devono
  restare leggibili come specifica quando l'implementazione cambia.
- **Un test per ogni severita' emessa e uno per il percorso "nessun
  finding"**: un check che ha solo test del caso positivo non prova che
  sappia anche tacere quando va tutto bene (falsi positivi sono un
  costo di credibilita' quanto i falsi negativi).
- **Un test per la degradazione**: se un check consulta una risorsa che
  puo' mancare (RBAC, metrics-server), simulare l'errore con un reactor
  su `fake.Clientset` (`PrependReactor`) e verificare `Result.Skipped`,
  non solo il percorso felice.
- **Copertura minima**: soglia verificata in CI, vedi
  `.github/workflows/ci.yml` per il valore corrente. La build fallisce
  sotto soglia. Aggiornare la soglia solo verso l'alto, e solo quando il
  nuovo codice la supera davvero (non abbassarla per far passare la CI).
- **E2E su kind**: uno scenario per ciascuna causa classificata da
  `watch` (criterio di qualita' del progetto), in `test/e2e/`. Isolati
  dal resto della suite con il build tag `e2e`: non girano in `go test
  ./...` ne' in CI (nessun cluster disponibile li), solo con `make
  test-e2e` contro un cluster kind attivo (`make kind-up`). Tutti gli 11
  scenari del set minimo passano su kind v0.32/Kubernetes v1.36.1. Ogni
  scenario crea un namespace usa-e-getta e lo elimina a fine test
  (`t.Cleanup`); usa
  sempre riferimenti a immagini/registry reali e reindirizzabili (Docker
  Hub, DNS che non risolve) per esercitare i messaggi di errore veri di
  containerd/kubelet/scheduler, mai una simulazione.
- **Diagnose**: ogni CauseID determinata e ogni fallback `*-undetermined`
  richiedono fixture realistiche di Pod, ContainerStatus, Event,
  ReplicaSet/PVC quando pertinenti, usando fake clientset.
- **Watch loop**: testare separatamente eventi Pod e Deployment. Il
  secondo e' necessario per `ProgressDeadlineExceeded`, che puo' cambiare
  senza alcun evento Pod.
