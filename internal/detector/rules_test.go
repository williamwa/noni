package detector

import (
	"strings"
	"testing"

	"github.com/williamwa/noni/internal/proto"
)

func screenOf(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func TestRules_YesNo(t *testing.T) {
	cases := []struct {
		name    string
		screen  string
		want    proto.PromptType
		wantDef string
	}{
		{"y/n lower", "Continue? (y/n)", proto.PromptYesNo, ""},
		{"Y/n default y", "Proceed? [Y/n]", proto.PromptYesNo, "y"},
		{"y/N default n", "Delete everything? [y/N] ", proto.PromptYesNo, "n"},
		{"yes/no", "Are you sure? (yes/no)", proto.PromptYesNo, ""},
		{"trailing colon", "Confirm (Y/n): ", proto.PromptYesNo, "y"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Rules{}.Detect(Input{Screen: screenOf(c.screen)})
			if p == nil || p.Type != c.want {
				t.Fatalf("got %+v want type=%s", p, c.want)
			}
			if p.Default != c.wantDef {
				t.Errorf("default got %q want %q", p.Default, c.wantDef)
			}
		})
	}
}

func TestRules_Select(t *testing.T) {
	screen := []string{
		"? What account do you want to log into?",
		"  > GitHub.com",
		"    GitHub Enterprise Server",
	}
	p := Rules{}.Detect(Input{Screen: screen})
	if p == nil || p.Type != proto.PromptSelect {
		t.Fatalf("got %+v", p)
	}
	if len(p.Options) != 2 {
		t.Fatalf("options=%d want 2", len(p.Options))
	}
	if !p.Options[0].Selected || p.Options[1].Selected {
		t.Errorf("selection wrong: %+v", p.Options)
	}
	if !strings.Contains(p.Question, "What account") {
		t.Errorf("question got %q", p.Question)
	}
}

func TestRules_SelectFancyMarker(t *testing.T) {
	screen := []string{
		"? Pick one:",
		"    HTTPS",
		"  ❯ SSH",
	}
	p := Rules{}.Detect(Input{Screen: screen})
	if p == nil || p.Type != proto.PromptSelect {
		t.Fatalf("got %+v", p)
	}
	if p.Options[1].Label != "SSH" || !p.Options[1].Selected {
		t.Errorf("expected SSH selected: %+v", p.Options)
	}
}

func TestRules_Input(t *testing.T) {
	cases := []string{
		"Username: ",
		"Enter your name: ",
		"What is your email?",
		"> ",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			p := Rules{}.Detect(Input{Screen: screenOf(c)})
			if p == nil || p.Type != proto.PromptInput {
				t.Fatalf("got %+v", p)
			}
		})
	}
}

func TestRules_Password(t *testing.T) {
	// echo off + icanon on (line-buffered, hidden) = password
	p := Rules{}.Detect(Input{Screen: screenOf("Password: "), EchoOff: true})
	if p == nil || p.Type != proto.PromptPassword {
		t.Fatalf("got %+v", p)
	}
	if p.Echo {
		t.Errorf("password should be echo=false")
	}
}

// Regression: a TUI in raw mode (echo off + canon off) must NOT be
// classified as password. gh, fzf, etc. disable both flags to read
// arrow keys, but they're not asking for a hidden secret.
func TestRules_RawModeTUINotPassword(t *testing.T) {
	screen := []string{
		"? Where do you use GitHub?  [Use arrows to move, type to filter]",
		"  GitHub.com",
		"  Other",
	}
	p := Rules{}.Detect(Input{Screen: screen, EchoOff: true, CanonOff: true})
	if p != nil && p.Type == proto.PromptPassword {
		t.Fatalf("raw-mode TUI was misclassified as password: %+v", p)
	}
}

func TestRules_NoMatch(t *testing.T) {
	cases := []string{
		"",
		"this is just running output",
		"some progress 42%",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			p := Rules{}.Detect(Input{Screen: screenOf(c)})
			if p != nil {
				t.Fatalf("expected nil got %+v", p)
			}
		})
	}
}

// Regression: a select prompt should win over a generic-input fallback
// when the question line happens to end in '?'.
func TestRules_SelectBeatsInput(t *testing.T) {
	screen := []string{
		"? Choose:",
		"  > one",
		"    two",
	}
	p := Rules{}.Detect(Input{Screen: screen})
	if p == nil || p.Type != proto.PromptSelect {
		t.Fatalf("got %+v want select", p)
	}
}
