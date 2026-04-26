package detector

import (
	"regexp"
	"strings"

	"github.com/williamwa/noni/internal/proto"
)

// Rules is a layered detector: termios echo signal → yesno → select →
// generic input → unknown. Each layer returns nil to fall through.
type Rules struct{}

func (Rules) Detect(in Input) *proto.Prompt {
	// ECHO off + ICANON on = password (line-buffered, hidden input).
	// ECHO off + ICANON off = TUI in raw mode (arrow-key menus, etc.) —
	// the termios signal is ambiguous, so fall through to screen rules.
	if in.EchoOff && !in.CanonOff {
		return &proto.Prompt{
			Type:       proto.PromptPassword,
			Echo:       false,
			Confidence: 0.99,
			Question:   lastNonEmpty(in.Screen),
		}
	}
	tail := lastNonEmpty(in.Screen)
	if tail == "" {
		return nil
	}

	if p := matchYesNo(tail); p != nil {
		return p
	}
	if p := matchSelect(in.Screen); p != nil {
		return p
	}
	if p := matchInput(tail); p != nil {
		return p
	}
	return nil
}

// --- yesno ---

var (
	// (y/n), (Y/n), (yes/no), [y/N], [Y/n] etc. Case-insensitive.
	reYesNoParen   = regexp.MustCompile(`(?i)[\(\[]\s*(y|yes)\s*/\s*(n|no)\s*[\)\]]\s*[:?>]?\s*$`)
	reYesNoParenNY = regexp.MustCompile(`(?i)[\(\[]\s*(n|no)\s*/\s*(y|yes)\s*[\)\]]\s*[:?>]?\s*$`)
)

func matchYesNo(line string) *proto.Prompt {
	trimmed := strings.TrimSpace(line)
	def := ""
	if reYesNoParen.MatchString(trimmed) || reYesNoParenNY.MatchString(trimmed) {
		// Capital letter inside the bracket is the default.
		// e.g. "[Y/n]" → default y; "[y/N]" → default n.
		switch {
		case strings.Contains(trimmed, "Y/n") || strings.Contains(trimmed, "Yes/no"):
			def = "y"
		case strings.Contains(trimmed, "y/N") || strings.Contains(trimmed, "yes/No"):
			def = "n"
		}
		return &proto.Prompt{
			Type:       proto.PromptYesNo,
			Question:   trimmed,
			Default:    def,
			Echo:       true,
			Confidence: 0.9,
		}
	}
	return nil
}

// --- select ---

var (
	// "  > GitHub.com" / "  ❯ option" / "  * option"
	reSelectMarker = regexp.MustCompile(`^\s*([>❯*])\s+(\S.*)$`)
	// plain leading-whitespace option lines below the marker
	reSelectOption = regexp.MustCompile(`^\s{2,}(\S.*)$`)
)

func matchSelect(screen []string) *proto.Prompt {
	// Walk from bottom up looking for at least one marker line; collect
	// contiguous option lines around it; question is the nearest non-empty
	// line above.
	markerIdx := -1
	for i := len(screen) - 1; i >= 0; i-- {
		if reSelectMarker.MatchString(screen[i]) {
			markerIdx = i
			break
		}
	}
	if markerIdx < 0 {
		return nil
	}
	// Collect a contiguous block (going up and down) of option-shaped lines.
	start, end := markerIdx, markerIdx
	for start > 0 {
		l := screen[start-1]
		if reSelectMarker.MatchString(l) || reSelectOption.MatchString(l) {
			start--
			continue
		}
		break
	}
	for end < len(screen)-1 {
		l := screen[end+1]
		if reSelectMarker.MatchString(l) || reSelectOption.MatchString(l) {
			end++
			continue
		}
		break
	}

	var opts []proto.SelectOption
	for i := start; i <= end; i++ {
		if m := reSelectMarker.FindStringSubmatch(screen[i]); m != nil {
			opts = append(opts, proto.SelectOption{Label: strings.TrimSpace(m[2]), Selected: true})
		} else if m := reSelectOption.FindStringSubmatch(screen[i]); m != nil {
			opts = append(opts, proto.SelectOption{Label: strings.TrimSpace(m[1]), Selected: false})
		}
	}
	if len(opts) < 2 {
		return nil
	}
	q := ""
	for i := start - 1; i >= 0; i-- {
		if s := strings.TrimSpace(screen[i]); s != "" {
			q = s
			break
		}
	}
	return &proto.Prompt{
		Type:       proto.PromptSelect,
		Question:   q,
		Options:    opts,
		Echo:       true,
		Confidence: 0.85,
	}
}

// --- generic input ---

var (
	reInputTail = regexp.MustCompile(`[:?>]\s*$`)
)

func matchInput(line string) *proto.Prompt {
	trimmed := strings.TrimRight(line, " \t")
	if trimmed == "" {
		return nil
	}
	if !reInputTail.MatchString(trimmed) {
		return nil
	}
	return &proto.Prompt{
		Type:       proto.PromptInput,
		Question:   trimmed,
		Echo:       true,
		Confidence: 0.7,
	}
}

func lastNonEmpty(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}
