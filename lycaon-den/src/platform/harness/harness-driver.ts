/** Semantic interface driver installed only in harness mode. */

import { EditorView } from "@codemirror/view";
import { harnessControlFetch } from "../connection/backend.ts";
import {
  type Json,
  byTestid,
  findTarget,
  isDisabled,
  resolve,
  setNativeValue,
  sleep,
  waitUntil,
} from "../semantic-driver/core.ts";
import { MAX_TEXT_SCALE, applyTextScale } from "../desktop/accessibility-text-size.ts";
import {
  CHAT_STAGE_SELECTOR,
  LIVE_CHAT_STAGE_SELECTOR,
} from "../../../shared/stage-selectors.ts";
import { getLycaonClient, getRegisteredNoticeStore } from "../connection/app-connection.ts";
import { APP_SCOPE, noticeScopeKey, type NoticeScope } from "../../notices/notice-scope.ts";
import type { NoticeInput } from "../../notices/notice-model.ts";
import type { PreviewEvent, SourceChangesEvent } from "../../api/types.ts";
import { applyPreviewEvent } from "../../chat/visual/preview-store.ts";
import { applySourceChangesEvent } from "../../files/source/source-events.ts";
import { openReviewLens } from "../../files/review/review-pane.ts";
import type { ReviewLensScope } from "../../files/review/review-model.ts";
import * as jump from "./harness-jump.ts";

type ToolCallInput = {
  id?: string;
  name: string;
  args?: Record<string, unknown>;
  args_truncated?: boolean;
  args_malformed?: boolean;
};

type StreamChunkInput = {
  content?: string;
  toolCalls?: ToolCallInput[];
  done?: boolean;
  progress?: boolean;
};

/** Supplies assistant turns for the manual provider (LYCAON_LLM_MANUAL=1). */
export interface HarnessLlmApi {
  auto(text?: string | false): Promise<Json>;
  manual(): Promise<Json>;
  pending(waitMs?: number): Promise<Json>;
  respond(reply: {
    text?: string;
    toolCalls?: ToolCallInput[];
    /** Streamed verbatim in place of tokenized text. */
    streamChunks?: StreamChunkInput[];
  }): Promise<Json>;
}

export interface HarnessApi {
  help(): Json;
  state(): Json;
  testids(): string[];
  transcript(): Json;
  waitForIdle(timeoutMs?: number): Promise<Json>;
  waitForTestid(testid: string, timeoutMs?: number): Promise<Json>;
  waitForText(text: string, timeoutMs?: number): Promise<Json>;
  openProject(name?: string): Promise<Json>;
  newSession(prompt?: string): Promise<Json>;
  prompt(text: string): Promise<Json>;
  sendPrompt(text: string): Promise<Json>;
  stop(): Promise<Json>;
  openTab(tabId: string): Promise<Json>;
  approveBlueprint(editedMarkdown?: string): Promise<Json>;
  openSettings(): Promise<Json>;
  goHome(): Promise<Json>;
  click(testidOrSelector: string): Promise<Json>;
  fill(testidOrSelector: string, value: string): Promise<Json>;
  clickText(text: string): Promise<Json>;
  /** Inject OS text scale. Omit scale to use locked AccessibilityXXXL max. */
  setTextScale(scale?: number): Json;
  /** Harness-only: push a user notice into the app store (e.g. spend-ceiling card). */
  publishNotice(input: NoticeInput, scope?: NoticeScope): Json;
  /** Harness-only: apply a preview SSE-shaped event into the live preview store. */
  applyPreview(ev: PreviewEvent): Json;
  /** Harness-only: apply a source_changed batch (inventory + open buffers). */
  applySourceChanges(ev: SourceChangesEvent): Json;
  /** Harness-only: open the Review lens (same module graph as the Files stage). */
  openReviewLens(projectId: string, scope?: ReviewLensScope): Json;
  /** Jump to a seeded scenario (checkpoint-held, huge-transcript, …). */
  goto(scenario: string, opts?: Json): Promise<Json>;
  listSessions(): Json;
  switchSession(id: string): Promise<Json>;
  openStage(stageId: string): Promise<Json>;
  openSearch(): Promise<Json>;
  openFile(path: string): Promise<Json>;
  rewind(): Promise<Json>;
  approveCheckpoint(): Promise<Json>;
  denyCheckpoint(): Promise<Json>;
  approveWorkflow(): Promise<Json>;
  resize(width: number, height: number): Json;
  dump(): Json;
  llm: HarnessLlmApi;
}

