import { createEffect, createSignal, onCleanup } from "solid-js";
import { composerComposeBlockReason } from "../../chat/composer/composer-rules.ts";
import { addToChat, composerAttachBlockMessage, ingestChatDrop } from "../../chat/composer/add-to-chat.ts";
import { registerChatAttachmentDrop } from "../../chat/composer/chat-attachment-drag.ts";
import { registerChatDrop } from "../../platform/files/file-drop.ts";
import type { Accessor } from "solid-js";
import type { WorkflowRun } from "../../api/types.ts";
import type { NoticeReporter } from "../../notices/notice-store.ts";
import type { ChatViewProps } from "./chat-view-props.ts";

export function createChatViewAttachments(
  props: ChatViewProps,
  chatRootEl: Accessor<HTMLElement | undefined>,
  catalogRun: Accessor<WorkflowRun | undefined>,
  surfaceActive: Accessor<boolean>,
  notices: Accessor<NoticeReporter>,
) {
  const [dragActive, setDragActive] = createSignal(false);
  const [dropBlockedReason, setDropBlockedReason] = createSignal<string | null>(
    null,
  );
  const dropBlockMessage = () => {
    const block = composerComposeBlockReason(
      props.appStore.state.sidecarStatus,
      props.sessionId,
      catalogRun(),
      props.appStore.state.chatHydrationLock,
      props.needsProvider === true,
      props.appStore.state.currentSession?.status,
    );
    return block == null ? null : composerAttachBlockMessage(block);
  };
  const onDragActive = (active: boolean) => {
    setDragActive(active);
    setDropBlockedReason(active ? dropBlockMessage() : null);
  };
  // The overlay closes with the drop, so a refusal decided afterwards is reported as a notice.
  const refuse = (reason: string) => notices().publish({ message: reason });

  // A retained chat stays mounted while hidden; only the presented chat takes window drops.
  createEffect(() => {
    const el = chatRootEl();
    if (!el || !surfaceActive()) return;
    onCleanup(registerChatDrop({
      htmlTarget: el,
      onDragActive,
      onDrop: (event) => {
        setDragActive(false);
        if (dropBlockMessage() != null) return;
        void ingestChatDrop(event).then((result) => {
          if (result.refusedReason) refuse(result.refusedReason);
        });
      },
    }));
  });

  createEffect(() => {
    const el = chatRootEl();
    if (!el) return;
    onCleanup(registerChatAttachmentDrop(el, {
      onDragActive,
      onDrop: (ref) => {
        setDragActive(false);
        if (dropBlockMessage() != null) return;
        void addToChat(ref, {
          destination: { projectId: props.projectId, sessionId: props.sessionId },
        }).then((result) => {
          if (!result.ok) refuse(result.reason);
        });
      },
    }));
  });

  return { dragActive, dropBlockedReason };
}
