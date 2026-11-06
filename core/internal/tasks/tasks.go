// Package tasks is "scheduled tasks": a stored prompt plus when to run it.
//
// Not a to-do list — it actually runs an agent turn and leaves the result in a session, so the
// existing chat window is the best result viewer available.
package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/atomicfile"
	"github.com/NekoLOvO/dux-code/core/internal/store"
)

// Schedule kinds.
const (
	ScheduleDaily  = "daily"
	ScheduleEvery  = "every"
	ScheduleWeekly = "weekly"
	ScheduleManual = "manual"
)

// Schedule is the three kinds a person can actually write.
//
// No cron expressions: flexible, but nobody wants to type "0 9 * * 1-5" into a settings field and
// remember what it means. Complex schedules belong in a systemd timer, not a homegrown syntax.
type Schedule struct {
	Kind string `json:"kind"`
	// At is "HH:MM" in local time (daily / weekly).
	At string `json:"at,omitempty"`
	// Minutes is the interval (every).
	Minutes int `json:"minutes,omitempty"`
	// Weekday: 0=Sunday … 6=Saturday (weekly).
	Weekday int `json:"weekday,omitempty"`
}

// Task is one scheduled task.
type Task struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Prompt   string   `json:"prompt"`
	Schedule Schedule `json:"schedule"`
	// Model is "provider:model"; empty means the default model.
	Model string `json:"model,omitempty"`
	// WorkingDirectory empty means the daemon default (the user's home).
	WorkingDirectory string `json:"workingDirectory,omitempty"`
	// AutoApprove decides whether tools may run unattended.
	//
	// Default false: a run usually has no window attached, so nobody can press "allow" and tools are
	// refused — the task can read, but not write or run commands. Turning it on is what makes the task
	// able to act, at the price of never asking.
	AutoApprove bool  `json:"autoApprove,omitempty"`
	Enabled     bool  `json:"enabled"`
	CreatedAt   int64 `json:"createdAt,omitempty"`

	// Run state, maintained by the scheduler (not user-editable).
	LastRunAt   int64  `json:"lastRunAt,omitempty"`
	LastSession string `json:"lastSessionId,omitempty"`
	// LastStatus: ok / failed / running / cancelled.
	LastStatus string `json:"lastStatus,omitempty"`
	LastError  string `json:"lastError,omitempty"`
}

// Validate checks that what was stored can actually run. The UI already gates it; this is a
// second gate, because a task with a broken schedule never runs and nobody notices.
func (t Task) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("a task needs a name")
	}
	if strings.TrimSpace(t.Prompt) == "" {
		return fmt.Errorf("a task needs a prompt")
	}
	switch t.Schedule.Kind {
	case ScheduleManual:
		return nil
	case ScheduleEvery:
		if t.Schedule.Minutes < 1 {
			return fmt.Errorf("the interval must be at least 1 minute")
		}
		return nil
	case ScheduleDaily, ScheduleWeekly:
		if _, _, ok := t.Schedule.clock(); !ok {
			return fmt.Errorf("the time must be written HH:MM")
		}
		if t.Schedule.Kind == ScheduleWeekly && (t.Schedule.Weekday < 0 || t.Schedule.Weekday > 6) {
			return fmt.Errorf("the weekday must be between 0 (Sunday) and 6 (Saturday)")
		}
		return nil
	}
	return fmt.Errorf("unknown schedule kind: %q", t.Schedule.Kind)
}

