// Package orchestrator ejecuta jobs en paralelo con un pool de tamaño fijo,
// reintentos con backoff y emisión opcional de eventos.
package orchestrator

import (
	"context"
	"sync"
	"time"

	"aqua/internal/events"
)

// Job es una tarea procesable por un worker aislado.
type Job interface {
	ID() string       // identificador único para logs y resultados
	Prompt() string   // mensaje del usuario al worker
	System() []string // mensajes system adicionales (además de la personalidad)
}

// Result es el output de procesar un Job.
type Result struct {
	JobID   string
	Output  string
	Err     error
	Retries int
	Elapsed time.Duration
}

// ExecuteFunc procesa un Job y devuelve la respuesta cruda del worker.
// Default: agent.RunIsolated. Tests inyectan un stub.
type ExecuteFunc func(ctx context.Context, job Job) (string, error)

// PoolOptions configura una corrida del orquestador.
type PoolOptions struct {
	Size          int
	MaxRetries    int
	BackoffBase   time.Duration
	PerJobTimeout time.Duration
	// OnProgress se llama después de cada job terminado (éxito o fallo).
	// Recibe el contador, el total, y el Result del job recién completado.
	OnProgress func(done, total int, r Result)
	Execute    ExecuteFunc
	// Sink recibe eventos job_start/job_done. nil = sin emisión.
	Sink events.Sink
}

func (o *PoolOptions) applyDefaults() {
	if o.Size <= 0 {
		o.Size = 5
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	if o.BackoffBase <= 0 {
		o.BackoffBase = 200 * time.Millisecond
	}
	if o.PerJobTimeout <= 0 {
		o.PerJobTimeout = 2 * time.Minute
	}
}

func emit(sink events.Sink, typ, jobID string, payload map[string]any) {
	if sink == nil {
		return
	}
	sink.Publish(events.Event{
		Type:    typ,
		Time:    time.Now(),
		JobID:   jobID,
		Payload: payload,
	})
}

// Run ejecuta los jobs en paralelo respetando opts.Size. Devuelve los
// resultados en el mismo orden que los jobs recibidos. No aborta el batch si
// uno falla: el Result correspondiente lleva el error.
func Run(ctx context.Context, jobs []Job, opts PoolOptions) []Result {
	opts.applyDefaults()
	if len(jobs) == 0 {
		return nil
	}
	if opts.Size > len(jobs) {
		opts.Size = len(jobs)
	}
	exec := opts.Execute
	if exec == nil {
		return nil
	}

	results := make([]Result, len(jobs))

	type indexedJob struct {
		idx int
		job Job
	}
	jobCh := make(chan indexedJob)

	var wg sync.WaitGroup
	var doneMu sync.Mutex
	var done int

	for w := 0; w < opts.Size; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ij := range jobCh {
				emit(opts.Sink, "job_start", ij.job.ID(), nil)
				results[ij.idx] = runJob(ctx, ij.job, opts, exec)
				r := results[ij.idx]
				donePayload := map[string]any{
					"success":    r.Err == nil,
					"elapsed_ms": r.Elapsed.Milliseconds(),
					"retries":    r.Retries,
					"output":     r.Output,
				}
				if r.Err != nil {
					donePayload["error"] = r.Err.Error()
				}
				emit(opts.Sink, "job_done", r.JobID, donePayload)
				if opts.OnProgress != nil {
					doneMu.Lock()
					done++
					d := done
					doneMu.Unlock()
					opts.OnProgress(d, len(jobs), r)
				}
			}
		}()
	}

	for i, j := range jobs {
		select {
		case <-ctx.Done():
			for k := i; k < len(jobs); k++ {
				results[k] = Result{JobID: jobs[k].ID(), Err: ctx.Err()}
			}
			close(jobCh)
			wg.Wait()
			return results
		case jobCh <- indexedJob{idx: i, job: j}:
		}
	}
	close(jobCh)
	wg.Wait()
	return results
}

// runJob procesa un Job con reintentos y backoff exponencial.
func runJob(ctx context.Context, j Job, opts PoolOptions, exec ExecuteFunc) Result {
	start := time.Now()
	r := Result{JobID: j.ID()}

	for attempt := 0; attempt <= opts.MaxRetries; attempt++ {
		if attempt > 0 {
			wait := opts.BackoffBase << (attempt - 1)
			select {
			case <-ctx.Done():
				r.Err = ctx.Err()
				r.Retries = attempt
				r.Elapsed = time.Since(start)
				return r
			case <-time.After(wait):
			}
		}

		jobCtx, cancel := context.WithTimeout(ctx, opts.PerJobTimeout)
		out, err := exec(jobCtx, j)
		cancel()

		r.Retries = attempt
		if err == nil {
			r.Output = out
			r.Err = nil
			r.Elapsed = time.Since(start)
			return r
		}
		r.Err = err
	}
	r.Elapsed = time.Since(start)
	return r
}
