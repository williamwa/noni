package main

import (
	"errors"
	"net"
	"os"
	"os/exec"

	"github.com/creack/pty"
	"github.com/spf13/cobra"

	"github.com/williamwa/noni/internal/proto"
)

type doctorReport struct {
	Version       string         `json:"version"`
	SocketPath    string         `json:"socket_path"`
	SocketExists  bool           `json:"socket_exists"`
	SocketMode    string         `json:"socket_mode,omitempty"`
	DaemonUp      bool           `json:"daemon_up"`
	DaemonVersion string         `json:"daemon_version,omitempty"`
	DaemonUptimeS int64          `json:"daemon_uptime_s,omitempty"`
	NonidPath     string         `json:"nonid_path,omitempty"`
	PTYOK         bool           `json:"pty_ok"`
	Warnings      []string       `json:"warnings,omitempty"`
	Errors        []string       `json:"errors,omitempty"`
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose daemon, socket, and PTY support",
		RunE: func(_ *cobra.Command, _ []string) error {
			r := doctorReport{Version: Version, SocketPath: socketPath()}

			if fi, err := os.Stat(r.SocketPath); err == nil {
				r.SocketExists = true
				r.SocketMode = fi.Mode().Perm().String()
				if fi.Mode().Perm() != 0o600 {
					r.Warnings = append(r.Warnings, "socket mode "+r.SocketMode+" is not 0600")
				}
			}

			if cli, err := dial(); err == nil {
				var p proto.PingResp
				if err := cli.Call("Ping", struct{}{}, &p); err == nil {
					r.DaemonUp = true
					r.DaemonVersion = p.Version
					r.DaemonUptimeS = p.UptimeS
				} else {
					r.Errors = append(r.Errors, "daemon ping failed: "+err.Error())
				}
				cli.Close()
			} else {
				r.Errors = append(r.Errors, "could not reach daemon: "+err.Error())
			}

			if p, err := findDaemon(); err == nil {
				r.NonidPath = p
			} else {
				r.Warnings = append(r.Warnings, "nonid binary not found in PATH or alongside noni")
			}

			if f, _, err := openTestPTY(); err == nil {
				r.PTYOK = true
				_ = f.Close()
			} else {
				r.Errors = append(r.Errors, "PTY open failed: "+err.Error())
			}

			emit(r)
			if len(r.Errors) > 0 {
				return errors.New("doctor reported errors")
			}
			return nil
		},
	}
}

func openTestPTY() (*os.File, *exec.Cmd, error) {
	c := exec.Command("/bin/true")
	f, err := pty.Start(c)
	if err != nil {
		return nil, nil, err
	}
	_ = c.Wait()
	return f, c, nil
}

var _ = net.Dial
