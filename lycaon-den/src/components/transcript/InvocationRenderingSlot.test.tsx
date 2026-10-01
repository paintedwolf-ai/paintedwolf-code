import { stubClient } from "../../test/client-fixture.ts";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import { resetInvocationRecordingStoreForTests } from "../../chat/visual/invocation-recording-store.ts";
import {
  applyPreviewEvent,
  resetLivePreviewStoreForTests,
} from "../../chat/visual/preview-store.ts";
import { clearVisualArtifactSrcs } from "../../chat/visual/visual-artifact-src.ts";
import { InvocationRenderingSlot } from "./InvocationRenderingSlot.tsx";

function clientWithArtifacts(items: unknown[] = []): LycaonClient {
  return stubClient({
    watchPreview: vi.fn(async (
      _sessionId: string,
      req: { watching: boolean; page_id: string },
    ) => req),
    listSessionArtifacts: vi.fn(async () => ({ artifacts: items })),
    getSessionArtifact: vi.fn(async () => new Blob(["movie"], { type: "video/mp4" })),
  });
}

afterEach(() => {
  clearVisualArtifactSrcs();
  resetLivePreviewStoreForTests();
  resetInvocationRecordingStoreForTests();
});

describe("InvocationRenderingSlot", () => {
  it("renders only the preview for its ordered invocation", () => {
    const client = clientWithArtifacts();
    applyPreviewEvent({
      op: "frame",
      session_id: "sess",
      page_id: "page-1",
      assistant_message_id: "assistant-1",
      tool_call_id: "call-1",
      seq: 1,
      jpeg_b64: "invocation-frame",
    });

    render(() => (
      <>
        <InvocationRenderingSlot
          client={client}
          sessionId="sess"
          assistantMessageId="assistant-1"
          toolCallId="call-1"
          layout="chat"
        />
        <InvocationRenderingSlot
          client={client}
          sessionId="sess"
          assistantMessageId="assistant-2"
          toolCallId="call-2"
          layout="chat"
        />
      </>
    ));

    expect(screen.getAllByTestId("invocation-rendering-slot")).toHaveLength(1);
    expect(screen.getByTestId("invocation-rendering-slot").dataset.toolCallId).toBe("call-1");
  });

  it("hydrates a durable recording back into its originating invocation", async () => {
    const client = clientWithArtifacts([
      {
        id: "recording-1",
        session_id: "sess",
        mime: "video/mp4",
        source: "capture",
        page_id: "page-1",
        origin_message_id: "assistant-1",
        tool_call_id: "call-1",
        duration_ms: 5400,
        created_at: "2026-08-14T00:00:00Z",
      },
    ]);

    render(() => (
      <InvocationRenderingSlot
        client={client}
        sessionId="sess"
        assistantMessageId="assistant-1"
        toolCallId="call-1"
        layout="chat"
      />
    ));

    expect(await screen.findByTestId("live-tool-recording-player")).toBeTruthy();
    expect(client.listSessionArtifacts).toHaveBeenCalledWith("sess");
  });

  it("mirrors a worker-held live page only inside its parent task row", async () => {
    const client = clientWithArtifacts();
    applyPreviewEvent({
      op: "frame",
      session_id: "child",
      parent_session_id: "parent",
      page_id: "page-1",
      assistant_message_id: "worker-assistant",
      tool_call_id: "worker-call",
      seq: 1,
      jpeg_b64: "worker-frame",
    });

    render(() => (
      <InvocationRenderingSlot
        client={client}
        sessionId="parent"
        assistantMessageId="parent-assistant"
        toolCallId="task-call"
        mirrorHolderSessionId="child"
        layout="chat"
      />
    ));

    expect(screen.getByTestId("live-preview-pane")).toBeTruthy();
    await waitFor(() => expect(client.listSessionArtifacts).not.toHaveBeenCalled());
  });
});