/** Same-origin call to a /harness control endpoint with the app's bearer token. */
async function harnessFetch(path: string, init?: RequestInit): Promise<Json> {
  return harnessControlFetch(path, init) as Promise<Json>;
}

function mapToolCalls(toolCalls: ToolCallInput[] = []) {
  return toolCalls.map((t) => ({
    id: t.id ?? `tc-${Date.now()}`,
    name: t.name,
    args: t.args ?? {},
    args_truncated: t.args_truncated === true,
    args_malformed: t.args_malformed === true,
  }));
}

const harnessLlm: HarnessLlmApi = {
  auto: (text) =>
    harnessFetch("/harness/llm/auto", {
      method: "POST",
      body: JSON.stringify({ enabled: text !== false, text: typeof text === "string" ? text : "" }),
    }),
  manual: () =>
    harnessFetch("/harness/llm/auto", { method: "POST", body: JSON.stringify({ enabled: false }) }),
  pending: (waitMs = 10_000) =>
    harnessFetch(`/harness/llm/pending?wait=${encodeURIComponent(String(waitMs))}`),
  respond: ({ text = "", toolCalls = [], streamChunks } = {}) =>
    harnessFetch("/harness/llm/pending?wait=0").then((p) => {
      if (!p.pending) return { ok: false, error: "no pending completion to respond to" };
      const body: Record<string, unknown> = {
        id: p.id,
        content: text,
        tool_calls: mapToolCalls(toolCalls),
      };
      if (streamChunks && streamChunks.length > 0) {
        body.stream_chunks = streamChunks.map((c) => ({
          content: c.content ?? "",
          tool_calls: mapToolCalls(c.toolCalls ?? []),
          done: c.done === true,
          progress: c.progress === true,
        }));
      }
      return harnessFetch("/harness/llm/respond", {
        method: "POST",
        body: JSON.stringify(body),
      });
    }),
};

declare global {
  interface Window {
    __harness?: HarnessApi;
  }
}

/** The foreground chat stage — never the crossfade's outgoing copy. */
function liveChatStage(): HTMLElement | null {
  return document.querySelector<HTMLElement>(LIVE_CHAT_STAGE_SELECTOR);
}

/** Resolves selectors against the foreground chat stage. */
function liveResolve(selector: string): HTMLElement | null {
  if (document.querySelector(CHAT_STAGE_SELECTOR)) {
    return liveChatStage()?.querySelector<HTMLElement>(selector) ?? null;
  }
  return resolve(selector);
}

function liveByTestid(testid: string): HTMLElement | null {
  return liveResolve(`[data-testid="${testid}"]`);
}

/** Waits for one stable foreground chat stage. */
async function waitForSettledSession(timeoutMs = 90_000): Promise<Json> {
  let lastSessionId = "";
  return waitUntil(
    () => {
      if (document.querySelectorAll(LIVE_CHAT_STAGE_SELECTOR).length !== 1) {
        lastSessionId = "";
        return false;
      }
      const id = (
        liveChatStage()
          ?.querySelector('[data-testid="chat-stream"]')
          ?.getAttribute("data-session-id") ?? ""
      ).trim();
      if (!id || id !== lastSessionId) {
        lastSessionId = id;
        return false;
      }
      return true;
    },
    { timeoutMs, settleMs: 400, label: "settled chat session" },
  );
}

