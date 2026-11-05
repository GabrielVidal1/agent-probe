// agent-probe: what every Bash call of Claude Code — and every process it
// started — really cost: CPU, peak memory, disk I/O, GPU and exit code, per
// call and per part of its command line, subagents included; plus the plan
// and the official usage reading at each turn.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/GabrielVidal1/agent-probe/internal/agents"

	"github.com/GabrielVidal1/agent-probe/internal/caps"
	"github.com/GabrielVidal1/agent-probe/internal/daemon"
	"github.com/GabrielVidal1/agent-probe/internal/hook"
	"github.com/GabrielVidal1/agent-probe/internal/paths"
	"github.com/GabrielVidal1/agent-probe/internal/probe"
	"github.com/GabrielVidal1/agent-probe/internal/report"
	"github.com/GabrielVidal1/agent-probe/internal/setup"
	"github.com/GabrielVidal1/agent-probe/internal/store"
)

var version = "dev"

const usage = `agent-probe — what each shell call of a coding agent (Claude Code, opencode, Codex,
pi) really cost, and what its sessions keep on disk.

  agent-probe install [--agent NAME]... [--statusline] [--config-dir DIR]...
                                         the hooks / plugins of every agent found here
  agent-probe uninstall [--agent NAME]... [--config-dir DIR]...   take them out again
  sudo agent-probe setup-kernel [--remove]                    Linux: exact mode (eBPF), once
  agent-probe status                                          what runs, in which mode
  agent-probe migrate [--from DIR] [--dry-run]                claude-probe's records and storage config

  agent-probe report [SESSION] [--agent NAME] [--parts] [--json]   one session's calls (default: the latest)
  agent-probe top [--since 7d] [--by part|program|call|agent] [--agent NAME] [--limit 20] [--json]
  agent-probe export [--after ID] [--limit N]       the raw records (JSON)

  agent-probe storage [--session ID] [--kind K] [--agent NAME] [--limit 20] [--json] [--scan]
                                    what sessions keep on disk: scratchpads, worktrees, transcripts
  agent-probe storage rm PATH… [--force] [--dry-run]       remove a scratch dir or a worktree, safely
  agent-probe storage clean [--scratch 3] [--worktrees 3] [--yes]
                                    the scratch dirs and merged worktrees quiet for N days (dry run
                                    without --yes)
  agent-probe storage config [--root DIR [--label L]] [--worktree-root DIR] [--rm-root DIR]
                              [--auto-clean DAYS]          extra dirs to track; the daemon's own cleaning

  agent-probe hook [--agent NAME]  (the agents' hooks run it)
  agent-probe statusline [--wrap CMD]   (the status line segment)
  agent-probe daemon [--idle 30m] [--no-kernel]   (started on demand by the hook)
  agent-probe debug                (run inside a Bash call: what the probe sees)
  agent-probe version
`

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd != "daemon" && cmd != "setup-kernel" {
		caps.Drop() // only the daemon needs the file capabilities (setup-kernel: root's own)
	}
	var err error
	switch cmd {
	case "hook":
		fs := flag.NewFlagSet("hook", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		agent := fs.String("agent", agents.ClaudeCode, "the agent running the hook")
		_ = fs.Parse(args)
		hook.Run(os.Stdin, os.Stdout, self(), *agent)
	case "daemon":
		err = runDaemon(args)
	case "install":
		err = runInstall(args, false)
	case "uninstall":
		err = runInstall(args, true)
	case "setup-kernel":
		err = runSetupKernel(args)
	case "status":
		err = runStatus()
	case "migrate":
		err = runMigrate(args)
	case "report":
		err = runReport(args)
	case "top":
		err = runTop(args)
	case "storage":
		err = runStorage(args)
	case "statusline":
		fs := flag.NewFlagSet("statusline", flag.ExitOnError)
		wrap := fs.String("wrap", "", "the user's own status line command, run first")
		_ = fs.Parse(args)
		err = report.Statusline(os.Stdout, os.Stdin, paths.DB(), *wrap)
	case "export":
		fs := flag.NewFlagSet("export", flag.ExitOnError)
		after := fs.Int64("after", 0, "records after this id")
		limit := fs.Int("limit", 1000, "at most this many")
		_ = fs.Parse(args)
		var st *store.Store
		if st, err = store.Open(paths.DB(), true); err == nil {
			defer st.Close()
			err = report.Export(os.Stdout, st, *after, *limit)
		} else if os.IsNotExist(err) {
			fmt.Println(`{"records":[],"last":0,"head":0,"enabled":true}`)
			err = nil
		}
	case "debug":
		// what the probe sees from inside a Bash call: run it as one
		pid := os.Getppid()
		for i := 0; i < 8 && pid > 1; i++ {
			argv := probe.ArgvOf(pid)
			b, _ := json.Marshal(map[string]any{"pid": pid, "comm": probe.Comm(pid), "argv": argv,
				"toolCommand": probe.ToolCommand(argv), "uid": probe.UID(pid)})
			fmt.Println(string(b))
			pid = probe.ParentOf(pid)
		}
	case "version", "--version", "-v":
		b, _ := json.Marshal(map[string]string{"version": version})
		fmt.Println(string(b))
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-probe:", err)
		os.Exit(1)
	}
}

