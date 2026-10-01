// @vitest-environment jsdom
/** Main-workspace placement and peer-window boundaries. */
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import * as ts from "typescript";
import { readSourceText } from "./test/stylesheet-source.ts";
import { CONTEXT_NAV_CATALOG } from "../shared/app-state-types.ts";
import { STAGE_REGISTRY, stageLabelFor } from "./components/stage/stage-registry.tsx";

const DEN_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");

function read(rel: string): string {
  return readSourceText(join(DEN_ROOT, rel));
}

function callSource(source: string, callee: string, marker?: string): string {
  const ast = ts.createSourceFile("Shell.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const matches: string[] = [];
  const visit = (node: ts.Node) => {
    if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === callee) {
      const text = node.getText(ast);
      if (marker === undefined || text.includes(marker)) matches.push(text);
    }
    ts.forEachChild(node, visit);
  };
  visit(ast);
  expect(matches, `${callee} call${marker ? ` containing ${marker}` : ""}`).toHaveLength(1);
  return matches[0]!;
}

describe("workspace placement contract", () => {
  it("keeps main-workspace placement to inline and split", () => {
    const types = read("shared/app-state-types.ts");
    expect(types).toContain('export type StagePlacement = "inline" | "split"');
    const placement = read("src/shell/stage-placement.ts");
    expect(placement).toContain("export function resolveStageColumn");
    expect(placement).toContain('export const DEFAULT_PLACEMENT: StagePlacement = "inline"');
    expect(placement).toContain(
      'export const DEFAULT_SPLIT_COMPANION: ContextNavItemId = "files"',
    );
  });

  it("opens Context as an independently addressed peer", () => {
    const shell = read("src/components/shell/shell-peer-windows.ts");
    const start = shell.indexOf("const detachStageToWindow");
    const detach = shell.slice(start, shell.indexOf("\n  };", start));
    expect(detach).toMatch(/openItemWindow\([\s\S]*kind: "context"/);
  });

  it("uses one host-managed registry for every peer view", () => {
    const peer = read("src/platform/windows/item-windows.ts");
    expect(peer).toContain("list_item_window_views");
    expect(peer).toContain("focus_item_window");
    expect(peer).toContain("close_item_window");
    const shell = read("src/components/shell/shell-peer-windows.ts");
    expect(shell).toContain("itemWindowViews");
  });

  it("gives a pulled-off file its editor area before optional workspace chrome", () => {
    const panes = read("src/components/shell/shell-pane-visibility.ts");
    expect(panes).toMatch(
      /fileWindowNavCollapsed[\s\S]*windowSubject\?\.kind === "file"/,
    );
  });

  it("binds a pulled-off file's project before mounting its Files stage", () => {
    const shell = read("src/components/shell/shell-peer-windows.ts");
    const start = shell.indexOf("let itemFileOpened");
    const itemFile = shell.slice(start, shell.indexOf("let itemContextOpened", start));
    expect(itemFile.indexOf("beginStageSwitch")).toBeGreaterThan(-1);
    expect(itemFile.indexOf("beginStageSwitch")).toBeLessThan(
      itemFile.indexOf('openStage("files");'),
    );
  });

  it("reads a pulled-off file from its addressed root", () => {
    const shell = read("src/components/shell/shell-peer-windows.ts");
    const start = shell.indexOf("let itemFileOpened");
    const itemFile = shell.slice(start, shell.indexOf("let itemContextOpened", start));
    expect(itemFile).toContain("rootId: itemSubject.rootId");
  });

  it("peers talk through the host event channel", () => {
    const channel = read("src/platform/windows/window-channel.ts");
    expect(channel).toContain("export async function listenHostEvent");
  });

  it("keeps the context registry total", () => {
    expect(Object.keys(STAGE_REGISTRY).sort()).toEqual(
      [...CONTEXT_NAV_CATALOG].sort(),
    );
    for (const id of CONTEXT_NAV_CATALOG) {
      expect(stageLabelFor(id).length).toBeGreaterThan(0);
      expect(typeof STAGE_REGISTRY[id].render).toBe("function");
    }
  });

  it("routes reactive return state through registry-declared chrome placement", () => {
    const registry = read("src/components/stage/stage-registry.tsx");
    expect(registry).toContain("back: () => StageBack | null");
    const stageChromeBacks = CONTEXT_NAV_CATALOG.filter(
      (id) => STAGE_REGISTRY[id].backPlacement === "stage",
    );
    expect(registry.match(/back=\{ctx\.back\(\)\}/g)).toHaveLength(
      stageChromeBacks.length,
    );
    const backs = read("src/components/shell/shell-workspace-context.ts");
    expect(backs).toContain('stageDefinition(stage).backPlacement !== "titlebar"');
    expect(backs).toContain('stageDefinition(stage).backPlacement !== "stage"');
    const shell = read("src/components/shell/Shell.tsx");
    expect(shell).toContain("back: () => stageBack(stage)");
    expect(shell).toMatch(/<ShellStageColumn[\s\S]*?back=\{titlebarBack\(\)\}/);
    expect(read("src/components/shell/ShellColumns.tsx")).toMatch(/<ShellHeaderTitlebar back=\{props\.back\}/);
  });

  it("keeps the Files return chip out of Files layout flow", () => {
    expect(STAGE_REGISTRY.files.backPlacement).toBe("titlebar");
    const files = read("src/files/components/ProjectFilesView.tsx");
    expect(files).not.toContain("den-files-stage-gutter");
    expect(files).not.toContain("StageBackChip");

    const titlebar = read("src/components/shell/ShellHeaderTitlebar.tsx");
    expect(titlebar).toContain("<StageBackChip back={back()} />");
    const shellCss = read("src/shell-domain.css");
    expect(shellCss).toMatch(
      /\.den-custom-chrome \.den-shell-header-titlebar\s*\{[^}]*position:\s*absolute/s,
    );
    expect(shellCss).toMatch(
      /\.den-custom-chrome[\s\S]*\.den-shell-stage--pane:has\(\.den-browse-stage\)[\s\S]*min-height:\s*var\(--den-titlebar-inset\)/,
    );
  });

  it("declares routed and deliberate Context arrivals at their call sites", () => {
    const sinks = read("src/components/shell/shell-navigation-sinks.ts");
    const sourceSink = sinks.slice(
      sinks.indexOf("const unregisterOpenSource"),
      sinks.indexOf("const unregisterOpenFilesSurface"),
    );
    expect(sourceSink).toContain(
      'openStageWithPlacement("files", "routed")',
    );
    const shell = read("src/components/shell/Shell.tsx");
    const contextNav = shell.slice(
      shell.indexOf("const goToContextStage"),
      shell.indexOf("const openStageWithPlacement"),
    );
    expect(contextNav).toContain(
      'openStageWithPlacement(stage, "deliberate")',
    );
  });

  it("claims stage chrome on every arrival, not only a Context click", () => {
    const shell = read("src/components/shell/Shell.tsx");
    const start = shell.indexOf("const openStageWithPlacement");
    const open = shell.slice(start, shell.indexOf("\n  };", start));
    expect(open).toContain("claimStageChrome");
    const panes = read("src/components/shell/shell-pane-visibility.ts");
    const collapse = callSource(panes, "createEffect", "shouldAutomaticallyCollapseNav");
    expect(collapse).toContain("shouldAutomaticallyCollapseNav");
    expect(collapse).toContain("untrack(windowNavUserCollapsed)");
    expect(collapse).not.toContain("widenWindowBy");
    expect(collapse).not.toContain("claimStageChrome");
    const show = callSource(
      panes,
      "createPaneVisibilityIntent",
      "setWindowNavCollapsed",
    );
    expect(show).toContain("setWindowNavCollapsed(false)");
    expect(show).toContain("stageOpenWindowDeficitPx");
    expect(show).not.toContain("navAutomaticallyCollapsed()");
  });

  it("stores one conversation width for the workspace", () => {
    const types = read("shared/app-state-types.ts");
    expect(types).toMatch(/chatWidthPx\?: number/);
    const store = read("src/shell/layout-store.ts");
    expect(store).toContain("export function preferredChatWidthPx()");
    expect(store).toContain("export function beginChatWidthResize");
    const collapse = read("src/shell/responsive-collapse.ts");
    expect(collapse).toContain("export function workspaceStageFloorPx");
  });
});
