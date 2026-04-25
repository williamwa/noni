package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/williamwa/noni/internal/proto"
)

func streamCmd() *cobra.Command {
	var asJSON bool
	var skipBacklog bool
	c := &cobra.Command{
		Use:   "stream <id>",
		Short: "Tail PTY output of a session in real time",
		Long: "Stream PTY bytes from a session as they arrive. Default mode writes\n" +
			"raw bytes to stdout (so ANSI sequences render in your terminal).\n" +
			"With --json, emits one JSON frame per line: initial / chunk / state / end.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cli, err := dial()
			if err != nil {
				return err
			}
			defer cli.Close()
			req := proto.StreamReq{SessionID: args[0], SkipBacklog: skipBacklog}
			enc := json.NewEncoder(os.Stdout)
			return cli.CallStream("Stream", req, func(raw json.RawMessage) (bool, error) {
				var frame proto.StreamFrame
				if err := json.Unmarshal(raw, &frame); err != nil {
					return false, err
				}
				if asJSON {
					_ = enc.Encode(frame)
				} else if frame.Bytes != "" {
					b, err := base64.StdEncoding.DecodeString(frame.Bytes)
					if err != nil {
						return false, err
					}
					if _, err := os.Stdout.Write(b); err != nil {
						return false, err
					}
				}
				if frame.Kind == "end" {
					if !asJSON {
						code := 0
						if frame.ExitCode != nil {
							code = *frame.ExitCode
						}
						fmt.Fprintf(os.Stderr, "\n[noni: %s, exit_code=%d]\n", frame.Status, code)
					}
					return true, nil
				}
				return false, nil
			})
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit one JSON frame per line instead of raw bytes")
	c.Flags().BoolVar(&skipBacklog, "skip-backlog", false, "do not include bytes already buffered before the stream started")
	return c
}
