package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tools"
)

// Runner is what the scheduler needs to execute a run (agent.Runner satisfies it).
type Runner interface {
	Send(sessionID, text, modelID, reasoning string, attachments []string, env tools.Env) (string, string, error)
}

// EnvFactory builds the tool environment for one run.
// autoApprove decides whether tools may run unattended: a scheduled run usually has no window
// to ask in.
type EnvFactory func(sessionID string, autoApprove bool) tools.Env

// Manager runs things when their time comes.
// errBusy means "the previous run is still going, skip this tick" — not a failure, no
// notification.
var errBusy = errors.New("this task is already running")

type Manager struct {
	store    *Store
	sessions *store.Store
	runner   Runner
	envFor   EnvFactory

	mu sync.Mutex
	// running: taskID → runID. The value may be empty — "slot taken, run not sent yet" — which is
	// what stops two ticks from entering at once.
	running map[string]string

	// OnFinish is called when a run ends (scheduled or manual RunNow). IPC is not touched here — the
	// daemon uses it to broadcast to the UI; being unattended is the whole point of scheduling.
	OnFinish func(Task, string, string)
}

func NewManager(st *Store, sessions *store.Store, runner Runner, envFor EnvFactory) *Manager {
	return &Manager{
		store:    st,
		sessions: sessions,
		runner:   runner,
		envFor:   envFor,
		running:  make(map[string]string),
	}
}

// Start scans every 30 seconds, and once at startup — that is where catch-up happens.
//
// 30s because the finest schedule is one minute: accurate enough, without constant wakeups.
func (m *Manager) Start(ctx context.Context) {
	go func() {
		m.tick()
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.tick()
			}
		}
	}()
}

func (m *Manager) tick() {
	now := time.Now()
	for _, t := range m.store.List() {
		if !t.Enabled || t.Schedule.Kind == ScheduleManual {
			continue
		}
		if !t.Due(now) {
			continue
		}
		if _, err := m.run(t); err != nil {
			if errors.Is(err, errBusy) {
				continue
			}
			applog.Warnf("task %q did not start: %v", t.Name, err)
			// Advance the baseline on failure too, or it retries every 30 seconds and floods the log.
			m.patch(t.ID, func(x *Task) {
				x.LastRunAt = now.UnixMilli()
				x.LastStatus = "failed"
				x.LastError = err.Error()
			})
			// Even failing to start (no model, cannot create a session) is reported: the worst outcome for a
			// schedule is silence.
			if m.OnFinish != nil {
				m.OnFinish(t, "failed", err.Error())
			}
			continue
		}
		applog.Infof("task %q started (was due %s)", t.Name, t.NextRun(now).Format("01-02 15:04"))
	}
}

// RunNow runs immediately regardless of the schedule (UI button, CLI).
func (m *Manager) RunNow(id string) (string, error) {
	t, ok := m.store.Get(id)
	if !ok {
		return "", fmt.Errorf("task %s not found", id)
	}
	return m.run(t)
}

func (m *Manager) run(t Task) (string, error) {
	m.mu.Lock()
	if _, busy := m.running[t.ID]; busy {
		m.mu.Unlock()
		return "", errBusy
	}
	// Claim the slot first: creating the session and sending take time, and the ticker must not
	// enter again meanwhile.
	m.running[t.ID] = ""
	m.mu.Unlock()

	fail := func(err error) (string, error) {
		m.mu.Lock()
		delete(m.running, t.ID)
		m.mu.Unlock()
		return "", err
	}

	// One new session per run. The result needs somewhere to live and to be followed up, which beats
	// building a separate task-result viewer.
	title := strings.TrimSpace(t.Name)
	if title == "" {
		title = "Task"
	}
	title = fmt.Sprintf("%s · %s", title, time.Now().Format("01-02 15:04"))

	sess, err := m.sessions.Create(title)
	if err != nil {
		return fail(fmt.Errorf("create session: %w", err))
	}
	if dir := strings.TrimSpace(t.WorkingDirectory); dir != "" {
		if err := m.sessions.SetWorkingDirectory(sess.ID, dir); err != nil {
			applog.Warnf("task %q could not set the working directory (using the default): %v", t.Name, err)
		}
	}

	env := m.envFor(sess.ID, t.AutoApprove)
	runID, _, err := m.runner.Send(sess.ID, t.Prompt, strings.TrimSpace(t.Model), "", nil, env)
	if err != nil {
		return fail(fmt.Errorf("send: %w", err))
	}

	m.mu.Lock()
	m.running[t.ID] = runID
	m.mu.Unlock()

	m.patch(t.ID, func(x *Task) {
		x.LastStatus = "running"
		x.LastError = ""
		x.LastSession = sess.ID
	})
	return sess.ID, nil
}

// IsRunning reports whether this task is running (the UI shows "running").
func (m *Manager) IsRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

// HandleEvent takes agent events and writes "this run ended" back to the task. Wired by the daemon
// to Runner.OnEvent; events from runs the task did not start are ignored.
func (m *Manager) HandleEvent(data map[string]any) {
	kind, _ := data["kind"].(string)
	if kind != "run.finished" && kind != "run.failed" {
		return
	}
	runID, _ := data["runId"].(string)
	if runID == "" {
		return
	}

	m.mu.Lock()
	taskID := ""
	for id, rid := range m.running {
		if rid == runID {
			taskID = id
			break
		}
	}
	if taskID != "" {
		delete(m.running, taskID)
	}
	m.mu.Unlock()

	if taskID == "" {
		return
	}

	status := "ok"
	detail := ""
	if kind == "run.failed" {
		status = "failed"
		detail, _ = data["error"].(string)
	} else if s, _ := data["status"].(string); s == "cancelled" {
		status = "cancelled"
	}

	finished, err := m.store.Patch(taskID, func(x *Task) {
		x.LastRunAt = time.Now().UnixMilli()
		x.LastStatus = status
		x.LastError = detail
	})
	if err != nil {
		applog.Warnf("failed to update task %s: %v", taskID, err)
	}
	applog.Infof("task %s finished (%s)", taskID, status)
	if m.OnFinish != nil && finished.ID != "" {
		m.OnFinish(finished, status, detail)
	}
}

func (m *Manager) patch(id string, fn func(*Task)) {
	if _, err := m.store.Patch(id, fn); err != nil {
		applog.Warnf("failed to update task %s: %v", id, err)
	}
}
