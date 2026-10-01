import { stubClient } from "../../test/client-fixture.ts";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../api/client.ts";
import {
  applyPreviewEvent,
  getPreviewForInvocation,
  resetLivePreviewStoreForTests,
} from "../../chat/visual/preview-store.ts";
import { LivePreviewPane } from "./LivePreviewPane.tsx";

function mockClient(): LycaonClient {
  return stubClient({
    watchPreview: vi.fn(async (
      _sessionId: string,
      req: { watching: boolean; page_id: string },
    ) => req),
  });
}

function apply(op: "attach" | "frame" | "action" | "detach", seq: number): void {
  applyPreviewEvent({
    op,
    session_id: "sess",
    page_id: "page-1",
    assistant_message_id: "assistant-1",
    tool_call_id: "call-1",
    seq,
    title: "Demo",
    jpeg_b64: op === "frame" ? "/9j/4AAQ" : undefined,
    action: op === "action"
      ? { label: "click · #go", target: "#go", x: 4, y: 8, w: 20, h: 10 }
      : undefined,
  });
}

describe("LivePreviewPane", () => {
  it("paints an invocation frame, overlays actions, and never forwards clicks", () => {
    resetLivePreviewStoreForTests();
    const client = mockClient();
    apply("attach", 1);
    apply("frame", 2);
    apply("action", 3);

    render(() => (
      <LivePreviewPane
        sessionId="sess"
        client={client}
        snapshot={() => getPreviewForInvocation("sess", "assistant-1", "call-1")}
      />
    ));

    expect(screen.getByTestId("live-preview-status").textContent).toMatch(/Live|Driving/);
    expect(screen.getByTestId("live-preview-action").textContent).toContain("click");
    expect(fireEvent.click(screen.getByTestId("live-preview-stage"))).toBe(false);
    expect(client.watchPreview).toHaveBeenCalledTimes(1);
  });

  it("retains the final frame at the same invocation without reacquiring a watch", () => {
    resetLivePreviewStoreForTests();
    const client = mockClient();
    apply("attach", 1);
    apply("frame", 2);
    apply("detach", 3);

    render(() => (
      <LivePreviewPane
        sessionId="sess"
        client={client}
        snapshot={() => getPreviewForInvocation("sess", "assistant-1", "call-1")}
      />
    ));

    expect(screen.getByTestId("live-preview-status").textContent).toBe("Final frame");
    expect(screen.getByLabelText("Final frame from live tool session")).toBeTruthy();
    expect(screen.getByAltText("Demo")).toBeTruthy();
    expect(client.watchPreview).not.toHaveBeenCalled();
  });
});
