package main

import (
	"context"
	"sync"
	"time"
)

// Job es una tarea procesable por un worker aislado.
type Job interface {
	ID() string         // identificador único para logs y resultados
	Prompt() string     // mensaje del usuario al worker
	System() []string   // mensajes system adicionales (además de la personalidad)
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
// Default: agent.runIsolated. Tests inyectan un stub.
type ExecuteFunc func(ctx context.Context, job Job) (string, error)

// PoolOptions configura una corrida del orquestador.
type PoolOptions struct {
	Size          int
	MaxRetries    int
	BackoffBase   time.Duration
	PerJobTimeout time.Duration
	OnProgress    func(done, total int)
	Execute       ExecuteFunc
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

// runPool ejecuta los jobs en paralelo respetando opts.Size. Devuelve los
// resultados en el mismo orden que los jobs recibidos. No aborta el batch si
// uno falla: el Result correspondiente lleva el error.
func (a *agent) runPool(ctx context.Context, jobs []Job, opts PoolOptions) []Result {
	opts.applyDefaults()
	if len(jobs) == 0 {
		return nil
	}
	if opts.Size > len(jobs) {
		opts.Size = len(jobs)
	}
	exec := opts.Execute
	if exec == nil {
		exec = a.runIsolated
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
				results[ij.idx] = runJob(ctx, ij.job, opts, exec)
				if opts.OnProgress != nil {
					doneMu.Lock()
					done++
					d := done
					doneMu.Unlock()
					opts.OnProgress(d, len(jobs))
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

// runIsolated procesa un Job en un worker: agente con history limpia que reusa
// http y mcp del agente principal. No tiene skills ni sessions.
func (a *agent) runIsolated(ctx context.Context, j Job) (string, error) {
	worker := &agent{
		endpoint:    a.endpoint,
		model:       a.model,
		apiKey:      a.apiKey,
		personality: a.personality,
		http:        a.http,
		mcp:         a.mcp,
	}
	for _, sys := range j.System() {
		worker.history = append(worker.history, message{Role: "system", Content: sys})
	}
	worker.history = append(worker.history, message{Role: "user", Content: j.Prompt()})
	return worker.runConversation(ctx, &worker.history)
}
