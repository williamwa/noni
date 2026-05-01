package session

import (
	"strings"
	"testing"
	"time"

	"github.com/williamwa/noni/internal/proto"
)

// Verify the DSR responder unblocks a child that asks for cursor position.
// The shell prints CSI 6 n, reads the reply terminated by 'R', and echoes
// it on stdout. If noni doesn't answer, the read blocks forever.
func TestReplyDSR_UnblocksChild(t *testing.T) {
	m := NewManager()
	defer m.Stop()

	script := `printf '\033[6n'; IFS=';' read -rsd R row col; printf 'GOT row=%s col=%s\n' "$row" "${col#$'\033'[}"`
	s, err := m.Run(proto.RunReq{Cmd: "bash", Args: []string{"-c", script}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status() == proto.StatusExited {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.Status() != proto.StatusExited {
		t.Fatalf("session did not exit (DSR not answered?); status=%s", s.Status())
	}
	out := string(s.RawBytes())
	if !strings.Contains(out, "GOT row=") {
		t.Fatalf("expected DSR reply echoed; got: %q", out)
	}
}
