// Package scheduler implementa el motor cron + persistencia de tareas
// programadas. La ejecución concreta de cada disparo se delega al Runner
// inyectado por el caller.
package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	DefaultDir            = "schedules"
	schedulerTickInterval = 15 * time.Second
)

var (
	validScheduleID = regexp.MustCompile(`^sch-[a-f0-9]{8}$`)
	cronParser      = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
)

// Schedule es una tarea programada. Una sola fuente de verdad para el trigger:
// si Cron es no vacío → recurrente; si no → one-shot disparado por NextRun.
type Schedule struct {
	ID       string     `json:"id"`
	Created  time.Time  `json:"created"`
	Label    string     `json:"label"`
	Command  string     `json:"command"`
	NextRun  time.Time  `json:"next_run"`
	Cron     string     `json:"cron,omitempty"`
	LastRun  *time.Time `json:"last_run,omitempty"`
	RunCount int        `json:"run_count"`
	Enabled  bool       `json:"enabled"`
}

// Scheduler gestiona las tareas programadas: persistencia en disco, tick loop
// que dispara las que están vencidas, y delega ejecución al Runner inyectado.
type Scheduler struct {
	dir   string
	mu    sync.Mutex
	items map[string]*Schedule
	// Runner se invoca cuando una tarea vence. El caller lo asigna después
	// de construir el Scheduler (típicamente apuntando al método del agente).
	Runner func(ctx context.Context, sched *Schedule)
}

func New(dir string) (*Scheduler, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creando %s: %w", dir, err)
	}
	s := &Scheduler{
		dir:   dir,
		items: make(map[string]*Schedule),
	}
	if err := s.loadAll(); err != nil {
		return nil, fmt.Errorf("cargando schedules: %w", err)
	}
	return s, nil
}

func (s *Scheduler) loadAll() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "sched: warning leyendo %s: %v\n", e.Name(), err)
			continue
		}
		var sched Schedule
		if err := json.Unmarshal(data, &sched); err != nil {
			fmt.Fprintf(os.Stderr, "sched: warning parseando %s: %v\n", e.Name(), err)
			continue
		}
		if !validScheduleID.MatchString(sched.ID) {
			fmt.Fprintf(os.Stderr, "sched: warning ID inválido %s en %s\n", sched.ID, e.Name())
			continue
		}
		// Si era recurrente pero NextRun quedó en el pasado (laptop apagada),
		// recomputamos hacia el futuro sin disparar las pasadas.
		if sched.Cron != "" && time.Now().After(sched.NextRun) {
			if next, nerr := cronNext(sched.Cron, time.Now()); nerr == nil {
				sched.NextRun = next
			} else {
				fmt.Fprintf(os.Stderr, "sched: warning recomputando cron de %s: %v\n", sched.ID, nerr)
				sched.Enabled = false
			}
		}
		s.items[sched.ID] = &sched
	}
	return nil
}

func (s *Scheduler) Start(ctx context.Context) {
	if s.Runner == nil {
		fmt.Fprintln(os.Stderr, "sched: runner no inyectado, scheduler no arranca")
		return
	}
	tick := time.NewTicker(schedulerTickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			s.fireDue(ctx, now)
		}
	}
}

// fireDue identifica schedules vencidas, actualiza su NextRun/Enabled atómicamente
// para evitar re-disparos, y delega cada ejecución a una goroutine.
func (s *Scheduler) fireDue(ctx context.Context, now time.Time) {
	s.mu.Lock()
	var due []*Schedule
	for _, sched := range s.items {
		if !sched.Enabled || now.Before(sched.NextRun) {
			continue
		}
		if sched.Cron != "" {
			if next, err := cronNext(sched.Cron, now); err == nil {
				sched.NextRun = next
			} else {
				sched.Enabled = false
			}
		} else {
			sched.Enabled = false
		}
		s.persistLocked(sched)
		due = append(due, sched)
	}
	s.mu.Unlock()

	for _, sched := range due {
		go s.Runner(ctx, sched)
	}
}

func (s *Scheduler) Add(sched *Schedule) error {
	if sched.ID == "" {
		sched.ID = newScheduleID()
	}
	if !validScheduleID.MatchString(sched.ID) {
		return fmt.Errorf("ID inválido: %s", sched.ID)
	}
	if sched.Created.IsZero() {
		sched.Created = time.Now()
	}
	if sched.Cron != "" {
		next, err := cronNext(sched.Cron, time.Now())
		if err != nil {
			return fmt.Errorf("cron inválido %q: %w", sched.Cron, err)
		}
		sched.NextRun = next
	}
	if sched.NextRun.IsZero() {
		return fmt.Errorf("schedule sin trigger (ni next_run ni cron)")
	}
	sched.Enabled = true
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[sched.ID] = sched
	return s.persistLocked(sched)
}

func (s *Scheduler) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sched, ok := s.items[id]
	if !ok {
		return fmt.Errorf("schedule %s no existe", id)
	}
	sched.Enabled = false
	delete(s.items, id)
	path := filepath.Join(s.dir, id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Scheduler) List() []*Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Schedule, 0, len(s.items))
	for _, sc := range s.items {
		copy := *sc
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NextRun.Before(out[j].NextRun) })
	return out
}

// MarkRun se llama desde el runner tras cada disparo exitoso para actualizar
// LastRun y RunCount. NextRun ya se actualizó en fireDue.
func (s *Scheduler) MarkRun(id string, when time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sched, ok := s.items[id]
	if !ok {
		return
	}
	t := when
	sched.LastRun = &t
	sched.RunCount++
	_ = s.persistLocked(sched)
}

func (s *Scheduler) persistLocked(sched *Schedule) error {
	path := filepath.Join(s.dir, sched.ID+".json")
	data, err := json.MarshalIndent(sched, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func newScheduleID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "sch-" + hex.EncodeToString(b[:])
}

// cronNext calcula el próximo disparo a partir de una expresión cron estándar
// de 5 campos (min hora dia mes diasem) o un descriptor (@daily, @hourly, etc.).
func cronNext(expr string, from time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(from), nil
}
