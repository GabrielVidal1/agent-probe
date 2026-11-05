package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeProbeHooksAreReplaced(t *testing.T) {
	t.Setenv("AGENT_PROBE_STATE_DIR", t.TempDir())
	p := filepath.Join(t.TempDir(), "settings.json")
	old := `{"hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "/usr/local/bin/claude-probe hook  # claude-probe"}]}],
	  "Stop": [{"hooks": [{"type": "command", "command": "/usr/local/bin/claude-probe hook  # claude-probe"}]}]}}`
	os.WriteFile(p, []byte(old), 0o600)
	if changed, err := Apply(p, Options{Bin: "/usr/local/bin/agent-probe"}); err != nil || !changed {
		t.Fatal(changed, err)
	}
	raw, _ := os.ReadFile(p)
	s := string(raw)
	if strings.Contains(s, "claude-probe") || strings.Count(s, "/usr/local/bin/agent-probe hook --agent claude-code  # agent-probe") != 6 {
		t.Fatal(s)
	}
}

func TestCodexHooksFile(t *testing.T) {
	t.Setenv("AGENT_PROBE_STATE_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	ts := Targets([]string{"codex"}, nil)
	if len(ts) != 1 || ts[0].Path != filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json") {
		t.Fatal(ts)
	}
	if _, err := Install(ts[0], Options{Bin: "/b/agent-probe", Statusline: true}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(ts[0].Path)
	s := string(raw)
	if !strings.Contains(s, `"matcher": "`+CodexMatcher+`"`) || !strings.Contains(s, "/b/agent-probe hook --agent codex") ||
		strings.Contains(s, "statusLine") || !Installed(ts[0]) {
		t.Fatal(s)
	}
}

func TestPluginsWrittenAndRemoved(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	for _, name := range []string{"opencode", "pi"} {
		ts := Targets([]string{name}, nil)
		if len(ts) != 1 || ts[0].Kind != "plugin" {
			t.Fatal(ts)
		}
		tg := ts[0]
		if changed, err := Install(tg, Options{Bin: `/opt/a "b"/agent-probe`}); err != nil || !changed {
			t.Fatal(changed, err)
		}
		raw, _ := os.ReadFile(tg.Path)
		if !strings.Contains(string(raw), `const BIN = "/opt/a \"b\"/agent-probe"`) || !strings.Contains(string(raw), "--agent\", \""+name) {
			t.Fatal(string(raw))
		}
		if changed, _ := Install(tg, Options{Bin: `/opt/a "b"/agent-probe`}); changed {
			t.Fatal("a second install changed it")
		}
		if changed, _ := Install(tg, Options{Remove: true}); !changed || Installed(tg) {
			t.Fatal("not removed")
		}
	}
	// someone else's file of that name is left alone
	tg := Targets([]string{"pi"}, nil)[0]
	os.MkdirAll(filepath.Dir(tg.Path), 0o755)
	os.WriteFile(tg.Path, []byte("// mine"), 0o644)
	if _, err := Install(tg, Options{Bin: "/x"}); err == nil {
		t.Fatal("overwrote a foreign file")
	}
	if changed, _ := Install(tg, Options{Remove: true}); changed {
		t.Fatal("removed a foreign file")
	}
}
