/** Harness navigation waits for destination surfaces to settle. */

import {
  type Json,
  byTestid,
  waitUntil,
} from "../semantic-driver/core.ts";
import { harnessControlFetch } from "../connection/backend.ts";
import { getRegisteredNoticeStore } from "../connection/app-connection.ts";
import { sessionScope } from "../../notices/notice-scope.ts";

import { scrollTranscriptFixture } from "./scroll-transcript-fixture.ts";

type ReadState = () => Json;
type WaitForSettled = (timeoutMs?: number) => Promise<Json>;
type LiveByTestid = (testid: string) => HTMLElement | null;
type LiveResolve = (selector: string) => HTMLElement | null;

export type JumpDeps = {
  readState: ReadState;
  waitForSettledSession: WaitForSettled;
  liveByTestid: LiveByTestid;
  liveResolve: LiveResolve;
  waitForIdle: (timeoutMs?: number) => Promise<Json>;
  sendPrompt: (text: string) => Promise<Json>;
  llmManual: () => Promise<Json>;
  llmRespond: (reply: {
    text?: string;
    toolCalls?: Array<{ id?: string; name: string; args?: Record<string, unknown> }>;
  }) => Promise<Json>;
};

async function harnessFetch(path: string, init?: RequestInit): Promise<Json> {
  return harnessControlFetch(path, init) as Promise<Json>;
}

function sessionIdFromState(state: Json): string {
  const id = state && typeof state === "object" && "sessionId" in state ? state.sessionId : null;
  return typeof id === "string" ? id.trim() : "";
}

const STAGE_NAV: Record<string, { entry: string; ready: string }> = {
  files: { entry: "project-files-entry", ready: "files-tree" },
  search: { entry: "project-search-entry", ready: "global-search-view" },
  security: { entry: "project-security-entry", ready: "project-scans-view" },
  cost: { entry: "project-cost-entry", ready: "project-cost-view" },
  artifacts: { entry: "project-artifacts-entry", ready: "project-artifacts-view" },
  blueprints: { entry: "project-blueprints-entry", ready: "project-blueprints-view" },
  extensions: { entry: "project-extensions-entry", ready: "project-extensions-view" },
};

export function listSessions(): Json {
  const rows = [...document.querySelectorAll<HTMLElement>('[data-testid="focused-session-list"] [data-session-id]')];
  return {
    ok: true,
    sessions: rows.map((el) => ({
      id: el.getAttribute("data-session-id") ?? "",
      projectId: el.getAttribute("data-project-id") ?? "",
      title: el.querySelector(".focused-session-list__title")?.textContent?.trim() ?? "",
      current: el.getAttribute("aria-current") === "true" || el.classList.contains("focused-session-list__row--active"),
    })),
  };
}

export async function switchSession(deps: JumpDeps, id: string): Promise<Json> {
  const sid = String(id ?? "").trim();
  if (!sid) return { ok: false, error: "session id required", state: deps.readState() };
  const row = () => document.querySelector<HTMLElement>(
    `[data-testid="focused-session-list"] [data-session-id="${CSS.escape(sid)}"]`,
  );
  if (!row()) {
    return { ok: false, error: `no session row for ${sid}`, ...listSessions(), state: deps.readState() };
  }
  const revealAfterSelection = !deps.liveByTestid("chat-composer") && row()?.getAttribute("aria-current") !== "true";
  row()?.click();
  const selected = await waitUntil(() => row()?.getAttribute("aria-current") === "true", {
    timeoutMs: 15_000, label: "selected session",
  });
  if (!selected.ok) return { ...selected, state: deps.readState() };
  // Selecting another chat preserves a full stage; activating the selected row reveals it.
  if (revealAfterSelection) row()?.click();
  const settled = await deps.waitForSettledSession();
  if (!settled.ok) return { ...settled, state: deps.readState() };
  const state = deps.readState();
  const current = sessionIdFromState(state);
  return {
    ok: current === sid,
    error: current === sid ? undefined : `settled on ${current || "(none)"}, wanted ${sid}`,
    state,
  };
}

