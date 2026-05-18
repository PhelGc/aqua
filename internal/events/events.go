// Package events define el tipo Event y un Sink fanout reusable por las
// distintas interfaces (web SSE, futuras integraciones). El agente y el
// orchestrator publican; los transports consumen.
package events

import (
	"sync"
	"time"
)

// Event es un evento del runtime para visualización en vivo (UI mode).
// Los campos son JSON-friendly porque se serializan tal cual para SSE.
type Event struct {
	Type    string         `json:"type"`
	Time    time.Time      `json:"time"`
	JobID   string         `json:"job_id,omitempty"` // worker ID, "" = agente principal
	Payload map[string]any `json:"payload,omitempty"`
}

// Sink recibe eventos del agente y orquestador. Implementaciones deben
// ser concurrent-safe (los workers publican desde múltiples goroutines).
// nil = no emission (chat/discord modes funcionan igual sin sink).
type Sink interface {
	Publish(evt Event)
}

// FanoutSink implementa Sink haciendo fanout a todos los clientes
// suscriptos. Guarda un historial corto para que clientes que se conectan
// tarde puedan reconstruir el estado actual.
type FanoutSink struct {
	chanBuf int
	maxHist int

	mu      sync.Mutex
	clients map[chan Event]struct{}
	history []Event
}

// NewFanout crea un FanoutSink. chanBuf es el tamaño del buffer por cliente;
// maxHist es la cantidad máxima de eventos retenidos en el ring de historia.
func NewFanout(chanBuf, maxHist int) *FanoutSink {
	if chanBuf <= 0 {
		chanBuf = 128
	}
	if maxHist <= 0 {
		maxHist = 500
	}
	return &FanoutSink{
		chanBuf: chanBuf,
		maxHist: maxHist,
		clients: make(map[chan Event]struct{}),
	}
}

func (f *FanoutSink) Publish(evt Event) {
	f.mu.Lock()
	f.history = append(f.history, evt)
	if len(f.history) > f.maxHist {
		f.history = f.history[len(f.history)-f.maxHist:]
	}
	for ch := range f.clients {
		// Non-blocking: si el cliente está lento, dropeamos en vez de bloquear el publisher.
		select {
		case ch <- evt:
		default:
		}
	}
	f.mu.Unlock()
}

// Subscribe registra un cliente nuevo. Devuelve el canal por donde se
// reciben los eventos en vivo, una copia del historial actual, y un
// cleanup que el cliente debe ejecutar al desconectar.
func (f *FanoutSink) Subscribe() (<-chan Event, []Event, func()) {
	ch := make(chan Event, f.chanBuf)
	f.mu.Lock()
	f.clients[ch] = struct{}{}
	histCopy := make([]Event, len(f.history))
	copy(histCopy, f.history)
	f.mu.Unlock()
	cleanup := func() {
		f.mu.Lock()
		delete(f.clients, ch)
		f.mu.Unlock()
		close(ch)
	}
	return ch, histCopy, cleanup
}
