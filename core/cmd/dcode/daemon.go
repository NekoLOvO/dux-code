package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/NekoLOvO/dux-code/core/internal/agent"
	"github.com/NekoLOvO/dux-code/core/internal/api"
	"github.com/NekoLOvO/dux-code/core/internal/applog"
	"github.com/NekoLOvO/dux-code/core/internal/config"
	"github.com/NekoLOvO/dux-code/core/internal/files"
	"github.com/NekoLOvO/dux-code/core/internal/ipc"
	"github.com/NekoLOvO/dux-code/core/internal/mcp"
	"github.com/NekoLOvO/dux-code/core/internal/store"
	"github.com/NekoLOvO/dux-code/core/internal/tasks"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the background daemon (IPC only, no UI)",
	RunE:  runDaemon,
}

var daemonForeground bool

func init() {
	daemonCmd.Flags().BoolVar(&daemonForeground, "foreground", true, "Stay in the foreground (normally needed outside systemd)")
}

func runDaemon(_ *cobra.Command, _ []string) error {
	path := socketPath()

	// Never start a second daemon while one is alive.
	if c, err := ipc.Dial(path); err == nil {
		_ = c.Close()
		return fmt.Errorf("a daemon is already running (%s)", path)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	applog.Infof("Config file %s", config.Path())

	st, skipped, err := store.Open()
	if err != nil {
		return err
	}
	for _, e := range skipped {
		applog.Warnf("Skipping unreadable session file: %v", e)
	}
	applog.Infof("Session directory %s (%d sessions)", store.Dir(), len(st.List()))

	bus := ipc.NewEventBus()
	runner := agent.New(bus, st, cfg)

	// Scheduled tasks. A broken file must not keep the daemon down — treat it as empty and leave the
	// original for the user to inspect.
	var deps api.Deps
	taskStore, taskErr := tasks.Open()
	if taskErr != nil {
		applog.Warnf("Failed to read scheduled tasks (treating as empty): %v", taskErr)
	}
	var taskMgr *tasks.Manager
	if taskStore != nil {
		// Runs usually happen with nobody watching the window, so tools are either approved by policy
		// or refused (no one to ask means refusal — the tools layer's fixed default).
		envFor := api.TaskEnvFactory(func() api.Deps { return deps })
		taskMgr = tasks.NewManager(taskStore, st, runner, envFor)
		runner.OnEvent = taskMgr.HandleEvent
		// Tell the UI when a run finishes. Scheduled means unattended; no notification means it did
		// not happen.
		taskMgr.OnFinish = func(t tasks.Task, status, detail string) {
			bus.Publish("task", map[string]any{
				"kind":      "task.finished",
				"taskId":    t.ID,
				"name":      t.Name,
				"status":    status,
				"detail":    detail,
				"sessionId": t.LastSession,
			})
		}
	}

	// MCP servers are external processes and startup can take tens of seconds in the background
	// (npx downloads on first use). Sync therefore means "get it going", not "wait until ready"; the
	// tools appearing one round later is fine.
	mcpMgr := mcp.New(Version)
	// A server starts in the background, so the save that asked for it replies with "starting" and the
	// answer ("Running · 46 tools", or why it failed) is only known here. Push it: the settings page
	// has nothing else to go on, and without this the row keeps saying "Starting…" until the page is
	// rebuilt by a tab switch.
	mcpMgr.OnChange = func(reason string) {
		bus.Publish("agent", map[string]any{"kind": "mcp.changed", "reason": reason})
	}
	runner.SetMCP(mcpMgr)
	mcpMgr.Sync(cfg)
	defer mcpMgr.Close()

	deps = api.Deps{
		Version: Version,
		Store:   st,
		Agent:   runner,
		Config:  cfg,
		Bus:     bus,
		Files:   files.New(),
		MCP:     mcpMgr,
		Tasks:   taskStore,
		TaskMgr: taskMgr,
	}
	srv := api.NewServer(deps, path)

	if err := srv.Listen(); err != nil {
		return err
	}
	// Closing the listener removes the socket file, which is already gone when the process is being
	// torn down for a reason worth reporting; that reason has priority over a close error.
	defer func() { _ = srv.Close() }()

	applog.Infof("dcode %s ready (socket=%s)", Version, srv.SocketPath())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One scan at startup: schedules missed while the app was closed are caught up here.
	if taskMgr != nil {
		taskMgr.Start(ctx)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ctx) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		applog.Infof("Received %s, shutting down", sig)
		cancel()
		// Give Serve a moment to close the listener. A real failure there during shutdown is still
		// worth a line — it explains a socket that was left behind — while a clean cancel is not.
		select {
		case <-time.After(500 * time.Millisecond):
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				applog.Warnf("ipc server stopped with an error: %v", err)
			}
		}
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("ipc server: %w", err)
		}
	}
	return nil
}