function isBusy(): boolean {
  return !!liveByTestid("thinking-indicator") || !!liveByTestid("stop-coordinator");
}

function currentView(): string {
  // The card outlives the ask as the decision's record, so the approve control —
  // not the card — is what says this chat is still waiting on a person.
  if (liveByTestid("blueprint-approve")) return "blueprint-approval";
  if (liveByTestid("chat-composer")) return "chat";
  if (resolve(".den-settings-view") || byTestid("settings-nav-providers")) {
    if (resolve(".den-settings-editor")) return "settings";
  }
  if (byTestid("home-nav")) return "welcome";
  return byTestid("shell") ? "shell" : "loading";
}

function readState(): Json {
  const projectName =
    resolve('[data-testid="project-switcher"] .project-switcher__name')?.textContent?.trim() ?? null;
  const tabScope = liveChatStage() ?? document;
  const tabs = [...tabScope.querySelectorAll<HTMLElement>('[data-testid^="tab-"]')]
    .filter((el) => el.offsetParent !== null || el.getClientRects().length > 0)
    .map((el) => el.getAttribute("data-testid")?.slice(4))
    .filter(Boolean);
  const sessionId =
    liveResolve('[data-testid="chat-stream"][data-session-id]')
      ?.getAttribute("data-session-id")
      ?.trim() ||
    liveResolve("[data-session-id]")?.getAttribute("data-session-id")?.trim() ||
    null;
  return {
    view: currentView(),
    connected: byTestid("shell")?.getAttribute("data-sidecar-status") === "connected",
    busy: isBusy(),
    project: projectName,
    sessionId,
    hasComposer: !!liveByTestid("chat-composer"),
    pendingBlueprintApproval: !!byTestid("blueprint-approve"),
    pendingWorkflowProposal: !!byTestid("workflow-start-proposal-card"),
    tabs,
  };
}

function readTranscript(): Json {
  const stream =
    liveResolve('[data-testid="chat-stream"]') ?? liveResolve('[data-testid="message-stream"]');
  const text = (stream?.innerText ?? "").trim();
  const cardScope = liveChatStage() ?? document;
  const toolCards = cardScope.querySelectorAll('[data-testid^="tool-"], [data-testid$="tool-card"]').length;
  return { text, length: text.length, toolCards, busy: isBusy(), view: currentView() };
}

const HELP: Json = {
  note: "Live driver for the Den UI (harness mode). Methods perform real DOM actions and return JSON. Call await __harness.state() to see where you are.",
  typicalFlow: [
    "await __harness.openProject('Harness')",
    "await __harness.newSession('look at the repo')   // or pass nothing to just open a chat",
    "await __harness.prompt('say hello')              // sends + waits for the mock reply",
    "await __harness.transcript()                     // read what came back",
  ],
  reads: ["state()", "transcript()", "testids()"],
  waits: ["waitForIdle(timeoutMs?)", "waitForTestid(testid, timeoutMs?)", "waitForText(text, timeoutMs?)"],
  actions: [
    "openProject(name?)",
    "newSession(prompt?)",
    "prompt(text)  // sendPrompt + waitForIdle",
    "sendPrompt(text)  // fire-and-forget",
    "stop()",
    "openTab(tabId)  // e.g. 'progress', 'workflows', 'git', 'workers'",
    "approveBlueprint(editedMarkdown?)",
    "openSettings()",
    "goHome()",
  ],
  jump: jump.JUMP_HELP.jump,
  escapeHatches: ["click(testidOrSelector)", "fill(testidOrSelector, value)", "clickText(visibleText)"],
  a11y: [
    "setTextScale()                 // inject locked max AccessibilityXXXL scale",
    "setTextScale(1)                // restore Den baseline",
    "resize(720, 480)               // best-effort; dispatch resize epoch",
  ],
  playBothSides: {
    note: "Only in manual mode (LYCAON_LLM_MANUAL=1): you supply the assistant's turns. Auto-reply is on by default so internal calls don't block.",
    flow: [
      "await __harness.llm.manual()                         // stop auto-replying; the next turn will block",
      "await __harness.sendPrompt('plan a refactor')",
      "await __harness.llm.pending()                        // read the blocked request (messages, tools)",
      "await __harness.llm.respond({ text: 'Here is the plan…' })",
      "await __harness.llm.respond({ toolCalls: [{ name: 'read', args: { path: 'main.go' } }] })  // drive a tool card",
      "await __harness.llm.respond({ text: '', toolCalls: [{ name: 'read', args: {}, args_truncated: true }] })",
      "await __harness.llm.respond({ streamChunks: [{ content: 'Hel' }, { content: 'lo', done: true }] })",
      "await __harness.llm.auto('canned reply')             // resume auto-replies",
    ],
  },
};

