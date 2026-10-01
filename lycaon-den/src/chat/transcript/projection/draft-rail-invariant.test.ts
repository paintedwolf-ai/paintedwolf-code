import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import type { Message, WorkflowRun } from "../../../api/types.ts";
import {
  coordinatorDraftPriorCount,
  draftSummaryLine,
  isCoordinatorDraftVariantB,
} from "./draft-model.ts";
import { messagesToTranscriptItems } from "./transcript-items.ts";
import { createTranscriptDisplayProjector } from "./transcript-display-projection.ts";
import { buildChatTranscriptBlocks } from "../../workflow/workflow-spans.ts";

const ambientRun: WorkflowRun = {
  id: "run-ambient",
  session_id: "s1",
  workflow_id: "implement",
  workflow_version: "1.0.0",
	revision: 1,
  attach_policy: "session_create",
  status: "running",
  current_phase: "boot",
  start_message_id: "u1",
  created_at: "t",
  updated_at: "t",
};

describe("draft rail invariant", () => {
  it("without retries: priorCount 0 shows first-line summary only", () => {
    const messages: Message[] = [
      {
        id: "d1",
        ord: 1,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: "First line of the step\nMore body lines",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 1,
        tool_calls: [{ id: "tc1", name: "read", args: {} }],
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(messages);
    const draft = items.find((i) => i.kind === "draft");
    expect(draft?.kind).toBe("draft");
    if (draft?.kind === "draft") {
      expect(draft.draftVersionCount).toBe(1);
      expect(
        coordinatorDraftPriorCount({ draft_version_count: draft.draftVersionCount }),
      ).toBe(0);
      expect(draftSummaryLine(draft.text)).toBe("First line of the step");
      expect(isCoordinatorDraftVariantB({ draft_version_count: draft.draftVersionCount })).toBe(
        false,
      );
    }
  });

  it("with retries: priorCount > 0 is driven by draft_version_count", () => {
    const messages: Message[] = [
      {
        id: "d2",
        ord: 2,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: "Accepted attempt body",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 3,
        tool_calls: [{ id: "tc2", name: "read", args: {} }],
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(messages);
    const draft = items.find((i) => i.kind === "draft");
    expect(draft?.kind).toBe("draft");
    if (draft?.kind === "draft") {
      expect(
        coordinatorDraftPriorCount({ draft_version_count: draft.draftVersionCount }),
      ).toBe(2);
      expect(isCoordinatorDraftVariantB({ draft_version_count: draft.draftVersionCount })).toBe(
        true,
      );
    }
  });

  it("DraftRail.tsx never imports or renders MarkdownBody", () => {
    const src = readFileSync(
      join(import.meta.dirname, "../../../components/transcript/DraftRail.tsx"),
      "utf8",
    );
    expect(src).not.toMatch(/MarkdownBody/);
    expect(src).toMatch(/den-draft-rail-plain/);
  });

  it("reloaded draft_status=live without streaming renders settled; last ord is the sole tail", () => {
    const messages: Message[] = [];
    for (let i = 1; i <= 16; i++) {
      messages.push({
        id: `stuck-${i}`,
        ord: i,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: `step ${i} body`,
        visibility: "transcript",
        draft_status: "live",
        tool_calls: [{ id: `tc-${i}`, name: "read", args: {} }],
        draft_version_count: 1,
        created_at: "t",
      });
    }
    const items = messagesToTranscriptItems(messages);
    const drafts = items.filter((i) => i.kind === "draft");
    expect(drafts).toHaveLength(16);
    for (const d of drafts) {
      if (d.kind === "draft") {
        expect(d.live).toBe(false);
      }
    }
    expect(drafts[drafts.length - 1]?.key).toBe("stuck-16");
  });

  it("provisional to committed keeps the same key and ord", () => {
    const provisional: Message[] = [
      { id: "u1", ord: 1, role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", created_at: "t" },
      {
        id: "slot",
        ord: 2,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "streaming answer",
        visibility: "internal",
        draft_status: "live",
        status: "streaming",
        created_at: "t",
      },
    ];
    const committed: Message[] = [
      provisional[0]!,
      {
        ...provisional[1]!,
        visibility: "transcript",
        draft_status: "committed",
        status: "complete",
        seq: 99,
      },
    ];
    const before = createTranscriptDisplayProjector()(provisional, undefined, { verboseMode: true })[0]!.map((i) => i.key);
    const after = createTranscriptDisplayProjector()(committed, undefined, { verboseMode: true })[0]!.map((i) => i.key);
    expect(after).toEqual(before);
  });

  it("with retries: keeps the draft rail when wire content is blank between retries", () => {
    const messages: Message[] = [
      {
        id: "slot",
        ord: 2,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "internal",
        draft_status: "live",
        draft_version_count: 2,
        status: "streaming",
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(messages);
    const draft = items.find((i) => i.kind === "draft");
    expect(draft?.kind).toBe("draft");
    if (draft?.kind === "draft") {
      expect(draft.draftVersionCount).toBe(2);
      expect(isCoordinatorDraftVariantB({ draft_version_count: draft.draftVersionCount })).toBe(
        true,
      );
    }
  });

  it("with retries: keeps the draft rail when a rejected retry commits as an empty-body tool step", () => {
    const messages: Message[] = [
      {
        id: "slot",
        ord: 2,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        kind: "draft",
        content: "",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 2,
        status: "complete",
        tool_calls: [{ id: "tc1", name: "grep", args: {} }],
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(messages);
    const draft = items.find((i) => i.kind === "draft");
    expect(draft?.kind).toBe("draft");
    if (draft?.kind === "draft") {
      expect(draft.draftVersionCount).toBe(2);
      expect(isCoordinatorDraftVariantB({ draft_version_count: draft.draftVersionCount })).toBe(
        true,
      );
    }
  });

  it("with retries: keeps version history when grounding arrives on an empty body frame", () => {
    const messages: Message[] = [
      {
        id: "slot",
        ord: 2,
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "transcript",
        draft_status: "committed",
        draft_version_count: 2,
        status: "complete",
        grounding: { traced: true, checks: [] },
        created_at: "t",
      },
    ];
    const items = messagesToTranscriptItems(messages);
    expect(items.find((i) => i.key === "slot")).toMatchObject({
      kind: "assistant",
      key: "slot",
    });
  });

  it("keeps an empty host-declared live slot without retries", () => {
    const items = messagesToTranscriptItems(
      [
        {
          id: "slot",
          role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
          content: "",
          visibility: "internal",
          draft_status: "live",
          draft_version_count: 1,
          created_at: "t",
        },
      ],
    );
    expect(items.some((item) => item.key === "slot" && item.kind === "draft")).toBe(true);
  });

  it("uses the current host body on a blank live wire frame", () => {
    const messages: Message[] = [
      {
        id: "slot",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "Streaming synthesis prose.",
        visibility: "internal",
        draft_status: "live",
        status: "streaming",
        draft_version_count: 1,
        created_at: "t",
      },
    ];
    expect(
      messagesToTranscriptItems(messages).some(
        (item) => item.key === "slot" && item.kind === "draft",
      ),
    ).toBe(true);
    const blanked: Message[] = [
      {
        ...messages[0]!,
        content: "",
        status: "complete",
        tool_calls: [{ id: "tc1", name: "read", args: { path: "a.go" } }],
      },
    ];
    const draft = messagesToTranscriptItems(blanked).find(
      (item) => item.key === "slot" && item.kind === "draft",
    );
    expect(draft?.kind).toBe("draft");
    if (draft?.kind === "draft") {
      expect(draft.text).toBe("");
    }
  });

  it("span prebuild keeps an empty host-declared live slot", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", workflow_run_id: ambientRun.id, created_at: "t" },
      {
        id: "slot",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "internal",
        draft_status: "live",
        status: "streaming",
        draft_version_count: 1,
        workflow_run_id: ambientRun.id,
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [ambientRun], ambientRun);
    const prebuilt = blocks[0]?.items ?? [];
    expect(prebuilt.some((item) => item.key === "slot" && item.kind === "draft")).toBe(true);
  });

  it("prebuilt display keeps the current empty draft frame", () => {
    const messages: Message[] = [
      { id: "u1", role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const, content: "go", workflow_run_id: ambientRun.id, created_at: "t" },
      {
        id: "slot",
        role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
        content: "",
        visibility: "internal",
        draft_status: "live",
        status: "streaming",
        draft_version_count: 1,
        workflow_run_id: ambientRun.id,
        created_at: "t",
      },
    ];
    const blocks = buildChatTranscriptBlocks(messages, [ambientRun], ambientRun);
    const prebuilt = blocks[0]?.items ?? [];
    expect(prebuilt.some((item) => item.key === "slot" && item.kind === "draft")).toBe(true);
    const displayed = createTranscriptDisplayProjector()(messages, [prebuilt], { verboseMode: true })[0]!;
    expect(displayed.some((item) => item.key === "slot" && item.kind === "draft")).toBe(true);
  });
});
