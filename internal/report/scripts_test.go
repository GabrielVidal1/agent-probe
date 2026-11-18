package report

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GabrielVidal1/agent-probe/internal/store"
)

func TestReportNestsCallsUnderTheirScript(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t0 := 1795027827.0
	call := func(id, cmd string, start, ms float64, cpu float64, peak int64, script string, guess bool) map[string]any {
		end := start + ms/1000
		m := map[string]any{"v": 1, "agent": "claude-code", "tool": "Bash", "toolUseId": id, "sessionId": "s",
			"command": cmd, "status": "done", "source": "kernel", "startedAt": start, "endedAt": end,
			"durationMs": ms, "exitCode": 0, "totals": map[string]any{"cpu": cpu, "peakRss": peak}}
		if script != "" {
			m["script"] = script
		}
		if guess {
			m["scriptGuess"] = true
		}
		return m
	}
	for _, c := range []map[string]any{
		call("repl_1", "sleep 2 && echo slow-done", t0, 2100, 0.011, 5<<20, "toolu_A", false),
		call("repl_2", "ls /usr/lib | wc -l", t0, 283, 0.044, 12<<20, "toolu_A", false),
		call("repl_3", "python3 -c 'x=1'", t0, 1500, 0.2, 91<<20, "toolu_A", false),
		call("repl_4", "cd '/usr' && git -C ~/x log --oneline -2", t0+2.2, 115, 0.03, 9<<20, "toolu_A", true),
		call("toolu_B", "ls", t0+30, 12, 0.004, 3<<20, "", false),
	} {
		_ = st.Put("call", "s", c["toolUseId"].(string), c)
	}
	_ = st.Put("script", "s", "toolu_A", map[string]any{"toolUseId": "toolu_A", "sessionId": "s",
		"code": "const [a,b,c] = await Promise.all([", "startedAt": t0 - 0.1, "calls": []string{"repl_1"}})
	var b bytes.Buffer
	if err := Report(&b, st, "s", "", false, false); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	t.Log("\n" + out)
	for _, want := range []string{"1 script · 5 calls", "⟨code⟩ 4 calls ×1.7 parallel: sleep · ls | wc · python3 · git",
		"│ sleep 2 && echo slow-done", "│?cd '/usr'", "│? matched to its script by time"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	// the model's own call stays a top-level row, after the script
	if i, j := strings.Index(out, "⟨code⟩"), strings.LastIndex(out, " ls\n"); i < 0 || j < i {
		t.Error("row order")
	}
	b.Reset()
	if err := Report(&b, st, "s", "", true, false); err != nil || !strings.Contains(b.String(), `"scripts":[{"toolUseId":"toolu_A"`) {
		t.Fatal(err, b.String())
	}
}

func TestMissedWhy(t *testing.T) {
	got := MissedWhy(map[string]any{"why": "no shell of the call under its agent process", "root": "claude",
		"parents": []any{"sh", "2.1.325", "claude"}})
	if got != "no shell of the call under its agent process (claude); the hook ran under sh ← 2.1.325 ← claude" {
		t.Fatal(got)
	}
}
