import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { SessionTranscript } from "./SessionTranscript.tsx";

describe("SessionTranscript worker layout", () => {
  it("renders worker summary envelope as markdown", () => {
    render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "ws-1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: `<task job_id="job-1" state="complete">
  <summary>Short</summary>
  <task_result>## Worker result</task_result>
</task>`,
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe(
      "Worker result",
    );
  });

  it("linkifies cited paths in worker summary prose", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="worker"
        projectId="p1"
        messages={[
          {
            id: "ws-2",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: `<task job_id="job-2" state="complete">
  <summary>Short</summary>
  <task_result>The fix landed in \`src/foo.ts\`.</task_result>
</task>`,
            grounding: {
              traced: true,
              cited_evidence: [{ path: "src/foo.ts", line: 9 }],
            },
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    const button = container.querySelector(".den-source-path-link");
    expect(button).toBeTruthy();
    expect(button?.getAttribute("data-den-source-path")).toBe("src/foo.ts");
    expect(button?.getAttribute("data-den-source-line")).toBe("9");
  });

  it("renders user prompt as markdown", () => {
    render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "u1",
            role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
            content: "## Assignment\n\n- **Add** `grep` builtin",
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe(
      "Assignment",
    );
    expect(screen.getByText("Add").tagName).toBe("STRONG");
  });

  it("shows authored HTML literally in worker activity", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "u-mark",
            role: "user", origin: "user" as const, authority: "user" as const, trust_tier: "trusted" as const,
            content:
              "<!-- worker-assignment -->\nUse snippet() with <mark> tags, ordered by bm25.",
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));

    expect(container.querySelector("mark")).toBeNull();
    expect(container.textContent).toContain(
      "Use snippet() with <mark> tags, ordered by bm25.",
    );
    expect(container.textContent).not.toContain("worker-assignment");
  });

  it("does not promote a hybrid Harmony dump into a Complete card", () => {
    render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a-hybrid",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content:
              "analysisWe attempted tea CLI." +
              'assistantfinal{"leg_status":"complete","brief":"done"}',
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    expect(screen.queryByRole("heading", { level: 2, name: "Complete" })).toBeNull();
  });

  it("renders worker completion JSON as markdown, not raw JSON", () => {
    render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a-json",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content:
              '{"leg_status":"complete","files_modified":["main.go"],"objectives_met":["wired handler"],"remaining_risk":[],"suggested_next_task":"","brief":"## Summary\\n\\nAll set."}',
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    expect(screen.getByRole("heading", { level: 2, name: "Complete" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Summary" })).toBeTruthy();
    expect(screen.getByText("All set.")).toBeTruthy();
    expect(screen.getByText("wired handler")).toBeTruthy();
    expect(screen.queryByText(/leg_status/)).toBeNull();
  });

  it("renders assistant prose as markdown", () => {
    render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "**Bold** answer",
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    expect(screen.getByText("Bold").tagName).toBe("STRONG");
  });

  it("renders accepted worker completion JSON as markdown even when kind is draft", () => {
    render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a-json",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            kind: "draft",
            content:
              '{"leg_status":"complete","files_modified":["main.go"],"objectives_met":["wired handler"],"remaining_risk":[],"suggested_next_task":"","brief":"## Summary\\n\\nAll set."}',
            created_at: "2026-01-01T00:00:00Z",
          },
        ]}
      />
    ));
    expect(screen.getByRole("heading", { level: 2, name: "Summary" })).toBeTruthy();
    expect(screen.getByText("All set.")).toBeTruthy();
  });

  it("renders git_status as a worker tool card, not coordinator placeholder", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "",
            tool_calls: [
              { id: "tc1", name: "list_dir", args: { path: "." } },
              { id: "tc2", name: "git_status", args: {} },
            ],
            created_at: "2026-01-01T00:00:00Z",
          },
          {
            id: "t1",
            role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: '{"entries":[]}',
            tool_result: { content: '{"entries":[]}' },
            created_at: "2026-01-01T00:00:01Z",
          },
          {
            id: "t2",
            role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: "branch main",
            tool_result: { content: "branch main" },
            created_at: "2026-01-01T00:00:02Z",
          },
        ]}
      />
    ));
    expect(container.textContent).not.toContain("Coordinator working");
    expect(
      container.querySelectorAll('.den-tool-part[data-layout="worker"]').length,
    ).toBe(2);
  });

  it("renders tool calls as compact chips in worker layout", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "",
            tool_calls: [
              { id: "tc1", name: "read", args: { path: "main.go" } },
            ],
            created_at: "2026-01-01T00:00:00Z",
          },
          {
            id: "t1",
            role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: "package main",
            tool_result: {
              content: "package main",
              tool: "read",
              tool_call_id: "tc1",
              assistant_message_id: "a1",
              tool_args: { path: "main.go" },
            },
            created_at: "2026-01-01T00:00:01Z",
          },
        ]}
      />
    ));
    const readCard = container.querySelector(
      '.den-tool-part[data-layout="worker"][data-tool="read"]',
    );
    expect(readCard).toBeTruthy();
    expect(readCard?.hasAttribute("open")).toBe(false);
  });

  it("merges consecutive activity across worker tool turns", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a1",
            ord: 1,
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "",
            tool_calls: [{ id: "tc1", name: "read", args: { path: "main.go" } }],
            created_at: "2026-01-01T00:00:00Z",
          },
          {
            id: "t1",
            ord: 2,
            role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: "package main",
            tool_result: {
              content: "package main",
              tool: "read",
              tool_call_id: "tc1",
              assistant_message_id: "a1",
              tool_args: { path: "main.go" },
            },
            created_at: "2026-01-01T00:00:01Z",
          },
          {
            id: "a2",
            ord: 3,
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "",
            tool_calls: [{ id: "tc2", name: "verify", args: { command: "test" } }],
            created_at: "2026-01-01T00:00:02Z",
          },
          {
            id: "t2",
            ord: 4,
            role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: "ok",
            tool_result: {
              content: "ok",
              tool: "verify",
              tool_call_id: "tc2",
              assistant_message_id: "a2",
              tool_args: { command: "test" },
            },
            created_at: "2026-01-01T00:00:03Z",
          },
        ]}
      />
    ));

    const cards = container.querySelectorAll('[data-testid="activity-span-card"]');
    expect(cards).toHaveLength(1);
    expect(cards[0]?.getAttribute("data-count")).toBe("2");
  });

  it("renders a single worker file edit as a group diff card", () => {
    const { container } = render(() => (
      <SessionTranscript
        layout="worker"
        messages={[
          {
            id: "a1",
            role: "assistant", origin: "model" as const, authority: "none" as const, trust_tier: "trusted" as const,
            content: "",
            tool_calls: [{ id: "tc1", name: "write", args: { path: "main.go" } }],
            created_at: "2026-01-01T00:00:00Z",
          },
          {
            id: "t1",
            role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
            content: "ok",
            tool_result: {
              content: "ok",
              tool: "write",
              tool_call_id: "tc1",
              assistant_message_id: "a1",
              tool_args: { path: "main.go" },
              file_edit_preview: fileEditPreviewFixture({
                path: "main.go",
                before: "",
                after: "package main\n",
              }),
            },
            created_at: "2026-01-01T00:00:01Z",
          },
        ]}
      />
    ));
    const writeCard = container.querySelector(
      '.den-tool-part[data-layout="worker"][data-tool="write"]',
    );
    expect(writeCard?.hasAttribute("open")).toBe(false);
    expect(
      container.querySelector('[data-testid="diff-group"]')?.getAttribute(
        "data-files",
      ),
    ).toBe("1");
    expect(container.querySelector('[data-testid="file-edit-diff"]')).toBeTruthy();
  });
});
