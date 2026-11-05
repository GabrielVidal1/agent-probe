// agent-probe for pi — what each bash call really cost, and what the file
// tools changed on disk. Written by `agent-probe install`, removed by
// `agent-probe uninstall`; edits are overwritten.
// # agent-probe
//
// Each event hands the call to `agent-probe hook --agent pi` (the same JSON
// as Claude Code's hooks, plus this process's pid: the pi process that
// spawns the call's shell) and waits for it — a few milliseconds — so the
// probe knows the call before its shell starts. It never blocks a call: any
// failure is swallowed.
import { spawn } from "node:child_process";

const BIN = "@@BIN@@";
const FILES: Record<string, string> = { write: "Write", edit: "Edit" };

function send(ev: Record<string, unknown>): Promise<void> {
	return new Promise((resolve) => {
		let done = false;
		const end = () => {
			if (!done) {
				done = true;
				resolve();
			}
		};
		try {
			const c = spawn(BIN, ["hook", "--agent", "pi"], { stdio: ["pipe", "ignore", "ignore"] });
			c.on("error", end);
			c.on("exit", end);
			c.stdin.on("error", () => {});
			c.stdin.end(JSON.stringify({ agent_pid: process.pid, t: Date.now() / 1000, ...ev }));
			setTimeout(end, 1500).unref?.();
		} catch {
			end();
		}
	});
}

// biome-ignore lint: pi's ExtensionAPI, untyped so the file needs no package
export default function (pi: any) {
	const sid = (ctx: any) => {
		try {
			return String(ctx?.sessionManager?.getSessionId?.() ?? "");
		} catch {
			return "";
		}
	};
	const cwd = (ctx: any) => String(ctx?.cwd ?? process.cwd());
	const tool = (event: string, e: any, ctx: any) => {
		const name = String(e.toolName ?? "");
		const input = e.input ?? e.args ?? {};
		const base = { hook_event_name: event, session_id: sid(ctx), tool_use_id: String(e.toolCallId ?? ""), cwd: cwd(ctx) };
		if (name === "bash") return send({ ...base, tool_name: "Bash", tool_input: { command: String(input.command ?? "") } });
		const path = input.path ?? input.file_path;
		if (FILES[name] && path) return send({ ...base, tool_name: FILES[name], tool_input: { file_path: String(path) } });
	};
	pi.on("tool_call", async (e: any, ctx: any) => {
		await tool("PreToolUse", e, ctx);
	});
	pi.on("tool_result", async (e: any, ctx: any) => {
		await tool("PostToolUse", e, ctx);
	});
	pi.on("input", async (_e: any, ctx: any) => {
		await send({ hook_event_name: "UserPromptSubmit", session_id: sid(ctx), cwd: cwd(ctx) });
	});
	pi.on("agent_settled", async (_e: any, ctx: any) => {
		await send({ hook_event_name: "Stop", session_id: sid(ctx), cwd: cwd(ctx) });
	});
}
