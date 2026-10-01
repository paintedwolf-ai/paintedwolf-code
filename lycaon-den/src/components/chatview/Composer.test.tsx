import { Composer, pressComposerKey, typeDraft } from "../../test/composer-view-fixture.tsx";
import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, waitFor } from "@solidjs/testing-library";
import { composerDraftForSession, setComposerDraft } from "../../chat/composer/composer-drafts.ts";
import { stageComposerMutation } from "../../chat/composer/composer-document-store.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { mockAttachmentCapabilities } from "../../api/mocks/fixtures.ts";
import { setPreflightReport } from "../../platform/persistence/preflight-report.ts";
import { appBootPreparation } from "../../platform/connection/app-boot-readiness.ts";
import { registerOpenInSearchSink } from "../../search/search-nav.ts";

describe("Composer", () => {
  it("keeps a restored draft and attachment editable but holds both send controls until boot settles", async () => {
    const release = appBootPreparation.register("test boot", () => false);
    const onSend = vi.fn().mockResolvedValue(false);
    try {
      const view = render(() => <Composer sidecarStatus="connected" onSend={onSend} />);
      await stageComposerMutation({ projectId: "proj-1", sessionId: "sess-1" }, [{
        id: "retained", name: "note.txt", kind: "text", mime: "text/plain", blobId: "a".repeat(64), byteLength: 13,
      }]);
      const input = view.getByTestId("chat-composer") as HTMLTextAreaElement;
      typeDraft(input, "after restart");
      expect(input.disabled).toBe(false);
      expect(input.placeholder).toBe("Opening chat…");
      const send = view.getByRole("button", { name: "Send message" }) as HTMLButtonElement;
      expect(send.disabled).toBe(true);
      pressComposerKey(input, { key: "Enter", code: "Enter" });
      expect(onSend).not.toHaveBeenCalled();
      expect(input.value).toBe("after restart");
      expect(view.getByTestId("composer-attachment-chip")).toBeTruthy();
      release();
      expect(send.disabled).toBe(false);
      pressComposerKey(input, { key: "Enter", code: "Enter" });
      await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
      expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "after restart", attachments: [{ blob_id: "a".repeat(64) }] }));
    } finally { release(); }
  });
  it.each([undefined, 0, 8, 13, 20])("uses known attachment limits and defers unknown limits to the host: %s", async (limit) => {
    const onSend = vi.fn().mockResolvedValue(false);
    const view = render(() => <Composer sidecarStatus="connecting" onSend={onSend} />);
    await stageComposerMutation({ projectId: "proj-1", sessionId: "sess-1" }, [{
      id: "retained", name: "note.txt", kind: "text", mime: "text/plain", blobId: "a".repeat(64), byteLength: 13,
    }]);
    setPreflightReport(limit === undefined ? undefined : {
      overall: "ok", probes: [], attachment_capabilities: { ...mockAttachmentCapabilities, max_turn_bytes: limit },
    });
    // A restored composer can appear before the application's connection exists.
    setLycaonClientForTest(null);
    const input = view.getByTestId("chat-composer") as HTMLTextAreaElement;
    typeDraft(input, "see the note");
    pressComposerKey(input, { key: "Enter", code: "Enter" });
    pressComposerKey(input, { key: "Enter", code: "Enter" });
    if (limit === undefined || limit >= 13) {
      await waitFor(() => expect(onSend).toHaveBeenCalledOnce());
      expect(onSend).toHaveBeenCalledWith(expect.objectContaining({ text: "see the note", attachments: [{ blob_id: "a".repeat(64) }] }));
    } else {
      expect(onSend).not.toHaveBeenCalled();
      expect(view.getByTestId("composer-attach-error").textContent).toBeTruthy();
    }
    expect(input.value).toBe("see the note");
    expect(view.getByTestId("composer-attachment-chip")).toBeTruthy();
  });

  it("disables Send until the draft has sendable content", () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer");
    const send = getByTestId("composer-send") as HTMLButtonElement;

    expect(send.disabled).toBe(true);
    fireEvent.input(input, { target: { value: "   " } });
    expect(send.disabled).toBe(true);
    fireEvent.input(input, { target: { value: "ready" } });
    expect(send.disabled).toBe(false);
  });

  it("keeps the caret in place while editing the middle of a draft", () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    const valueDescriptor = Object.getOwnPropertyDescriptor(
      HTMLTextAreaElement.prototype,
      "value",
    );
    if (!valueDescriptor?.get || !valueDescriptor.set) {
      throw new Error("textarea value accessors unavailable");
    }
    const reactiveWrites: string[] = [];
    Object.defineProperty(input, "value", {
      configurable: true,
      get: () => valueDescriptor.get?.call(input) as string,
      set: (value: string) => {
        reactiveWrites.push(value);
        valueDescriptor.set?.call(input, value);
        input.setSelectionRange(value.length, value.length);
      },
    });

    valueDescriptor.set.call(input, "hello brave world");
    input.setSelectionRange(6, 6);
    input.dispatchEvent(new InputEvent("input", { bubbles: true }));

    expect(composerDraftForSession("sess-1")).toBe("hello brave world");
    expect(reactiveWrites).toEqual([]);
    expect([input.selectionStart, input.selectionEnd]).toEqual([6, 6]);
  });

  it("explains what Send means only once a direction is drafted", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        approvalRedirect
        onSend={vi.fn()}
      />
    ));
    // The rail is the only idle direction cue.
    expect(queryByTestId("composer-approval-redirect")).toBeNull();
    fireEvent.input(getByTestId("chat-composer"), {
      target: { value: "use bun install instead" },
    });
    const hint = getByTestId("composer-approval-redirect");
    expect(hint.textContent).toContain("Directing the agent");
    expect(hint.textContent).toContain("Send denies the action above");
  });

  it("submit calls onSend with trimmed text via composer.send", async () => {
    const onSend = vi.fn();
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        onSend={onSend}
      />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.input(input, { target: { value: "  hello  " } });
    pressComposerKey(input, { key: "Enter", code: "Enter" });
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({ text: "hello" }),
    );
  });

  it("names icon-only send and stop controls for VoiceOver", () => {
    const idle = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    expect(idle.getByTestId("composer-send").getAttribute("aria-label")).toBe(
      "Send message",
    );
    expect(idle.getByTestId("composer-attach").getAttribute("aria-label")).toBe(
      "Attach file",
    );
    idle.unmount();

    const streamingEmpty = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        streaming
        onSend={vi.fn()}
        onStop={vi.fn()}
      />
    ));
    expect(streamingEmpty.queryByTestId("composer-send")).toBeNull();
    expect(
      streamingEmpty.getByTestId("stop-coordinator").getAttribute("aria-label"),
    ).toBe("Stop response");
    streamingEmpty.unmount();

    const streamingDraft = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        streaming
        onSend={vi.fn()}
        onStop={vi.fn()}
      />
    ));
    fireEvent.input(streamingDraft.getByTestId("chat-composer"), {
      target: { value: "queue me" },
    });
    expect(streamingDraft.queryByTestId("stop-coordinator")).toBeNull();
    expect(
      streamingDraft.getByTestId("composer-send").getAttribute("aria-label"),
    ).toBe("Queue message");
  });

  it("starts the textarea at two rows", () => {
    const { getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    expect((getByTestId("chat-composer") as HTMLTextAreaElement).rows).toBe(2);
  });

  it("wraps the input in a themed scroll host beside the action pad", () => {
    const { container, getByTestId } = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    const footer = container.querySelector(".den-composer");
    const input = getByTestId("chat-composer");
    const scroll = footer?.querySelector(".den-composer-scroll");
    const pad = footer?.querySelector(".den-composer-action-pad");
    expect(scroll).not.toBeNull();
    expect(pad).not.toBeNull();
    expect(scroll!.contains(input)).toBe(true);
    expect(pad!.contains(input)).toBe(false);
  });

  it("shows the named status line and morphs send→stop when streaming with empty draft", () => {
    const idle = render(() => (
      <Composer sidecarStatus="connected" sessionId="sess-1" onSend={vi.fn()} />
    ));
    expect(idle.queryByTestId("thinking-indicator")).toBeNull();
    expect(idle.queryByTestId("stop-coordinator")).toBeNull();
    expect(idle.getByTestId("composer-status-cluster").children).toHaveLength(0);
    idle.unmount();

    const onStop = vi.fn();
    const active = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        activityLabel="Running terminal"
        streaming
        onSend={vi.fn()}
        onStop={onStop}
      />
    ));
    expect(active.getByTestId("thinking-indicator").textContent).toBe(
      "Running terminal",
    );
    expect(active.getByTestId("thinking-spinner")).toBeTruthy();
    expect(active.queryByTestId("composer-send")).toBeNull();
    const stop = active.getByTestId("stop-coordinator");
    expect(stop.classList.contains("den-composer-stop")).toBe(true);
    fireEvent.click(stop);
    expect(onStop).toHaveBeenCalledTimes(1);

    fireEvent.input(active.getByTestId("chat-composer"), {
      target: { value: "follow up" },
    });
    expect(active.queryByTestId("stop-coordinator")).toBeNull();
    expect(active.getByTestId("composer-send")).toBeTruthy();
    expect(active.getByTestId("thinking-indicator")).toBeTruthy();
  });

  it("keeps Stop primary while streaming with an armed empty submit", () => {
    const onStop = vi.fn();
    const { getByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        emptySubmitEnabled
        streaming
        onSend={vi.fn()}
        onStop={onStop}
      />
    ));
    expect(queryByTestId("composer-send")).toBeNull();
    fireEvent.click(getByTestId("stop-coordinator"));
    expect(onStop).toHaveBeenCalledTimes(1);

    fireEvent.input(getByTestId("chat-composer"), {
      target: { value: "queue this" },
    });
    expect(queryByTestId("stop-coordinator")).toBeNull();
    expect(getByTestId("composer-send")).toBeTruthy();
  });

  it("disables repeat stop clicks while stopping", () => {
    const onStop = vi.fn();
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        streaming
        stopping
        onSend={vi.fn()}
        onStop={onStop}
      />
    ));
    const stop = getByTestId("stop-coordinator") as HTMLButtonElement;
    expect(stop.disabled).toBe(true);
    expect(stop.getAttribute("aria-busy")).toBe("true");
    expect(stop.getAttribute("aria-label")).toBe("Stopping response");
    fireEvent.click(stop);
    expect(onStop).not.toHaveBeenCalled();
  });

  it("restores Stop while a queued message is pending", async () => {
    let settle: ((value: boolean | void) => void) | undefined;
    const onSend = vi.fn(() => new Promise<boolean | void>((resolve) => {
      settle = resolve;
    }));
    const { getByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        streaming
        onSend={onSend}
        onStop={vi.fn()}
      />
    ));
    fireEvent.input(getByTestId("chat-composer"), {
      target: { value: "queue this" },
    });
    fireEvent.click(getByTestId("composer-send"));

    expect(queryByTestId("composer-send")).toBeNull();
    expect(getByTestId("stop-coordinator")).toBeTruthy();

    settle?.(false);
    await waitFor(() => expect(getByTestId("composer-send")).toBeTruthy());
  });

  it("keeps Stop available when composing is blocked", () => {
    setComposerDraft("sess-1", "unsent draft");
    const { getByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="disconnected"
        sessionId="sess-1"
        streaming
        onSend={vi.fn()}
        onStop={vi.fn()}
      />
    ));

    expect(queryByTestId("composer-send")).toBeNull();
    expect(getByTestId("stop-coordinator")).toBeTruthy();
  });

  it("puts the activity line in the hint slot and leaves the action pad alone", () => {
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-1"
        externalContent
        streaming
        activityLabel="Running command · go test ./internal/confine/..."
        onSend={vi.fn()}
        onStop={vi.fn()}
      />
    ));
    const cluster = getByTestId("composer-status-cluster");
    expect(cluster.classList.contains("den-composer-status-cluster")).toBe(true);
    const ids = Array.from(cluster.children).map(
      (el) => (el as HTMLElement).dataset.statusId,
    );
    expect(ids).toEqual(["external"]);
    expect(getByTestId("external-content-badge").closest("[data-status-id='external']"))
      .toBeTruthy();

    // Activity spans the composer body above the input row.
    const line = getByTestId("thinking-indicator");
    const footer = getByTestId("chat-composer").closest(".den-composer");
    const body = footer?.querySelector(":scope > .den-composer-body");
    const row = body?.querySelector(".den-composer-row");
    expect(body).not.toBeNull();
    expect(line.parentElement).toBe(body);
    expect(row).not.toBeNull();
    expect(row!.contains(line)).toBe(false);
    expect(
      line.compareDocumentPosition(row!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("opens session web search from the External content icon", () => {
    const sink = vi.fn();
    const detach = registerOpenInSearchSink(sink);
    const { getByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        sessionId="sess-9"
        projectId="proj-9"
        externalContent
        onSend={vi.fn()}
      />
    ));
    fireEvent.click(getByTestId("external-content-badge"));
    expect(sink).toHaveBeenCalledWith({
      originProjectId: "proj-9",
      query: "session:sess-9 untrusted:true",
    });
    detach();
  });
});
