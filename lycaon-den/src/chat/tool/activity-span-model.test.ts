import { sourceTextHash, fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import {
  activitySpanComposition,
  activitySpanFailureCount,
  activitySpanHint,
  activitySpanStatus,
  formatActivitySpanComposition,
  omitIndexWarmingFromTranscript,
  projectActivitySpans,
} from "./activity-span-model.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import type { ActivitySpanEntry, RawTranscriptItem, TranscriptItem } from "../transcript/projection/transcript-item-model.ts";

function part(
  id: string,
  tool: string,
  title: string,
  options?: Partial<ToolPartView>,
): ToolPartView {
  return {
    id,
    toolCallId: id,
    assistantMessageId: "assistant-1",
    messageId: `result-${id}`,
    tool,
    kind: "generic",
    status: "completed",
    title,
    args: {},
    output: "ok",
    error: null,
    ...options,
  };
}

function tool(partView: ToolPartView): RawTranscriptItem {
  return { kind: "tool", key: partView.id, part: partView };
}

function spanOf(items: readonly RawTranscriptItem[]) {
  const projected = projectActivitySpans(items);
  expect(projected).toHaveLength(1);
  expect(projected[0]?.kind).toBe("activity_span");
  return projected[0] as Extract<TranscriptItem, { kind: "activity_span" }>;
}

function toolEntry(partView: ToolPartView): ActivitySpanEntry {
  return { kind: "tool", part: partView };
}

function turnLoadItem(key: string, anchorMessageId: string): RawTranscriptItem {
  return {
    kind: "turn_load",
    key,
    row: {
      key,
      role: "tools",
      assistantMessageId: anchorMessageId,
      anchorMessageId,
      sub: 8,
      load: {
        session_id: "s1", trigger: "turn", opening_message_id: "u1", abstained: false, elapsed_ms: 400,
        engine: { name: "Bialy", label: "Bialy/mmbert-base#turn-load" },
        floor: [], tools: [{ tool: "read", source: "predicted", p: 0.9, carried: false }],
      },
    },
  };
}

describe("decision rows in activity spans", () => {
  it("lead the span of the assistant row they decided for without naming its work", () => {
    const span = spanOf([
      turnLoadItem("tl-1", "assistant-1"),
      turnLoadItem("tl-2", "assistant-1"),
      tool(part("r1", "read", "README.md")),
      tool(part("c1", "command", "build")),
    ]);
    expect(span.entries.map((entry) => entry.kind)).toEqual(["turn_load", "turn_load", "tool", "tool"]);
    expect(span.label).toBe("investigating");
    expect(activitySpanComposition(span.entries).map((p) => `${p.label} ×${p.count}`)).toEqual([
      "Local AI ×2", "read ×1", "command ×1",
    ]);
  });

  it("are dropped when no call follows them, so an answer-only turn adds no card", () => {
    expect(projectActivitySpans([turnLoadItem("tl-1", "assistant-1")])).toEqual([]);
    const withProse = projectActivitySpans([
      turnLoadItem("tl-1", "assistant-1"),
      { kind: "assistant", key: "assistant-1", text: "Done." },
    ]);
    expect(withProse.map((item) => item.kind)).toEqual(["assistant"]);
  });

  it("stay with their own assistant row when the next batch is another row's", () => {
    const projected = projectActivitySpans([
      turnLoadItem("tl-1", "assistant-1"),
      tool(part("r1", "read", "a.go")),
      tool(part("r2", "read", "b.go", { assistantMessageId: "assistant-2" })),
    ]);
    expect(projected.map((item) => item.kind === "activity_span" ? item.entries.map((e) => e.kind) : item.kind)).toEqual([
      ["turn_load", "tool"], ["tool"],
    ]);
  });
});

describe("activity span membership", () => {
  it("wraps a single tool immediately", () => {
    const span = spanOf([tool(part("r1", "read", "README.md"))]);

    expect(span.key).toBe("r1");
    expect(span.label).toBe("investigating");
    expect(span.entries).toHaveLength(1);
  });

  it("forms one span for consecutive tools in an assistant batch", () => {
    const span = spanOf([
      tool(part("r1", "read", "a.go")),
      tool(part("c1", "command", "build")),
      tool(part("w1", "write", "b.go", { kind: "write" })),
      tool(part("v1", "verify", "tests", { kind: "command" })),
    ]);

    expect(span.entries).toHaveLength(4);
  });

  it("keeps different durable batches separate", () => {
    const projected = projectActivitySpans([
      tool(part("r1", "read", "a.go")),
      tool(
        part("r2", "read", "b.go", {
          assistantMessageId: "assistant-2",
        }),
      ),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "activity_span",
    ]);
  });

  it("joins consecutive tool turns in a worker transcript", () => {
    const projected = projectActivitySpans(
      [
        tool(part("r1", "read", "a.go")),
        tool(
          part("r2", "read", "b.go", {
            assistantMessageId: "assistant-2",
          }),
        ),
      ],
      { layout: "worker" },
    );

    expect(projected).toHaveLength(1);
    expect(projected[0]).toMatchObject({
      kind: "activity_span",
      entries: [{ kind: "tool" }, { kind: "tool" }],
    });
  });

  it("still stops worker activity at a visible transcript boundary", () => {
    const projected = projectActivitySpans(
      [
        tool(part("r1", "read", "a.go")),
        { kind: "assistant", key: "a-note", text: "Now checking the result." },
        tool(
          part("v1", "verify", "tests", {
            assistantMessageId: "assistant-2",
          }),
        ),
      ],
      { layout: "worker" },
    );

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "assistant",
      "activity_span",
    ]);
  });

  it("uses a workflow run as the durable scope", () => {
    const span = spanOf([
      tool(
        part("r1", "read", "a.go", {
          assistantMessageId: "assistant-1",
          workflowRunId: "workflow-1",
        }),
      ),
      tool(
        part("w1", "write", "b.go", {
          assistantMessageId: "assistant-2",
          workflowRunId: "workflow-1",
          kind: "write",
        }),
      ),
    ]);

    expect(span.entries).toHaveLength(2);
  });

  it("anchors a diff row below the span holding the first write", () => {
    const projected = projectActivitySpans([
      tool(
        part("w1", "write", "a.go", {
          workflowRunId: "workflow-1",
          kind: "write",
          fileEdit: fileEditPreviewFixture({ path: "a.go", before: "old", after: "new" }),
        }),
      ),
      tool(
        part("c1", "command", "go test", {
          assistantMessageId: "assistant-2",
          workflowRunId: "workflow-1",
          kind: "command",
        }),
      ),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "file_edit",
      "activity_span",
    ]);
    expect(projected[0]).toMatchObject({ key: "w1" });
    expect(projected[1]).toMatchObject({
      key: "file-edit:w1",
      folds: [{ path: "a.go" }],
    });
    expect(projected[2]).toMatchObject({ key: "c1" });
  });

  it("folds consecutive writes into one span with one diff row per path", () => {
    const projected = projectActivitySpans([
      tool(
        part("w1", "write", "a.go", {
          workflowRunId: "workflow-1",
          kind: "write",
          fileEdit: fileEditPreviewFixture({ path: "a.go", before: "old", after: "new" }),
        }),
      ),
      tool(
        part("w2", "write", "b.go", {
          assistantMessageId: "assistant-2",
          workflowRunId: "workflow-1",
          kind: "write",
          fileEdit: fileEditPreviewFixture({ path: "b.go", before: "", after: "new file" }),
        }),
      ),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "file_edit",
    ]);
    expect(projected[0]).toMatchObject({
      entries: [{ kind: "tool" }, { kind: "tool" }],
    });
    expect(projected[1]).toMatchObject({
      folds: [{ path: "a.go" }, { path: "b.go" }],
    });
  });

  it("composes repeat writes of a path into the first card, not a second one", () => {
    const projected = projectActivitySpans([
      tool(
        part("w1", "write", "a.go", {
          workflowRunId: "workflow-1",
          kind: "write",
          fileEdit: fileEditPreviewFixture({ path: "a.go", before: "old", after: "v1" }),
        }),
      ),
      tool(
        part("c1", "command", "go test", {
          assistantMessageId: "assistant-2",
          workflowRunId: "workflow-1",
          kind: "command",
        }),
      ),
      tool(
        part("w2", "write", "a.go", {
          assistantMessageId: "assistant-3",
          workflowRunId: "workflow-1",
          kind: "write",
          fileEdit: fileEditPreviewFixture({ path: "a.go", before: "v1", after: "v2" }),
        }),
      ),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "file_edit",
      "activity_span",
    ]);
    expect(projected[1]).toMatchObject({
      key: "file-edit:w1",
      folds: [
        {
          path: "a.go",
          net: { before_sha256: sourceTextHash("old"), after_sha256: sourceTextHash("v2") },
          steps: [
            { key: "w1", toolCallId: "w1", messageId: "result-w1" },
            { key: "w2", toolCallId: "w2", messageId: "result-w2" },
          ],
        },
      ],
    });
    // Repeated writes keep their activity entries without duplicating the diff.
    expect(projected[2]).toMatchObject({
      kind: "activity_span",
      entries: [{ kind: "tool" }, { kind: "tool" }],
    });
  });

  it("does not compose writes across a user turn", () => {
    const projected = projectActivitySpans([
      tool(
        part("w1", "write", "a.go", {
          workflowRunId: "workflow-1",
          kind: "write",
          fileEdit: fileEditPreviewFixture({ path: "a.go", before: "old", after: "v1" }),
        }),
      ),
      { kind: "user", key: "u2", text: "keep going" },
      tool(
        part("w2", "write", "a.go", {
          assistantMessageId: "assistant-2",
          workflowRunId: "workflow-1",
          kind: "write",
          fileEdit: fileEditPreviewFixture({ path: "a.go", before: "v1", after: "v2" }),
        }),
      ),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "file_edit",
      "user",
      "activity_span",
      "file_edit",
    ]);
    expect(projected[1]).toMatchObject({
      folds: [{ path: "a.go", net: { before_sha256: sourceTextHash("old"), after_sha256: sourceTextHash("v1") } }],
    });
    expect(projected[4]).toMatchObject({
      folds: [{ path: "a.go", net: { before_sha256: sourceTextHash("v1"), after_sha256: sourceTextHash("v2") } }],
    });
  });

  it("never crosses a non-tool transcript boundary", () => {
    const projected = projectActivitySpans([
      tool(part("r1", "read", "a.go")),
      { kind: "user", key: "u1", text: "stop" },
      tool(part("r2", "read", "b.go")),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "user",
      "activity_span",
    ]);
  });

  it("wraps an unscoped tool as its own span", () => {
    const span = spanOf([
      tool(part("r1", "read", "a.go", { assistantMessageId: "" })),
    ]);
    expect(span.entries).toHaveLength(1);
  });

  it("keeps worker groups outside activity spans", () => {
    const projected = projectActivitySpans([
      tool(part("r1", "read", "source")),
      tool(part("t1", "task", "worker", { kind: "task" })),
      tool(part("d1", "request_decision", "question")),
      tool(
        part("p1", "page_snapshot", "final state", {
          visual: {
            id: "artifact-1",
            mime: "image/png",
            store_ref: true,
            source: "capture",
            caption: "final state",
          },
        }),
      ),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "activity_span",
      "worker_group",
      "activity_span",
    ]);
    expect(projected[1]).toMatchObject({
      kind: "worker_group",
      parts: [{ tool: "task" }],
    });
    const trailing = projected[2];
    expect(trailing?.kind).toBe("activity_span");
    if (trailing?.kind === "activity_span") {
      expect(trailing.entries).toHaveLength(2);
    }
  });

  it("uses the worker group for both runs and singletons", () => {
    const projected = projectActivitySpans([
      tool(part("t1", "task", "first worker", { kind: "task" })),
      tool(part("t2", "delegate_dispatch", "second worker", { kind: "task" })),
      { kind: "user", key: "u1", text: "continue" },
      tool(part("t3", "task", "third worker", { kind: "task" })),
    ]);

    expect(projected.map((item) => item.kind)).toEqual([
      "worker_group",
      "user",
      "worker_group",
    ]);
    const group = projected[0];
    expect(group?.kind).toBe("worker_group");
    if (group?.kind === "worker_group") {
      expect(group.parts.map((entry) => entry.id)).toEqual(["t1", "t2"]);
    }
    const singleton = projected[2];
    expect(singleton?.kind).toBe("worker_group");
    if (singleton?.kind === "worker_group") {
      expect(singleton.parts.map((entry) => entry.id)).toEqual(["t3"]);
    }
  });
});

