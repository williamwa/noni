package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/williamwa/noni/internal/ipc"
	"github.com/williamwa/noni/internal/proto"
)

const Version = "0.1.0-dev"

func main() {
	root := &cobra.Command{
		Use:   "noni",
		Short: "Drive interactive CLIs from anywhere",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(runCmd(), inputCmd(), keyCmd(), secretCmd(), readCmd(), waitCmd(), statusCmd(), listCmd(), killCmd(), pingCmd(), versionCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitCodeFor(err))
	}
}

func exitCodeFor(err error) int {
	var rpcErr *proto.RPCError
	if errors.As(err, &rpcErr) {
		switch rpcErr.Code {
		case proto.ENotFound, proto.EBadRequest, proto.ENotWaiting, proto.EAlreadyExited:
			return 1
		case proto.ETimeout:
			return 3
		case proto.EDaemonDown, proto.EPTYFailed, proto.EInternal:
			return 2
		}
	}
	return 1
}

func socketPath() string {
	if p := os.Getenv("NONI_SOCKET"); p != "" {
		return p
	}
	if r := os.Getenv("XDG_RUNTIME_DIR"); r != "" {
		return filepath.Join(r, "noni", "sock")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".noni", "sock")
}

// dial connects to the daemon, auto-spawning it if needed.
func dial() (*ipc.Client, error) {
	path := socketPath()
	if c, err := ipc.Dial(path); err == nil {
		return c, nil
	}
	if err := startDaemon(); err != nil {
		return nil, proto.NewError(proto.EDaemonDown, "spawn daemon: "+err.Error())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := ipc.Dial(path); err == nil {
			return c, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, proto.NewError(proto.EDaemonDown, "daemon did not come up at "+path)
}

func startDaemon() error {
	exe, err := findDaemon()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = detachAttrs()
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func findDaemon() (string, error) {
	if p := os.Getenv("NONID"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("nonid"); err == nil {
		return p, nil
	}
	// Sibling of current binary
	exe, err := os.Executable()
	if err == nil {
		cand := filepath.Join(filepath.Dir(exe), "nonid")
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("nonid binary not found in PATH or alongside noni")
}

func emit(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// --- commands ---

func runCmd() *cobra.Command {
	var waitMs, cols, rows int
	c := &cobra.Command{
		Use:   "run [--] <cmd> [args...]",
		Short: "Start a command in a PTY and return its first stable snapshot",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			req := proto.RunReq{
				Cmd: args[0], Args: args[1:],
				WaitMs: waitMs, Cols: cols, Rows: rows,
			}
			var snap proto.Snapshot
			if err := cli.Call("Run", req, &snap); err != nil {
				return err
			}
			emit(snap)
			return nil
		},
	}
	c.Flags().IntVar(&waitMs, "wait", 500, "settle time in ms before returning")
	c.Flags().IntVar(&cols, "cols", 0, "PTY columns")
	c.Flags().IntVar(&rows, "rows", 0, "PTY rows")
	return c
}

func inputCmd() *cobra.Command {
	var noNewline bool
	c := &cobra.Command{
		Use:   "input <id> <text>",
		Short: "Send text to a session (newline appended unless --no-newline)",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var snap proto.Snapshot
			if err := cli.Call("Input", proto.InputReq{
				SessionID: args[0], Text: args[1], Newline: !noNewline,
			}, &snap); err != nil {
				return err
			}
			emit(snap)
			return nil
		},
	}
	c.Flags().BoolVar(&noNewline, "no-newline", false, "do not append newline")
	return c
}

func keyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "key <id> <key>...",
		Short: "Send named keys (enter, up, ctrl-c, tab, ...) to a session",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var snap proto.Snapshot
			if err := cli.Call("Key", proto.KeyReq{
				SessionID: args[0], Keys: args[1:],
			}, &snap); err != nil {
				return err
			}
			emit(snap)
			return nil
		},
	}
}

func secretCmd() *cobra.Command {
	var envVar string
	var noNewline bool
	c := &cobra.Command{
		Use:   "secret <id> --env VAR",
		Short: "Send a secret read from the daemon's environment (no echo to logs)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if envVar == "" {
				return fmt.Errorf("--env is required")
			}
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var snap proto.Snapshot
			if err := cli.Call("Secret", proto.SecretReq{
				SessionID: args[0], EnvVar: envVar, Newline: !noNewline,
			}, &snap); err != nil {
				return err
			}
			emit(snap)
			return nil
		},
	}
	c.Flags().StringVar(&envVar, "env", "", "name of env var (read from daemon process env)")
	c.Flags().BoolVar(&noNewline, "no-newline", false, "do not append newline")
	return c
}

func readCmd() *cobra.Command {
	var tail int
	var raw bool
	c := &cobra.Command{
		Use:   "read <id>",
		Short: "Read current session screen",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var resp proto.ReadResp
			if err := cli.Call("Read", proto.ReadReq{
				SessionID: args[0], TailLines: tail, Raw: raw,
			}, &resp); err != nil {
				return err
			}
			emit(resp)
			return nil
		},
	}
	c.Flags().IntVar(&tail, "tail", 0, "only return last N lines")
	c.Flags().BoolVar(&raw, "raw", false, "include base64 raw bytes")
	return c
}

func waitCmd() *cobra.Command {
	var timeoutMs int
	var until string
	c := &cobra.Command{
		Use:   "wait <id>",
		Short: "Block until session state changes",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var snap proto.Snapshot
			if err := cli.Call("Wait", proto.WaitReq{
				SessionID: args[0], TimeoutMs: timeoutMs, Until: until,
			}, &snap); err != nil {
				return err
			}
			emit(snap)
			return nil
		},
	}
	c.Flags().IntVar(&timeoutMs, "timeout", 10000, "wait timeout in ms")
	c.Flags().StringVar(&until, "until", "state_change", "state_change|prompt|exit|idle")
	return c
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <id>",
		Short: "Get session snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var snap proto.Snapshot
			if err := cli.Call("Status", proto.IDReq{SessionID: args[0]}, &snap); err != nil {
				return err
			}
			emit(snap)
			return nil
		},
	}
}

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active sessions",
		RunE: func(_ *cobra.Command, _ []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var resp proto.ListResp
			if err := cli.Call("List", struct{}{}, &resp); err != nil {
				return err
			}
			emit(resp)
			return nil
		},
	}
}

func killCmd() *cobra.Command {
	var sig string
	c := &cobra.Command{
		Use:   "kill <id>",
		Short: "Send a signal to the session",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var resp proto.OKResp
			if err := cli.Call("Kill", proto.KillReq{SessionID: args[0], Signal: sig}, &resp); err != nil {
				return err
			}
			emit(resp)
			return nil
		},
	}
	c.Flags().StringVar(&sig, "signal", "TERM", "TERM|KILL|INT|HUP")
	return c
}

func pingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ping",
		Short: "Check daemon liveness",
		RunE: func(_ *cobra.Command, _ []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			var resp proto.PingResp
			if err := cli.Call("Ping", struct{}{}, &resp); err != nil {
				return err
			}
			emit(resp)
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print client version",
		RunE: func(_ *cobra.Command, _ []string) error {
			emit(map[string]string{"version": Version})
			return nil
		},
	}
}

var _ = net.Dial
