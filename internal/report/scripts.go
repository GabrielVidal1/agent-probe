package report

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/GabrielVidal1/agent-probe/internal/shellsplit"
	"github.com/GabrielVidal1/agent-probe/internal/store"
)

// Script is the part of a stored "script" record the views read: one Code
// Mode script (Claude Code's REPL tool) and the calls its code made.
type Script struct {
	ToolUseID  string   `json:"toolUseId"`
	SessionID  string   `json:"sessionId"`
	AgentType  *string  `json:"agentType"`
	Code       string   `json:"code"`
	StartedAt  float64  `json:"startedAt"`
	ReturnedAt *float64 `json:"returnedAt"`
	EndedAt    *float64 `json:"endedAt"`
	Calls      []string `json:"calls"`
	Running    int      `json:"running"`
}

func scriptsOf(st *store.Store, session string) map[string]Script {
	recs, err := st.Latest("script", session, 0)
	if err != nil {
		return nil
	}
	out := map[string]Script{}
	for _, r := range recs {
		var s Script
		if json.Unmarshal(r.Data, &s) == nil && s.ToolUseID != "" {
			out[s.ToolUseID] = s
		}
	}
	return out
}

// ScriptGroup is one script with its calls and what they cost together.
type ScriptGroup struct {
	ToolUseID string  `json:"toolUseId"`
	Code      string  `json:"code"`
	StartedAt float64 `json:"startedAt"`
	// Wall: from the script's start to its last call's end
	Wall float64 `json:"wallSeconds"`
	CPU  float64 `json:"cpuSeconds"`
	// PeakRss: the heaviest of its calls (parallel calls' peaks don't add up
	// for sure: they may not have peaked at the same moment)
	PeakRss int64 `json:"peakRss"`
	DiskNet int64 `json:"diskNet"`
	// Parallel: its calls' summed wall over the script's own (×1 = one at a
	// time; only shown from ×1.1)
	Parallel float64  `json:"parallel"`
	Failed   int      `json:"failed"`
	Calls    []string `json:"calls"`
	Guessed  []string `json:"guessed,omitempty"`
	Programs string   `json:"programs"`
	calls    []Call
}

// Label is the script's row: what it ran, not its JavaScript (whose first
// line is mostly `const [a, b] = await Promise.all([`).
func (g *ScriptGroup) Label() string {
	n := len(g.calls)
	s := fmt.Sprintf("⟨code⟩ %d call", n)
	if n != 1 {
		s += "s"
	}
	if g.Parallel >= 1.1 {
		s += fmt.Sprintf(" ×%.1f parallel", g.Parallel)
	}
	if g.Programs != "" {
		s += ": " + g.Programs
	}
	return s
}

func (g *ScriptGroup) exit() string {
	if g.Failed > 0 {
		return fmt.Sprintf("%d✗", g.Failed)
	}
	for _, c := range g.calls {
		if c.ExitCode != nil {
			return "0"
		}
	}
	return ""
}

// Row is a report line: a call of the model's own, or a script with its calls.
type Row struct {
	Call   *Call
	Script *ScriptGroup
}

func (r Row) start() float64 {
	if r.Script != nil {
		return r.Script.StartedAt
	}
	return r.Call.StartedAt
}

// Rows groups a session's calls under their Code Mode scripts, in time
// order; it says how many scripts there were and whether a call's script
// was a guess.
func Rows(cs []Call, scripts map[string]Script) ([]Row, int, bool) {
	groups := map[string]*ScriptGroup{}
	var rows []Row
	guessed := false
	for i := range cs {
		c := cs[i]
		if c.Script == "" {
			rows = append(rows, Row{Call: &cs[i]})
			continue
		}
		g := groups[c.Script]
		if g == nil {
			g = &ScriptGroup{ToolUseID: c.Script, StartedAt: c.StartedAt}
			if s, ok := scripts[c.Script]; ok {
				g.Code, g.StartedAt = s.Code, math.Min(s.StartedAt, c.StartedAt)
			}
			groups[c.Script] = g
			rows = append(rows, Row{Script: g})
		}
		g.calls = append(g.calls, c)
		g.Calls = append(g.Calls, c.ToolUseID)
		if c.ScriptGuess {
			g.Guessed = append(g.Guessed, c.ToolUseID)
			guessed = true
		}
	}
	for _, g := range groups {
		g.sum()
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].start() < rows[j].start() })
	return rows, len(groups), guessed
}

func (g *ScriptGroup) sum() {
	end, walls := g.StartedAt, 0.0
	for _, c := range g.calls {
		g.CPU += c.Totals.CPU
		g.PeakRss = max(g.PeakRss, c.Totals.PeakRss)
		if c.Files != nil {
			g.DiskNet += c.Files.Net
		}
		w := wall(c)
		walls += w
		end = math.Max(end, c.StartedAt+w)
		if (c.ExitCode != nil && *c.ExitCode != 0) || c.Signal != nil {
			g.Failed++
		}
	}
	g.Wall = math.Round((end-g.StartedAt)*1000) / 1000
	g.CPU = math.Round(g.CPU*1e4) / 1e4
	if g.Wall > 0 && walls > 0 {
		g.Parallel = math.Round(walls/g.Wall*10) / 10
	}
	g.Programs = programsOf(g.calls)
}

// programsOf: the programs the calls ran, in order, a pipeline's joined by
// " | ", a repeated one counted ("sed ×12 · git").
func programsOf(cs []Call) string {
	var order []string
	n := map[string]int{}
	for _, c := range cs {
		p := programs(c)
		if p == "" {
			continue
		}
		if n[p] == 0 {
			order = append(order, p)
		}
		n[p]++
	}
	for i, p := range order {
		if n[p] > 1 {
			order[i] = fmt.Sprintf("%s ×%d", p, n[p])
		}
	}
	return strings.Join(order, " · ")
}

// programs is what one call ran: "ls | wc", "git", "edit".
func programs(c Call) string {
	if c.Tool != "" && c.Tool != "Bash" {
		return strings.ToLower(c.Tool)
	}
	var b strings.Builder
	pipe := false
	for _, p := range shellsplit.Split(c.Command) {
		if p.Program != nil {
			if b.Len() > 0 {
				if pipe {
					b.WriteString(" | ")
				} else {
					b.WriteString(" · ")
				}
			}
			b.WriteString(*p.Program)
		}
		pipe = p.Op != nil && *p.Op == "|"
	}
	return b.String()
}

func disk(net int64) string {
	switch {
	case net > -4096 && net < 4096:
		return ""
	case net < 0:
		return "-" + Bytes(-net)
	}
	return "+" + Bytes(net)
}
