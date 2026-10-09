import { createEffect, createSignal, onCleanup } from "solid-js";
import { composerComposeBlockReason } from "../../chat/composer/composer-rules.ts";
import { addToChat, composerAttachBlockMessage, ingestChatDrop } from "../../chat/composer/add-to-chat.ts";
import { registerChatAttachmentDrop } from "../../chat/composer/chat-attachment-drag.ts";
import { registerChatDrop } from "../../platform/files/file-drop.ts";
import type { Accessor } from "solid-js";
import type { WorkflowRun } from "../../api/types.ts";
import type { ChatViewProps } from "./chat-view-props.ts";

export function createChatViewAttachments(props: ChatViewProps, chatRootEl: Accessor<HTMLElement | undefined>, catalogRun: Accessor<WorkflowRun | undefined>) {
  const [dragActive, setDragActive] = createSignal(false);
  const [dropBlockedReason, setDropBlockedReason] = createSignal<string | null>(
    null,
  );
  createEffect(() => {
    const el = chatRootEl();
    if (!el) return;
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
      if (!active) {
        setDropBlockedReason(null);
        return;
      }
      setDropBlockedReason(dropBlockMessage());
    };
    const dropBlocked = () => {
      const message = dropBlockMessage();
      if (message == null) return false;
      setDropBlockedReason(message);
      return true;
    };
    const detachFileDrop = registerChatDrop({
      htmlTarget: el,
      onDragActive,
      onDrop: (event) => {
        setDragActive(false);
        if (dropBlocked()) return;
        void ingestChatDrop(event).then((result) => {
          if (result.refusedReason) {
            setDropBlockedReason(result.refusedReason);
          }
        });
      },
    });
    const detachAttachmentDrop = registerChatAttachmentDrop(el, {
      onDragActive,
      onDrop: (ref) => {
        setDragActive(false);
        if (dropBlocked()) return;
        void addToChat(ref, {
          destination: { projectId: props.projectId, sessionId: props.sessionId },
        }).then((result) => {
          if (!result.ok) setDropBlockedReason(result.reason);
        });
      },
    });
    onCleanup(() => {
      detachFileDrop();
      detachAttachmentDrop();
    });
  });

  return { dragActive, dropBlockedReason };
}
