// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import {
  formatSelectedTextAttachmentBody,
  formatSelectedTextHeader,
  normalizeMessageId,
  resolveSelectionProvenance,
  selectionAttachmentFilename,
  USER_SELECTED_TEXT_PREFIX,
} from "./selection-provenance.ts";

describe("selection-provenance", () => {
  it("normalizes tool composite message ids", () => {
    expect(normalizeMessageId("msg-1:call-9")).toBe("msg-1");
    expect(normalizeMessageId("msg-1")).toBe("msg-1");
    expect(normalizeMessageId("  ")).toBeUndefined();
  });

  it("formats header omitting empty fields", () => {
    expect(
      formatSelectedTextHeader({
        text: "x",
        sessionId: "s1",
        messageId: "m1",
        path: "src/a.go",
        line: 42,
      }),
    ).toBe(
      `${USER_SELECTED_TEXT_PREFIX}session=s1; message=m1; path=src/a.go; line=42]`,
    );
  });

  it("includes handle= when stamped", () => {
    expect(
      formatSelectedTextHeader({
        text: "x",
        sessionId: "s1",
        handle: "read#1",
        path: "src/a.go",
      }),
    ).toBe(
      `${USER_SELECTED_TEXT_PREFIX}session=s1; handle=read#1; path=src/a.go]`,
    );
  });

  it("builds attachment body with header then snippet", () => {
    const body = formatSelectedTextAttachmentBody(
      { text: "hello", sessionId: "s1" },
      "hello",
    );
    expect(body.startsWith(USER_SELECTED_TEXT_PREFIX)).toBe(true);
    expect(body.endsWith("\nhello")).toBe(true);
  });

  it("names the chip from path basename when present", () => {
    expect(selectionAttachmentFilename(undefined)).toBe("selection.txt");
    expect(selectionAttachmentFilename("pkg/foo.go")).toBe("foo.go (selection).txt");
  });

  it("resolves stamped ancestors from the event target", () => {
    const stream = document.createElement("div");
    stream.setAttribute("data-testid", "chat-stream");
    stream.setAttribute("data-session-id", "sess-a");
    stream.setAttribute("data-project-id", "proj-a");

    const row = document.createElement("div");
    row.setAttribute("data-msg-id", "msg-9:tool-1");
    row.setAttribute("data-tool-call-ids", "tool-1");

    const pathEl = document.createElement("div");
    pathEl.setAttribute("data-den-source-path", "src/main.go");
    pathEl.setAttribute("data-den-source-line", "7");
    pathEl.setAttribute("data-root-id", "root-1");
    pathEl.setAttribute("data-project-id", "proj-a");
    pathEl.textContent = "selected";

    row.appendChild(pathEl);
    stream.appendChild(row);
    document.body.appendChild(stream);

    const prov = resolveSelectionProvenance(pathEl, "selected");
    expect(prov).toMatchObject({
      text: "selected",
      sessionId: "sess-a",
      messageId: "msg-9",
      toolCallId: "tool-1",
      projectId: "proj-a",
      path: "src/main.go",
      line: 7,
      rootId: "root-1",
    });

    stream.remove();
  });

  it("prefers data-den-source-path over data-path", () => {
    const wrap = document.createElement("div");
    wrap.setAttribute("data-path", "other.go");
    const inner = document.createElement("span");
    inner.setAttribute("data-den-source-path", "preferred.go");
    wrap.appendChild(inner);
    document.body.appendChild(wrap);
    expect(resolveSelectionProvenance(inner, "t").path).toBe("preferred.go");
    wrap.remove();
  });

  it("reads citation-row handle and path stamps", () => {
    const row = document.createElement("li");
    row.setAttribute("data-testid", "citation-grounding-citation");
    row.setAttribute("data-handle", "read#1");
    row.setAttribute("data-path", "src/a.go");
    row.setAttribute("data-line", "12");
    row.setAttribute("data-project-id", "proj-cite");
    const excerpt = document.createElement("p");
    excerpt.textContent = "fn main";
    row.appendChild(excerpt);
    document.body.appendChild(row);

    const prov = resolveSelectionProvenance(excerpt, "fn main");
    expect(prov).toMatchObject({
      handle: "read#1",
      path: "src/a.go",
      line: 12,
      projectId: "proj-cite",
    });
    row.remove();
  });

  it("reads search-detail stamps outside the chat stream", () => {
    const detail = document.createElement("div");
    detail.setAttribute("data-testid", "search-hit-detail");
    detail.setAttribute("data-session-id", "sess-hit");
    detail.setAttribute("data-project-id", "proj-hit");
    detail.setAttribute("data-source-ref", "msg-3");
    detail.setAttribute("data-hit-kind", "message");
    detail.setAttribute("data-den-source-path", "docs/a.md");
    detail.setAttribute("data-root-id", "r1");
    const pre = document.createElement("pre");
    pre.textContent = "snippet";
    detail.appendChild(pre);
    document.body.appendChild(detail);

    const prov = resolveSelectionProvenance(pre, "snippet");
    expect(prov).toMatchObject({
      sessionId: "sess-hit",
      projectId: "proj-hit",
      sourceRef: "msg-3",
      hitKind: "message",
      path: "docs/a.md",
      rootId: "r1",
    });
    detail.remove();
  });
});
