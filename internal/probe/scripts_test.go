package probe

import (
	"encoding/json"
	"testing"
)

func latestRec(t *testing.T, pr *Probe, kind, sid, key string) map[string]any {
	t.Helper()
	recs, err := pr.Store.Latest(kind, sid, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Key == key {
			var m map[string]any
			_ = json.Unmarshal(r.Data, &m)
			return m
		}
	}
	return nil
}

// Code Mode: a script's file-tool call is attributed to it, its record lists
// the calls; a call whose pre hook lands just after the script returned
// still goes to it (the hooks race); two open scripts make the newest a guess.
func TestScriptsGroupTheirCalls(t *testing.T) {
	pr, _ := newProbe(t, nil)
	t0 := nowF()
	ev := func(e, tool, tid string, dt float64) Event {
		return Event{E: e, T: t0 + dt, Agent: "claude-code", Sid: "s1", Tool: tool, Tid: tid, FilePath: "/tmp/x",
			File: &FStat{Exists: true, Bytes: 4096, Size: 3}}
	}
	// script A: one Edit, returns, then its late Bash pre (the race)
	pr.Handle(Event{E: "pre", T: t0, Sid: "s1", Tool: ScriptTool, Tid: "toolu_A", Code: "await edit(…)"})
	pr.Handle(ev("pre", "Edit", "repl_e1", 0.1))
	pr.Handle(ev("post", "Edit", "repl_e1", 0.2))
	pr.Handle(Event{E: "post", T: t0 + 0.3, Sid: "s1", Tool: ScriptTool, Tid: "toolu_A"})
	pr.Handle(ev("pre", "Edit", "repl_e2", 0.35))
	pr.Handle(ev("post", "Edit", "repl_e2", 0.4))
	call := latestRec(t, pr, "call", "s1", "repl_e1")
	if call["script"] != "toolu_A" || call["scriptGuess"] != nil {
		t.Fatalf("e1: %v", call)
	}
	if c := latestRec(t, pr, "call", "s1", "repl_e2"); c["script"] != "toolu_A" || c["scriptGuess"] != nil {
		t.Fatalf("e2 (pre after the script's post, within the grace): %v", c)
	}
	sc := latestRec(t, pr, "script", "s1", "toolu_A")
	if sc == nil || sc["code"] != "await edit(…)" || len(sc["calls"].([]any)) != 2 || sc["endedAt"] == nil {
		t.Fatalf("script A: %v", sc)
	}
	// two scripts open at once: the newest, flagged
	pr.Handle(Event{E: "pre", T: t0 + 10, Sid: "s1", Tool: ScriptTool, Tid: "toolu_B"})
	pr.Handle(Event{E: "pre", T: t0 + 11, Sid: "s1", Tool: ScriptTool, Tid: "toolu_C"})
	pr.Handle(ev("pre", "Edit", "repl_e3", 12))
	pr.Handle(ev("post", "Edit", "repl_e3", 12.1))
	if c := latestRec(t, pr, "call", "s1", "repl_e3"); c["script"] != "toolu_C" || c["scriptGuess"] != true {
		t.Fatalf("e3: %v", c)
	}
	// a subagent's calls stay with its own scripts; the model's own calls with none
	pr.Handle(Event{E: "pre", T: t0 + 20, Sid: "s1", AgentID: "sub1", Tool: ScriptTool, Tid: "toolu_D"})
	e4 := ev("pre", "Edit", "repl_e4", 20.5)
	e4.AgentID = "sub1"
	pr.Handle(e4)
	e4.E, e4.T = "post", t0+20.6
	pr.Handle(e4)
	if c := latestRec(t, pr, "call", "s1", "repl_e4"); c["script"] != "toolu_D" || c["scriptGuess"] != nil {
		t.Fatalf("e4: %v", c)
	}
	pr.Handle(ev("pre", "Edit", "toolu_plain", 21))
	pr.Handle(ev("post", "Edit", "toolu_plain", 21.1))
	if c := latestRec(t, pr, "call", "s1", "toolu_plain"); c["script"] != nil {
		t.Fatalf("a call of the model's own: %v", c)
	}
}

// a late PostToolUse (the call a script left running ended) doesn't reopen it
func TestScriptLatePost(t *testing.T) {
	pr, _ := newProbe(t, nil)
	t0 := nowF()
	pr.Handle(Event{E: "pre", T: t0, Sid: "s", Tool: ScriptTool, Tid: "toolu_A"})
	pr.Handle(Event{E: "post", T: t0 + 0.1, Sid: "s", Tool: ScriptTool, Tid: "toolu_A"})
	pr.Handle(Event{E: "post", T: t0 + 12, Sid: "s", Tool: ScriptTool, Tid: "toolu_A", Late: true})
	sc := latestRec(t, pr, "script", "s", "toolu_A")
	if sc["returnedAt"].(float64) > t0+1 || sc["endedAt"].(float64) < t0+11 {
		t.Fatalf("%v", sc)
	}
}
