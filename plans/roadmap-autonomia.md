# Roadmap a aqua "trabajadora casi autónoma"

Snapshot al **2026-05-16** después de cerrar fase 1 (orquestador) y fase 2 (UI + scheduler).

## Estado actual — objetivo

Aqua hoy es **una asistente reactiva muy competente**. Si vos le pedís algo, lo hace bien y rápido. Te ahorra ~3h/día porque sacó la fricción de tareas repetitivas (evaluación de tickets, prefetch de manuales, formato consistente, reportes batch, scheduler básico).

Diría que estás en **~40% del camino a "trabajadora autónoma"**.

### Lo que tiene fuerte

- **Observa**: lee Jira y manuales con `INDEX-first`, mantiene historial por sesión.
- **Evalúa**: aplica criterios con workers autónomos en paralelo (orquestador con pool).
- **Reporta**: artifacts limpios, persistidos, accesibles, formato consistente con red de seguridad anti-preámbulo.
- **Programa**: dispara cron + one-shot con aislamiento por sesión, persistencia en disco.
- **Visualiza**: dashboard web con SSE en tiempo real, drawer por worker con tool-log.
- **Personalizable**: skills como `.md` editables, personalidad ajustable, multiples MCPs.

### Lo que NO tiene y la mantiene en "asistente"

| Dimensión | Falta |
|-----------|-------|
| **Memoria** | No recuerda nada entre sesiones. Si la corrijo algo, mañana lo vuelve a hacer mal. |
| **Iniciativa** | Reacciona a lo que pide el usuario. No detecta sola "GI-X lleva 5 días sin avanzar". |
| **Acción** | Solo lee Jira. No crea tickets derivados, no comenta, no mueve estados. |
| **Notificación push** | Si un schedule termina mientras nadie mira, no llega aviso. |
| **Autocrítica** | No revisa su propio output. Confía en su primer veredicto. |
| **Memoria del usuario** | No construye perfil del usuario, su equipo, sus prioridades. |

---

## Roadmap 80/20 (priorizado por ROI)

### 🥇 1. Memoria persistente cruzada (1-2 días)

**Qué**: sistema tipo `memory/` con bullets de hechos, lecciones, preferencias. Cada turno aqua puede leer/escribir.

**Estructura propuesta**:
```
memory/
  team.md         — "Roy siempre olvida Story Points en TAREAS"
                  — "Roymar suele dejar valor perimetral sin declarar"
  feedback.md     — "cuando digo 'el sprint', siempre es el actual"
                  — "no abuses del rioplatense"
  projects.md     — "GI es Gestión Integral, todos los tickets ahí"
                  — "Mutual Ser, Salud Social, FUNDASER son clientes activos"
  user.md         — "Steven prefiere reportes breves"
                  — "horario de trabajo 9-18 Colombia"
```

**Cómo**: cargar al inicio del agente (igual que `personality.md`), exponerla como context fijo + tools para añadir/editar entradas. El propio modelo escribe cuando aprende algo nuevo.

**ROI**: ENORME. Resuelve el problema de "tengo que repetirle las mismas cosas todos los días". Salto cualitativo más grande por menos código.

---

### 🥈 2. Notificaciones push a Discord (medio día)

**Qué**: cuando un schedule termina, cuando aqua detecta algo crítico, cuando un reporte tiene >N rechazos → DM al usuario por Discord.

**Cómo**: el bot Discord ya está conectado. Falta abrir un canal de eventos del agente hacia el bot:

- Nuevo tipo de evento: `notify_user` con `{message, priority, attachment?}`.
- En modo discord, el bot está suscrito a esos eventos y DM-ea al usuario autorizado.
- En modo UI/terminal, los eventos quedan disponibles pero sin destino (o aparecen como toast).

**ROI**: convierte el scheduler de "feature técnico" a "asistente que te avisa cuando importa". Crucial para el sentimiento de "aqua está trabajando para mí".

---

### 🥉 3. Escritura en Jira con patrón preview/confirm (1-2 días)

**Qué**: habilitar acciones de escritura del MCP Jira (`add_comment`, `transition_issue`, `create_issue`, `link_issues`) con un guardrail human-in-the-loop.

