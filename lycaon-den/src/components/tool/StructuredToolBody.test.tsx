import { describe, expect, it, vi, beforeEach } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { ToolPartView } from "../../chat/tool/tool-part-model.ts";
import { ToolContentProvider } from "../../chat/tool/tool-content.tsx";
import { StructuredToolBody } from "./StructuredToolBody.tsx";

const openFilesSurface = vi.hoisted(() => vi.fn());
vi.mock("../../platform/navigation/open-files-surface.ts", () => ({openFilesSurface}));

const openSourceLocation = vi.hoisted(() => vi.fn());
const browseProjectSource = vi.hoisted(() => vi.fn().mockResolvedValue({ entries: [{ name: "main.go", is_dir: false }] }));

vi.mock("../../platform/navigation/open-source.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../platform/navigation/open-source.ts")>(), openSourceLocation,
}));
vi.mock("../../platform/connection/app-connection.ts", () => ({ getLycaonClient: () => ({ browseProjectSource }) }));

vi.mock("../../search/search-nav.ts", () => ({
  openInSearch: vi.fn(),
}));

function part(overrides: Partial<ToolPartView> = {}): ToolPartView {
  return {
    id: "p1",
    toolCallId: "tc1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "read",
    kind: "read",
    status: "completed",
    title: "read",
    args: { path: "src/main.go" },
    output: "package main\n",
    error: null,
    ...overrides,
  };
}

function factPathLink(): HTMLElement {
  const el = document.querySelector(
    ".den-tool-part-card-facts [data-testid='source-path-link']",
  );
  if (!(el instanceof HTMLElement)) throw new Error("fact path link not found");
  return el;
}

function renderBody(current: () => ToolPartView) {
  return render(() => <ToolContentProvider value={{
    identity: () => ({projectId:"proj-1",sessionId:"session",messageId:"m1",toolCallId:"tc1"}),
    output: () => undefined, args: () => undefined, outputOffset: () => undefined, argsOffset: () => undefined,
  }}><StructuredToolBody part={current()} projectId="proj-1" /></ToolContentProvider>);
}

describe("StructuredToolBody live updates", () => {
  it("keeps facts mounted and opens the latest output without a nested scrollport", () => {
    const [current, setCurrent] = createSignal(part());
    const view = renderBody(current);
    const link = view.getAllByTestId("tool-content-link")[0]!;
    const fact = view.container.querySelector(".den-tool-part-card-facts > div");
    setCurrent(part());
    expect(view.getAllByTestId("tool-content-link")[0]).toBe(link);
    expect(view.container.querySelector(".den-tool-part-card-facts > div")).toBe(fact);
    setCurrent(part({ output: "package main\nfunc main() {}" }));
    fireEvent.click(link);
    expect(openFilesSurface).toHaveBeenLastCalledWith(expect.objectContaining({document:expect.objectContaining({content:expect.objectContaining({text:"package main\nfunc main() {}"})})}));
    expect(view.container.querySelector(".den-tool-part-raw")).toBeNull();
  });
});

