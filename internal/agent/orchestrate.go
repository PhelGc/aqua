package agent

import (
	"context"
	"fmt"

	"aqua/internal/llm"
	"aqua/internal/marker"
	"aqua/internal/orchestrator"
)

// DispatchOrchestrate despacha el marker al adaptador correspondiente según kind.
// Cada adaptador devuelve (path-del-artifact, summary-texto, err).
func (a *Agent) DispatchOrchestrate(ctx context.Context, m marker.Marker) (artifact, summary string, err error) {
	switch m.Kind {
	case "report":
		return a.RunReport(ctx, m.Payload)
	case "schedule":
		return a.RunScheduleAdapter(m.Payload)
	default:
		return "", "", fmt.Errorf("kind de orchestrate desconocido: %q", m.Kind)
	}
}

// RunIsolated procesa un Job en un worker: agente con history limpia que reusa
// http y mcp del agente principal. No tiene skills ni sessions. El label del
// worker es el JobID para que los tool-calls en los logs se identifiquen.
// El sink de eventos se hereda para que los eventos del worker fluyan al UI.
func (a *Agent) RunIsolated(ctx context.Context, j orchestrator.Job) (string, error) {
	worker := &Agent{
		endpoint:    a.endpoint,
		model:       a.model,
		apiKey:      a.apiKey,
		personality: a.personality,
		http:        a.http,
		mcp:         a.mcp,
		label:       j.ID(),
		events:      a.events,
	}
	for _, sys := range j.System() {
		worker.history = append(worker.history, llm.Message{Role: "system", Content: sys})
	}
	worker.history = append(worker.history, llm.Message{Role: "user", Content: j.Prompt()})
	return worker.RunConversation(ctx, &worker.history)
}

// RunPool delega en orchestrator.Run inyectando como executor el RunIsolated
// del agente y el sink de eventos. Existe principalmente para que los callers
// del agente (reports, etc.) no tengan que conocer el orquestador.
func (a *Agent) RunPool(ctx context.Context, jobs []orchestrator.Job, opts orchestrator.PoolOptions) []orchestrator.Result {
	if opts.Execute == nil {
		opts.Execute = a.RunIsolated
	}
	if opts.Sink == nil {
		opts.Sink = a.events
	}
	return orchestrator.Run(ctx, jobs, opts)
}