**Patrón propuesto**:
```
/derivar GI-4711 "crear seguimiento para GI-4997 link faltante"
→ aqua propone el ticket en texto plano
→ usuario aprueba con /si o /ok
→ aqua lo crea y devuelve la KEY nueva
```

Implementación:
- Nuevo marker `<orchestrate kind="jira_action">` con payload describiendo la acción.
- Adapter `runJiraAction` que NO ejecuta directo: guarda en `pending/<id>.json`, devuelve "pendiente de aprobación".
- Skill `/si <id>` o `/aprobar <id>` para confirmar y ejecutar.
- Skill `/no <id>` para descartar.

**ROI**: pasa de **lectora a ejecutora**. Permite delegar "armá los seguimientos que faltan del sprint" y el usuario solo aprueba. Reduce más fricción que cualquier otro item de la lista.

---

### 4. Auditoría proactiva diaria (1 día)

**Qué**: skill `/auditar-sprint` que corre con `cron 0 8 * * 1-5` (días hábiles 8am) y notifica hallazgos por Discord.

**Lo que reportaría**:
- Tickets sin movimiento en N días
- Tickets cerrados sin Story Points (TAREAS)
- Vinculaciones rotas o ausentes
- Soportes con prioridad alta sin asignar
- Discrepancias entre tipo de incidencia y contenido

**Cómo**: cero infra nueva. Es un skill que emite `<orchestrate kind="report">` con criterios distintos, registrado como schedule. Las notificaciones del item #2 entregan el resultado.

**ROI**: aqua empieza el día contándote qué necesita atención. Pasa de "tengo que pedirle" a "ella me pide".

---

### 5. Verificación con segundo worker (medio día)

**Qué**: después de cada `/reporte`, un worker auditor revisa el output buscando contradicciones internas, hallazgos obvios omitidos, criterios mal aplicados, etc.

**Cómo**: en `runReport`, tras consolidar `[]Result`, despachar un job extra con `kind="report_audit"`:

```
<input al auditor>
- El reporte que estás auditando:
  [contenido del .md]
- Tu trabajo: identificar inconsistencias, omisiones, errores de criterio.
- Output: sección "Auditoría" que se appendea al final del .md.
```

**Costo**: ~$0.001 por reporte. Despreciable.

**ROI**: sube la confianza dramáticamente. Vos podés actuar sobre el reporte sin tener que validarlo manualmente. Especialmente útil para reportes que disparan acciones del item #3.

---

## Resumen del salto

| Hoy | Con items 1-3 | Con items 1-5 |
|-----|---------------|----------------|
| Asistente reactiva | Asistente proactiva con manos | Trabajadora casi autónoma |
| ~3h/día ahorradas | ~5-6h/día | 80% del workflow ideal |
| Vos preguntás siempre | Aqua te avisa cuando importa | Aqua decide y solo pide permiso para impacto |

**Esfuerzo total estimado**: 5-7 días de trabajo distribuidos.

**Riesgo**: bajo. Todos los items son extensiones de patrones que ya probamos (skills, markers, orquestador, sesiones, scheduler).

---

## Lo que NO entra en este roadmap (deferido conscientemente)

- **Multi-agente especializado** (revisor, proponedor, etc. con personalidades distintas): interesante pero costo > beneficio con la complejidad actual.
- **Más MCPs** (GitHub, Slack, calendar, gmail): valioso pero solo si emergen casos de uso concretos primero.
- **Dashboard de métricas históricas**: nice-to-have para confianza a largo plazo, no crítico para llegar al 80/20.
- **UI dedicada de app** (en lugar del browser): mencionada para más adelante. Por ahora la UI web cubre.

---

## Orden de implementación recomendado

1. **Memoria** primero, porque todo lo demás se beneficia de tenerla.
2. **Notificaciones** segundo, porque sin push, el resto sigue siendo reactivo.
3. **Escritura con confirm** tercero, porque es el cambio que más reduce fricción operativa.
4. **Auditoría proactiva** cuarto, combina los anteriores.
5. **Verificación** quinto, opcional según cuánta confianza necesite el usuario en los outputs.

---

**Próximo paso cuando se retome**: arrancar con memoria. Planear `plans/memory.md` con el diseño detallado antes de tirar código.