export async function openStage(deps: JumpDeps, stageId: string): Promise<Json> {
  const id = String(stageId ?? "").trim();
  const meta = STAGE_NAV[id];
  if (!meta) {
    return {
      ok: false,
      error: `unknown stage "${id}"`,
      available: Object.keys(STAGE_NAV),
      state: deps.readState(),
    };
  }
  const entry = byTestid(meta.entry);
  if (!entry) {
    return {
      ok: false,
      error: `stage entry "${meta.entry}" not visible (may be hidden in context nav)`,
      state: deps.readState(),
    };
  }
  entry.click();
  const r = await waitUntil(() => !!byTestid(meta.ready), {
    timeoutMs: 15_000,
    label: `stage ${id}`,
  });
  return { ...r, stage: id, state: deps.readState() };
}

export async function openSearch(): Promise<Json> {
  const already = byTestid("crossbar");
  if (already) return { ok: true, state: "open" };
  const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
  document.dispatchEvent(
    new KeyboardEvent("keydown", {
      key: "k",
      code: "KeyK",
      metaKey: isMac,
      ctrlKey: !isMac,
      bubbles: true,
      cancelable: true,
    }),
  );
  return waitUntil(() => !!byTestid("crossbar"), {
    timeoutMs: 10_000,
    label: "crossbar",
  });
}

export async function openFile(deps: JumpDeps, path: string): Promise<Json> {
  const filePath = String(path ?? "").trim();
  if (!filePath) return { ok: false, error: "path required", state: deps.readState() };
  const stage = await openStage(deps, "files");
  if (!stage.ok) return stage;

  const parts = filePath.split("/").filter(Boolean);
  const treeRow = (testid: string, rowPath: string) =>
    document.querySelector<HTMLElement>(`[data-testid="${testid}"][data-path="${CSS.escape(rowPath)}"]`);
  // Rows render after the listing loads, and each expanded directory loads its own.
  for (let i = 0; i < parts.length - 1; i++) {
    const dirPath = parts.slice(0, i + 1).join("/");
    await waitUntil(() => !!treeRow("files-tree-dir", dirPath), { timeoutMs: 15_000, label: `directory ${dirPath}` });
    const dir = treeRow("files-tree-dir", dirPath);
    const toggle = dir?.querySelector<HTMLElement>('[data-testid="files-tree-dir-toggle"]') ?? dir;
    if (toggle && dir?.getAttribute("aria-expanded") !== "true") toggle.click();
  }

  const found = await waitUntil(() => !!treeRow("files-tree-file", filePath), {
    timeoutMs: 15_000,
    label: `file ${filePath}`,
  });
  const file = treeRow("files-tree-file", filePath);
  if (!found.ok || !file) {
    return { ok: false, error: `file not found: ${filePath}`, state: deps.readState() };
  }
  file.click();
  const r = await waitUntil(
    () => !!(byTestid("files-editor") || byTestid("files-editor-host") || byTestid("files-editor-crumb")),
    { timeoutMs: 15_000, label: "files editor" },
  );
  return { ...r, path: filePath, state: deps.readState() };
}

export async function rewind(deps: JumpDeps): Promise<Json> {
  const btn = deps.liveByTestid("user-bubble-rewind") ?? byTestid("user-bubble-rewind");
  if (!btn) {
    // Hover the last user bubble so the action toolbar mounts.
    const bubbles = [...document.querySelectorAll<HTMLElement>(".bubble--user")];
    const last = bubbles[bubbles.length - 1];
    if (last) {
      last.dispatchEvent(new MouseEvent("mouseenter", { bubbles: true }));
      last.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
    }
  }
  // The button stays mounted through a turn; rewind waits until it is available.
  const rewindBtn = await waitUntil(
    () => {
      const button = deps.liveByTestid("user-bubble-rewind") ?? byTestid("user-bubble-rewind");
      return !!button && button.getAttribute("aria-disabled") !== "true";
    },
    { timeoutMs: 5_000, label: "rewind button" },
  );
  if (!rewindBtn.ok) {
    return { ok: false, error: "no user-bubble-rewind control", state: deps.readState() };
  }
  (deps.liveByTestid("user-bubble-rewind") ?? byTestid("user-bubble-rewind"))?.click();
  const dialog = await waitUntil(() => !!byTestid("session-recover-dialog"), {
    timeoutMs: 10_000,
    label: "recover dialog",
  });
  if (!dialog.ok) return { ...dialog, state: deps.readState() };
  byTestid("session-recover-confirm")?.click();
  return { ok: true, state: deps.readState() };
}