func self() string {
	p, err := os.Executable()
	if err != nil {
		return "agent-probe"
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func runDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	idle := fs.Duration("idle", 30*time.Minute, "exit after this long with nothing to follow (0: never)")
	noKernel := fs.Bool("no-kernel", false, "sample /proc even when the kernel layer could load")
	noStorage := fs.Bool("no-storage", false, "don't track what sessions keep on disk")
	scanDelay := fs.Duration("scan-delay", time.Minute, "before the first storage pass")
	_ = fs.Parse(args)
	err := daemon.Run(daemon.Options{Idle: *idle, NoKernel: *noKernel, NoStorage: *noStorage,
		ScanDelay: *scanDelay, Version: version})
	if errors.Is(err, daemon.ErrRunning) {
		return nil
	}
	return err
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// hookBin is the binary the hooks should run: the capable copy setup-kernel
// installed, when there is one, else this one.
func hookBin() string {
	if setup.DefaultKernelPath != "" {
		if b, p := caps.FileCaps(setup.DefaultKernelPath); b && p {
			return setup.DefaultKernelPath
		}
	}
	return self()
}

func runInstall(args []string, remove bool) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var dirs, only multi
	fs.Var(&only, "agent", "claude-code | opencode | codex | pi (repeatable; default: every agent on this machine)")
	fs.Var(&dirs, "config-dir", "a Claude Code config dir (default: $CLAUDE_CONFIG_DIR or ~/.claude); repeatable")
	statusline := fs.Bool("statusline", false, "also show the last call's cost in Claude Code's status line")
	bin := fs.String("bin", "", "the binary the hooks run (default: this one, or the setup-kernel copy)")
	all := fs.Bool("all", false, "every agent, even those not found on this machine")
	_ = fs.Parse(args)
	b := *bin
	if b == "" {
		b = hookBin()
	}
	n := 0
	for _, t := range setup.Targets(only, dirs) {
		if len(only) == 0 && !*all && !remove && !setup.Present(t.Agent, dirs) {
			continue
		}
		changed, err := setup.Install(t, setup.Options{Bin: b, Statusline: *statusline, Remove: remove})
		if err != nil {
			return fmt.Errorf("%s: %w", t.Agent, err)
		}
		if remove && !changed {
			continue
		}
		n++
		verb := map[bool]string{false: "set in", true: "removed from"}[remove]
		if !changed {
			verb = "already up to date:"
		}
		what := map[string]string{"hooks": "hooks", "plugin": "plugin"}[t.Kind]
		fmt.Printf("%-12s %s %s %s\n", t.Agent, what, verb, t.Path)
		if !remove && t.Agent == agents.Codex {
			// Codex runs a hook only once its hash is trusted
			if n, err := setup.TrustCodexHooks("", 30*time.Second); err != nil {
				fmt.Printf("%-12s not trusted yet (%v): /hooks in codex trusts them\n", "", err)
			} else if n > 0 {
				fmt.Printf("%-12s %d hooks trusted\n", "", n)
			}
		}
	}
	if remove {
		if n == 0 {
			fmt.Println("nothing to remove")
		}
		return nil
	}
	if n == 0 {
		fmt.Println("no agent found here (claude, opencode, codex, pi): --agent NAME to install anyway")
		return nil
	}
	fmt.Println("New sessions report from their first shell call: `agent-probe report` to see them.")
	if runtime.GOOS == "linux" && b == self() {
		fmt.Println("Exact mode (every process, exit codes, from the kernel): sudo", self(), "setup-kernel")
	}
	return nil
}

func runSetupKernel(args []string) error {
	fs := flag.NewFlagSet("setup-kernel", flag.ExitOnError)
	remove := fs.Bool("remove", false, "take the capabilities off again")
	target := fs.String("path", setup.DefaultKernelPath, "where the capable binary goes")
	_ = fs.Parse(args)
	if err := setup.SetupKernel(self(), *target, *remove); err != nil {
		return err
	}
	if *remove {
		fmt.Println("capabilities removed from", *target)
		return nil
	}
	fmt.Printf("%s: CAP_BPF + CAP_PERFMON set.\nNow, as yourself: %s install   (the hooks then run this copy)\n", *target, *target)
	return nil
}

func sockClient(timeout time.Duration) *http.Client {
	sock := paths.Socket()
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}}
}