func (s Schedule) clock() (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(s.At), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	m, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// NextAfter returns the first run time after `after` (exclusive), or the zero value if none.
func (s Schedule) NextAfter(after time.Time) time.Time {
	switch s.Kind {
	case ScheduleEvery:
		if s.Minutes <= 0 {
			return time.Time{}
		}
		return after.Add(time.Duration(s.Minutes) * time.Minute)

	case ScheduleDaily:
		hour, minute, ok := s.clock()
		if !ok {
			return time.Time{}
		}
		cand := time.Date(after.Year(), after.Month(), after.Day(), hour, minute, 0, 0, after.Location())
		if !cand.After(after) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand

	case ScheduleWeekly:
		hour, minute, ok := s.clock()
		if !ok || s.Weekday < 0 || s.Weekday > 6 {
			return time.Time{}
		}
		cand := time.Date(after.Year(), after.Month(), after.Day(), hour, minute, 0, 0, after.Location())
		// Eight days always contain the target weekday; not finding it means the settings are broken.
		for i := 0; i < 8; i++ {
			if int(cand.Weekday()) == s.Weekday && cand.After(after) {
				return cand
			}
			cand = cand.AddDate(0, 0, 1)
		}
		return time.Time{}
	}
	return time.Time{}
}

// NextRun returns when this task should next run.
//
// The baseline is the last real run, not now, and the difference shows up when runs were missed:
// from now, a missed run is simply skipped (two days off loses an hourly task and nobody notices);
// from the last run, the next start computes "this was due long ago" and catches up once — then
// moves the baseline to now, so three days off do not mean seventy-two catch-ups.
func (t Task) NextRun(now time.Time) time.Time {
	base := t.CreatedAt
	if t.LastRunAt > base {
		base = t.LastRunAt
	}
	if base == 0 {
		return time.Time{}
	}
	return t.Schedule.NextAfter(time.UnixMilli(base).In(now.Location()))
}

// Due means the next run time has already passed.
func (t Task) Due(now time.Time) bool {
	next := t.NextRun(now)
	return !next.IsZero() && !next.After(now)
}

// --- persistence ---

// Dir is where the task file lives (same data directory as sessions, different file).
func Dir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "dcode")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "dcode")
}

func Path() string { return filepath.Join(Dir(), "tasks.json") }

type file struct {
	Version int    `json:"version"`
	Tasks   []Task `json:"tasks"`
}

// Store is the task list: in memory, written back on change. A dozen tasks rather than tens of
// thousands, so no database, same as sessions.
type Store struct {
	mu    sync.RWMutex
	items []Task
}

// Open reads the task file. A missing file is not an error (first start).
func Open() (*Store, error) {
	s := &Store{}
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", Path(), err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		// A broken file must not keep the daemon down: start empty and leave the original for the user.
		return s, fmt.Errorf("failed to parse tasks.json (treating it as empty): %w", err)
	}
	s.items = f.Tasks
	if s.items == nil {
		s.items = []Task{}
	}
	return s, nil
}

// List returns copies in file order (the user's order is the user's order).
func (s *Store) List() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, len(s.items))
	copy(out, s.items)
	return out
}

func (s *Store) Get(id string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.items {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// Put adds or updates; an empty ID means add.
func (s *Store) Put(t Task) (Task, error) {
	if err := t.Validate(); err != nil {
		return Task{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if t.ID == "" {
		t.ID = "task-" + store.NewID()
		t.CreatedAt = time.Now().UnixMilli()
		s.items = append(s.items, t)
	} else {
		found := false
		for i := range s.items {
			if s.items[i].ID == t.ID {
				// Run state belongs to the scheduler; a form save must not overwrite it.
				t.LastRunAt = s.items[i].LastRunAt
				t.LastSession = s.items[i].LastSession
				t.LastStatus = s.items[i].LastStatus
				t.LastError = s.items[i].LastError
				t.CreatedAt = s.items[i].CreatedAt
				s.items[i] = t
				found = true
				break
			}
		}
		if !found {
			return Task{}, fmt.Errorf("task %s not found", t.ID)
		}
	}

	if err := s.saveLocked(); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.items[:0]
	removed := false
	for _, t := range s.items {
		if t.ID == id {
			removed = true
			continue
		}
		out = append(out, t)
	}
	if !removed {
		return fmt.Errorf("task %s not found", id)
	}
	s.items = out
	return s.saveLocked()
}

// Patch changes one task in place (used by the scheduler for run state).
func (s *Store) Patch(id string, fn func(*Task)) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID != id {
			continue
		}
		fn(&s.items[i])
		if err := s.saveLocked(); err != nil {
			return Task{}, err
		}
		return s.items[i], nil
	}
	return Task{}, fmt.Errorf("task %s not found", id)
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return fmt.Errorf("create task directory: %w", err)
	}
	data, err := json.MarshalIndent(file{Version: 1, Tasks: s.items}, "", "  ")
	if err != nil {
		return err
	}
	// 0600: a prompt may hold private content. Atomic, because one file holds every task: a write cut
	// short by a crash would take the whole list with it, not just the task being edited.
	if err := atomicfile.WriteFile(Path(), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", Path(), err)
	}
	return nil
}
