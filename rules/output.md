# Convenzioni di output

- **Due formati, un solo Report**: check e diagnose convertono i propri
  Result in `output.Group`; `internal/output.Report` alimenta entrambi i
  renderer (`RenderHuman`, `RenderJSON`). Nessun renderer separato per
  `watch`.
- **Umano di default, `--output json` esplicito**: mai il contrario. Chi
  lancia il comando a mano in un incidente non deve leggere JSON.
- **Ordine per severita' decrescente**: sempre High prima di Low, in
  entrambi i formati. Chi guarda sotto pressione legge le prime righe.
- **`Skipped` e' visibile quanto un finding**, non un dettaglio silente:
  l'output umano lo stampa come riga `SKIP` con il motivo, mai lo
  omette. Un check che tace perche' non ha potuto valutare non deve
  sembrare un check che ha valutato e trovato tutto ok.
- **Nessun finding senza remediation concreta o senza
  `ContextDependent: true` dichiarato esplicitamente** (criterio di
  qualita' del progetto, vedi `internal/model/finding.go`). Non esiste
  un terzo caso in cui si stampa un comando "a titolo indicativo" senza
  il flag.
- **Incertezza esplicita**: evidenza insufficiente usa una CauseID
  `*-undetermined`, `Finding.Undetermined=true` ed elenca quanto osservato.
  Mai scegliere la causa piu' probabile.
- **Exit code**: non zero se e solo se esiste almeno un finding
  `SeverityHigh` nel report finale, indipendentemente dal formato di
  output. E' quello che rende `check` utilizzabile come gate in CI/CD.
  Severity Medium/Low non alterano l'exit code: servono a dare contesto,
  non a bloccare la pipeline.
- **Messaggi in italiano**, coerenti con il resto del progetto (README,
  commit, documentazione). Se in futuro serve i18n, e' un cambio deliberato
  con una issue dedicata, non una deriva graduale bilingue.