describe("weighted activity headlines", () => {
  it("lets consequential work headline supporting investigation", () => {
    const span = spanOf([
      tool(part("r1", "read", "a.go")),
      tool(part("r2", "read", "b.go")),
      tool(part("r3", "grep", "TODO")),
      tool(part("w1", "edit", "a.go", { kind: "write" })),
    ]);
    expect(span.label).toBe("making changes");
  });

  it("changes the headline when accumulated work overtakes the incumbent", () => {
    const items = [
      tool(part("w1", "edit", "a.go", { kind: "write" })),
      tool(part("v1", "verify", "unit")),
    ];
    expect(spanOf(items).label).toBe("making changes");

    items.push(tool(part("v2", "verify", "integration")));
    expect(spanOf(items).label).toBe("checking work");
  });

  it("keeps the incumbent headline on a tie", () => {
    const span = spanOf([
      tool(part("w1", "edit", "a.go", { kind: "write" })),
      tool(part("v1", "verify", "unit")),
    ]);
    expect(span.label).toBe("making changes");
  });

  it("does not let incidental bookkeeping rename substantive work", () => {
    const span = spanOf([
      tool(part("r1", "read", "a.go")),
      tool(part("u1", "update_progress", "step one")),
      tool(part("u2", "state_update", "state")),
    ]);
    expect(span.label).toBe("investigating");
  });

  it("uses the first authored headline when every vote is incidental", () => {
    const span = spanOf([
      tool(part("u1", "update_progress", "step one")),
      tool(part("s1", "state_update", "state")),
    ]);
    expect(span.label).toBe("coordinating");
  });

  it("uses an uncatalogued tool name as an open-world supporting vote", () => {
    const span = spanOf([
      tool(part("m1", "acme__query_tickets", "first")),
      tool(part("m2", "acme__query_tickets", "second")),
    ]);
    expect(span.label).toBe("acme__query_tickets");
  });
});

