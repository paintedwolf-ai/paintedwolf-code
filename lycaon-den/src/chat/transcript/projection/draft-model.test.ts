import { describe, expect, it } from "vitest";
import {
  coordinatorDraftPriorCount,
  isCoordinatorDraftMessage,
  isCoordinatorDraftVariantB,
  isCoordinatorDraftWire,
  isCoordinatorIntermediateStep,
  draftSummaryLine,
} from "./draft-model.ts";

describe("draft-model", () => {
  it("classifies drafts by host-stamped kind only", () => {
    expect(isCoordinatorDraftMessage({ kind: "draft" })).toBe(true);
    expect(isCoordinatorDraftMessage({})).toBe(false);
    expect(isCoordinatorDraftMessage({ kind: "progress_update" })).toBe(false);
  });

  it("classifies uncommitted coordinator slots from wire machine state", () => {
    expect(
      isCoordinatorDraftWire({
        role: "assistant",
        draft_status: "live",
        visibility: "transcript",
      }),
    ).toBe(true);
    expect(
      isCoordinatorDraftWire({
        role: "assistant",
        visibility: "internal",
      }),
    ).toBe(true);
    expect(
      isCoordinatorDraftWire({
        role: "assistant",
        kind: "draft",
        draft_status: "committed",
        visibility: "transcript",
      }),
    ).toBe(false);
    expect(
      isCoordinatorDraftWire({
        role: "assistant",
        draft_status: "rejected",
        visibility: "internal",
      }),
    ).toBe(false);
    expect(
      isCoordinatorDraftWire({
        role: "assistant",
        visibility: "transcript",
      }),
    ).toBe(false);
  });

  it("flags committed orchestration steps by host-stamped kind=draft", () => {
    expect(
      isCoordinatorIntermediateStep({
        kind: "draft",
        draft_status: "committed",
        tool_calls: [{ id: "tc1", name: "read", args: {} }],
      }),
    ).toBe(true);
    // Tool-call-only rows lack kind=draft — tools render as chips only.
    expect(
      isCoordinatorIntermediateStep({
        draft_status: "committed",
        tool_calls: [{ id: "tc1", name: "read", args: {} }],
      }),
    ).toBe(false);
    expect(
      isCoordinatorIntermediateStep({
        kind: "draft",
        draft_status: "committed",
        tool_calls: [],
      }),
    ).toBe(false);
    expect(
      isCoordinatorIntermediateStep({
        kind: "draft",
        draft_status: "live",
        tool_calls: [{ id: "tc1", name: "read", args: {} }],
      }),
    ).toBe(false);
  });

  it("never infers draft-ness from content shape", () => {
    // A closeout-looking envelope without kind=draft is not a draft — the host
    // decides at commit; Den does not sniff prose.
    expect(isCoordinatorDraftMessage({ kind: undefined })).toBe(false);
  });

  it("summarizes draft prose for collapsed rail chrome", () => {
    expect(
      draftSummaryLine(
        "The scan completed and the build baseline was collected.",
      ),
    ).toBe("The scan completed and the build baseline was collected.");
    expect(draftSummaryLine("a".repeat(200)).length).toBe(120);
    expect(draftSummaryLine("line one\nline two")).toBe("line one");
  });

  it("detects draft retries from draft_version_count only", () => {
    expect(isCoordinatorDraftVariantB({ draft_version_count: 1 })).toBe(false);
    expect(isCoordinatorDraftVariantB({ draft_version_count: 2 })).toBe(true);
    expect(coordinatorDraftPriorCount({ draft_version_count: 3 })).toBe(2);
    expect(isCoordinatorDraftVariantB({ draft_version_count: 0 })).toBe(false);
  });
});
