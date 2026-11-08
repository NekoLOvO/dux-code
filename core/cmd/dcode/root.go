package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
	Commit    = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "dcode",
	Short: "dux-code Go daemon and command-line tool",
	Long: `dcode is the dux-code backend: it talks to LLM APIs and runs the
conversation loop itself, with no external agent in the way.

The UI (Quickshell) speaks NDJSON over a Unix socket; the command line
uses the same socket, so both always see the same state.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "dcode:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(pingCmd)
	rootCmd.AddCommand(sessionsCmd)
	rootCmd.AddCommand(newSessionCmd)
	rootCmd.AddCommand(sendCmd)
	rootCmd.AddCommand(showCmd)
	rootCmd.AddCommand(toggleCmd)
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	Run: func(_ *cobra.Command, _ []string) {
		fmt.Printf("dcode %s (built %s, commit %s)\n", Version, BuildTime, Commit)
	},
}

// socketPath is the fixed location shared by daemon and client.
//
// No per-pid filename on purpose: the UI has to reconnect after a daemon restart, and guessing
// a pid every time would make reconnection messy.
func socketPath() string {
	if p := os.Getenv("DUXCODE_SOCKET"); p != "" {
		return p
	}
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		runtime = os.TempDir()
	}
	return filepath.Join(runtime, "dcode", "daemon.sock")
}
