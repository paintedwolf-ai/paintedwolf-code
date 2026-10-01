import { fireEvent, render } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { ToolContentProvider } from "../../chat/tool/tool-content.tsx";
import { registerOpenFilesSurfaceSink, resetOpenFilesSurfaceForTests } from "../../platform/navigation/open-files-surface.ts";
import { ToolContentLink } from "./ToolContentLink.tsx";

afterEach(resetOpenFilesSurfaceForTests);

it("opens exact tool content in Files without fetching or creating a nested scroller", () => {
  const sink = vi.fn();
  registerOpenFilesSurfaceSink(sink);
  const content = { kind: "inline" as const, text: "A complete sentence.\n🌲" };
  const view = render(() => <ToolContentProvider value={{
    identity: () => ({ projectId: "project", sessionId: "session", messageId: "message", toolCallId: "call", title: "Read · file.md" }),
    output: () => undefined, args: () => undefined, outputOffset: () => undefined, argsOffset: () => undefined,
  }}><ToolContentLink pane="output" label="Output" content={content} /></ToolContentProvider>);
  expect(view.container.querySelector("[data-den-scrollport]")).toBeNull();
  fireEvent.click(view.getByRole("button", { name: "Output in Files" }));
  expect(sink).toHaveBeenCalledWith({ kind: "chat-content", projectId: "project", document: {
    kind: "tool",    sessionId: "session", messageId: "message", toolCallId: "call", pane: "output",
    title: "Output · Read · file.md", content, revealOffset: undefined,
  } });
});
