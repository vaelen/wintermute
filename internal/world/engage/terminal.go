// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package engage

import "strings"

// terminalCommands lists every command available inside a terminal
// engagement. M5.7 wires the dispatch; M6 fills in the real handlers.
var terminalCommands = []string{
	"mail", "bb", "bbread", "bbpost", "bbcatchup",
	"upload", "download", "help",
}

// TerminalHandler is the built-in handler for kind='terminal' hosts.
// In M5.7 every M6 command returns a stub. The prompt is host.Prompt
// or "terminal> " by default.
type TerminalHandler struct {
	host *Host
}

// NewTerminalHandler returns a fresh handler bound to host.
func NewTerminalHandler(host *Host) *TerminalHandler {
	return &TerminalHandler{host: host}
}

// OnOpen writes the terminal's prompt to the participant.
func (h *TerminalHandler) OnOpen(p *Participant) {
	h.writePrompt(p)
}

// OnClose is a no-op for terminals — the room-level exit broadcast
// announces departure.
func (h *TerminalHandler) OnClose(_ *Participant, _ CloseReason) {}

// Handle dispatches a single line of terminal input.
func (h *TerminalHandler) Handle(p *Participant, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		h.writePrompt(p)
		return
	}
	cmd, _ := splitTerminalCmd(line)
	switch strings.ToLower(cmd) {
	case "help":
		h.writeHelp(p)
	case "mail", "bb", "bbread", "bbpost", "bbcatchup", "upload", "download":
		_ = p.Write(cmd + ": not yet implemented (M6).\r\n")
	default:
		_ = p.Write("> command not recognized — try `help`.\r\n")
	}
	h.writePrompt(p)
}

func (h *TerminalHandler) writePrompt(p *Participant) {
	prompt := h.host.Prompt
	if prompt == "" {
		prompt = "terminal> "
	}
	_ = p.Write(prompt)
}

func (h *TerminalHandler) writeHelp(p *Participant) {
	var b strings.Builder
	b.WriteString("Terminal commands:\r\n")
	for _, c := range terminalCommands {
		b.WriteString("  ")
		b.WriteString(c)
		b.WriteString("\r\n")
	}
	b.WriteString("Meta:\r\n")
	b.WriteString("  disengage  — close the terminal\r\n")
	b.WriteString("  look, who, help — world commands (briefly leave the terminal view)\r\n")
	_ = p.Write(b.String())
}

func splitTerminalCmd(line string) (cmd, rest string) {
	i := strings.IndexAny(line, " \t")
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}