describe("StructuredToolBody source links", () => {
  beforeEach(() => {
    openSourceLocation.mockReset();
    openSourceLocation.mockResolvedValue({ status: "opened-in-app" });
  });

  it("renders the input Path fact as a SourcePathLink and opens on click", async () => {
    render(() => (
      <StructuredToolBody part={part()} projectId="proj-1" />
    ));
    const link = factPathLink();
    expect(link.textContent).toContain("src/main.go");
    fireEvent.click(link);
    expect(openSourceLocation).toHaveBeenCalledWith({
      intent: "permanent",
      projectId: "proj-1",
      path: "src/main.go",
      entryKind: "file",
      line: undefined,
      handle: undefined,
    });
  });

  it("updates a reused input fact's label and destination together", () => {
    const [current, setCurrent] = createSignal(part({ args: { path: "first" } }));
    render(() => <StructuredToolBody part={current()} projectId="proj-1" rootRefs={[{ id: "root", path: "/repo" }]} />);
    const link = factPathLink();
    setCurrent(part({ args: { path: "second" } }));
    expect(factPathLink()).toBe(link);
    expect(link.textContent).toBe("second");
    fireEvent.click(link);
    expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({ rootId: "root", path: "second" }));
  });

  it("preserves a secondary root and a host directory kind in output facts", () => {
    render(() => <StructuredToolBody part={part({ args: {}, output: JSON.stringify({ path: "src", root_id: "other", is_dir: true }) })}
      projectId="proj-1" rootRefs={[{ id: "primary", path: "/repo", is_primary: true }, { id: "other", path: "/other" }]} />);
    fireEvent.click(factPathLink());
    expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({ path: "src", rootId: "other", entryKind: "folder" }));
  });

  it("offers Add to chat on Path fact context menu when rootRefs resolve", async () => {
    render(() => (
      <StructuredToolBody
        part={part()}
        projectId="proj-1"
        sessionId="sess-1"
        rootRefs={[{ id: "root-a", path: "/proj" }]}
      />
    ));
    fireEvent.contextMenu(factPathLink());
    expect(await screen.findByTestId("menu-add-to-chat")).toBeTruthy();
  });

  it("renders path facts as plain text when projectId is absent", () => {
    render(() => <StructuredToolBody part={part()} />);
    expect(
      document.querySelector("[data-testid='source-path-link']"),
    ).toBeNull();
    expect(document.body.textContent).toContain("src/main.go");
  });

  it("opens read-range contents in Files and keeps the source path in its input facts", () => {
    const output = JSON.stringify({path:"src/foo.go",ranges:[{offset:42,end_line:44,content:"func foo() {}\n"}]});
    const view=renderBody(()=>part({args:{path:"src/foo.go"},output}));
    expect(factPathLink().textContent).toContain("src/foo.go");
    fireEvent.click(view.getAllByTestId("tool-content-link")[0]!);
    expect(openFilesSurface).toHaveBeenLastCalledWith(expect.objectContaining({document:expect.objectContaining({content:expect.objectContaining({text:expect.stringContaining("func foo() {}")})})}));
    expect(view.container.querySelector("pre")).toBeNull();
  });

  it("keeps the wire spill path as host-data chrome (not SourcePathLink)", () => {
    const output = JSON.stringify({ ok: true, wire_spill_path: "tool-spills/tc1.json" });
    render(() => (
      <StructuredToolBody
        part={part({ tool: "command", kind: "command", args: { command: "x" }, output })}
        projectId="proj-1"
      />
    ));
    const spill = document.querySelector(
      '[data-testid="tool-compaction-spill"]',
    ) as HTMLElement | null;
    // Spill paths can appear in fact rows or compaction banners.
    const spillText = spill ?? document.evaluate(
      "//*[text()[contains(.,'tool-spills/tc1.json')]]",
      document.body,
      null,
      XPathResult.FIRST_ORDERED_NODE_TYPE,
      null,
    ).singleNodeValue;
    expect(spillText).toBeTruthy();
    if (spillText instanceof HTMLElement) {
      expect(spillText.tagName).not.toBe("BUTTON");
    }
    expect(
      document.querySelector("[data-testid='source-path-link']"),
    ).toBeNull();
    if (spill) {
      expect(spill.getAttribute("data-path")).toBe("tool-spills/tc1.json");
    }
  });
});

it("renders an outside-root tool path as an inert fact", () => {
  const { container } = render(() => <StructuredToolBody part={part({ args: { path: "/home/person/settings.json" } })} projectId="proj-1" rootRefs={[{ id: "root", path: "/repo" }]} />);
  expect(container.querySelector("[data-testid=source-path-link]")).toBeNull();
  expect(container.textContent).toContain("/home/person/settings.json");
});