describe("index warming activity", () => {
  const warming: RawTranscriptItem = {
    kind: "index_warming",
    key: "warm-1",
    workflowRunId: "web-1",
    meta: { trigger: "declared_url", tier: "crawl", topic: "docs.example" },
  };

  it("projects a singleton host row through the same activity surface", () => {
    const span = spanOf([warming]);
    expect(span.label).toBe("researching");
    expect(span.entries[0]?.kind).toBe("index_warming");
  });

  it("joins web tools in the same workflow run", () => {
    const span = spanOf([
      warming,
      tool(
        part("web-1", "web_search", "widgets", {
          workflowRunId: "web-1",
        }),
      ),
    ]);
    expect(span.label).toBe("researching");
    expect(span.entries).toHaveLength(2);
  });

  it("can be removed before activity projection", () => {
    const omitted = omitIndexWarmingFromTranscript([
      warming,
      tool(
        part("web-1", "web_search", "widgets", {
          workflowRunId: "web-1",
        }),
      ),
    ]);
    expect(omitted).toHaveLength(1);
    expect(omitted[0]?.kind).toBe("tool");
  });
});

describe("activity span summary", () => {
  it("reports status from running and newest settled calls", () => {
    const entries = (...statuses: ToolPartView["status"][]) =>
      statuses.map((status, index) =>
        toolEntry(part(`r${index}`, "read", `${index}.go`, { status })),
      );

    expect(activitySpanStatus(entries("completed", "error"))).toBe("error");
    expect(activitySpanStatus(entries("error", "completed"))).toBe("completed");
    expect(activitySpanStatus(entries("completed", "running"))).toBe("running");
    expect(activitySpanFailureCount(entries("error", "completed", "error"))).toBe(2);
  });

  it("puts a failed call first in the collapsed hint", () => {
    const hint = activitySpanHint(
      [
        toolEntry(part("r1", "read", "a.go")),
        toolEntry(part("r2", "read", "missing.go", { status: "error" })),
      ],
      "investigating",
    );
    expect(hint).toBe("missing.go, +1");
  });

  it("keeps an ordered composition for expanded details", () => {
    const composition = activitySpanComposition([
      toolEntry(part("r1", "read", "a.go")),
      toolEntry(part("r2", "read", "b.go")),
      toolEntry(part("w1", "edit", "a.go")),
    ]);
    expect(formatActivitySpanComposition(composition)).toBe("Read ×2 · Edit ×1");
  });
});

