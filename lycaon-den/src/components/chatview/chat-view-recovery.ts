import type { RewindPreviewResponse } from "../../api/types.ts";
import { createEffect, createMemo, createSignal, on, onCleanup } from "solid-js";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import type { MessageRecoveryHandlers } from "../transcript/UserBubble.tsx";
import { lastRewindAnchorId, runRewind, type RecoveryTarget } from "../../chat/recovery/session-recovery.ts";
import { registerSessionPromptActions } from "../../notices/notice-actions.ts";
import { copyTextToClipboard } from "../../utils/clipboard.ts";
import type { Accessor, Setter } from "solid-js";
import type { ChatViewProps } from "./chat-view-props.ts";
import type { ComposerSendPayload } from "./Composer.tsx";

type RecoveryOptions = {
  chatLive: Accessor<boolean>;
  sendWithStreamFollow: (payload: ComposerSendPayload) => Promise<boolean | void>;
  setComposerRehydrate: Setter<number>;
  setComposerRefocus: Setter<number>;
};

export function createChatViewRecovery(props: ChatViewProps, options: RecoveryOptions) {
  const { chatLive, sendWithStreamFollow, setComposerRehydrate, setComposerRefocus } = options;
  const [recoverTarget, setRecoverTarget] = createSignal<RecoveryTarget | null>(
    null,
  );
  const [recoverError, setRecoverError] = createSignal<string | null>(null);
  const [recoverBusy, setRecoverBusy] = createSignal(false);
  const [recoverPreview, setRecoverPreview] = createSignal<RewindPreviewResponse | null>(null);
  const [recoverPreviewLoading, setRecoverPreviewLoading] = createSignal(false);
  const [recoverPreviewRevision, setRecoverPreviewRevision] = createSignal(0);
  createEffect(() => {
    recoverPreviewRevision();
    const target = recoverTarget();
    const sessionId = props.sessionId;
    setRecoverPreview(null);
    setRecoverError(null);
    if (!target) { setRecoverPreviewLoading(false); return; }
    const client = getLycaonClient();
    if (!client) { setRecoverPreviewLoading(false); setRecoverError("The host is unavailable."); return; }
    let active = true;
    onCleanup(() => { active = false; });
    setRecoverPreviewLoading(true);
    void client.previewSessionRewind(sessionId, { message_id: target.messageId })
      .then((preview) => { if (active) setRecoverPreview(preview); })
      .catch((error: unknown) => { if (active) setRecoverError(error instanceof Error ? error.message : "Could not preview this rewind."); })
      .finally(() => { if (active) setRecoverPreviewLoading(false); });
  });
  const lastAskId = createMemo(() =>
    lastRewindAnchorId(props.appStore.state.messages),
  );

  const turnHasProgress = createMemo(() => {
    const messages = props.appStore.state.messages;
    const anchorId = lastAskId();
    if (!anchorId) return false;
    const anchorIdx = messages.findIndex((m) => m.id === anchorId);
    if (anchorIdx === -1) return false;
    for (let i = anchorIdx + 1; i < messages.length; i++) {
      const m = messages[i];
      if (m && (m.role === "assistant" || m.role === "tool" || (m.tool_calls && m.tool_calls.length > 0))) {
        return true;
      }
    }
    return false;
  });

  // Persistent row actions keep turn-state changes from resizing rows.
  const recovery: MessageRecoveryHandlers = {
    held: chatLive,
    onEdit: (messageId, text) =>
      setRecoverTarget({ action: "edit", messageId, text, operationId: crypto.randomUUID() }),
    onRewind: (messageId, text) =>
      setRecoverTarget({ action: "rewind", messageId, text, operationId: crypto.randomUUID() }),
    onCopy: (text) => void copyTextToClipboard(text),
  };

  const recoveryAction = (action: "continue" | "retry", text: string) => {
    const afterMessageId = props.appStore.state.messages.at(-1)?.id;
    if (!afterMessageId) return undefined;
    return () => sendWithStreamFollow({
      text, recovery: { action, after_message_id: afterMessageId },
    });
  };

  const retryLastAsk = () => {
    const messages = props.appStore.state.messages;
    const id = lastAskId();
    const ask = id ? messages.find((m) => m.id === id) : undefined;
    const text = ask?.content?.trim();
    if (!text || chatLive()) return undefined;
    return recoveryAction("retry", text);
  };

  const keepGoingLastAsk = () => {
    if (chatLive()) return undefined;
    return recoveryAction("continue", "Keep going");
  };

  const rewindAndRetryLastAsk = () => {
    const messages = props.appStore.state.messages;
    const id = lastAskId();
    const ask = id ? messages.find((m) => m.id === id) : undefined;
    const text = ask?.content?.trim();
    const client = getLycaonClient();
    if (!id || !text || !client || chatLive()) return undefined;

    return async () => {
      try {
        const preview = await client.previewSessionRewind(props.sessionId, { message_id: id });
        if (!preview.plan_digest || preview.issues.length > 0) {
          setRecoverTarget({ action: "rewind", messageId: id, text, operationId: crypto.randomUUID() });
          return;
        }
        await runRewind(
          {
            client,
            appStore: props.appStore,
            projectId: props.projectId,
            sessionId: props.sessionId,
            projectDir: props.projectDir,
            projects: props.projects.state.projects,
          },
          { action: "rewind", messageId: id, text, operationId: crypto.randomUUID() },
          preview.plan_digest,
        );
        setComposerRehydrate((n) => n + 1);
        setComposerRefocus((n) => n + 1);
        await sendWithStreamFollow({ text });
      } catch {
        setRecoverTarget({ action: "rewind", messageId: id, text, operationId: crypto.randomUUID() });
      }
    };
  };

  createEffect(
    on(
      () => props.sessionId,
      (sessionId) => {
        if (!sessionId) return;
        onCleanup(
          registerSessionPromptActions(sessionId, {
            retry: () => void retryLastAsk()?.(),
            keepGoing: () => void keepGoingLastAsk()?.(),
            rewindAndRetry: () => void rewindAndRetryLastAsk()?.(),
          }),
        );
      },
    ),
  );

  const confirmRecovery = async () => {
    const target = recoverTarget();
    const client = getLycaonClient();
    const preview = recoverPreview();
    if (!target || !client || !preview?.plan_digest || preview.issues.length > 0) return;
    setRecoverBusy(true);
    setRecoverError(null);
    try {
      await runRewind(
        {
          client,
          appStore: props.appStore,
          projectId: props.projectId,
          sessionId: props.sessionId,
          projectDir: props.projectDir,
          projects: props.projects.state.projects,
        },
        target,
        preview.plan_digest,
      );
      setRecoverTarget(null);
      setComposerRehydrate((n) => n + 1);
      setComposerRefocus((n) => n + 1);
    } catch (err) {
      setRecoverError(
        err instanceof Error ? err.message : "Could not complete that.",
      );
    } finally {
      setRecoverBusy(false);
    }
  };

  return { recoverTarget, setRecoverTarget, recoverBusy, recoverPreview, recoverPreviewLoading, setRecoverPreviewRevision, recoverError, setRecoverError, confirmRecovery, recovery, turnHasProgress, retryLastAsk, keepGoingLastAsk, rewindAndRetryLastAsk };
}