export async function approveCheckpoint(deps: JumpDeps): Promise<Json> {
  const btn =
    deps.liveByTestid("approval-approve-primary") ?? byTestid("approval-approve-primary");
  if (!btn) {
    return { ok: false, error: "no approval-approve-primary", state: deps.readState() };
  }
  btn.click();
  return { ok: true, state: deps.readState() };
}

export async function denyCheckpoint(deps: JumpDeps): Promise<Json> {
  const btn = deps.liveByTestid("approval-no") ?? byTestid("approval-no");
  if (!btn) {
    return { ok: false, error: "no approval-no", state: deps.readState() };
  }
  btn.click();
  return { ok: true, state: deps.readState() };
}

export async function approveWorkflow(deps: JumpDeps): Promise<Json> {
  const btn =
    deps.liveByTestid("workflow-start-proposal-start") ?? byTestid("workflow-start-proposal-start");
  if (!btn) {
    return {
      ok: false,
      error: "no workflow-start-proposal-start",
      pending: !!(deps.liveByTestid("workflow-start-proposal-card") ?? byTestid("workflow-start-proposal-card")),
      state: deps.readState(),
    };
  }
  btn.click();
  return { ok: true, state: deps.readState() };
}

export function resize(width: number, height: number): Json {
  const w = Math.max(320, Math.floor(Number(width) || 0));
  const h = Math.max(240, Math.floor(Number(height) || 0));
  if (!w || !h) return { ok: false, error: "width and height required" };
  // window.resizeTo is best-effort in a harness tab.
  try {
    window.resizeTo(w, h);
  } catch {
    /* ignore */
  }
  window.dispatchEvent(new Event("resize"));
  return {
    ok: true,
    requested: { width: w, height: h },
    inner: { width: window.innerWidth, height: window.innerHeight },
  };
}

export function dump(deps: JumpDeps, testids: () => string[]): Json {
  const state = deps.readState();
  const stream =
    deps.liveResolve('[data-testid="chat-stream"]') ??
    deps.liveResolve('[data-testid="message-stream"]');
  const text = (stream?.innerText ?? "").trim();
  return {
    ok: true,
    at: new Date().toISOString(),
    state,
    transcript: { text, length: text.length },
    testids: testids(),
    sessions: listSessions(),
    url: location.href,
    debugHints: {
      scrollJsonl:
        import.meta.env.VITE_DEN_SCROLL_DEBUG_FILE ?? "scroll debug disabled",
      sidecarDebug: "~/.config/paintedwolf-dev/debug/latest (or lease state dir when harness-isolated)",
    },
  };
}

async function seedHugeTranscript(sessionId: string, count = 200): Promise<Json> {
  const n = Math.min(Math.max(1, count), 1000);
  const messages = Array.from({ length: n }, (_, i) => ({
    role: i % 2 === 0 ? "user" : "assistant",
    content: i % 2 === 0 ? `Seeded user turn ${i}` : `Seeded assistant reply ${i}`,
  }));
  return harnessFetch("/harness/transcript", {
    method: "POST",
    body: JSON.stringify({ session_id: sessionId, messages }),
  });
}