func health() (map[string]any, error) {
	c := sockClient(time.Second)
	resp, err := c.Get("http://probe/health")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var h map[string]any
	raw, _ := io.ReadAll(resp.Body)
	return h, json.Unmarshal(raw, &h)
}

func runStatus() error {
	fmt.Printf("agent-probe %s (%s/%s) at %s\n", version, runtime.GOOS, runtime.GOARCH, self())
	if runtime.GOOS == "linux" {
		b, p := caps.FileCaps(hookBin())
		mode := "sampled (/proc) — exact mode: sudo agent-probe setup-kernel"
		if b && p {
			mode = "exact (eBPF) available: " + hookBin() + " has CAP_BPF + CAP_PERFMON"
		}
		fmt.Println("kernel layer:", mode)
	} else {
		fmt.Println("kernel layer: kqueue (no setup)")
	}
	if h, err := health(); err == nil {
		fmt.Printf("daemon: running (pid %.0f), mode %v, %v call(s) in flight %v\n", h["pid"], h["mode"], h["running"], h["note"])
	} else {
		fmt.Println("daemon: not running (the next hook starts it)")
	}
	fmt.Println("store:", paths.DB())
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tHERE\tMEASURED BY\tFILE")
	for _, t := range setup.Targets(nil, nil) {
		here := "no"
		if setup.Present(t.Agent, nil) {
			here = "yes"
		}
		how := "process tree only (no ids: `agent-probe install`)"
		if setup.Installed(t) {
			how = map[string]string{"hooks": "hooks", "plugin": "plugin"}[t.Kind] + " + process tree"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Agent, here, how, t.Path)
	}
	return tw.Flush()
}

func openStore() (*store.Store, error) {
	st, err := store.Open(paths.DB(), true)
	if os.IsNotExist(err) {
		return nil, errors.New("nothing recorded yet — `agent-probe install`, then run an agent")
	}
	return st, err
}

func runReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	parts := fs.Bool("parts", false, "the parts of every call, not only the heavy ones")
	agent := fs.String("agent", "", "the latest session of this agent (claude, opencode, codex, pi)")
	_ = fs.Parse(reorder(args))
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return report.Report(os.Stdout, st, fs.Arg(0), *agent, *asJSON, *parts)
}

func runTop(args []string) error {
	fs := flag.NewFlagSet("top", flag.ExitOnError)
	since := fs.String("since", "7d", "how far back (e.g. 24h, 7d, 30d)")
	by := fs.String("by", "part", "part | program | call | agent")
	agent := fs.String("agent", "", "one agent's calls only")
	limit := fs.Int("limit", 20, "rows")
	asJSON := fs.Bool("json", false, "JSON output")
	_ = fs.Parse(args)
	d, err := parseSince(*since)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	return report.Top(os.Stdout, st, d, *by, *agent, *limit, *asJSON)
}

func parseSince(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// reorder puts flags before positionals (`report SID --json` works).
func reorder(args []string) []string {
	var flags, pos []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// runMigrate imports claude-probe's records (ids kept: the ai-agent hub's
// cursor carries on) and its storage config, once, into an empty store.
func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	oldState, oldSock := paths.Legacy()
	from := fs.String("from", oldState, "claude-probe's state dir")
	dry := fs.Bool("dry-run", false, "say what would be copied")
	_ = fs.Parse(args)
	db := filepath.Join(*from, "probe.db")
	if _, err := os.Stat(db); err != nil {
		return fmt.Errorf("no claude-probe store at %s", db)
	}
	if c, err := net.DialTimeout("unix", oldSock, 300*time.Millisecond); err == nil {
		c.Close()
		return errors.New("claude-probe's daemon is still running (it would keep writing): stop it first — pkill -f 'claude-probe daemon'")
	}
	if *dry {
		fmt.Printf("would copy %s → %s, and %s if this has none\n", db, paths.DB(), filepath.Join(*from, "storage.json"))
		return nil
	}
	st, err := store.Open(paths.DB(), false)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.Import(db, "usage") // the plan's usage readings: agent-probe doesn't keep them
	if err != nil {
		return err
	}
	fmt.Printf("%d records copied from %s (their ids kept)\n", n, db)
	cfgFrom, cfgTo := filepath.Join(*from, "storage.json"), filepath.Join(paths.StateDir(), "storage.json")
	if raw, err := os.ReadFile(cfgFrom); err == nil {
		if _, err := os.Stat(cfgTo); os.IsNotExist(err) {
			if err := os.WriteFile(cfgTo, raw, 0o600); err != nil {
				return err
			}
			fmt.Println("storage config copied:", cfgTo)
		}
	}
	return nil
}
