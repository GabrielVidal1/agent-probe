// Package hook is `agent-probe hook [--agent NAME]`: every agent's hook on
// its shell tool (before / after), prompt submit, turn end and subagent
// start / stop. Claude Code and Codex run it with their hook JSON on stdin;
// the opencode plugin and the pi extension send the same shape (plus
// agent_pid: the agent's own process, which runs the plugin). It hands the
// event to the daemon and exits — fire and forget, never fails the call.
// When no daemon answers it starts one and retries for up to a second.
//
// It prints nothing — a UserPromptSubmit hook's stdout lands in the model's
// context — except one line on a prompt when a filesystem the session writes
// to is nearly full (storage.NudgeLine; `storage config --nudge-at`).
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/GabrielVidal1/agent-probe/internal/agents"
	"github.com/GabrielVidal1/agent-probe/internal/paths"
	"github.com/GabrielVidal1/agent-probe/internal/probe"
	"github.com/GabrielVidal1/agent-probe/internal/storage"
)

var events = map[string]string{"PreToolUse": "pre", "PostToolUse": "post",
	"UserPromptSubmit": "prompt", "Stop": "stop", "SubagentStart": "subStart",
	"SubagentStop": "subStop"}

// Event turns a hook's stdin into the daemon's event (nil: not ours). agent
// is who runs the hook (claude-code when the hook line names none).
func Event(in map[string]any, agent string) *probe.Event {
	e := events[str(in["hook_event_name"])]
	if e == "" {
		return nil
	}
	a := agents.ByName(agent)
	tool := str(in["tool_name"])
	shellTool := agents.ShellTools[tool]
	if (e == "pre" || e == "post") && !shellTool && !probe.FileTools[tool] {
		return nil
	}
	ev := &probe.Event{E: e, T: float64(time.Now().UnixNano()) / 1e9, Agent: a.Name,
		Sid: str(in["session_id"]), PromptID: orStr(str(in["prompt_id"]), str(in["turn_id"])),
		AgentID: str(in["agent_id"]), AgentType: str(in["agent_type"]), Cwd: str(in["cwd"]),
		Effort: str(in["effort"])}
	if t, ok := in["t"].(float64); ok && t > 0 { // a plugin's own clock: when the tool started
		ev.T = t
	}
	if p, ok := in["agent_pid"].(float64); ok && p > 1 {
		ev.Root = int(p)
	}
	switch a.Name {
	case agents.ClaudeCode:
		ev.ConfigDir, ev.TmpDir = os.Getenv("CLAUDE_CONFIG_DIR"), os.Getenv("CLAUDE_CODE_TMPDIR")
	case agents.Codex:
		ev.ConfigDir = os.Getenv("CODEX_HOME")
	}
	inp, _ := in["tool_input"].(map[string]any)
	if (e == "pre" || e == "post") && probe.FileTools[tool] {
		ev.Tid, ev.Tool = str(in["tool_use_id"]), tool
		ev.FilePath = str(inp["file_path"])
		if ev.FilePath == "" {
			ev.FilePath = str(inp["notebook_path"])
		}
		if ev.FilePath != "" && !filepath.IsAbs(ev.FilePath) && ev.Cwd != "" {
			ev.FilePath = filepath.Join(ev.Cwd, ev.FilePath)
		}
		ev.File = statFile(ev.FilePath) // before the tool writes (pre), after (post)
		if e == "post" {
			if d, ok := in["duration_ms"].(float64); ok {
				v := int64(d)
				ev.DurationMs = &v
			}
		}
		return ev
	}
	if e == "pre" || e == "post" {
		ev.Tid, ev.Tool, ev.Command = str(in["tool_use_id"]), "Bash", command(inp["command"])
		if ev.Tid == "" {
			ev.Tid = str(in["call_id"])
		}
		ev.Bg, _ = inp["run_in_background"].(bool)
	}
	if e == "pre" && ev.Root == 0 {
		ev.Chain = chain()
	}
	if e == "post" {
		if d, ok := in["duration_ms"].(float64); ok {
			v := int64(d)
			ev.DurationMs = &v
		}
		if resp, ok := in["tool_response"].(map[string]any); ok {
			ev.BgTask = str(resp["backgroundTaskId"])
			ev.Interrupted, _ = resp["interrupted"].(bool)
		}
	}
	if e == "stop" || e == "subStop" {
		if bt, ok := in["background_tasks"].([]any); ok {
			n := len(bt)
			ev.BgTasks = &n
		}
	}
	return ev
}

// statFile is a file's size now, for a file tool's before / after.
func statFile(p string) *probe.FStat {
	if p == "" {
		return nil
	}
	var st unix.Stat_t
	if unix.Stat(p, &st) != nil {
		return &probe.FStat{}
	}
	return &probe.FStat{Exists: true, Bytes: int64(st.Blocks) * 512, Size: st.Size}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// command is a shell tool's command: a string (Claude Code, opencode, pi),
// or an argv (Codex's shell tool: ["bash", "-lc", "cmd"]).
func command(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		argv := make([]string, 0, len(c))
		for _, x := range c {
			argv = append(argv, str(x))
		}
		if cmd := agents.ShellCommand(argv); cmd != "" {
			return cmd
		}
		return strings.Join(argv, " ")
	}
	return ""
}

// chain is this hook's ancestors, nearest first (the CLI runs hooks through
// a short-lived shell, gone before the daemon could walk it itself).
func chain() []int {
	var out []int
	pid := os.Getppid()
	for i := 0; i < 6 && pid > 1; i++ {
		out = append(out, pid)
		pid = probe.ParentOf(pid)
	}
	return out
}

func client() *http.Client {
	sock := paths.Socket()
	return &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}}
}

func send(c *http.Client, body []byte) error {
	resp, err := c.Post("http://probe/event", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Run is the hook's whole life. self is this binary's path; out is where a
// prompt's nudge goes (the hook's stdout — read by Claude Code and Codex
// as context for the prompt).
func Run(stdin io.Reader, out io.Writer, self, agent string) {
	var in map[string]any
	if json.NewDecoder(stdin).Decode(&in) != nil {
		return
	}
	ev := Event(in, agent)
	if ev == nil {
		return
	}
	if ev.E == "prompt" && out != nil && (ev.Agent == agents.ClaudeCode || ev.Agent == agents.Codex) {
		if line := storage.NudgeLine(ev.Sid, ev.Cwd, ev.TmpDir); line != "" {
			io.WriteString(out, line+"\n")
		}
	}
	body, _ := json.Marshal(ev)
	c := client()
	if send(c, body) == nil {
		return
	}
	if os.Getenv("AGENT_PROBE_NO_SPAWN") != "" {
		return
	}
	spawn(self)
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		if send(c, body) == nil {
			return
		}
	}
}

// spawn starts the daemon detached (its own session; it outlives the hook).
func spawn(self string) {
	_ = paths.Ensure()
	logf, err := os.OpenFile(paths.Log(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer logf.Close()
	cmd := exec.Command(self, "daemon", "--idle", "30m")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}
