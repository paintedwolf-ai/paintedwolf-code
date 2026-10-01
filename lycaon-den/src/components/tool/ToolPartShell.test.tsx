import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { ToolPartShell } from "./ToolPartShell.tsx";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../../chat/stream/transcript-viewport.tsx";
import { clearTranscriptEntryMemory } from "../../chat/transcript/presentation/transcript-entry.ts";
import { transcriptDisclosureKey } from "../../chat/transcript/presentation/transcript-disclosure-key.ts";

const part = (toolCallId: string, assistantId = "a1"): ToolPartView => ({
  id: `${assistantId}:${toolCallId}`,
  toolCallId,
  assistantMessageId: assistantId,
  messageId: "m1",
  tool: "read",
  kind: "read",
  status: "completed",
  args: { path: "main.go" },
  output: "package main",
  error: null,
});

let controller: ReturnType<typeof createTranscriptViewportController>;

beforeEach(() => {
  controller = createTranscriptViewportController({ sessionId: () => "s1" });
});

afterEach(() => {
  clearTranscriptEntryMemory();
});

describe("ToolPartShell open-state persistence", () => {
  it("preserves HTTP capitalization in the http_request tool label", () => {
    const item = part("tc-http");
    item.tool = "http_request";
    const { getByText } = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ToolPartShell part={item} layout="chat" sessionId="s1">
          body
        </ToolPartShell>
      </TranscriptViewportProvider>
    ));

    expect(getByText("HTTP request")).toBeTruthy();
  });

  it("starts closed for a fresh chicklet", () => {
    const { container } = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ToolPartShell part={part("tc-fresh")} layout="chat" sessionId="s1">
          body
        </ToolPartShell>
      </TranscriptViewportProvider>
    ));
    expect((container.querySelector("details") as HTMLDetailsElement).open).toBe(
      false,
    );
  });

  it("projects invocation identity onto the card", () => {
    const item = part("tc-receipt");
    item.invocation = {
      id: "inv-1",
      tool: "read",
      tool_call_id: "tc-receipt",
      contract_digest: "contract",
      args_digest: "args",
      owner: "filesystem",
      lifecycle: "read_only",
      reversibility: "reversible",
      evidence_policy: "result",
      recovery_policy: "none",
      status: "completed",
      invoked: true,
      evidence: { kind: "result" },
      started_at: "2026-08-13T00:00:00Z",
    };
    const { container } = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ToolPartShell part={item} layout="chat" sessionId="s1">
          body
        </ToolPartShell>
      </TranscriptViewportProvider>
    ));
    const card = container.querySelector("details");
    expect(card?.getAttribute("data-invocation-owner")).toBe("filesystem");
    expect(card?.getAttribute("data-invocation-lifecycle")).toBe("read_only");
    expect(card?.getAttribute("data-invocation-status")).toBe("completed");
  });

  it("reopens on remount after the user expanded it", () => {
    const first = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ToolPartShell part={part("tc-recover")} layout="chat" sessionId="s1">
          body
        </ToolPartShell>
      </TranscriptViewportProvider>
    ));
    const firstDetails = first.container.querySelector(
      "details",
    ) as HTMLDetailsElement;
    firstDetails.setAttribute("open", "");
    firstDetails.dispatchEvent(new Event("toggle"));
    expect(firstDetails.open).toBe(true);
    expect(controller.disclosures.isOpen(transcriptDisclosureKey.tool("a1:tc-recover"))).toBe(true);

    // Open state follows the stable key across remounts.
    const second = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ToolPartShell part={part("tc-recover")} layout="chat" sessionId="s1">
          body
        </ToolPartShell>
      </TranscriptViewportProvider>
    ));
    expect(
      (second.container.querySelector("details") as HTMLDetailsElement).open,
    ).toBe(true);
  });

  it("stays closed on mount when the key was never expanded", () => {
    controller.disclosures.setUserOpen(transcriptDisclosureKey.tool("tc-unrelated"), true);
    const { container } = render(() => (
      <TranscriptViewportProvider value={controller}>
        <ToolPartShell part={part("tc-closed")} layout="chat" sessionId="s1">
          body
        </ToolPartShell>
      </TranscriptViewportProvider>
    ));
    expect((container.querySelector("details") as HTMLDetailsElement).open).toBe(
      false,
    );
  });
});