describe("application testing and title stability", () => {
  it("differentiates local loopback endpoints from external services", () => {
    const local = spanOf([
      tool(part("h1", "http_request", "http://localhost:3000/api/auth/signin", {
        args: { url: "http://localhost:3000/api/auth/signin" },
      })),
    ]);
    expect(local.label).toBe("testing endpoints");

    const remote = spanOf([
      tool(part("h2", "http_request", "https://api.github.com/repos", {
        args: { url: "https://api.github.com/repos" },
      })),
    ]);
    expect(remote.label).toBe("connecting to services");
  });

  it("synthesizes 'testing the application' for composite testing clusters", () => {
    const span = spanOf([
      tool(part("c1", "command", "curl http://localhost:3000/signin")),
      tool(part("p1", "page_open", "http://localhost:3000/signin", {
        args: { url: "http://localhost:3000/signin" },
      })),
      tool(part("p2", "page_snapshot", "Sign-in page")),
      tool(part("p3", "page_act", "click SSO")),
      tool(part("h1", "http_request", "http://localhost:8081/realms/focus", {
        args: { url: "http://localhost:8081/realms/focus/protocol/openid-connect/auth" },
      })),
      tool(part("c2", "command", "curl http://localhost:3000/todos")),
      tool(part("v1", "view_image", "screenshot.png")),
    ]);

    expect(span.label).toBe("testing the application");
  });

  it("ranks a span by its tools' catalog roles, so a renamed headline keeps its tier", () => {
    // view_video and view_image share the inspection role; either outranks a stream of reads.
    const viewing = spanOf([
      tool(part("r1", "read", "a.ts")),
      tool(part("r2", "read", "b.ts")),
      tool(part("v1", "view_video", "bug.mp4")),
      tool(part("r3", "read", "c.ts")),
      tool(part("r4", "read", "d.ts")),
    ]);
    expect(viewing.label).toBe("viewing videos");
    // A tool the catalog does not know has no role and never outranks one that does.
    const unknown = spanOf([
      tool(part("v1", "view_image", "shot.png")),
      tool(part("x1", "mystery_tool", "?")),
      tool(part("x2", "mystery_tool", "?")),
      tool(part("x3", "mystery_tool", "?")),
      tool(part("x4", "mystery_tool", "?")),
    ]);
    expect(unknown.label).toBe("viewing images");
  });

  it("maintains title stability and prevents flip-flopping across a testing stream", () => {
    const items: RawTranscriptItem[] = [
      tool(part("h1", "http_request", "http://localhost:3000/signin", {
        args: { url: "http://localhost:3000/signin" },
      })),
    ];
    expect(spanOf(items).label).toBe("testing endpoints");

    // Supporting command cannot downgrade tier 2 endpoint testing.
    items.push(tool(part("c1", "command", "curl http://localhost:3000/api/auth/signin")));
    expect(spanOf(items).label).toBe("testing endpoints");

    // Adding interface driving promotes to composite tier 4 application testing.
    items.push(tool(part("p1", "page_open", "http://localhost:3000/signin", {
      args: { url: "http://localhost:3000/signin" },
    })));
    expect(spanOf(items).label).toBe("testing the application");

    items.push(tool(part("p2", "page_snapshot", "sign in")));
    items.push(tool(part("p3", "page_act", "submit")));
    items.push(tool(part("h2", "http_request", "http://localhost:8081/auth", {
      args: { url: "http://localhost:8081/auth" },
    })));
    expect(spanOf(items).label).toBe("testing the application");

    // Subsequent mechanical commands cannot downgrade composite tier 4.
    for (let i = 2; i <= 9; i++) {
      items.push(tool(part(`c${i}`, "command", `curl test step ${i}`)));
    }
    expect(spanOf(items).label).toBe("testing the application");
  });
});
