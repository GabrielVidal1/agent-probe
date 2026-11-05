package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GabrielVidal1/agent-probe/internal/storage"
	"github.com/GabrielVidal1/agent-probe/internal/store"
)

// StorageView is what `agent-probe storage` shows.
type StorageView struct {
	Disks  []storage.Disk                          `json:"disks"`
	Kinds  map[string]storage.KindTotal            `json:"kinds"`
	Agents map[string]map[string]storage.KindTotal `json:"agents"` // agent → kind → total ("" agent: anyone's — worktrees, roots)
	Items  []storage.Item                          `json:"items"`
}

func (v *StorageView) add(it storage.Item) {
	v.Items = append(v.Items, it)
	k := v.Kinds[it.Kind]
	k.Items++
	k.Bytes += it.Bytes
	k.Own += it.Own
	v.Kinds[it.Kind] = k
	if v.Agents[it.Agent] == nil {
		v.Agents[it.Agent] = map[string]storage.KindTotal{}
	}
	a := v.Agents[it.Agent][it.Kind]
	a.Items++
	a.Bytes += it.Bytes
	a.Own += it.Own
	v.Agents[it.Agent][it.Kind] = a
}

func newView(disks []storage.Disk) StorageView {
	return StorageView{Disks: disks, Kinds: map[string]storage.KindTotal{},
		Agents: map[string]map[string]storage.KindTotal{}}
}

// LoadStorage reads the newest version of every live item and filesystem.
func LoadStorage(st *store.Store) (StorageView, error) {
	v := newView(nil)
	recs, err := st.Latest("storage", "", 0)
	if err != nil {
		return v, err
	}
	for _, r := range recs {
		var it storage.Item
		if json.Unmarshal(r.Data, &it) != nil || it.Gone {
			continue
		}
		if it.Agent == "" && (it.Kind == "scratchpad" || it.Kind == "transcript") {
			it.Agent = "claude-code" // recorded by claude-probe
		}
		v.add(it)
	}
	sort.SliceStable(v.Items, func(i, j int) bool { return v.Items[i].Own > v.Items[j].Own })
	drecs, _ := st.Latest("disk", "", 0)
	for _, r := range drecs {
		var d storage.Disk
		if json.Unmarshal(r.Data, &d) == nil {
			v.Disks = append(v.Disks, d)
		}
	}
	sort.Slice(v.Disks, func(i, j int) bool { return v.Disks[i].Mount < v.Disks[j].Mount })
	return v, nil
}

// Filter keeps the items of one session (id prefix), kind and/or agent.
func (v StorageView) Filter(session, kind, agent string) StorageView {
	if session == "" && kind == "" && agent == "" {
		return v
	}
	out := newView(v.Disks)
	for _, it := range v.Items {
		if kind != "" && it.Kind != kind {
			continue
		}
		if agent != "" && it.Agent != agent && !strings.HasPrefix(it.Agent, agent) {
			continue
		}
		if session != "" && !belongs(it, session) {
			continue
		}
		out.add(it)
	}
	return out
}

func belongs(it storage.Item, sid string) bool {
	if strings.HasPrefix(it.SessionID, sid) {
		return true
	}
	for _, s := range it.Sessions {
		if strings.HasPrefix(s, sid) {
			return true
		}
	}
	return false
}

// Tilde shortens a path under the home directory.
func Tilde(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}

func ago(s float64) string {
	if s <= 0 {
		return ""
	}
	switch {
	case s < 3600:
		return fmt.Sprintf("%dm", int(s/60))
	case s < 86400:
		return fmt.Sprintf("%dh", int(s/3600))
	}
	return fmt.Sprintf("%dd", int(s/86400))
}

var kindPlural = map[string]string{"scratchpad": "scratchpads", "worktree": "worktrees",
	"transcript": "transcripts", "snapshot": "snapshots", "output": "shell output", "root": "other"}

// GitState is a worktree's state in a few words.
func GitState(g *storage.Git) string {
	if g == nil {
		return ""
	}
	var w []string
	if g.Branch != "" {
		w = append(w, g.Branch)
	} else if g.Head != "" {
		w = append(w, "detached "+g.Head[:min(7, len(g.Head))])
	}
	if g.Merged != nil {
		if *g.Merged {
			w = append(w, "merged")
		} else if g.Ahead != nil {
			w = append(w, fmt.Sprintf("%d ahead of %s", *g.Ahead, g.Base))
		}
	}
	if g.Dirty != nil {
		if *g.Dirty > 0 {
			w = append(w, fmt.Sprintf("%d uncommitted", *g.Dirty))
		} else {
			w = append(w, "clean")
		}
	}
	if g.Locked {
		w = append(w, "locked")
	}
	return strings.Join(w, " · ")
}

