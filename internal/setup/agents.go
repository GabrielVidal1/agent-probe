package setup

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GabrielVidal1/agent-probe/internal/agents"
)

//go:embed plugins/opencode.js
var opencodePlugin string

//go:embed plugins/pi.ts
var piExtension string

// Target is one place an agent reads agent-probe's hooks from.
type Target struct {
	Agent string // agents.ClaudeCode…
	Path  string // the file
	Kind  string // hooks (a settings / hooks JSON) | plugin (a file of ours)
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// CodexHome is $CODEX_HOME, else ~/.codex.
func CodexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(home(), ".codex")
}

// OpencodeConfig is opencode's config dir ($OPENCODE_CONFIG_DIR, else
// $XDG_CONFIG_HOME/opencode, else ~/.config/opencode).
func OpencodeConfig() string {
	if d := os.Getenv("OPENCODE_CONFIG_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "opencode")
	}
	return filepath.Join(home(), ".config", "opencode")
}

// PiAgentDir is pi's agent dir ($PI_CODING_AGENT_DIR, else ~/.pi/agent).
func PiAgentDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".pi", "agent")
}

// Present says whether an agent is on this machine: its config dir exists,
// or its program is on PATH.
func Present(agent string, claudeDirs []string) bool {
	var dir, prog string
	switch agent {
	case agents.ClaudeCode:
		for _, p := range SettingsPaths(claudeDirs) {
			if _, err := os.Stat(filepath.Dir(p)); err == nil {
				return true
			}
		}
		prog = "claude"
	case agents.Codex:
		dir, prog = CodexHome(), "codex"
	case agents.OpenCode:
		dir, prog = OpencodeConfig(), "opencode"
	case agents.Pi:
		dir, prog = PiAgentDir(), "pi"
	}
	if dir != "" {
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}
	_, err := exec.LookPath(prog)
	return err == nil
}

// Targets are where each agent named (all of them when none is) reads the
// hooks from. claudeDirs are Claude Code config dirs (default $CLAUDE_CONFIG_DIR
// or ~/.claude).
func Targets(names []string, claudeDirs []string) []Target {
	want := map[string]bool{}
	for _, n := range names {
		want[agents.ByName(n).Name] = true
	}
	var out []Target
	for _, a := range agents.All {
		if len(want) > 0 && !want[a.Name] {
			continue
		}
		switch a.Name {
		case agents.ClaudeCode:
			for _, p := range SettingsPaths(claudeDirs) {
				out = append(out, Target{a.Name, p, "hooks"})
			}
		case agents.Codex:
			out = append(out, Target{a.Name, filepath.Join(CodexHome(), "hooks.json"), "hooks"})
		case agents.OpenCode:
			out = append(out, Target{a.Name, filepath.Join(OpencodeConfig(), "plugins", "agent-probe.js"), "plugin"})
		case agents.Pi:
			out = append(out, Target{a.Name, filepath.Join(PiAgentDir(), "extensions", "agent-probe.ts"), "plugin"})
		}
	}
	return out
}

// Install puts (or, opt.Remove, takes out) agent-probe at one target.
func Install(t Target, opt Options) (bool, error) {
	if t.Kind == "hooks" {
		opt.Agent = t.Agent
		if t.Agent != agents.ClaudeCode {
			opt.Statusline = false // only Claude Code has a status line command
		}
		return Apply(t.Path, opt)
	}
	cur, err := os.ReadFile(t.Path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if opt.Remove {
		if err != nil || !bytes.Contains(cur, []byte(Marker)) {
			return false, nil // not there, or not ours
		}
		return true, os.Remove(t.Path)
	}
	src := opencodePlugin
	if t.Agent == agents.Pi {
		src = piExtension
	}
	next := []byte(strings.ReplaceAll(src, "@@BIN@@", jsString(opt.Bin)))
	if bytes.Equal(cur, next) {
		return false, nil
	}
	if len(cur) > 0 && !bytes.Contains(cur, []byte(Marker)) {
		return false, fmt.Errorf("%s exists and isn't agent-probe's: left alone", t.Path)
	}
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o755); err != nil {
		return false, err
	}
	tmp := t.Path + ".tmp"
	if err := os.WriteFile(tmp, next, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, t.Path)
}

// Installed says whether agent-probe is in at the target.
func Installed(t Target) bool {
	raw, err := os.ReadFile(t.Path)
	return err == nil && bytes.Contains(raw, []byte(Marker))
}

// jsString escapes a path for a JS / TS string literal in double quotes.
func jsString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}
