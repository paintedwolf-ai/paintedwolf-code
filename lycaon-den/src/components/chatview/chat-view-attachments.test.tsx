import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Show, createSignal } from "solid-js";
import { describe, expect, it, onTestFinished, vi } from "vitest";
import { registerComposerAttachmentSink } from "../../chat/composer/add-to-chat.ts";
import { CHAT_ATTACHMENT_DRAG_TYPE } from "../../chat/composer/chat-attachment-drag.ts";
import type { ComposerBlockReason } from "../../chat/composer/composer-rules.ts";
import { createAppStore } from "../../store/app-state.ts";
import { createChatViewAttachments } from "./chat-view-attachments.ts";
import type { ChatViewProps } from "./chat-view-props.ts";

const PROJECT = "00000000-0000-4000-8000-0000000000a1";
const SESSION = "00000000-0000-4000-8000-0000000000b1";

function mountAttachments() {
  const appStore = createAppStore();
  appStore.actions.setSidecarStatus("connected");
  appStore.actions.setCurrentSession({
    id: SESSION,
    owner_person_id: "00000000-0000-4000-8000-000000000002",
    project_id: PROJECT,
    workspace_path: "/tmp/p",
    posture: "build",
    status: "idle",
    created_at: "t",
    activity_at: "t",
    updated_at: "t",
  });
  const publish = vi.fn();
  const [surfaceActive, setSurfaceActive] = createSignal(true);
  render(() => {
    const [root, setRoot] = createSignal<HTMLElement>();
    const { dragActive } = createChatViewAttachments(
      { appStore, projectId: PROJECT, sessionId: SESSION } as ChatViewProps,
      root,
      () => undefined,
      surfaceActive,
      () => ({ publish, reportError: () => {} }),
    );
    return (
      <section data-testid="chat" ref={setRoot}>
        <Show when={dragActive()}><p data-testid="overlay" /></Show>
      </section>
    );
  });
  return { chat: screen.getByTestId("chat"), publish, setSurfaceActive };
}

function fileDrag() {
  return { dataTransfer: { types: ["Files"], files: [new File(["hello"], "notes.txt")] } };
}

function sinkBlockedBy(block: ComposerBlockReason) {
  onTestFinished(registerComposerAttachmentSink({
    sessionId: () => SESSION,
    blockReason: () => block,
    projectId: () => PROJECT,
    projectRoots: () => [{ id: `${PROJECT}-root`, path: "/tmp/p" }],
    reveal: () => {},
  }));
}

describe("chat view attachments", () => {
  it("takes file drops only while the chat is presented", () => {
    const { chat, setSurfaceActive } = mountAttachments();

    setSurfaceActive(false);
    fireEvent.dragEnter(chat, fileDrag());
    expect(screen.queryByTestId("overlay")).toBeNull();

    setSurfaceActive(true);
    fireEvent.dragEnter(chat, fileDrag());
    expect(screen.getByTestId("overlay")).toBeTruthy();
  });

  it("reports a file drop the composer refuses", async () => {
    sinkBlockedBy("offline");
    const { chat, publish } = mountAttachments();

    fireEvent.drop(chat, fileDrag());

    await waitFor(() => expect(publish).toHaveBeenCalledTimes(1));
  });

  it("reports an in-app reference the composer refuses", async () => {
    sinkBlockedBy("offline");
    const { chat, publish } = mountAttachments();
    const ref = { kind: "path-file", projectId: PROJECT, rootId: `${PROJECT}-root`, path: "src/main.ts", name: "main.ts" };

    fireEvent.drop(chat, {
      dataTransfer: { types: [CHAT_ATTACHMENT_DRAG_TYPE], getData: () => JSON.stringify(ref) },
    });

    await waitFor(() => expect(publish).toHaveBeenCalledTimes(1));
  });
});