export function installHarnessDriver(): void {
  if (window.__harness) return;

  const api: HarnessApi = {
    help: () => HELP,
    state: () => readState(),
    testids: () =>
      [...document.querySelectorAll<HTMLElement>("[data-testid]")]
        .filter((el) => el.offsetParent !== null || el.getClientRects().length > 0)
        .map((el) => el.getAttribute("data-testid"))
        .filter((v): v is string => typeof v === "string" && v.length > 0)
        .filter((v, i, a) => a.indexOf(v) === i),
    transcript: () => readTranscript(),

    waitForIdle: (timeoutMs = 60_000) =>
      waitUntil(() => !isBusy() && currentView() !== "loading", { timeoutMs, settleMs: 400, label: "idle" }).then(
        (r) => ({ ...r, state: readState(), transcript: readTranscript() }),
      ),
    waitForTestid: (testid, timeoutMs = 30_000) =>
      waitUntil(() => !!byTestid(testid), { timeoutMs, label: `testid ${testid}` }),
    waitForText: (text, timeoutMs = 30_000) =>
      waitUntil(() => (document.body.innerText ?? "").includes(text), { timeoutMs, label: `text "${text}"` }),

    async openProject(name) {
      if (liveByTestid("chat-composer")) {
        return { ok: true, note: "already inside a project", state: readState() };
      }
      // The home grid hydrates after the shell connects. The card's first button opens it.
      const openControl = () => {
        const cards = [
          ...document.querySelectorAll<HTMLElement>('[data-testid^="project-card-"]'),
        ];
        const card = name
          ? cards.find((el) => (el.textContent ?? "").includes(name))
          : cards[0];
        return card?.querySelector<HTMLElement>("button") ?? null;
      };
      const ready = await waitUntil(() => !!openControl(), {
        timeoutMs: 60_000,
        label: `project card${name ? ` "${name}"` : ""}`,
      });
      if (!ready.ok) {
        return { ok: false, error: `no project card${name ? ` for "${name}"` : ""}`, state: readState() };
      }
      openControl()?.click();

      const r = await waitUntil(() => !!(liveByTestid("chat-composer") || byTestid("new-chat-btn")), {
        timeoutMs: 90_000,
        label: "project view",
      });
      return { ...r, state: readState() };
    },

    async newSession(prompt) {
      // A mounted conversation may still be changing sessions. Stages have no chat to settle.
      if (liveChatStage()) {
        const pre = await waitForSettledSession();
        if (!pre.ok) return { ...pre, state: readState() };
      }
      const newChat = byTestid("new-chat-btn");
      if (!newChat) return { ok: false, error: "no new-chat-btn", state: readState() };
      newChat.click();
      const settled = await waitForSettledSession();
      if (!settled.ok) return { ...settled, state: readState() };
      const r = await waitUntil(() => !!liveByTestid("chat-composer"), { timeoutMs: 90_000, label: "chat composer" });
      if (!r.ok) return { ...r, state: readState() };
      if (prompt) return api.prompt(prompt);
      return { ...r, state: readState() };
    },

    async sendPrompt(text) {
      const composer = liveByTestid("chat-composer") as HTMLTextAreaElement | null;
      if (!composer) return { ok: false, error: "no chat composer — open a session first", state: readState() };
      setNativeValue(composer, text);
      const sendReady = await waitUntil(
        () => {
          const btn = liveByTestid("composer-send");
          return !!btn && !isDisabled(btn);
        },
        { timeoutMs: 5_000, label: "composer send enabled" },
      );
      if (!sendReady.ok) return { ...sendReady, state: readState() };
      const send = liveByTestid("composer-send");
      if (!send || isDisabled(send)) {
        return { ok: false, error: "composer send is unavailable", state: readState() };
      }
      const sessionId = readState().sessionId;
      const client = getLycaonClient();
      if (typeof sessionId !== "string" || !client) {
        return { ok: false, error: "no connected session", state: readState() };
      }
      const baseline = await client.listSessionMessages(sessionId, { limit: 1 });
      const lastMessageId = baseline.messages.at(-1)?.id;
      send.click();
      const started = Date.now();
      while (Date.now() - started < 5_000) {
        if (readState().sessionId !== sessionId) {
          return { ok: false, error: "session changed during prompt submission", state: readState() };
        }
        const page = await client.listSessionMessages(sessionId, {
          limit: 100,
          ...(lastMessageId ? { afterMessageId: lastMessageId } : { from: "oldest" as const }),
        });
        const admitted = page.messages.find(message => message.role === "user" && message.seq !== undefined && message.seq > baseline.watermark);
        if (admitted) {
          return { ok: true, sent: text, messageId: admitted.id, state: readState() };
        }
        await sleep(100);
      }
      return { ok: false, error: "prompt was not admitted to the session transcript", state: readState() };
    },

    async prompt(text) {
      const sent = await api.sendPrompt(text);
      if (!sent.ok) return sent;
      return api.waitForIdle();
    },

    async stop() {
      const btn = byTestid("stop-coordinator") ?? byTestid("workflow-stop");
      if (!btn) return { ok: false, error: "nothing running to stop", state: readState() };
      btn.click();
      return { ok: true, state: readState() };
    },

    async openTab(tabId) {
      const tab = liveByTestid(`tab-${tabId}`);
      if (!tab) return { ok: false, error: `no tab "${tabId}"`, available: readState().tabs, state: readState() };
      tab.click();
      return { ok: true, state: readState() };
    },

    async approveBlueprint(editedMarkdown) {
      const anyBlueprintChrome =
        byTestid("blueprint-review-modal") ??
        byTestid("blueprint-card");
      if (!anyBlueprintChrome) {
        return { ok: false, error: "no blueprint awaiting approval", state: readState() };
      }
      if (editedMarkdown) {
        if (!byTestid("blueprint-review-modal")) {
          byTestid("blueprint-open")?.click();
        }
        byTestid("files-editor-md-code")?.click();
        const cmHost = byTestid("files-editor-host")?.querySelector(".cm-editor");
        const view = cmHost ? EditorView.findFromDOM(cmHost as HTMLElement) : null;
        if (!view) {
          return { ok: false, error: "no blueprint editor", state: readState() };
        }
        view.dispatch({
          changes: { from: 0, to: view.state.doc.length, insert: editedMarkdown },
        });
      }
      // Approve flushes any pending edit before the host action.
      const btn =
        byTestid("blueprint-review-approve") ?? byTestid("blueprint-approve");
      if (!btn) return { ok: false, error: "no approve button", state: readState() };
      btn.click();
      return { ok: true, state: readState() };
    },

    async openSettings() {
      const button = byTestid("nav-settings");
      if (!button) return { ok: false, error: "no nav-settings", state: readState() };
      button.click();
      const r = await waitUntil(() => !!byTestid("settings-view"), { timeoutMs: 10_000, label: "settings" });
      return { ...r, state: readState() };
    },

    async goHome() {
      const brand = byTestid("nav-brand");
      if (!brand) return { ok: false, error: "no nav-brand", state: readState() };
      brand.click();
      return { ok: true, state: readState() };
    },

    async click(testidOrSelector) {
      const el = resolve(testidOrSelector);
      if (!el) return { ok: false, error: `not found: ${testidOrSelector}` };
      el.click();
      return { ok: true, state: readState() };
    },

    async fill(testidOrSelector, value) {
      const el = resolve(testidOrSelector);
      if (!(el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement)) {
        return { ok: false, error: `not a fillable field: ${testidOrSelector}` };
      }
      setNativeValue(el, value);
      return { ok: true, state: readState() };
    },

    async clickText(text) {
      const el = findTarget({ text });
      if (!el) return { ok: false, error: `no clickable element with text "${text}"` };
      el.click();
      return { ok: true, state: readState() };
    },

    setTextScale(scale) {
      const next = typeof scale === "number" && Number.isFinite(scale) ? scale : MAX_TEXT_SCALE;
      applyTextScale(next);
      return {
        ok: true,
        textScale: next,
        cssVar: document.documentElement.style.getPropertyValue("--den-text-scale"),
      };
    },

    /** Publishes a notice into the selected scope. */
    publishNotice(input, scope) {
      const store = getRegisteredNoticeStore();
      if (!store) {
        return { ok: false, error: "notice store not registered" };
      }
      const target = scope ?? APP_SCOPE;
      store.publish(input, target);
      return {
        ok: true,
        notices: (store.index().get(noticeScopeKey(target)) ?? []).length,
      };
    },

    applyPreview(ev) {
      applyPreviewEvent(ev);
      return { ok: true, op: ev?.op ?? null, page_id: ev?.page_id ?? null };
    },

    applySourceChanges(ev) {
      if (!ev || typeof ev !== "object" || !ev.project_id?.trim()) {
        return { ok: false, error: "SourceChangesEvent required" };
      }
      applySourceChangesEvent(ev);
      return { ok: true, changes: ev.changes.length, resync: ev.resync };
    },

    openReviewLens(projectId, scope) {
      const pid = String(projectId ?? "").trim();
      if (!pid) return { ok: false, error: "projectId required", state: readState() };
      openReviewLens(pid, scope ?? { kind: "new" });
      return { ok: true, projectId: pid, state: readState() };
    },

    goto: (scenario, opts) => jump.goto(jumpDeps(), scenario, opts),
    listSessions: () => jump.listSessions(),
    switchSession: (id) => jump.switchSession(jumpDeps(), id),
    openStage: (stageId) => jump.openStage(jumpDeps(), stageId),
    openSearch: () => jump.openSearch(),
    openFile: (path) => jump.openFile(jumpDeps(), path),
    rewind: () => jump.rewind(jumpDeps()),
    approveCheckpoint: () => jump.approveCheckpoint(jumpDeps()),
    denyCheckpoint: () => jump.denyCheckpoint(jumpDeps()),
    approveWorkflow: () => jump.approveWorkflow(jumpDeps()),
    resize: (width, height) => jump.resize(width, height),
    dump: () => jump.dump(jumpDeps(), () => api.testids()),

    llm: harnessLlm,
  };

  function jumpDeps(): jump.JumpDeps {
    return {
      readState,
      waitForSettledSession,
      liveByTestid,
      liveResolve,
      waitForIdle: api.waitForIdle,
      sendPrompt: api.sendPrompt,
      llmManual: harnessLlm.manual,
      llmRespond: harnessLlm.respond,
    };
  }

  window.__harness = api;

  console.info("[harness] window.__harness ready — call await __harness.help()");
}
