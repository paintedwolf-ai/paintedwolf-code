import { createEffect, createMemo, createSignal, onCleanup, type Accessor } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { getBackgroundProcessText, prepareBackgroundProcessOutput, subscribeBackgroundProcessStore } from "../../chat/tool/background-process-store.ts";
import type { ChatContentDocument } from "../../chat/transcript/content/chat-content-document.ts";

/** Live process output freezes when the first prepared text arrives in Pretty mode. */
export function useChatProcessText(
  document: Accessor<ChatContentDocument>, client: Accessor<LycaonClient | null>,
  pretty: Accessor<boolean>, onError: (cause: unknown) => void,
) {
  const session = createMemo(() => document().sessionId);
  const handle = createMemo(() => {
    const content = document().content;
    return content.kind === "process" ? content.handle : undefined;
  });
  const [text, setText] = createSignal("");
  let refresh: (() => void) | undefined;
  createEffect(() => {
    const processId = handle(), sessionId = session(), connection = client();
    setText("");
    if (!processId) return;
    const freeze = pretty();
    let frozen = false, current = true;
    const receive = () => {
      if (!current || frozen) return;
      const available = getBackgroundProcessText(sessionId, processId);
      if (available === undefined) return;
      setText(available);
      frozen = freeze;
    };
    const prepare = () => {
      frozen = false;
      receive();
      if (connection && getBackgroundProcessText(sessionId, processId) === undefined) {
        void prepareBackgroundProcessOutput(connection, sessionId, processId).then(receive, cause => {
          if (current && !frozen) onError(cause);
        });
      }
    };
    const unsubscribe = subscribeBackgroundProcessStore(receive, () => ({ sessionId, processId }));
    refresh = prepare;
    prepare();
    onCleanup(() => { current = false; refresh = undefined; unsubscribe(); });
  });
  return { text, refresh: () => refresh?.() };
}