// Storage prints the filesystems, the totals per kind and the biggest items.
func Storage(w io.Writer, v StorageView, limit int, asJSON bool) error {
	if asJSON {
		if v.Items == nil {
			v.Items = []storage.Item{}
		}
		if limit > 0 && len(v.Items) > limit {
			v.Items = v.Items[:limit]
		}
		return json.NewEncoder(w).Encode(v)
	}
	if len(v.Items) == 0 && len(v.Disks) == 0 {
		fmt.Fprintln(w, "nothing measured yet — the daemon's first pass runs a minute after it starts (`agent-probe storage --scan` runs one now)")
		return nil
	}
	now := float64(time.Now().Unix())
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "FILESYSTEM\tSIZE\tFREE\tUSED\t THE AGENTS'\t")
	for _, d := range v.Disks {
		name := d.Mount
		if d.RAM {
			name += " (RAM)"
		}
		var parts []string
		keys := make([]string, 0, len(d.Agents))
		for k := range d.Agents {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return d.Agents[keys[i]].Own > d.Agents[keys[j]].Own })
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %s", kindPlural[k], Bytes(d.Agents[k].Own)))
		}
		if d.EmptyScratch > 0 {
			parts = append(parts, fmt.Sprintf("%d empty session dirs", d.EmptyScratch))
		}
		used := 0.0
		if d.Total > 0 {
			used = 100 * float64(d.Total-d.Avail) / float64(d.Total)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.0f%%\t %s\t\n", name, Bytes(d.Total), Bytes(d.Avail), used, strings.Join(parts, " · "))
	}
	tw.Flush()
	fmt.Fprintln(w)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "AGENT\tKIND\tITEMS\tOWNED\tON DISK\t")
	type row struct {
		agent, kind string
		t           storage.KindTotal
	}
	var rows []row
	for a, kinds := range v.Agents {
		for k, t := range kinds {
			rows = append(rows, row{a, k, t})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].t.Own > rows[j].t.Own })
	for _, r := range rows {
		a := r.agent
		if a == "" {
			a = "any"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t\n", a, r.kind, r.t.Items, Bytes(r.t.Own), Bytes(r.t.Bytes))
	}
	tw.Flush()
	fmt.Fprintln(w)
	items := v.Items
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "   OWNED\t ON DISK\tAGENT\tKIND\tIDLE\tSESSION\tPATH")
	for _, it := range items {
		idle := now - it.NewestAt
		if it.Git != nil && it.Git.IdleFor > 0 {
			idle = float64(it.Git.IdleFor) + (now - it.Git.At)
		}
		sid := it.SessionID
		if len(sid) > 8 {
			sid = sid[:8]
		}
		what := Tilde(it.Path)
		if it.Label != "" {
			what = it.Label + "  " + what
		}
		if g := GitState(it.Git); g != "" {
			what += "  " + g
		}
		agent := shortAgent(it.Agent)
		if agent == "" {
			agent = "-"
		}
		fmt.Fprintf(tw, "%8s\t%8s\t%s\t%s\t%s\t%s\t%s\n", Bytes(it.Own), Bytes(it.Bytes), agent, it.Kind, ago(idle), sid, what)
		var top []string
		for _, e := range it.Top {
			if e.Own < 1<<20 && e.Bytes < 1<<20 {
				continue
			}
			s := e.Name + " " + Bytes(e.Own)
			if e.Bytes > e.Own+(1<<20) {
				s += fmt.Sprintf(" (+%s shared)", Bytes(e.Bytes-e.Own))
			}
			top = append(top, s)
			if len(top) == 4 {
				break
			}
		}
		if len(top) > 0 {
			fmt.Fprintf(tw, "\t\t\t\t\t\t  %s\n", strings.Join(top, " · "))
		}
	}
	tw.Flush()
	if len(v.Items) > len(items) {
		fmt.Fprintf(w, "… %d more (--limit 0 for all)\n", len(v.Items)-len(items))
	}
	return nil
}
