package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeJob struct {
	id     string
	prompt string
	system []string
}

func (j fakeJob) ID() string       { return j.id }
func (j fakeJob) Prompt() string   { return j.prompt }
func (j fakeJob) System() []string { return j.system }

func makeJobs(n int) []Job {
	jobs := make([]Job, n)
	for i := 0; i < n; i++ {
		jobs[i] = fakeJob{id: fmt.Sprintf("j-%d", i), prompt: "noop"}
	}
	return jobs
}

func TestRunPool_PreservesOrder(t *testing.T) {
	jobs := makeJobs(10)
	opts := PoolOptions{
		Size: 4,
		Execute: func(ctx context.Context, j Job) (string, error) {
			time.Sleep(5 * time.Millisecond)
			return j.ID() + "-ok", nil
		},
	}
	results := (&agent{}).runPool(context.Background(), jobs, opts)
	if len(results) != len(jobs) {
		t.Fatalf("len mismatch: got %d, want %d", len(results), len(jobs))
	}
	for i, r := range results {
		if r.JobID != jobs[i].ID() {
			t.Errorf("idx %d: JobID = %q, want %q", i, r.JobID, jobs[i].ID())
		}
		if r.Output != r.JobID+"-ok" {
			t.Errorf("idx %d: Output = %q", i, r.Output)
		}
		if r.Err != nil {
			t.Errorf("idx %d: unexpected err %v", i, r.Err)
		}
	}
}

func TestRunPool_ActuallyParallelizes(t *testing.T) {
	const n = 8
	const perJob = 50 * time.Millisecond
	opts := PoolOptions{
		Size: n,
		Execute: func(ctx context.Context, j Job) (string, error) {
			time.Sleep(perJob)
			return "ok", nil
		},
	}
	start := time.Now()
	results := (&agent{}).runPool(context.Background(), makeJobs(n), opts)
	elapsed := time.Since(start)
	if len(results) != n {
		t.Fatalf("len: %d", len(results))
	}
	// Si fuera serial, tardaría >= n*perJob. Permitimos algo de margen.
	if elapsed >= time.Duration(n)*perJob {
		t.Errorf("pool no parece estar paralelizando: elapsed=%v, serial=%v", elapsed, time.Duration(n)*perJob)
	}
}

func TestRunPool_RespectsPoolSize(t *testing.T) {
	const poolSize = 3
	const n = 12
	var inflight int32
	var maxSeen int32
	opts := PoolOptions{
		Size: poolSize,
		Execute: func(ctx context.Context, j Job) (string, error) {
			cur := atomic.AddInt32(&inflight, 1)
			defer atomic.AddInt32(&inflight, -1)
			for {
				m := atomic.LoadInt32(&maxSeen)
				if cur <= m || atomic.CompareAndSwapInt32(&maxSeen, m, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			return "", nil
		},
	}
	(&agent{}).runPool(context.Background(), makeJobs(n), opts)
	if int(maxSeen) > poolSize {
		t.Errorf("maxSeen=%d, esperaba <= %d", maxSeen, poolSize)
	}
	if maxSeen == 0 {
		t.Errorf("nunca midió inflight, algo anda mal")
	}
}

func TestRunPool_PartialFailuresDoNotAbortBatch(t *testing.T) {
	opts := PoolOptions{
		Size: 3,
		Execute: func(ctx context.Context, j Job) (string, error) {
			if j.ID() == "j-2" || j.ID() == "j-5" {
				return "", fmt.Errorf("simulated failure for %s", j.ID())
			}
			return "ok", nil
		},
	}
	results := (&agent{}).runPool(context.Background(), makeJobs(8), opts)
	failed := 0
	succeeded := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		} else {
			succeeded++
		}
	}
	if failed != 2 || succeeded != 6 {
		t.Errorf("got %d failed, %d ok; want 2/6", failed, succeeded)
	}
}

func TestRunPool_RetriesUntilSuccess(t *testing.T) {
	var calls sync.Map
	opts := PoolOptions{
		Size:        2,
		MaxRetries:  3,
		BackoffBase: time.Millisecond,
		Execute: func(ctx context.Context, j Job) (string, error) {
			n, _ := calls.LoadOrStore(j.ID(), new(int32))
			cnt := atomic.AddInt32(n.(*int32), 1)
			if cnt < 3 {
				return "", fmt.Errorf("transient")
			}
			return "finally ok", nil
		},
	}
	results := (&agent{}).runPool(context.Background(), makeJobs(2), opts)
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: expected success, got %v", r.JobID, r.Err)
		}
		if r.Retries != 2 {
			t.Errorf("%s: Retries = %d, want 2", r.JobID, r.Retries)
		}
		if r.Output != "finally ok" {
			t.Errorf("%s: Output = %q", r.JobID, r.Output)
		}
	}
}

func TestRunPool_RetriesExhausted(t *testing.T) {
	opts := PoolOptions{
		Size:        2,
		MaxRetries:  2,
		BackoffBase: time.Millisecond,
		Execute: func(ctx context.Context, j Job) (string, error) {
			return "", fmt.Errorf("always fails")
		},
	}
	results := (&agent{}).runPool(context.Background(), makeJobs(2), opts)
	for _, r := range results {
		if r.Err == nil {
			t.Errorf("%s: expected err", r.JobID)
		}
		if r.Retries != 2 {
			t.Errorf("%s: Retries = %d, want 2 (MaxRetries)", r.JobID, r.Retries)
		}
	}
}

func TestRunPool_ProgressCallback(t *testing.T) {
	const n = 5
	var calls int32
	var lastDone, lastTotal int32
	var mu sync.Mutex
	opts := PoolOptions{
		Size: 2,
		OnProgress: func(done, total int, r Result) {
			atomic.AddInt32(&calls, 1)
			mu.Lock()
			lastDone = int32(done)
			lastTotal = int32(total)
			mu.Unlock()
		},
		Execute: func(ctx context.Context, j Job) (string, error) {
			return "", nil
		},
	}
	(&agent{}).runPool(context.Background(), makeJobs(n), opts)
	if int(calls) != n {
		t.Errorf("OnProgress llamado %d veces, esperaba %d", calls, n)
	}
	if lastDone != n || lastTotal != n {
		t.Errorf("último progreso: %d/%d, esperaba %d/%d", lastDone, lastTotal, n, n)
	}
}

func TestRunPool_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	opts := PoolOptions{
		Size:        2,
		BackoffBase: time.Millisecond,
		Execute: func(ctx context.Context, j Job) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return "ok", nil
			}
		},
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	results := (&agent{}).runPool(ctx, makeJobs(20), opts)
	if len(results) != 20 {
		t.Fatalf("len = %d, want 20", len(results))
	}
	// Tras cancel, al menos algunos resultados deben llevar error
	withErr := 0
	for _, r := range results {
		if r.Err != nil {
			withErr++
		}
	}
	if withErr == 0 {
		t.Errorf("esperaba al menos algún Err tras cancel, ninguno")
	}
}

func TestRunPool_EmptyJobs(t *testing.T) {
	results := (&agent{}).runPool(context.Background(), nil, PoolOptions{Size: 5})
	if results != nil {
		t.Errorf("esperaba nil para jobs vacíos, got %v", results)
	}
}
