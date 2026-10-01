/** Editor actions invoked through contributions. */

import type { LycaonClient } from "../../../api/client.ts";
import type {
  CommandInvokeContext,
  SessionStatus,
} from "../../../api/types.ts";
import { contributionFrame } from "../../../contributions/contribution-store.ts";
import { dispatchContributionCommand } from "../../../contributions/dispatch.ts";

/** Action result destination. */
export type EditorActionOutput = "prose" | "edits";
export type EditorActionRunResult = { actionMessageId: string; actionOrd: number };

/** Where the next transcript read starts: after a known row, a host cursor, or the start. */
export type TranscriptReadPosition = { afterMessageId: string } | { after: string } | { from: "oldest" };

/** The read that continues past a page: its cursor, else past its newest row. */
export function nextTranscriptPosition(
  page: { messages: ReadonlyArray<{ id: string }>; after_cursor?: string },
  current: TranscriptReadPosition,
): TranscriptReadPosition {
  if (page.after_cursor) return { after: page.after_cursor };
  const newest = page.messages[page.messages.length - 1];
  return newest ? { afterMessageId: newest.id } : current;
}

const DEFAULT_TIMEOUT_MS = 180_000;

/** Route inspect output to prose and write output to hunk review. */
export function editorActionOutput(commandId: string): EditorActionOutput {
  const frame = contributionFrame();
  const command = frame?.commands.find((c) => c.id === commandId);
  const action = frame?.editor_actions.find((a) => a.id === command?.action_ref);
  return action?.preset === "inspect_file" ? "prose" : "edits";
}

export async function runEditorAction(args: {
  client: LycaonClient;
  projectId: string;
  sessionId: string;
  commandId: string;
  context: CommandInvokeContext;
  /** Poll timeout in milliseconds. */
  timeoutMs?: number;
  signal?: AbortSignal;
}): Promise<EditorActionRunResult> {
  const transcript = await args.client.listSessionMessages(args.sessionId, {
    limit: 1,
  });
  const newest = transcript.messages[transcript.messages.length - 1];
  const result = await dispatchContributionCommand(
    args.commandId,
    {
      client: args.client,
      projectId: args.projectId,
      sessionId: args.sessionId,
    },
    args.context,
  );
  if (!result.ok) {
    throw new Error(result.message ?? editorActionFailure(result.reason));
  }
  if (!result.messageId) {
    throw new Error("The editor action did not return its message.");
  }
  const actionOrd = await waitEditorAction(args.client, args.sessionId, {
    messageId: result.messageId,
    afterMessageId: newest?.id,
    afterOrd: newest?.ord ?? 0,
    timeoutMs: args.timeoutMs ?? DEFAULT_TIMEOUT_MS,
    signal: args.signal,
  });
  return { actionMessageId: result.messageId, actionOrd };
}

function editorActionFailure(reason: string): string {
  switch (reason) {
    case "no_frame":
    case "not_in_frame":
      return "This action is not available right now. Reopen the file and try again.";
    case "no_session":
      return "Open a chat for this project first.";
    default:
      return "The editor action could not start.";
  }
}

export async function waitEditorAction(
  client: LycaonClient,
  sessionId: string,
  opts: {
    messageId: string;
    /** Newest row before the submission; absent when the transcript was empty. */
    afterMessageId?: string;
    afterOrd: number;
    timeoutMs: number;
    signal?: AbortSignal;
  },
): Promise<number> {
  const deadline = Date.now() + opts.timeoutMs;
  let last: SessionStatus = "idle";
  let position: TranscriptReadPosition = opts.afterMessageId
    ? { afterMessageId: opts.afterMessageId }
    : { from: "oldest" };
  let actionOrd: number | undefined;
  while (Date.now() < deadline) {
    throwIfAborted(opts.signal);
    if (actionOrd === undefined) {
      const page = await client.listSessionMessages(sessionId, { ...position, limit: 500 });
      throwIfAborted(opts.signal);
      if (page.after_cursor && page.messages.length === 0) {
        throw new Error("The editor action transcript did not advance.");
      }
      const action = page.messages.find((message) => message.id === opts.messageId);
      if (action?.ord != null && action.ord > opts.afterOrd) actionOrd = action.ord;
      position = nextTranscriptPosition(page, position);
      if (actionOrd === undefined && page.after_cursor) continue;
    }
    // The host marks the session busy before appending this submission's row.
    const sess = await client.getSession(sessionId);
    throwIfAborted(opts.signal);
    last = sess.status;
    if (last === "error") {
      throw new Error("The editor action failed before it completed.");
    }
    if (actionOrd !== undefined && last === "idle") return actionOrd;
    await sleep(200, opts.signal);
  }
  throw new Error(`Editor action timed out (last status: ${last})`);
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const t = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(t);
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}
