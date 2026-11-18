package probe

import (
	"math"
	"sort"
)

// Code Mode: Claude writes JavaScript that Claude Code runs through its REPL
// tool, and every Bash / Edit… call that code makes reaches the hooks as a
// call of its own (tool_use_id "repl_<uuid>"). Nothing in a call's hook
// input names the script that made it, so the daemon matches them by time:
// a script is open from its PreToolUse to its first PostToolUse (the code's
// body ended — it can return while a call it started still runs, whose end
// then comes as a second, "late" PostToolUse).
//
// A call from code goes to the open script of its session and subagent. Its
// pre hook can reach the daemon a moment after the script's post (separate
// hook processes race), so a script stays a candidate for scriptGraceS after
// it returned. With two or more candidates, or none (a call started by a
// `.then` after its script returned), the newest is taken and the call is
// flagged scriptGuess.

const (
	scriptGraceS = 2.0    // a returned script still takes the calls whose pre hook arrives late
	scriptKeepS  = 3600.0 // how long a returned script is kept for late calls
)

// script is one Code Mode script the daemon follows.
type script struct {
	ev       Event // its PreToolUse
	t0       float64
	returned float64 // its first PostToolUse
	durMs    *int64
	lateAt   float64 // its late PostToolUse: the last call it left running ended
	calls    []string
	guessed  []string
	live     map[string]bool // its calls still measured
	lastEnd  float64         // the latest end among its finished calls
}

// scriptPre opens a script (pr.mu held).
func (pr *Probe) scriptPre(ev Event, now float64) *script {
	if ev.Tid == "" || pr.scripts[ev.Tid] != nil {
		return nil
	}
	s := &script{ev: ev, t0: orF(ev.T, now), live: map[string]bool{}}
	pr.scripts[ev.Tid] = s
	return s
}

// scriptPost marks a script returned, or its late post (pr.mu held).
func (pr *Probe) scriptPost(ev Event, now float64) *script {
	s := pr.scripts[ev.Tid]
	if s == nil {
		return nil
	}
	t := orF(ev.T, now)
	if ev.Late || s.returned > 0 {
		s.lateAt = t
		return s
	}
	s.returned, s.durMs = t, ev.DurationMs
	return s
}

// scriptFor is the script a call from code belongs to, and whether that is
// a guess (pr.mu held).
func (pr *Probe) scriptFor(ev Event, now float64) (*script, bool) {
	if !FromCode(ev.Tid) {
		return nil, false
	}
	t := orF(ev.T, now)
	var open, grace, any []*script
	for _, s := range pr.scripts {
		if s.ev.Sid != ev.Sid || s.ev.AgentID != ev.AgentID || s.t0 > t+scriptGraceS {
			continue
		}
		any = append(any, s)
		switch {
		case s.returned == 0:
			open = append(open, s)
		case t-s.returned <= scriptGraceS:
			grace = append(grace, s)
		}
	}
	newest := func(ss []*script) *script {
		sort.Slice(ss, func(i, j int) bool { return ss[i].t0 > ss[j].t0 })
		return ss[0]
	}
	switch {
	case len(open) == 1:
		return open[0], false
	case len(open) > 1:
		return newest(open), true
	case len(grace) == 1:
		return grace[0], false
	case len(grace) > 1:
		return newest(grace), true
	case len(any) > 0:
		return newest(any), true
	}
	return nil, false
}

// attach gives a call from code to its script (pr.mu held).
func (pr *Probe) attach(ev *Event, now float64) *script {
	s, guess := pr.scriptFor(*ev, now)
	if s == nil {
		return nil
	}
	ev.Script, ev.ScriptGuess = s.ev.Tid, guess
	s.calls = append(s.calls, ev.Tid)
	if guess {
		s.guessed = append(s.guessed, ev.Tid)
	}
	s.live[ev.Tid] = true
	return s
}

// callEnded: one of a script's calls has its final record (pr.mu held).
func (pr *Probe) callEnded(tid, sid string, ended float64) *script {
	for _, s := range pr.scripts {
		if s.live[tid] && s.ev.Sid == sid {
			delete(s.live, tid)
			s.lastEnd = math.Max(s.lastEnd, ended)
			return s
		}
	}
	return nil
}

// pruneScripts forgets the scripts that can't get another call (pr.mu held).
func (pr *Probe) pruneScripts(now float64) {
	for tid, s := range pr.scripts {
		if (s.returned > 0 && len(s.live) == 0 && now-math.Max(s.returned, s.lastEnd) > scriptKeepS) ||
			now-s.t0 > BgCapS {
			delete(pr.scripts, tid)
		}
	}
}

// record is a script's "script" record: its code, when it ran, and the
// calls it made (their costs are in their own "call" records).
func (s *script) record() map[string]any {
	out := map[string]any{
		"v": 1, "agent": orStr(s.ev.Agent, "claude-code"), "tool": ScriptTool, "toolUseId": s.ev.Tid,
		"sessionId": s.ev.Sid, "agentId": nilIfEmpty(s.ev.AgentID), "agentType": nilIfEmpty(s.ev.AgentType),
		"promptId": nilIfEmpty(s.ev.PromptID), "cwd": nilIfEmpty(s.ev.Cwd), "code": s.ev.Code,
		"startedAt": r3(s.t0), "returnedAt": nil, "durationMs": s.durMs, "endedAt": nil,
		"calls": append([]string{}, s.calls...), "running": len(s.live),
	}
	if len(s.guessed) > 0 {
		out["guessed"] = append([]string{}, s.guessed...)
	}
	if s.returned > 0 {
		out["returnedAt"] = r3(s.returned)
		if len(s.live) == 0 {
			out["endedAt"] = r3(math.Max(s.returned, math.Max(s.lastEnd, s.lateAt)))
		}
	}
	return out
}

func (pr *Probe) putScript(s *script) {
	if s == nil {
		return
	}
	pr.mu.Lock()
	rec := s.record()
	pr.mu.Unlock()
	_ = pr.Store.Put("script", s.ev.Sid, s.ev.Tid, rec)
}

// missed says why a call's shell was never found: the record's "missed",
// which `report` and `status` show — an agent process the probe doesn't
// know (a renamed binary, a new way of starting) shows up here first.
func missed(c *Call) map[string]any {
	var chain []string
	for _, pid := range c.ev.Chain {
		if n := Comm(pid); n != "" {
			chain = append(chain, n)
		}
	}
	out := map[string]any{"parents": chain}
	if c.root == 0 {
		out["why"] = "no agent process among the hook's parents"
		return out
	}
	out["why"] = "no shell of the call under its agent process"
	out["root"] = Comm(c.root)
	return out
}
