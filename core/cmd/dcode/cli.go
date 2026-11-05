package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NekoLOvO/dux-code/core/internal/ipc"
)

// connect is the shared opening for every subcommand: dial the daemon, or say so plainly.
func connect() (*ipc.Client, error) {
	c, err := ipc.Dial(socketPath())
	if err != nil {
		return nil, fmt.Errorf("cannot reach the daemon (%s). Start it with `dcode daemon`?", socketPath())
	}
	return c, nil
}

var pingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Check that the daemon is alive",
	RunE: func(_ *cobra.Command, _ []string) error {
		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		raw, err := c.Call("info", nil)
		if err != nil {
			return err
		}
		var info struct {
			Version          string `json:"version"`
			APIVersion       int    `json:"apiVersion"`
			DefaultModel     string `json:"defaultModel"`
			WorkingDirectory string `json:"workingDirectory"`
			Models           []struct {
				ID    string `json:"id"`
				Ready bool   `json:"ready"`
			} `json:"models"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			return err
		}

		fmt.Printf("dcode %s  api v%d\n", info.Version, info.APIVersion)
		fmt.Printf("Working directory %s\n", info.WorkingDirectory)
		fmt.Printf("Default model %s\n", info.DefaultModel)
		fmt.Println("Models:")
		for _, m := range info.Models {
			mark := "·"
			if m.Ready {
				mark = "✓"
			}
			fmt.Printf("  %s %s\n", mark, m.ID)
		}
		return nil
	},
}

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List sessions",
	RunE: func(_ *cobra.Command, _ []string) error {
		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		raw, err := c.Call("session.list", nil)
		if err != nil {
			return err
		}
		var out struct {
			Sessions []struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				UpdatedAt    int64  `json:"updatedAt"`
				Pinned       bool   `json:"pinned"`
				MessageCount int    `json:"messageCount"`
				Busy         bool   `json:"busy"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		if len(out.Sessions) == 0 {
			fmt.Println("(no sessions)")
			return nil
		}
		for _, s := range out.Sessions {
			flags := ""
			if s.Pinned {
				flags += "📌"
			}
			if s.Busy {
				flags += "…"
			}
			when := time.UnixMilli(s.UpdatedAt).Format("01-02 15:04")
			fmt.Printf("%-10s %s %-12s %2d msgs  %s\n", s.ID, flags, when, s.MessageCount, s.Title)
		}
		return nil
	},
}

var newSessionCmd = &cobra.Command{
	Use:   "new [title]",
	Short: "Create a new session",
	RunE: func(_ *cobra.Command, args []string) error {
		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		title := ""
		if len(args) > 0 {
			title = strings.Join(args, " ")
		}
		raw, err := c.Call("session.new", map[string]any{"title": title})
		if err != nil {
			return err
		}
		var out struct {
			Session struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"session"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return err
		}
		fmt.Println(out.Session.ID)
		return nil
	},
}

var (
	sendSession string
	sendModel   string
)

