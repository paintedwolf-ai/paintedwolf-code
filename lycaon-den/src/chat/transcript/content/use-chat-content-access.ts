import { createEffect, createMemo, createSignal, on, onCleanup, untrack, type Accessor } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ChatContentDocument } from "./chat-content-document.ts";
import { createChatContentAccess, type ChatContentAccess } from "./chat-content-reader.ts";
import { openCommandReceiptAccess } from "./command-receipt-access.ts";

/**
 * The access a chat content document reads through: retained content at once,
 * a receipt once it opens. A receipt that failed to open opens again when its
 * link is followed again or the connection changes.
 */
export function useChatContentAccess(document: Accessor<ChatContentDocument>, client: Accessor<LycaonClient | null>, onError: (cause: unknown) => void): Accessor<ChatContentAccess | undefined> {
  const retainedSource = createMemo<Parameters<typeof createChatContentAccess>[0] | undefined>(() => {
    const value = document(), connection = client(), content = value.content;
    if (value.kind !== "tool" || content.kind !== "retained" || !connection) return undefined;
    return { client: connection, sessionId: value.sessionId, messageId: value.messageId, reference: content.reference };
  }, undefined, {
    equals: (previous, next) => previous === next || !!previous && !!next &&
      previous.client === next.client && previous.sessionId === next.sessionId &&
      previous.messageId === next.messageId && previous.reference.field === next.reference.field &&
      previous.reference.tool_call_id === next.reference.tool_call_id &&
      previous.reference.sha256 === next.reference.sha256,
  });
  const receiptSource = createMemo(() => {
    const value = document(), connection = client(), content = value.content;
    if (content.kind !== "command_receipts" || !connection) return undefined;
    return { client: connection, sessionId: value.sessionId, scopes: content.scopes };
  }, undefined, {
    equals: (previous, next) => previous === next || !!previous && !!next && previous.client === next.client &&
      previous.sessionId === next.sessionId && JSON.stringify(previous.scopes) === JSON.stringify(next.scopes),
  });
  const [receiptAccess, setReceiptAccess] = createSignal<ChatContentAccess>();
  const [attempt, setAttempt] = createSignal(0);
  let failedAt: number | undefined;
  createEffect(on(() => document().openSeq, seq => {
    if (failedAt !== undefined && seq !== failedAt) { failedAt = undefined; setAttempt(value => value + 1); }
  }, { defer: true }));
  createEffect(() => {
    const source = receiptSource();
    attempt();
    setReceiptAccess(undefined);
    failedAt = undefined;
    if (!source) return;
    const abort = new AbortController();
    onCleanup(() => abort.abort());
    void openCommandReceiptAccess({ ...source, signal: abort.signal })
      .then(opened => { if (!abort.signal.aborted) setReceiptAccess(() => opened); })
      .catch(cause => {
        if (abort.signal.aborted) return;
        failedAt = untrack(() => document().openSeq) ?? 0;
        onError(cause);
      });
  });
  return createMemo<ChatContentAccess | undefined>(() => {
    const source = retainedSource();
    return source ? createChatContentAccess(source) : receiptAccess();
  });
}
