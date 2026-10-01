import { stubClient } from "../../test/client-fixture.ts";
import { describe, expect, it, vi, afterEach } from "vitest";
import { cleanup, render, screen } from "@solidjs/testing-library";
import type { LycaonClient } from "../../api/client.ts";
import { clearDraftVersionCache } from "../../chat/draft/draft-version-cache.ts";
import { clearTranscriptEntryMemory } from "../../chat/transcript/presentation/transcript-entry.ts";
import { DraftRail } from "./DraftRail.tsx";
import {
  TranscriptViewportProvider,
  createTranscriptViewportController,
} from "../../chat/stream/transcript-viewport.tsx";

afterEach(() => {
  cleanup();
  clearDraftVersionCache();
  clearTranscriptEntryMemory();
});

function mockClient(
  versions: { version_index: number; body: string; outcome_code: string; outcome_label?: string }[],
): LycaonClient {
  return stubClient({
    listDraftVersions: vi.fn().mockResolvedValue({ versions }),
  });
}

function expandRail() {
  const toggle = screen.getByTestId("draft-rail-toggle");
  toggle.click();
}

describe("DraftRail", () => {
  it.each([true, false])("preserves following=%s when the rail expands", (following) => {
    const controller = createTranscriptViewportController({ sessionId: () => "sess-1" });
    if (!following) controller.stopFollowing();
    render(() => (
      <TranscriptViewportProvider value={controller}>
        <DraftRail
          sessionId="sess-1"
          slotId="slot-1"
          body={"First line\nSecond line"}
          versionCount={1}
          live={false}
          client={mockClient([])}
        />
      </TranscriptViewportProvider>
    ));

    expandRail();
    expect(controller.following()).toBe(following);
  });
  it("renders live rail with no version toggle", () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="live body"
        versionCount={1}
        live
        client={mockClient([])}
      />
    ));
    expect(screen.getByTestId("draft-rail-live-body")).toBeTruthy();
    expect(screen.queryByTestId("draft-rail-toggle")).toBeNull();
    expect(screen.getByTestId("draft-rail").getAttribute("data-draft-variant")).toBe("a");
    expect(screen.queryByTestId("draft-rail-tabs")).toBeNull();
  });

  it("variant A closed shows first line only and never a Versions affordance", () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body={"First line of the draft\nSecond line kept for expand"}
        versionCount={1}
        live={false}
        client={mockClient([])}
      />
    ));
    expect(screen.getByTestId("draft-rail").getAttribute("data-draft-variant")).toBe("a");
    expect(screen.getByTestId("draft-rail-summary-body").textContent).toBe(
      "First line of the draft",
    );
    expect(screen.getByTestId("draft-rail-toggle").textContent).toBe("Show");
    expect(screen.getByTestId("draft-rail-toggle").textContent).not.toMatch(/Versions/);
  });

  it("variant A expanded body is plain text, never markdown-parsed", () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body={"# Heading\n\n**bold** body"}
        versionCount={1}
        live={false}
        client={mockClient([])}
      />
    ));
    expandRail();
    const expanded = screen.getByTestId("draft-rail-expanded-body");
    expect(expanded.querySelector(".den-draft-rail-plain")?.textContent).toContain("# Heading");
    expect(expanded.querySelector(".assistant-prose")).toBeNull();
  });

  it("shows the generating token heartbeat only while live", () => {
    const { unmount } = render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="live body"
        versionCount={1}
        live
        generatingTokens={1536}
        client={mockClient([])}
      />
    ));
    expect(screen.getByTestId("draft-rail-generating").textContent).toBe(
      "generating ~1.5k tokens",
    );
    unmount();

    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="settled body"
        versionCount={1}
        live={false}
        generatingTokens={1536}
        client={mockClient([])}
      />
    ));
    expect(screen.queryByTestId("draft-rail-generating")).toBeNull();
  });

  it("renders settled collapsed rail with history toggle", () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="settled body"
        versionCount={2}
        live={false}
        client={mockClient([])}
      />
    ));
    expect(screen.getByTestId("draft-rail-summary-body").textContent).toContain(
      "Draft versions (1)",
    );
    expect(screen.getByTestId("draft-rail-toggle").textContent).toContain("Versions (1)");
  });

  it("orders version tabs by version_index and labels them v{index}", async () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="final"
        versionCount={4}
        live={false}
        client={mockClient([
          { version_index: 2, body: "attempt 2", outcome_code: "INVEST_CITATIONS_REQUIRED" },
          { version_index: 0, body: "attempt 0", outcome_code: "INVEST_CITATIONS_REQUIRED" },
          { version_index: 1, body: "attempt 1", outcome_code: "INVEST_CITATIONS_REQUIRED" },
        ])}
      />
    ));
    expandRail();
    const v0 = await screen.findByTestId("draft-rail-tab-v0");
    const v1 = await screen.findByTestId("draft-rail-tab-v1");
    const v2 = await screen.findByTestId("draft-rail-tab-v2");
    expect(v0.textContent).toContain("v0");
    expect(v1.textContent).toContain("v1");
    expect(v2.textContent).toContain("v2");
    expect(v0.compareDocumentPosition(v1) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(v1.compareDocumentPosition(v2) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("shows outcome_label badges on rejected tabs", async () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="final"
        versionCount={2}
        live={false}
        client={mockClient([
          {
            version_index: 0,
            body: "attempt 0",
            outcome_code: "INVEST_CITATIONS_REQUIRED",
            outcome_label: "Investigate closeout cited nothing — host binds observed evidence",
          },
        ])}
      />
    ));
    expandRail();
    const tab = await screen.findByTestId("draft-rail-tab-v0");
    expect(tab.textContent).toContain(
      "Investigate closeout cited nothing — host binds observed evidence",
    );
  });

  it("selects the latest version_index tab by default", async () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="final"
        versionCount={3}
        live={false}
        client={mockClient([
          { version_index: 0, body: "attempt 0", outcome_code: "A" },
          { version_index: 1, body: "attempt 1", outcome_code: "B" },
        ])}
      />
    ));
    expandRail();
    const v1 = await screen.findByTestId("draft-rail-tab-v1");
    expect(v1.getAttribute("aria-selected")).toBe("true");
    expect(screen.getByTestId("draft-rail-expanded-body").textContent).toContain("attempt 1");
  });

  it("omits Show when the settled body is a single short line", () => {
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body="summarize"
        versionCount={1}
        live={false}
        client={mockClient([])}
      />
    ));
    expect(screen.getByTestId("draft-rail-summary-body").textContent).toContain("summarize");
    expect(screen.queryByTestId("draft-rail-toggle")).toBeNull();
  });

  it("keeps Show when the collapsed preview truncates a long single line", () => {
    const body = "x".repeat(140);
    render(() => (
      <DraftRail
        sessionId="sess-1"
        slotId="slot-1"
        body={body}
        versionCount={1}
        live={false}
        client={mockClient([])}
      />
    ));
    expect(screen.getByTestId("draft-rail-toggle")).toBeTruthy();
  });
});
