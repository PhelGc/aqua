package main

import "time"

// Event es un evento del runtime para visualización en vivo (UI mode).
// Los campos son JSON-friendly porque se serializan tal cual para SSE.
type Event struct {
	Type    string         `json:"type"`
	Time    time.Time      `json:"time"`
	JobID   string         `json:"job_id,omitempty"` // worker ID, "" = agente principal
	Payload map[string]any `json:"payload,omitempty"`
}

// EventSink recibe eventos del agente y orquestador. Implementaciones deben
// ser concurrent-safe (los workers publican desde múltiples goroutines).
// nil = no emission (chat/discord modes funcionan igual sin sink).
type EventSink interface {
	Publish(evt Event)
}

// emit es el helper interno. No-op si a.events es nil. Concurrent-safe siempre
// que la implementación de EventSink lo sea.
func (a *agent) emit(typ, jobID string, payload map[string]any) {
	if a.events == nil {
		return
	}
	a.events.Publish(Event{
		Type:    typ,
		Time:    time.Now(),
		JobID:   jobID,
		Payload: payload,
	})
}
