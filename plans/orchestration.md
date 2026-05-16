# Plan: Sistema de orquestación con workers paralelos

## Objetivo

Crear un **primitivo de orquestación reutilizable** que permita descomponer una tarea grande en N subtareas independientes, ejecutarlas en paralelo con un pool de workers aislados, y consolidar los resultados.

Reportes batch es **el primer caso de uso**, pero la arquitectura aplica a cualquier escenario donde:
- Hay N items que requieren el mismo tipo de procesamiento.
- Cada item se puede procesar de forma independiente.
- El tiempo total importa y el paralelismo lo reduce.

### Casos de uso previstos

| Caso | Input | Worker hace | Output consolidado |
|------|-------|-------------|--------------------|
| **Reporte de tickets** (referencia) | filtro Jira | evalúa 1 ticket con `evaluar-tarea` | reporte `.md` |
| Clasificación masiva | lista de incidencias sin tipo | aplica `clasificar-incidencia` | tabla de clasificaciones |
| Propuesta de títulos | lista de tickets sin título DoR | aplica `proponer-titulo` | sugerencias por ticket |
| Resumen de documentos | N archivos / URLs | resume 1 doc | índice de resúmenes |
| Validación batch | lista de KEYs | aplica `validar-incidencia` | reporte de issues que no cumplen DoR/DoD |
| Análisis de PRs | N PR URLs | review de 1 PR | dashboard de review |

El patrón es el mismo: **fan-out → trabajo aislado → fan-in**.

---

## Decisiones tomadas

| # | Decisión | Valor |
|---|----------|-------|
| 1 | Granularidad del worker | Configurable. Default: pool=5, items-por-worker=1 |
| 2 | Aislamiento de workers | Sí, cada worker arranca con historial limpio (personalidad + prompt específico de la tarea) |
| 3 | MCP compartido vs por-worker | **Compartido**. El SDK go (`modelcontextprotocol/go-sdk` v1.6.0) es thread-safe: `Connection` multiplexa por JSON-RPC ID y `writeMu` serializa escrituras internamente. Verificado en `transport.go:365` y `internal/jsonrpc2/conn.go:273`. Sin mutex propio. |
| 4 | Rate limits OpenCode Zen | No hay límites públicos. Pool configurable + retry con backoff exponencial sobre 429. Mantener headers `x-opencode-*` del CLI oficial para no caer en bucket anónimo |
| 5 | Progreso visible | Sí. En Discord editar un mensaje cada N segundos con contador. En terminal, log a stdout |
| 6 | Fallos parciales | No abortan el batch. Item fallido se marca "no procesado" con motivo en el output |
| 7 | Modelo | `deepseek-v4-flash` para workers y orquestador. Swappable vía config en fase futura |
| 8 | Forma del marker | **XML** con tag `<orchestrate kind="...">...</orchestrate>`. Robusto si el LLM agrega texto alrededor |
| 9 | Detalle del item | **Pre-fetched**. El orquestador trae todos los campos necesarios en la llamada inicial y se los pasa al worker en el prompt. Solo si el worker necesita algo puntual extra hace una llamada con su tool |
| 10 | Identidad del worker | **Instancia completa de `agent` por worker** con `httpClient` y `mcp` compartidos por puntero + `history` propia. Memoria despreciable, código trivial |
| 11 | Resultados | **Ordenados**. `RunPool` devuelve `[]Result` cuando todos terminan, en el mismo orden que los `Job` recibidos |
| 12 | Marker single/multi | **Single** con campo `kind`. Un solo parser y dispatcher; cada caso registra su adaptador con su `kind` |

---

## Arquitectura

### Capa 1 — Primitivo genérico (`orchestrator.go`)

```go
// Job es una tarea procesable por un worker.
type Job interface {
    ID() string          // identificador único para logs y resultados
    Prompt() string      // qué le decimos al LLM
    System() []string    // mensajes system extra (además de personalidad)
}

// Result es el output de procesar un Job.
type Result struct {
    JobID   string
    Output  string        // respuesta cruda del worker
    Err     error         // nil si OK
    Retries int
    Elapsed time.Duration
}

// PoolOptions configura una corrida.
type PoolOptions struct {
    Size            int           // workers simultáneos
    MaxRetries      int           // reintentos antes de marcar fallo
    BackoffBase     time.Duration // base del backoff exponencial
    OnProgress      func(done, total int) // callback opcional
    PerJobTimeout   time.Duration
}

// RunPool ejecuta los jobs en paralelo y devuelve los resultados en el mismo orden.
func (a *agent) RunPool(ctx context.Context, jobs []Job, opts PoolOptions) []Result
```

