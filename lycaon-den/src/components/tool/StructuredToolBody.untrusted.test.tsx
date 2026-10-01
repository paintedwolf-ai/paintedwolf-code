import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { StructuredToolBody } from "./StructuredToolBody.tsx";
import { ToolContentProvider } from "../../chat/tool/tool-content.tsx";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../../platform/navigation/open-files-surface.ts";

afterEach(resetOpenFilesSurfaceForTests);

function part(overrides: Partial<ToolPartView> = {}): ToolPartView {
  return {
    id: "p1",
    toolCallId: "tc1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "fetch_url",
    kind: "generic",
    status: "completed",
    title: "fetch",
    args: { url: "https://example.com" },
    output:
      "Page body with ![x](https://evil.example/a.png) and [link](https://evil.example/p).",
    error: null,
    ...overrides,
  };
}

describe("StructuredToolBody linked text", () => {
  it.each([true, false])("preserves image syntax as document text when external authorship is %s", (externallyAuthored) => {
    const source = part({ externallyAuthored });
    const sink = vi.fn();
    registerOpenFilesSurfaceSink(sink);
    const view = render(() => <ToolContentProvider value={{
      identity: () => ({ projectId: "project", sessionId: "session", messageId: source.messageId, toolCallId: source.toolCallId }),
      output: () => undefined, args: () => undefined, outputOffset: () => undefined, argsOffset: () => undefined,
    }}><StructuredToolBody part={source} projectId="project" sessionId="session" /></ToolContentProvider>);
    expect(view.container.querySelector("img, .den-md-remote-img, .den-md-href-host")).toBeNull();
    expect(sink).not.toHaveBeenCalled();
    for (const button of view.getAllByTestId("tool-content-link")) fireEvent.click(button);
    expect(sink.mock.calls.some(([request]) => request.kind === "chat-content" &&
      request.document.content.kind === "inline" && request.document.content.text === source.output)).toBe(true);
    expect(view.container.querySelector("img")).toBeNull();
  });
});