var sendCmd = &cobra.Command{
	Use:   "send <message>",
	Short: "Send a message and stream the reply",
	RunE: func(_ *cobra.Command, args []string) error {
		text := strings.Join(args, " ")
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("message is empty")
		}

		c, err := connect()
		if err != nil {
			return err
		}
		defer c.Close()

		// No session given: start a new one.
		sessionID := sendSession
		if sessionID == "" {
			raw, err := c.Call("session.new", map[string]any{})
			if err != nil {
				return err
			}
			var out struct {
				Session struct {
					ID string `json:"id"`
				} `json:"session"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return err
			}
			sessionID = out.Session.ID
			fmt.Fprintf(os.Stderr, "New session %s\n", sessionID)
		}

		exitCode := make(chan int, 1)

		c.OnEvent(func(ev ipc.Event) {
			if ev.Topic != "agent" {
				return
			}
			data, ok := ev.Data.(json.RawMessage)
			if !ok {
				return
			}
			var e struct {
				Kind    string `json:"kind"`
				RunID   string `json:"runId"`
				Text    string `json:"text"`
				Status  string `json:"status"`
				Error   string `json:"error"`
				Elapsed int64  `json:"elapsedMs"`
				Usage   *struct {
					Input  int `json:"input"`
					Output int `json:"output"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(data, &e); err != nil {
				return
			}
			switch e.Kind {
			case "delta":
				fmt.Print(e.Text)
			case "run.finished":
				fmt.Println()
				if e.Usage != nil && (e.Usage.Input > 0 || e.Usage.Output > 0) {
					fmt.Fprintf(os.Stderr, "[%s %.1fs  %d→%d tokens]\n",
						e.Status, float64(e.Elapsed)/1000, e.Usage.Input, e.Usage.Output)
				} else {
					fmt.Fprintf(os.Stderr, "[%s %.1fs]\n", e.Status, float64(e.Elapsed)/1000)
				}
				select {
				case exitCode <- 0:
				default:
				}
			case "run.failed":
				fmt.Fprintf(os.Stderr, "\nError: %s\n", e.Error)
				select {
				case exitCode <- 1:
				default:
				}
			}
		})

		params := map[string]any{"sessionId": sessionID, "text": text}
		if sendModel != "" {
			params["model"] = sendModel
		}
		if _, err := c.Call("chat.send", params); err != nil {
			return err
		}

		code := <-exitCode
		if code != 0 {
			os.Exit(code)
		}
		return nil
	},
}

var showCmd = &cobra.Command{
	Use:   "show",
	Short: "Ask the UI to show its window (requires quickshell)",
	RunE: func(_ *cobra.Command, _ []string) error {
		pid, err := findShellPID()
		if err != nil {
			return err
		}
		return callShell(pid, "raise")
	},
}

var toggleCmd = &cobra.Command{
	Use:   "toggle",
	Short: "Toggle the UI window (requires quickshell)",
	RunE: func(_ *cobra.Command, _ []string) error {
		pid, err := findShellPID()
		if err != nil {
			return err
		}
		return callShell(pid, "toggle")
	},
}

// findShellPID locates the running dux-code shell.
//
// Deliberately not `qs ipc`'s instance selection: that derives the instance from a config path,
// and neither daemon nor CLI knows whether the user runs from the repo (`make run`) or from an
// install. Reading cmdline from /proc is simpler — this project is Linux-only.
func findShellPID() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("read /proc: %w", err)
	}
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue
		}
		raw, readErr := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if readErr != nil {
			continue
		}
		// argv[0] only: matching on the whole command line would also hit a shell running `grep
		// quickshell`, since its own text contains the word.
		parts := strings.Split(string(raw), "\x00")
		if len(parts) == 0 {
			continue
		}
		// quickshell and its `qs` alias both count; other shells (dfiles, dms, dcal) are told apart by
		// the path marker below, not by the binary name.
		if name := filepath.Base(parts[0]); name != "quickshell" && name != "qs" {
			continue
		}
		cmd := strings.Join(parts, " ")
		// From the repo the path ends in ".../local/dux-code"; a copied binary may be ".../dcode".
		if strings.Contains(cmd, "dux-code") || strings.Contains(cmd, "dcode") {
			return pid, nil
		}
	}
	return 0, errors.New("no running UI found. Launch Dux Code first (`dcode ui`)")
}

// runningShellPID is findShellPID without the "nothing is running" error: callers that treat a miss as
// normal (ui) must not have to string-match an error message.
func runningShellPID() (int, bool) {
	pid, err := findShellPID()
	return pid, err == nil
}

// callShell calls a function on the IpcHandler in shell.qml (target dcode).
func callShell(pid int, fn string) error {
	out, err := exec.Command("qs", "ipc", "--pid", strconv.Itoa(pid), "call", "dcode", fn).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w", msg, err)
		}
		return err
	}
	return nil
}

func init() {
	sendCmd.Flags().StringVarP(&sendSession, "session", "s", "", "Reuse an existing session (default: new session)")
	sendCmd.Flags().StringVarP(&sendModel, "model", "m", "", "Override the model")
}
