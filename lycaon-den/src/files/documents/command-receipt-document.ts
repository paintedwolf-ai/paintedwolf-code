import { createEffect, createSignal, onCleanup } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { ChatContentDocument, CommandReceiptContent } from "../../chat/transcript/content/chat-content-document.ts";

export async function readCommandReceiptDocument(client: LycaonClient, session: string, content: CommandReceiptContent, signal: AbortSignal): Promise<string> {
  const lines = [content.context];
  for (const program of content.programs) {
    let cursor: string | undefined;
    let remaining: number | undefined;
    const cursors = new Set<string>();
    do {
      signal.throwIfAborted();
      const page = await client.getCommandProgramReceipt(session, program.id, { limit: 200, cursor, activity_id: program.activityId, signal });
      signal.throwIfAborted();
      if (remaining === undefined) {
        remaining = page.executions.length + page.executions_omitted;
        lines.push(JSON.stringify({ program: page.program, activity_id: program.activityId, execution_count: remaining }));
      }
      const records = page.executions.slice(0, remaining);
      lines.push(...records.map(execution => JSON.stringify({ execution })));
      remaining -= records.length;
      if (remaining === 0) break;
      cursor = page.next_cursor;
      if (!records.length || !cursor || cursors.has(cursor)) throw new Error("Execution records could not be read completely.");
      cursors.add(cursor);
    } while (remaining > 0);
  }
  return lines.join("\n");
}

/** Files loads a bounded snapshot of the ledger when the document opens. */
export function useCommandReceiptDocument(document: () => ChatContentDocument, client: () => LycaonClient | null, onError: (cause: unknown) => void) {
  const [state, setState] = createSignal({ text: "", ready: false });
  createEffect(() => {
    const value = document(), connection = client();
    if (value.content.kind !== "command_receipts") return;
    const controller = new AbortController();
    onCleanup(() => controller.abort());
    setState({ text: "Loading execution records…", ready: false });
    const read = connection ? readCommandReceiptDocument(connection, value.sessionId, value.content, controller.signal)
      : Promise.reject(new Error("Reconnect to read execution records."));
    void read.then(text => { if (!controller.signal.aborted) setState({ text, ready: true }); }).catch(cause => {
      if (controller.signal.aborted) return;
      setState({ text: "Execution records are unavailable. Reopen the document to try again.", ready: false });
      onError(cause);
    });
  });
  return state;
}
