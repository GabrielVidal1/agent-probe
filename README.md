# agent-probe

What every shell command a coding agent runs really costs: CPU, peak memory,
disk I/O, GPU and exit code, for every process it started, per call and per
part of its command line (`cd x && npm run build | tail` is three parts),
subagents included. And what the agents' sessions keep on disk: scratch dirs,
git worktrees, transcripts, snapshots. Who made each, and what removing it
would free.

It works with **[Claude Code](https://claude.com/claude-code)**,
**[opencode](https://opencode.ai)**, **[Codex](https://github.com/openai/codex)**
and **[pi](https://pi.dev)**. Their transcripts keep none of this.

One static binary for Linux and macOS. No Docker, no Python, no daemon to
install: the first hook starts it.

```sh
curl -fsSL https://raw.githubusercontent.com/GabrielVidal1/agent-probe/main/install.sh | sh
```

That puts `agent-probe` in `~/.local/bin` and wires it into every agent it
finds on the machine. The agent's own hooks and settings are kept. New
sessions report from their first shell call.

```text
$ agent-probe report --agent codex
codex session 01a1ab3c-1968-71f0-b6fd-e7072bd08e0c · 1 calls · 0.24 s CPU · peak 8.3 MB · 1/1 from the kernel

      TIME   WALL     CPU    PEAK  DISK  EXIT COMMAND
  11:23:50  2.2 s  0.24 s  8.3 MB           0 sleep 2 && echo probe-ok && python3 -c 'print(sum(range(10**7)))'

$ agent-probe report        # a Claude Code session: heavy calls show their parts
claude session 5a405517-… · 5 calls · 0.91 s CPU · peak 132 MB · 5/5 from the kernel

      TIME    WALL     CPU     PEAK  EXIT COMMAND
  23:17:51  671 ms  0.62 s   132 MB     0 python3 -c 'x=bytearray(120*1024*1024) …
            612 ms  0.61 s   129 MB     0   └ python3
  23:17:53   52 ms   20 ms   9.2 MB     0 ls /nonexistent; echo after
             32 ms   14 ms   6.1 MB     2   └ ls
  23:18:01   69 ms  0.22 s   20.4 MB    0 [general-purpose] find /usr/lib -name "*.so" | wc -l

$ agent-probe top --by agent --since 7d
     CPU  RUNS     MAX    PEAK  AVG WALL  SESSIONS    AGENTS  AGENT
    45 s    16    26 s  473 MB     2.7 s         2    claude  claude
  0.38 s     5  0.24 s  8.3 MB    475 ms         3     codex  codex
  0.17 s     1  0.17 s  8.6 MB     2.2 s         1  opencode  opencode
```

## The agents

| | How it is wired in | Ids on each call | Its shell |
|---|---|---|---|
| Claude Code | hooks in `~/.claude/settings.json` (each `--config-dir`) | session, tool use, prompt, subagent | `bash -c '… eval '\''cmd'\'' …'` under `claude` |
| Codex | hooks in `$CODEX_HOME/hooks.json`, trusted for you | session, tool use, turn | `bash -c cmd`, under its sandbox (`codex-linux-sandbox → bwrap → codex`) when sandboxed |
| opencode (v2) | a server plugin, `~/.config/opencode/plugins/agent-probe.js` | session, tool call, message | `$SHELL -c cmd` under the `opencode` process |
| pi | an extension, `~/.pi/agent/extensions/agent-probe.ts` | session, tool call | `bash -c cmd` under `pi` |

`agent-probe install` wires in every agent present (its config dir or its
program exists). `--agent NAME` picks some, `agent-probe status` says which
are wired in, and `agent-probe uninstall` takes it all out. Codex runs a
hook only once its hash is trusted. `install` trusts agent-probe's own hooks
through `codex app-server`, the way `/hooks` does, and never anyone else's.

**Without a hook it still measures.** The daemon follows every agent process
(`claude`, `opencode`, `codex`, `pi`, found by name) and every shell its
shell tool starts. When no hook claims a shell within a second, the probe
follows it anyway. Such a call carries no tool id. Its session is the hooked
session of the same agent process, or else `<agent>:<pid>@<start>`, and its
record says `adopted: true`. An agent with no hooks, or one you didn't wire
in, is still measured this way. The daemon has to be running for that,
because no hook will start it: run `agent-probe daemon --idle 0` from a
service.

## How exact

| | Linux | macOS |
|---|---|---|
| Every process, even 1-ms ones | ✅ kernel (eBPF) | ✅ kqueue |
| Exit codes, nested too | ✅ | ✅ |
| CPU per process | ✅ | ✅ |
| Peak memory, disk I/O | ✅ | ✅ (a lower bound when the parent reaps first) |
| Setup | `sudo agent-probe setup-kernel`, once | none |

- **Linux, exact mode**: a CO-RE eBPF program embedded in the binary follows
  every fork, exec and exit under your agents' processes. It roots on their
  names at exec; a scan every 3 s catches the ones that rename themselves
  later (pi). Loading it needs `CAP_BPF` + `CAP_PERFMON`. `sudo agent-probe
  setup-kernel` copies the binary to `/usr/local/bin` (root-owned) and gives
  it those two file capabilities. It loads only its own program, traces only
  your own agents, and drops the capabilities right after loading. It needs
  kernel ≥ 5.8 with BTF (every current distribution). Without it, the probe
  samples `/proc`: processes shorter than a sample fold into their parent,
  and nested ones get no exit codes.
- **macOS, no setup**: kqueue (`EVFILT_PROC`) reports fork, exec and exit,
  with the exit status, for any process of yours. `proc_pid_rusage` still
  answers for a process that just exited: exact CPU, lifetime peak footprint
  and disk I/O. If its parent reaps it first, the last 40-ms sample stands,
  flagged `exact: false`. The call's CPU still counts it, through the
  shell's own child time.

## What sessions keep on disk

| Agent | Measured | |
|---|---|---|
| Claude Code | scratch dirs `/tmp/claude-<uid>/<cwd>/<session>/`, transcripts `~/.claude/projects/…` | scratch dirs can be removed and cleaned |
| Codex | rollouts `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-…-<session>.jsonl` | measured only |
| opencode | its db (`opencode.db` + `-wal`: every session), file snapshots per project (`snapshot/`), shell-call output (`shell/`) | measured only |
| pi | sessions `~/.pi/agent/sessions/<cwd>/<time>_<session>.jsonl` | measured only |
| any | git worktrees, extra roots you name | worktrees can be removed and cleaned |

The daemon measures them on a background-priority thread. It runs after
each shell call (the session's scratch dir, and its worktrees when the call
wrote a lot), at the end of each turn, and every few hours for the rest:

```text
$ agent-probe storage
  FILESYSTEM     SIZE     FREE  USED                                                       THE AGENTS'
           /   443 GB  80.9 GB   82%   worktrees 6.8 GB · transcripts 4.6 GB · snapshots 456 KB · shell output 268 KB
  /tmp (RAM)  14.2 GB   8.4 GB   40%                     scratchpads 1.9 GB · 11593 empty session dirs

        AGENT        KIND  ITEMS    OWNED  ON DISK
          any    worktree     13   6.8 GB  12.6 GB
  claude-code  transcript    669   5.3 GB   5.3 GB
  claude-code  scratchpad    161   1.9 GB   1.9 GB
     opencode  transcript      1  21.8 MB  21.8 MB
        codex  transcript     17   724 KB   724 KB

   OWNED   ON DISK  AGENT   KIND        IDLE  SESSION   PATH
  3.9 GB    3.9 GB  claude  scratchpad  1d    6a24cf1a  /tmp/claude-1000/-home-me-projects-app/6a24cf1a-…
                                                          scratchpad/dl 3.0 GB · scratchpad/work 636 MB
  880 MB   1010 MB  -       worktree    9d    b81c0e2a  ~/worktrees/app-crowd  crowd · merged · clean
```

- **Owned vs on disk**: a file counts as *owned* only when every hard link
  to it is inside the item. That is what removing the item would free. A
  worktree whose `node_modules` was hardlink-copied (`cp -al`) is gigabytes
  on disk and almost nothing owned.
- **Who**: a scratch dir and a transcript carry their session's id. A
  worktree is attributed to the call whose process ran `git worktree add`
  (the kernel layer sees its argv), and to every session whose cwd or
  command named it.
- **Every write, exact (Linux, kernel mode)**: the eBPF program also sees
  each regular file a call's processes open for writing. It reads the file's
  allocated size when opened and again when its last reference goes, so
  writes through a mapping count too (kernel ≥ 6.8). It also sees every last
  link they unlink or rename over. A call's record then carries `files`:
  bytes written, freed and net, files created and deleted, and the biggest
  files and directories. An install is one line:
  `…/node_modules +312 MB in 41 000 files`. The file tools (Claude Code's
  Write / Edit, opencode's and pi's write / edit) are measured by their hook
  with a stat before and after. Not counted: directories' own blocks, and
  what another daemon writes for the call (Docker's layers). macOS has no
  kernel layer for files, so there only the remeasures below apply.
- **Per call, everywhere**: what a call added to its session's scratch dir
  or worktrees is also recorded against it (`grow` records, e.g. `+1.9 GB`).
- `agent-probe storage rm PATH` removes a scratch dir or a worktree. It
  refuses when that would lose work (uncommitted changes, commits the main
  branch lacks) or when a live session uses it, unless `--force`. A merged
  branch goes with its worktree. `agent-probe storage clean` sweeps the
  scratch dirs of sessions quiet for 3 days, and the merged, clean worktrees
  idle as long. Without `--yes` it is a dry run.
- **The agent is told**: when the filesystem under a session's scratch dir
  or cwd is over 90 % full, its next prompt carries one line saying so, with
  how much its own scratch dir holds. This works for Claude Code and Codex,
  whose prompt hooks feed the model, and it is the only time the hook prints
  anything. `storage config --nudge-at 95%` moves the threshold, and `off`
  turns it off.
- `storage config --auto-clean 3` lets the daemon clean scratch dirs itself.
  `--worktree-root DIR` names a directory whose children are worktrees
  (`~/worktrees`). `--root DIR --label L` adds any other directory to measure.

## How it attributes

Every hook hands the call's ids to `agent-probe hook --agent NAME`, which
passes them to the daemon before the call's shell starts and never blocks
the call. Claude Code and Codex run it themselves. The opencode plugin and
the pi extension run it from inside the agent's process and add that
process's pid. The daemon then takes the agent's new shell whose command
matches: a child of the agent process, or a grandchild under a sandbox
helper. From there it follows everything under it, background jobs and
`setsid` daemons included, until they exit (24 h at most).

## Commands

```text
agent-probe install [--agent NAME]… [--statusline] [--config-dir DIR]…   wire in the agents found here
agent-probe uninstall [--agent NAME]… [--config-dir DIR]…
sudo agent-probe setup-kernel [--remove]                                  Linux exact mode, once
agent-probe status                                                        mode, and how each agent is measured
agent-probe migrate [--from DIR]                                          claude-probe's records, once

agent-probe report [SESSION] [--agent NAME] [--parts] [--json]   one session (default: the latest)
agent-probe top [--since 7d] [--by part|program|call|agent] [--agent NAME]
agent-probe export [--after ID]                                  the raw records (JSON)

agent-probe storage [--session ID] [--kind K] [--agent NAME] [--scan]
agent-probe storage rm PATH… [--force] [--dry-run]
agent-probe storage clean [--scratch 3] [--worktrees 3] [--yes]
agent-probe storage config [--worktree-root DIR] [--root DIR --label L] [--auto-clean DAYS]
```

`--statusline` wraps Claude Code's own status line and appends the session's
last call: `… │ ⚙ npm run build 9.9 s · 900 MB`.

Data stays on the machine: `~/.local/state/agent-probe/probe.db` (macOS:
`~/Library/Application Support/agent-probe/`), kept 30 days.

## Coming from claude-probe

agent-probe is [claude-probe](https://github.com/GabrielVidal1/claude-probe)
for every agent. It drops the Claude plan and usage readings and keeps
performance and storage. `agent-probe install` replaces claude-probe's hooks
and status line in place. `agent-probe migrate` copies its store and storage
config, keeping record ids so a reader's cursor carries on. Stop
claude-probe's daemon first.

## Building

```sh
make build          # bin/agent-probe for this machine (Go only: the eBPF objects are committed)
make test
make generate       # recompile internal/probe/bpf/probe.bpf.c (needs Docker: clang + bpftool)
make dist           # release binaries + checksums.txt
```

MIT.