Características:
- Cada worker es un `agent` con `httpClient` y `mcp manager` compartidos, pero `history` propia.
- Workers se reciclan dentro del pool (un worker puede procesar varios jobs secuencialmente si N items > N workers).
- Errores de un job no abortan los demás.
- `OnProgress` se llama cada vez que un job termina (éxito o fallo).
- Backoff exponencial sobre `429`/timeouts dentro del worker.

### Capa 2 — Adaptadores por caso de uso

Cada caso de uso provee:
- Cómo armar los `Job` desde el input del usuario.
- Cómo consolidar los `[]Result` en un output final.

```
reports.go    -> ReportJob, runReport()         consolida -> .md en reports/
classify.go   -> ClassifyJob, runClassify()     consolida -> tabla
summarize.go  -> SummarizeJob, runSummarize()   consolida -> índice
...
```

Solo `orchestrator.go` es obligatorio en fase 1. Los adaptadores se agregan a medida que aparezcan casos.

### Capa 3 — Activación

El orquestador se dispara cuando el LLM emite un marker XML en su respuesta:

```
<orchestrate kind="report">
{"jql": "...", "descripcion": "...", "max": 10, "fields": ["summary", "status", "assignee"]}
</orchestrate>
```

El runtime (terminal o Discord) intercepta el bloque, parsea el JSON interno, despacha al adaptador correspondiente según `kind`, y devuelve el resultado.

Skills como `/reporte`, `/clasificar-batch`, etc. instruyen al LLM a emitir el marker apropiado.

---

```
                Usuario (Discord / Terminal)
                          |
                  /reporte "filtro" (u otro skill)
                          |
                          v
                   agent.send() -- LLM emite <orchestrate kind="...">
                          |
                          v
                   runtime detecta marker
                          |
                          v
                  +----------------+
                  |  Adaptador     |  ej. runReport()
                  +----------------+
                          |
              prep:       v          fan-out:
            arma []Job          agent.RunPool(jobs, opts)
                                     |
                                     v
                             +-----------+
                             | Worker 1  |   cada worker:
                             | Worker 2  |   - history propia
                             | Worker 3  |   - mismos MCP/httpClient
                             | Worker 4  |   - retry + backoff propio
                             | Worker 5  |
                             +-----------+
                                     |
                                     v
                              []Result
                                     |
              fan-in:                v
              consolidar a output final (md, tabla, json, lo que toque)
                                     |
                                     v
                       responder al usuario con summary + artefacto
```

---

## Configuración (env vars)

Globales del orquestador:

```
ORCH_POOL_SIZE=5             # workers simultáneos default
ORCH_MAX_RETRIES=2           # reintentos por job
ORCH_BACKOFF_BASE=200ms      # base del backoff exponencial
ORCH_PER_JOB_TIMEOUT=2m      # timeout por job individual
ORCH_PROGRESS_INTERVAL=3s    # cada cuánto refrescar progreso
```

Específicas por caso de uso (ej. reportes):

```
REPORT_MAX_TICKETS=50        # tope duro de items por reporte
REPORT_OUTPUT_DIR=reports
```

Defaults razonables embebidos; env vars sobreescriben; skill puede overridear via params.

---

## Estructura del worker (genérico)

Al inicializar cada worker:

```
[system] <personalidad.md>                        ← común a todos
[system] <Job.System()...>                        ← prompt específico del caso
[user]   <Job.Prompt()>                           ← input concreto del item
```

El worker:
- Tiene las mismas tools MCP que el agente principal.
- `maxToolIterations` = 8 (configurable).
- Su contexto está acotado al item — no ve la conversación del usuario.
- Persiste `reasoning_content` entre rounds (DeepSeek requirement).
- Backoff exponencial interno sobre fallos transitorios.

