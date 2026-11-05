// Package agents is what agent-probe knows about each coding agent it
// measures: the program a session runs as (the root the kernel layer
// follows), the shape of the shell its shell tool starts, and how its hook
// or plugin names things.
//
// Adding an agent is one entry here, a ShellCommand shape if its shell is
// unusual, an installer in internal/setup and, for storage, its data dirs in
// internal/storage.
package agents

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Agent is one coding agent.
type Agent struct {
	Name  string   // the records' "agent": claude-code | opencode | codex
	Short string   // a report's column
	Comms []string // its process names (comm, or argv[0]'s base)
	// Scripts: an interpreter running one of these files (a base name, or a
	// path's tail) is the agent too — a node-run Claude Code.
	Scripts []string
	// Eval: the shell tool wraps the command in `eval '…'` (Claude Code);
	// a shell of the agent without it is not a call (its hooks, its status
	// line) and is never matched to one.
	Eval bool
	// Fifo: when no shell's command equals a waiting call's, the oldest
	// unmatched call takes the oldest unmatched shell (a PreToolUse hook
	// such as rtk rewrote the command after ours saw it).
	Fifo bool
	// Depth: how far under the agent's process the call's shell may be
	// (Codex runs it under its sandbox helper).
	Depth int
}

const (
	ClaudeCode = "claude-code"
	OpenCode   = "opencode"
	Codex      = "codex"
	Pi         = "pi"
)

// All are the agents agent-probe knows, in the order reports list them.
var All = []*Agent{
	{Name: ClaudeCode, Short: "claude", Comms: []string{"claude"}, Scripts: []string{"claude", "claude-code/cli.js"},
		Eval: true, Fifo: true, Depth: 2},
	{Name: OpenCode, Short: "opencode", Comms: []string{"opencode"}, Depth: 2},
	// sandboxed: codex → codex-linux-sandbox → bwrap → codex (in the
	// sandbox's namespaces) → bash -c cmd
	{Name: Codex, Short: "codex", Comms: []string{"codex"}, Depth: 4},
	// pi is a node program that names itself pi (process.title) once running
	{Name: Pi, Short: "pi", Comms: []string{"pi"}, Scripts: []string{"pi"}, Depth: 2},
}

// MaxComms is how many root names the kernel program holds.
const MaxComms = 4

// Comms are every agent's process names (what the kernel layer roots on).
func Comms() []string {
	var out []string
	for _, a := range All {
		out = append(out, a.Comms...)
	}
	return out
}

// ByName is the agent called name ("" or unknown: Claude Code — the hooks
// of claude-probe ≤ 0.3 name no agent).
func ByName(name string) *Agent {
	for _, a := range All {
		if a.Name == name || a.Short == name {
			return a
		}
	}
	return All[0]
}

// Known says whether name is an agent of ours.
func Known(name string) bool {
	for _, a := range All {
		if a.Name == name || a.Short == name {
			return true
		}
	}
	return false
}

// OfProcess is the agent a process is, from its comm and argv (nil: none).
func OfProcess(comm string, argv []string) *Agent {
	prog := ""
	if len(argv) > 0 {
		prog = filepath.Base(argv[0])
	}
	for _, a := range All {
		for _, c := range a.Comms {
			if comm == c || prog == c {
				return a
			}
		}
		if len(a.Scripts) > 0 && (prog == "node" || prog == "bun" || comm == "node" || comm == "bun") {
			for _, arg := range argv[1:min(3, len(argv))] {
				for _, s := range a.Scripts {
					if filepath.Base(arg) == s || strings.HasSuffix(arg, "/"+s) {
						return a
					}
				}
			}
		}
	}
	return nil
}

var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "fish": true, "ksh": true}

// cFlag: -c, or a group holding it (-lc, -ic, -ec).
var cFlag = regexp.MustCompile(`^-[a-zA-Z]*c[a-zA-Z]*$`)

// ShellCommand is the command a shell runs with -c — `bash -lc 'cmd'`,
// `/bin/sh -c cmd` — tolerating a command line the kernel cut short; ""
// if argv isn't such a shell.
func ShellCommand(argv []string) string {
	if len(argv) < 3 || !shells[filepath.Base(argv[0])] {
		return ""
	}
	// the options come first (`bash -c -l cmd` is a login shell running
	// cmd); with -c among them, the first operand is the command
	hasC := false
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			if hasC && i+1 < len(argv) {
				return argv[i+1]
			}
			return ""
		}
		if len(a) > 1 && (a[0] == '-' || a[0] == '+') {
			if cFlag.MatchString(a) {
				hasC = true
			}
			if a == "-o" || a == "+o" || a == "-O" || a == "+O" {
				i++ // its argument
			}
			continue
		}
		if hasC {
			return a
		}
		return ""
	}
	return ""
}

// EvalCommand is the command a Claude Code Bash-tool shell runs (its
// `eval '…'`); "" if argv isn't one.
func EvalCommand(argv []string) string {
	if ShellCommand(argv) == "" {
		return ""
	}
	last := argv[len(argv)-1]
	i := strings.Index(last, "eval '")
	if i < 0 {
		return ""
	}
	return unquoteEval(last[i+6:])
}

// ToolCommand is the command a's shell tool runs in a shell started with
// argv ("" if argv isn't one of its calls). a nil: any agent's shape.
func (a *Agent) ToolCommand(argv []string) string {
	if a == nil {
		if c := EvalCommand(argv); c != "" {
			return c
		}
		return ShellCommand(argv)
	}
	if a.Eval {
		return EvalCommand(argv)
	}
	return ShellCommand(argv)
}

// unquoteEval reads a single-quoted shell word from its first character on:
// a quote inside it is written '\” or '"'"' (what Claude Code uses); a
// command line the kernel cut short simply ends early.
func unquoteEval(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch {
		case strings.HasPrefix(s[i:], `'\''`):
			b.WriteByte('\'')
			i += 4
		case strings.HasPrefix(s[i:], `'"'"'`):
			b.WriteByte('\'')
			i += 5
		default:
			return b.String()
		}
	}
	return b.String()
}

// ShellTools are the tool names that run a shell command, per agent's hook
// (opencode v2 calls it shell, Codex Bash, local_shell or exec_command).
var ShellTools = map[string]bool{"Bash": true, "bash": true, "shell": true, "local_shell": true,
	"exec_command": true, "container.exec": true}
