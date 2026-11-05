package storage

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/GabrielVidal1/agent-probe/internal/agents"
)

// What the other agents keep (Claude Code's scratch dirs and transcripts are
// found in discover):
//
//   - Codex: a rollout per session, $CODEX_HOME/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl;
//   - opencode: every session in one SQLite db (opencode.db, its -wal), a git
//     object store of file snapshots per project (snapshot/<project>) and the
//     output of its shell calls (shell/<…>), under $XDG_DATA_HOME/opencode;
//   - pi: a .jsonl per session, ~/.pi/agent/sessions/<cwd>/<time>_<id>.jsonl.
//
// They are measured, never removed (`storage rm` and the cleaning only touch
// Claude Code's scratch dirs and worktrees).

// tailID: the session id a file name ends with (a UUID).
var tailID = regexp.MustCompile(`([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// The agents' dirs (tests replace them).
var (
	CodexHome = func() string {
		if d := os.Getenv("CODEX_HOME"); d != "" {
			return d
		}
		h, _ := os.UserHomeDir()
		return filepath.Join(h, ".codex")
	}
	OpencodeData = func() string {
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, "opencode")
		}
		h, _ := os.UserHomeDir()
		return filepath.Join(h, ".local", "share", "opencode")
	}
	PiAgentDir = func() string {
		if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
			return d
		}
		h, _ := os.UserHomeDir()
		return filepath.Join(h, ".pi", "agent")
	}
)

func (s *Scanner) otherAgents() []candidate {
	var out []candidate
	// Codex: sessions/YYYY/MM/DD/rollout-…-<id>.jsonl
	root := filepath.Join(CodexHome(), "sessions")
	years, _ := os.ReadDir(root)
	for _, y := range years {
		months, _ := os.ReadDir(filepath.Join(root, y.Name()))
		for _, m := range months {
			days, _ := os.ReadDir(filepath.Join(root, y.Name(), m.Name()))
			for _, d := range days {
				dir := filepath.Join(root, y.Name(), m.Name(), d.Name())
				files, _ := os.ReadDir(dir)
				for _, f := range files {
					if id := tailID.FindStringSubmatch(f.Name()); id != nil && !f.IsDir() {
						p := filepath.Join(dir, f.Name())
						out = append(out, candidate{kind: "transcript", agent: agents.Codex, path: p,
							sid: id[1], jsonl: p, files: []string{p}})
					}
				}
			}
		}
	}
	// opencode
	data := OpencodeData()
	if _, err := os.Stat(filepath.Join(data, "opencode.db")); err == nil {
		db := filepath.Join(data, "opencode.db")
		out = append(out, candidate{kind: "transcript", agent: agents.OpenCode, path: db,
			label: "every session", jsonl: db + "-wal", files: []string{db, db + "-wal", db + "-shm"}})
	}
	for kind, sub := range map[string]string{"snapshot": "snapshot", "output": "shell"} {
		ents, _ := os.ReadDir(filepath.Join(data, sub))
		for _, e := range ents {
			if e.IsDir() {
				out = append(out, candidate{kind: kind, agent: agents.OpenCode, path: filepath.Join(data, sub, e.Name())})
			}
		}
	}
	// pi: sessions/<cwd>/<time>_<id>.jsonl
	pis := filepath.Join(PiAgentDir(), "sessions")
	projects, _ := os.ReadDir(pis)
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(pis, p.Name()))
		for _, f := range files {
			if id := tailID.FindStringSubmatch(f.Name()); id != nil && !f.IsDir() {
				path := filepath.Join(pis, p.Name(), f.Name())
				out = append(out, candidate{kind: "transcript", agent: agents.Pi, path: path, sid: id[1],
					project: strings.Trim(p.Name(), "-"), jsonl: path, files: []string{path}})
			}
		}
	}
	return out
}