export async function goto(deps: JumpDeps, scenario: string, opts?: Json): Promise<Json> {
  const name = String(scenario ?? "").trim();
  const options = opts && typeof opts === "object" ? (opts as Record<string, unknown>) : {};
  const state = deps.readState();
  const sessionId = sessionIdFromState(state);
  if (!sessionId) {
    return { ok: false, error: "no session — openProject + newSession first", state };
  }

  switch (name) {
    case "scroll-transcript": {
      const messages = scrollTranscriptFixture(
        typeof options.turns === "number" ? options.turns : 5,
        typeof options.start === "number" ? options.start : 0,
        options.activity === true,
      );
      const seeded = await harnessFetch("/harness/transcript", {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId, messages }),
      });
      if (seeded.ok !== true) return { ok: false, error: "scroll transcript admission failed", seeded };
      return { ok: true, scenario: name, seeded, state: deps.readState() };
    }
    case "huge-transcript": {
      const count = typeof options.count === "number" ? options.count : 200;
      const seeded = await seedHugeTranscript(sessionId, count);
      return { ok: true, scenario: name, seeded, state: deps.readState() };
    }
    case "checkpoint-held": {
      const body: Record<string, unknown> = {
        session_id: sessionId,
        command: typeof options.command === "string" ? options.command : "git push origin main",
      };
      if (typeof options.consequence_band === "string") body.consequence_band = options.consequence_band;
      if (typeof options.approved_path === "string") body.approved_path = options.approved_path;
      if (options.direct_ip === true) body.direct_ip = true;
      const seeded = await harnessFetch("/harness/checkpoints/tool_approval", {
        method: "POST",
        body: JSON.stringify(body),
      });
      await waitUntil(
        () => !!(deps.liveByTestid("tool-approval-card") ?? byTestid("tool-approval-card")),
        { timeoutMs: 15_000, label: "tool-approval-card" },
      );
      return { ok: true, scenario: name, seeded, state: deps.readState() };
    }
    case "ask-user": {
      const seeded = await harnessFetch("/harness/ask_user", {
        method: "POST",
        body: JSON.stringify({
          session_id: sessionId,
          prompt: typeof options.prompt === "string" ? options.prompt : "Harness ask?",
          ...(typeof options === "object" ? options : {}),
        }),
      });
      return { ok: true, scenario: name, seeded, state: deps.readState() };
    }
    case "untrusted-content": {
      const seeded = await harnessFetch("/harness/sessions/untrusted-content", {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId }),
      });
      return { ok: true, scenario: name, seeded, state: deps.readState() };
    }
    case "review-verdict": {
      const seeded = await harnessFetch("/harness/review_loop_verdict", {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId, ...(typeof options === "object" ? options : {}) }),
      });
      return { ok: true, scenario: name, seeded, state: deps.readState() };
    }
    case "visual-fixture": {
      const seeded = await harnessFetch("/harness/visual_fixture", {
        method: "POST",
        body: JSON.stringify({ session_id: sessionId, ...(typeof options === "object" ? options : {}) }),
      });
      return { ok: true, scenario: name, seeded, state: deps.readState() };
    }
    case "spend-ceiling": {
      const store = getRegisteredNoticeStore();
      if (!store) return { ok: false, error: "notice store not registered", state };
      const projectId =
        state && typeof state === "object" && "projectId" in state && typeof state.projectId === "string"
          ? state.projectId
          : "";
      store.publish(
        {
          code: "session_spend_ceiling_reached",
          title: "Spend ceiling reached",
          message: "Harness spend-ceiling scenario",
        },
        sessionScope(projectId, sessionId),
      );
      return { ok: true, scenario: name, state: deps.readState() };
    }
    case "mid-stream": {
      await deps.llmManual();
      const sent = await deps.sendPrompt(
        typeof options.prompt === "string" ? options.prompt : "hold this turn for mid-stream",
      );
      const pending = await harnessFetch("/harness/llm/pending?wait=15000");
      return { ok: true, scenario: name, sent, pending, state: deps.readState() };
    }
    case "plan-awaiting": {
      // Holds the plan prompt mid-completion for a manual response.
      await deps.llmManual();
      const sent = await deps.sendPrompt(
        typeof options.prompt === "string" ? options.prompt : "/plan harness jump",
      );
      const pending = await harnessFetch("/harness/llm/pending?wait=15000");
      return {
        ok: true,
        scenario: name,
        note: "completion held — respond with plan text or use workflow API seed",
        sent,
        pending,
        state: deps.readState(),
      };
    }
    default:
      return {
        ok: false,
        error: `unknown scenario "${name}"`,
        available: [
          "huge-transcript",
          "scroll-transcript",
          "checkpoint-held",
          "ask-user",
          "untrusted-content",
          "review-verdict",
          "visual-fixture",
          "spend-ceiling",
          "mid-stream",
          "plan-awaiting",
        ],
        state,
      };
  }
}

export const JUMP_HELP: Json = {
  jump: [
    "goto(scenario, opts?)  // huge-transcript | scroll-transcript | checkpoint-held | ask-user | untrusted-content | review-verdict | visual-fixture | spend-ceiling | mid-stream | plan-awaiting",
    "listSessions()",
    "switchSession(id)",
    "openStage(id)  // files | search | security | cost | artifacts | blueprints | extensions",
    "openSearch()",
    "openFile(path)",
    "rewind()",
    "approveCheckpoint() / denyCheckpoint()",
    "approveWorkflow()",
    "resize(width, height)",
    "dump()",
  ],
};