---

## Manejo de errores

| Error | Acción |
|-------|--------|
| Prep falla (ej. no se puede traducir filtro a JQL) | Abortar antes de fan-out, devolver error al usuario |
| Búsqueda inicial falla (ej. `jira_search`) | Abortar antes de fan-out |
| Worker falla en 1 job | Reintentar hasta `ORCH_MAX_RETRIES` con backoff. Si sigue fallando, marcar "no procesado" con el motivo |
| Rate limit 429 | Backoff exponencial dentro del worker (base × 2^n con jitter) |
| Tope `REPORT_MAX_TICKETS` excedido | Truncar y avisar en el output ("se procesaron primeros N de M") |
| Discord timeout | Subir `discordRequestTimeout` a 30min cuando hay orquestación corriendo. Editar mensaje de progreso para indicar que sigue vivo |
| Cancelación del contexto | Workers en vuelo respetan `ctx.Done()` y abortan limpio |

---

## Progreso visible

### Discord
Mensaje editado cada `ORCH_PROGRESS_INTERVAL`:

```
Procesando...
Progreso: 4/10
Workers activos: 5
```

Al terminar, edita una última vez con summary y adjunta el artefacto si aplica.

### Terminal
Una línea por job terminado al stdout:

```
[orch] 1/10 GI-1234 ok (12s)
[orch] 2/10 GI-1235 ok (9s)
[orch] 3/10 GI-1236 fail: timeout, retry 1
...
```

---

## Fases de implementación

### Fase 1 — Primitivo + primer caso (esta rama)

**Core orquestación**:
- [ ] `orchestrator.go`: `Job`, `Result`, `PoolOptions`, `RunPool()`
- [ ] Tests unitarios del pool (jobs sintéticos, sin LLM)
- [ ] Integración del marker `__ORCHESTRATE__` en el runtime (`main.go` + `discord.go`)

**Primer caso — reportes**:
- [ ] `reports.go`: `ReportJob`, `runReport()`, escritura de `.md`
- [ ] `skills/reporte.md`: prompt que extrae `{jql, descripcion, max_items}` y emite el marker
- [ ] `.gitignore`: agregar `reports/`
- [ ] Discord adjunta el `.md` resultante
- [ ] Smoke test con 3-5 tickets reales

**Mínimo viable de progreso**:
- [ ] Logs en terminal (sin editar mensajes de Discord aún)

### Fase 2 — Progreso, robustez, casos adicionales (rama separada)
- [ ] Edición de mensaje de progreso en Discord cada N segundos
- [ ] Backoff exponencial completo con jitter
- [ ] Verificar/agregar headers `x-opencode-*` en el HTTP client
- [ ] Segundo caso de uso (ej. `runClassify` o `runValidate`) para ejercitar la generalidad del primitivo

### Fase 3 — Programación temporal (rama separada, futura)
- [ ] `-mode oneshot -prompt "..."` para Task Scheduler
- [ ] `schedules.yaml` o similar
- [ ] Webhook Discord para notificar reportes programados

---

## Lo que NO se hace en esta fase

- Periodicidad / scheduling (fase 3).
- Múltiples adaptadores (solo reportes). Los demás son aspiración del diseño, no compromiso de implementación.
- Persistencia histórica en DB (artefactos viven como archivos).
- UI propia (todo es markdown / texto).
- Modelo distinto entre orquestador y workers.

---

## Preguntas abiertas

Ninguna pendiente de respuesta para arrancar fase 1.

---

## Referencias técnicas

- **MCP SDK thread-safety**: `transport.go:365` (writeMu), `internal/jsonrpc2/conn.go:273` (atomic ID + map de outgoing calls). Compartir el `ClientSession` entre N goroutines es seguro.
- **OpenCode Zen rate limits**: no documentados. Manejar con backoff cliente-side. Mantener headers `x-opencode-client`, `x-opencode-session`, `x-opencode-project`, `x-opencode-request`, `User-Agent`.
- **DeepSeek `reasoning_content`**: ya manejado en `main.go` (`message.ReasoningContent` preservado en rounds de tool-call). Aplica idéntico a workers.
