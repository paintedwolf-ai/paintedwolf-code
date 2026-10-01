import { Composer, typeDraft } from "../../test/composer-view-fixture.tsx";
import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, waitFor } from "@solidjs/testing-library";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import type { AttachmentUploadResponse } from "../../api/types.ts";
import { mockAttachmentCapabilities } from "../../api/mocks/fixtures.ts";
import { setPreflightReport } from "../../platform/persistence/preflight-report.ts";
import { registerComposerAttachmentSink, addToChat } from "../../chat/composer/add-to-chat.ts";

import { createSignal } from "solid-js";

describe("Composer", () => {

  it("shows an explicit non-vision notice when images are attached", async () => {
    const { getByTestId, queryByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-1"
        visionSupport="unsupported"
        onSend={vi.fn()}
      />
    ));
    expect(queryByTestId("composer-vision-hint")).toBeNull();

    const bytes = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10]);
    const file = new File([bytes], "shot.png", { type: "image/png" });
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    fireEvent.change(input);

    expect(await findByTestId("composer-vision-hint")).toBeTruthy();
    expect(getByTestId("composer-attachment-chips")).toBeTruthy();
  });

  it("attaches a text-family file as a chip and sends attachments payload", async () => {
    const onSend = vi.fn();
    const { getByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-1"
        onSend={onSend}
      />
    ));
    const file = new File([new TextEncoder().encode("package main\n")], "main.go", {
      type: "text/plain",
    });
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    fireEvent.change(input);

    const chip = await findByTestId("composer-attachment-chip");
    expect(chip.getAttribute("data-kind")).toBe("text");
    expect(await findByTestId("composer-provider-send-hint")).toBeTruthy();

    fireEvent.click(getByTestId("composer-send"));
    // Submissions reference uploaded content by its handle.
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({
        text: "",
        attachments: [expect.objectContaining({ blob_id: "a".repeat(64) })],
      }),
    );
  });

  it("turns a large plain-text paste into a staged attachment without growing the draft", async () => {
    let finishUpload: ((receipt: AttachmentUploadResponse) => void) | undefined;
    const uploadAttachment = vi.fn(
      () => new Promise<AttachmentUploadResponse>((resolve) => {
        finishUpload = resolve;
      }),
    );
    setLycaonClientForTest(stubClient({ uploadAttachment }));
    const { getByTestId, findByTestId } = render(() => (
      <Composer sidecarStatus="connected" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.focus(input);
    typeDraft(input, "keep this ask");
    const pasted = "x".repeat(mockAttachmentCapabilities.auto_attach_paste_bytes);

    fireEvent.paste(input, {
      clipboardData: { items: [], getData: () => pasted },
    });

    expect(input.value).toBe("keep this ask");
    expect(getByTestId("composer-pasted-text-upload").getAttribute("aria-busy")).toBe("true");
    expect((getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(true);
    expect(uploadAttachment).toHaveBeenCalledWith(
      "proj-1",
      "Pasted text.txt",
      "text/plain",
      expect.any(File),
    );

    finishUpload?.({
      blob_id: "b".repeat(64),
      filename: "Pasted text.txt",
      mime: "text/plain",
      kind: "text",
      bytes: pasted.length,
    });
    const chip = await findByTestId("composer-attachment-chip");
    expect(chip.textContent).toContain("Pasted text.txt");
    expect(input.value).toBe("keep this ask");
    await waitFor(() =>
      expect((getByTestId("composer-send") as HTMLButtonElement).disabled).toBe(false),
    );
  });

  it("does not upload a large paste that exceeds the remaining turn budget", async () => {
    setPreflightReport({
      overall: "ok",
      probes: [],
      attachment_capabilities: {
        ...mockAttachmentCapabilities,
        max_turn_bytes: mockAttachmentCapabilities.auto_attach_paste_bytes - 1,
      },
    });
    const uploadAttachment = vi.fn();
    setLycaonClientForTest(stubClient({ uploadAttachment }));
    const { getByTestId, findByTestId } = render(() => (
      <Composer sidecarStatus="connected" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.paste(input, {
      clipboardData: {
        items: [],
        getData: () => "x".repeat(mockAttachmentCapabilities.auto_attach_paste_bytes),
      },
    });

    await findByTestId("composer-pasted-text-retry");
    expect(uploadAttachment).not.toHaveBeenCalled();
  });

  it("keeps a failed large paste retryable until it is staged or removed", async () => {
    const uploadAttachment = vi
      .fn()
      .mockRejectedValueOnce(Object.assign(new Error("offline"), { code: "unsupported_attachment" }))
      .mockResolvedValueOnce({
        blob_id: "c".repeat(64),
        filename: "Pasted text.txt",
        mime: "text/plain",
        kind: "text",
        bytes: mockAttachmentCapabilities.auto_attach_paste_bytes,
      } satisfies AttachmentUploadResponse);
    setLycaonClientForTest(stubClient({ uploadAttachment }));
    const { getByTestId, findByTestId } = render(() => (
      <Composer sidecarStatus="connected" onSend={vi.fn()} />
    ));
    const input = getByTestId("chat-composer") as HTMLTextAreaElement;
    const pasted = "x".repeat(mockAttachmentCapabilities.auto_attach_paste_bytes);

    fireEvent.paste(input, {
      clipboardData: { items: [], getData: () => pasted },
    });

    const retry = await findByTestId("composer-pasted-text-retry");
    expect(getByTestId("composer-pasted-text-upload").classList.contains("den-composer-chip--reject")).toBe(true);
    expect(getByTestId("composer-attach-count").textContent).toBe("1 needs attention");
    fireEvent.click(retry);
    const chip = await findByTestId("composer-attachment-chip");
    expect(chip.textContent).toContain("Pasted text.txt");
    expect(uploadAttachment).toHaveBeenCalledTimes(2);
  });

  it("refocuses after attach so Enter sends instead of inserting a newline", async () => {
    const onSend = vi.fn();
    const { getByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-attach-enter"
        onSend={onSend}
      />
    ));
    const composer = getByTestId("chat-composer") as HTMLTextAreaElement;
    fireEvent.focus(composer);
    fireEvent.input(composer, { target: { value: "what is this" } });
    // Simulate file-picker blur.
    fireEvent.blur(composer);
    expect(document.activeElement).not.toBe(composer);

    const file = new File([new TextEncoder().encode("hello\n")], "note.txt", {
      type: "text/plain",
    });
    const fileInput = getByTestId("composer-file-input") as HTMLInputElement;
    Object.defineProperty(fileInput, "files", { value: [file], configurable: true });
    fireEvent.change(fileInput);
    await findByTestId("composer-attachment-chip");

    await waitFor(() => expect(document.activeElement).toBe(composer));
    composer.dispatchEvent(
      new KeyboardEvent("keydown", {
        key: "Enter",
        code: "Enter",
        bubbles: true,
        cancelable: true,
      }),
    );
    await waitFor(() =>
      expect(onSend).toHaveBeenCalledWith(
        expect.objectContaining({
          text: "what is this",
          attachments: [expect.objectContaining({ blob_id: "a".repeat(64) })],
        }),
      ),
    );
    expect(composer.value).not.toContain("\n");
  });

  it("keeps the message field on its own row beside the chip rail", async () => {
    const { getByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-row"
        onSend={() => { }}
      />
    ));
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    const files = Array.from({ length: 5 }, (_, i) =>
      new File([new TextEncoder().encode(`line ${i}\n`)], `f${i}.txt`, {
        type: "text/plain",
      }),
    );
    Object.defineProperty(input, "files", { value: files, configurable: true });
    fireEvent.change(input);

    await waitFor(() =>
      expect(getByTestId("composer-attach-count").textContent).toBe("5 attached"),
    );
    // Sibling rows preserve the textarea width.
    const rail = await findByTestId("composer-attachment-chips");
    const textarea = getByTestId("chat-composer");
    const row = textarea.closest(".den-composer-row");
    expect(row).toBeTruthy();
    expect(rail.closest(".den-composer-row")).toBeNull();
    expect(row?.parentElement).toBe(rail.closest(".den-composer-chip-rail")?.parentElement);
    fireEvent.click(getByTestId("composer-attach-clear"));
    expect(
      document.querySelectorAll('[data-testid="composer-attachment-chip"]'),
    ).toHaveLength(0);
  });

  it("does not upload selected files beyond count, image, or turn-byte caps", async () => {
    setPreflightReport({
      overall: "ok",
      probes: [],
      attachment_capabilities: {
        ...mockAttachmentCapabilities,
        max_attachments: 2,
        max_images: 1,
        max_upload_bytes: 10,
        max_turn_bytes: 10,
      },
    });
    const uploadAttachment = vi.fn(
      async (_projectId: string, filename: string): Promise<AttachmentUploadResponse> => ({
        blob_id: filename.endsWith(".png") ? "b".repeat(64) : "a".repeat(64),
        filename,
        mime: filename.endsWith(".png") ? "image/png" : "text/plain",
        kind: filename.endsWith(".png") ? "image" : "text",
        bytes: filename.endsWith(".png") ? 1 : 6,
      }),
    );
    setLycaonClientForTest(stubClient({ uploadAttachment }));
    const { getByTestId, findByTestId } = render(() => (
      <Composer sidecarStatus="connected" onSend={vi.fn()} />
    ));
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    const files = [
      new File(["123456"], "first.txt", { type: "text/plain" }),
      new File(["123456"], "over-bytes.txt", { type: "text/plain" }),
      new File(["x"], "first.png", { type: "image/png" }),
      new File(["x"], "over-count.png", { type: "image/png" }),
    ];
    Object.defineProperty(input, "files", { value: files, configurable: true });
    fireEvent.change(input);

    await findByTestId("composer-attach-error");
    await waitFor(() => expect(uploadAttachment).toHaveBeenCalledTimes(2));
    expect(uploadAttachment.mock.calls.map((call) => call[1])).toEqual([
      "first.txt",
      "first.png",
    ]);
  });

  it("sends path-file chips as references[] and clears the shared store", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "sess-ref",
      blockReason: () => null,
      projectId: () => "proj-1",
      projectRoots: () => [{ id: "root-a", path: "/proj" }],
      reveal: () => { },
    });
    await expect(
      addToChat(
        {
          kind: "path-file",
          projectId: "proj-1",
          rootId: "root-a",
          path: "src/a.ts",
          name: "a.ts",
        },
        { destination: { projectId: "proj-1", sessionId: "sess-ref" } },
      ),
    ).resolves.toMatchObject({ ok: true });

    const onSend = vi.fn();
    const { getByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-ref"
        onSend={onSend}
      />
    ));
    const chip = await findByTestId("composer-attachment-chip");
    expect(chip.getAttribute("data-kind")).toBe("path-file");

    fireEvent.click(getByTestId("composer-send"));
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({
        text: "",
        references: [
          expect.objectContaining({
            kind: "path-file",
            project_id: "proj-1",
            root_id: "root-a",
            path: "src/a.ts",
          }),
        ],
      }),
    );
  });

  it("sends selection range lines on path-file references", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "sess-range",
      blockReason: () => null,
      projectId: () => "proj-1",
      projectRoots: () => [{ id: "root-a", path: "/proj" }],
      reveal: () => { },
    });
    await expect(
      addToChat(
        {
          kind: "path-file",
          projectId: "proj-1",
          rootId: "root-a",
          path: "src/a.ts",
          name: "a.ts:12–34",
          startLine: 12,
          endLine: 34,
        },
        { destination: { projectId: "proj-1", sessionId: "sess-range" } },
      ),
    ).resolves.toMatchObject({ ok: true });

    const onSend = vi.fn();
    const { getByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-range"
        onSend={onSend}
      />
    ));
    const chip = await findByTestId("composer-attachment-chip");
    expect(chip.textContent).toContain("a.ts:12–34");

    fireEvent.click(getByTestId("composer-send"));
    expect(onSend).toHaveBeenCalledWith(
      expect.objectContaining({
        references: [
          expect.objectContaining({
            kind: "path-file",
            path: "src/a.ts",
            start_line: 12,
            end_line: 34,
          }),
        ],
      }),
    );
  });

  it("surfaces unsupported_attachment for rejected files", async () => {
    const { getByTestId, findByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-1"
        onSend={vi.fn()}
      />
    ));
    const bytes = new Uint8Array([0, 1, 2, 3, 4, 5]);
    const file = new File([bytes], "blob.bin", { type: "application/octet-stream" });
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    fireEvent.change(input);

    const chip = await findByTestId("composer-attachment-chip");
    expect(chip.getAttribute("data-kind")).toBe("reject");
    expect(chip.getAttribute("data-reject-code")).toBe("unsupported_attachment");
    expect(await findByTestId("composer-attach-error")).toBeTruthy();
  });

  it("keeps Stop primary when a streaming attachment is rejected", async () => {
    const { getByTestId, findByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-reject"
        streaming
        onSend={vi.fn()}
        onStop={vi.fn()}
      />
    ));
    const file = new File([new Uint8Array([0, 1, 2, 3])], "blob.bin", {
      type: "application/octet-stream",
    });
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    fireEvent.change(input);

    await findByTestId("composer-attachment-chip");
    expect(queryByTestId("composer-send")).toBeNull();
    expect(getByTestId("stop-coordinator")).toBeTruthy();
  });

  it("clears attachment errors with the offending chips", async () => {
    const { getByTestId, findByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId="sess-reject"
        onSend={vi.fn()}
      />
    ));
    const file = new File([new Uint8Array([0, 1, 2, 3])], "blob.bin", {
      type: "application/octet-stream",
    });
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    fireEvent.change(input);

    await findByTestId("composer-attach-error");
    fireEvent.click(getByTestId("composer-attach-clear"));
    expect(queryByTestId("composer-attach-error")).toBeNull();
  });

  it("keeps attachment errors with their chat", async () => {
    const [sessionId, setSessionId] = createSignal("sess-reject");
    const { getByTestId, findByTestId, queryByTestId } = render(() => (
      <Composer
        sidecarStatus="connected"
        projectId="proj-1"
        sessionId={sessionId()}
        onSend={vi.fn()}
      />
    ));
    const file = new File([new Uint8Array([0, 1, 2, 3])], "blob.bin", {
      type: "application/octet-stream",
    });
    const input = getByTestId("composer-file-input") as HTMLInputElement;
    Object.defineProperty(input, "files", { value: [file], configurable: true });
    fireEvent.change(input);
    await findByTestId("composer-attach-error");

    setSessionId("sess-clean");
    expect(queryByTestId("composer-attach-error")).toBeNull();
    expect(queryByTestId("composer-attachment-chip")).toBeNull();

    setSessionId("sess-reject");
    expect(getByTestId("composer-attach-error")).toBeTruthy();
    expect(getByTestId("composer-attachment-chip")).toBeTruthy();
  });
});
