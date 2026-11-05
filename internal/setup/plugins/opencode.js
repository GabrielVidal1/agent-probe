// agent-probe for opencode (v2 server plugin) — what each shell call really
// cost, and what the file tools changed on disk. Written by `agent-probe
// install`, removed by `agent-probe uninstall`; edits are overwritten.
// # agent-probe
//
// Each hook hands the call to `agent-probe hook --agent opencode` (the same
// JSON as Claude Code's hooks, plus this process's pid: the opencode process
// that spawns the call's shell) and waits for it — a few milliseconds — so
// the probe knows the call before its shell starts. It never blocks a call:
// any failure is swallowed.
import { spawn } from "node:child_process"

const BIN = "@@BIN@@"
const SHELL = new Set(["shell", "bash"])
const FILES = { write: "Write", edit: "Edit", multiedit: "MultiEdit", patch: "Edit" }

function send(ev) {
  return new Promise((resolve) => {
    let done = false
    const end = () => {
      if (!done) {
        done = true
        resolve()
      }
    }
    try {
      const c = spawn(BIN, ["hook", "--agent", "opencode"], { stdio: ["pipe", "ignore", "ignore"] })
      c.on("error", end)
      c.on("exit", end)
      c.stdin.on("error", () => {})
      c.stdin.end(JSON.stringify({ agent_pid: process.pid, t: Date.now() / 1000, ...ev }))
      setTimeout(end, 1500).unref?.()
    } catch {
      end()
    }
  })
}

function filePath(input) {
  return input?.filePath ?? input?.file_path ?? input?.path ?? ""
}

export default {
  id: "agent-probe",
  async setup(ctx) {
    const cwd = () => ctx.location?.directory ?? ctx.location?.path ?? process.cwd()
    const base = (e) => ({ session_id: String(e.sessionID ?? ""), tool_use_id: String(e.id ?? ""),
      prompt_id: String(e.messageID ?? ""), cwd: e.input?.workdir ?? cwd() })
    await ctx.tool.hook("execute.before", async (e) => {
      const name = String(e.tool ?? "").toLowerCase()
      if (SHELL.has(name)) {
        await send({ hook_event_name: "PreToolUse", tool_name: "Bash", ...base(e),
          tool_input: { command: String(e.input?.command ?? "") } })
      } else if (FILES[name] && filePath(e.input)) {
        await send({ hook_event_name: "PreToolUse", tool_name: FILES[name], ...base(e),
          tool_input: { file_path: filePath(e.input) } })
      }
    })
    await ctx.tool.hook("execute.after", async (e) => {
      const name = String(e.tool ?? "").toLowerCase()
      if (SHELL.has(name)) {
        await send({ hook_event_name: "PostToolUse", tool_name: "Bash", ...base(e),
          tool_input: { command: String(e.input?.command ?? "") } })
      } else if (FILES[name] && filePath(e.input)) {
        await send({ hook_event_name: "PostToolUse", tool_name: FILES[name], ...base(e),
          tool_input: { file_path: filePath(e.input) } })
      }
    })
  },
}
