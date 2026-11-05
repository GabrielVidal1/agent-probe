package agents

import "testing"

func TestShellCommand(t *testing.T) {
	for argv, want := range map[*[]string]string{
		{"/bin/bash", "-c", "ls -la"}:                            "ls -la",
		{"bash", "-lc", "sleep 3 && echo hi"}:                    "sleep 3 && echo hi",
		{"/usr/bin/zsh", "-c", "--", "make"}:                     "make",
		{"sh", "-e", "-c", "true"}:                               "true",
		{"bash", "script.sh", "-c"}:                              "",
		{"python3", "-c", "print(1)"}:                            "",
		{"bash", "-c"}:                                           "",
		{"/bin/bash", "-c", "-l", "eval 'npm test' < /dev/null"}: "eval 'npm test' < /dev/null",
		{"bash", "-o", "pipefail", "-c", "a | b"}:                "a | b",
	} {
		if got := ShellCommand(*argv); got != want {
			t.Errorf("%v: %q, want %q", *argv, got, want)
		}
	}
}

func TestToolCommandPerAgent(t *testing.T) {
	cc := []string{"/bin/bash", "-c", "source /tmp/snap && shopt -u extglob && eval 'npm run build' < /dev/null && pwd -P >| /tmp/claude-1-cwd"}
	if got := ByName(ClaudeCode).ToolCommand(cc); got != "npm run build" {
		t.Fatal(got)
	}
	// Claude Code's own shells (its hooks, its status line) are not calls
	if got := ByName(ClaudeCode).ToolCommand([]string{"/bin/sh", "-c", "agent-probe hook"}); got != "" {
		t.Fatal(got)
	}
	if got := ByName(Codex).ToolCommand([]string{"bash", "-lc", "cargo build"}); got != "cargo build" {
		t.Fatal(got)
	}
	if got := (*Agent)(nil).ToolCommand(cc); got != "npm run build" {
		t.Fatal(got)
	}
}

func TestOfProcess(t *testing.T) {
	for _, c := range []struct {
		comm string
		argv []string
		want string
	}{
		{"claude", []string{"claude", "--resume"}, ClaudeCode},
		{"2.1.309", []string{"/home/u/.local/share/claude/versions/2.1.309/claude"}, ClaudeCode},
		{"node", []string{"node", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js"}, ClaudeCode},
		{"opencode", []string{"/home/u/.local/share/opencode-cli/2.0.32/opencode", "service"}, OpenCode},
		{"codex", []string{"/x/codex", "exec"}, Codex},
		{"pi", []string{"pi"}, Pi},
		{"node", []string{"node", "/r/node_modules/.bin/pi", "--mode", "json"}, Pi},
		{"node", []string{"node", "/r/node_modules/@earendil-works/pi-coding-agent/dist/cli.js"}, ""},
		{"bash", []string{"bash"}, ""},
	} {
		a := OfProcess(c.comm, c.argv)
		got := ""
		if a != nil {
			got = a.Name
		}
		if got != c.want {
			t.Errorf("%s %v: %q, want %q", c.comm, c.argv, got, c.want)
		}
	}
}
