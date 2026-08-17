# Licenza

- **Apache License 2.0**, testo completo e non modificato in
  [LICENSE](../LICENSE). Scelta per coerenza con l'ecosistema CNCF di cui
  questo progetto vuole far parte (Kubernetes, client-go, cli-runtime,
  krew-index e la quasi totalita' dei tool dell'ecosistema kubectl-plugin
  sono Apache 2.0): riduce l'attrito per chi vuole vendorizzare, contribuire o
  redistribuire il plugin insieme al resto del proprio toolchain.
- **Nessun file NOTICE.** Non e' richiesto dalla licenza a meno di
  ridistribuire un'opera derivata che gia' porta un proprio NOTICE (Sez.
  4(d) della licenza): questo repository non vendorizza ne' copia sorgenti
  di terze parti, le dipendenze restano esterne via `go.mod`/`go.sum` e
  portano la propria licenza per conto proprio. Se in futuro il progetto
  vendorizza codice di terzi con NOTICE proprio, questa decisione va
  rivista qui, non aggirata in silenzio.
- **Header di boilerplate in ogni file `.go`**, applicazione uniforme, non
  parziale: ogni file sorgente, compresi i test, porta l'header standard
  dell'Appendice della licenza (vedi in cima a qualsiasi file, es.
  [internal/model/finding.go](../internal/model/finding.go)). Enforced in
  CI dal linter `goheader` (vedi [.golangci.yml](../.golangci.yml)): un
  file nuovo senza header fa fallire `make lint`, non e' affidato alla
  disciplina di chi scrive il commit.
- **Anno nel header**: il template usa il placeholder `{{ YEAR }}` di
  `goheader`, non un anno letterale — un file scritto in un anno diverso
  da quello del primo commit resta valido senza toccare la config.
- **Copyright holder**: Gabriel Harnagea (autore del progetto). Se il
  progetto acquisisce co-autori con contributi sostanziali, e' una
  decisione da rivalutare esplicitamente, non da lasciare che il primo
  nome resti per inerzia.
