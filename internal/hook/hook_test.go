package hook

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventMapping(t *testing.T) {
	pre := Event(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash",
		"session_id": "s", "tool_use_id": "toolu_1", "prompt_id": "p", "agent_id": "a1",
		"agent_type": "Explore", "tool_input": map[string]any{"command": "ls", "run_in_background": true}}, "")
	if pre == nil || pre.E != "pre" || pre.Tid != "toolu_1" || !pre.Bg || pre.AgentType != "Explore" || len(pre.Chain) == 0 {
		t.Fatal(pre)
	}
	post := Event(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash",
		"tool_use_id": "toolu_1", "duration_ms": 12.0, "tool_input": map[string]any{"command": "rtk ls"},
		"tool_response": map[string]any{"backgroundTaskId": "b1"}}, "")
	if post.DurationMs == nil || *post.DurationMs != 12 || post.BgTask != "b1" || post.Chain != nil {
		t.Fatal(post)
	}
	if Event(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read"}, "") != nil {
		t.Fatal("a Read call is not ours")
	}
	stop := Event(map[string]any{"hook_event_name": "Stop", "background_tasks": []any{1, 2}}, "")
	if stop.E != "stop" || *stop.BgTasks != 2 {
		t.Fatal(stop)
	}
	if Event(map[string]any{"hook_event_name": "Notification"}, "") != nil {
		t.Fatal("Notification is not ours")
	}
}

func TestOtherAgents(t *testing.T) {
	// Codex: Claude Code's shape, a turn id, the shell tool's argv
	cx := Event(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "shell",
		"session_id": "019a", "tool_use_id": "call_1", "turn_id": "t1",
		"tool_input": map[string]any{"command": []any{"bash", "-lc", "sleep 3 && echo hi"}}}, "codex")
	if cx == nil || cx.Agent != "codex" || cx.Command != "sleep 3 && echo hi" || cx.PromptID != "t1" || len(cx.Chain) == 0 {
		t.Fatal(cx)
	}
	// opencode's plugin: its own pid and clock, no chain to walk
	oc := Event(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "shell",
		"session_id": "ses_1", "tool_use_id": "c1", "agent_pid": 4242.0, "t": 1700000000.5,
		"tool_input": map[string]any{"command": "ls"}}, "opencode")
	if oc == nil || oc.Agent != "opencode" || oc.Root != 4242 || oc.T != 1700000000.5 || oc.Chain != nil {
		t.Fatal(oc)
	}
	if e := Event(map[string]any{"hook_event_name": "Stop", "session_id": "s"}, "nobody"); e.Agent != "claude-code" {
		t.Fatal("an unknown agent is Claude Code's", e)
	}
}

func TestNoDaemonPrintsNothingAndReturns(t *testing.T) {
	t.Setenv("AGENT_PROBE_SOCK", t.TempDir()+"/none.sock")
	t.Setenv("AGENT_PROBE_NO_SPAWN", "1")
	state := t.TempDir()
	t.Setenv("AGENT_PROBE_STATE_DIR", state)
	os.WriteFile(filepath.Join(state, "storage.json"), []byte(`{"nudgeAt": -1}`), 0o600)
	var out bytes.Buffer
	Run(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s"}`), &out, "/nonexistent", "")
	if out.Len() != 0 {
		t.Fatalf("printed %q", out.String())
	}
}

func TestPromptNudgesWhenTheDiskIsFull(t *testing.T) {
	t.Setenv("AGENT_PROBE_SOCK", t.TempDir()+"/none.sock")
	t.Setenv("AGENT_PROBE_NO_SPAWN", "1")
	state := t.TempDir()
	t.Setenv("AGENT_PROBE_STATE_DIR", state)
	// any disk is "full" past 0.01 %
	os.WriteFile(filepath.Join(state, "storage.json"), []byte(`{"nudgeAt": 0.0001}`), 0o600)
	var out bytes.Buffer
	Run(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s","cwd":"`+state+`"}`), &out, "/nonexistent", "")
	if !strings.HasPrefix(out.String(), "[agent-probe] Disk nearly full: ") {
		t.Fatalf("no nudge: %q", out.String())
	}
	out.Reset()
	// only a prompt carries it: a tool call's stdout isn't context
	Run(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","session_id":"s","cwd":"`+state+`"}`), &out, "/nonexistent", "")
	if out.Len() != 0 {
		t.Fatalf("a PreToolUse hook printed %q", out.String())
	}
}
