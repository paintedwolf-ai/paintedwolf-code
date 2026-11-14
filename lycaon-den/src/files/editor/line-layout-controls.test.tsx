// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NATIVE_UI_HANDLER_IDS } from "../../contributions/native-ui-handlers.generated.ts";
import { resetContributionStoreForTest } from "../../contributions/contribution-store.ts";
import { seedStockFrame, stockId } from "../../contributions/stock-frame-test.ts";
import { buildAppMenuSpec } from "../../shortcuts/app-menu-model.ts";
import { STOCK_FRAME } from "../../contributions/stock-frame.generated.ts";
import {
  editorLineLayoutPref,
  editorPrettyPref,
  editorWordWrapPref,
  resetEditorPrefsForTests,
  saveEditorLineLayout,
} from "../../settings/editor/editor-prefs.ts";
import { extractCssRuleBlock } from "../../styling/css-contract.ts";
import { fileBufferKey, type FileBufferKey } from "../components/project-files-model.ts";
import { resolveEditorIdentity } from "../documents/editor-identity.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { FilesTab } from "../tabs/FilesTab.tsx";
import { fileTabKind } from "../tabs/files-tab-kind.ts";
import { FilesEditorStatus } from "./FilesEditorChrome.tsx";

afterEach(() => {
  cleanup();
  resetEditorPrefsForTests();
  resetContributionStoreForTest();
});

function makeBuffer(overrides: Partial<FileBuffer> = {}): FileBuffer {
  const key = overrides.key ?? fileBufferKey("root", overrides.path ?? "src/sample.json");
  return {
    key,
    rootId: "root",
    rootLabel: "workspace",
    path: "src/sample.json",
    name: "sample.json",
    kind: "text",
    content: '{"a":1}\n',
    draft: '{"a":1}\n',
    eol: "lf",
    mixedEol: false,
    dirty: false,
    pinned: false,
    preview: false,
    loading: false,
    error: null,
    absent: false,
    deleted: null,
    binary: false,
    truncated: false,
    sizeBytes: 8,
    mtimeMs: 1,
    language: "json",
    ...overrides,
  } as FileBuffer;
}

function renderStatus() {
  return render(() => (
    <FilesEditorStatus
      identity={resolveEditorIdentity({
        historical: false,
        workerDraft: false,
        deleted: false,
        writable: true,
        preview: false,
        editable: true,
      })}
      dirtyCount={0}
      language="JSON"
    />
  ));
}

const pressed = (view: ReturnType<typeof renderStatus>, id: string) =>
  view.getByTestId(`files-editor-layout-${id}`).getAttribute("aria-pressed");

describe("line layout controls", () => {
  it("pulls Off, Wrap and Pretty out of the View menu with the saved mode checked", () => {
    for (const handler of ["editor.lineLayoutOff", "editor.toggleWrap", "editor.togglePretty"]) {
      expect(NATIVE_UI_HANDLER_IDS).toContain(handler);
    }
    for (const mode of ["off", "wrap", "pretty"] as const) {
      seedStockFrame({ lineLayout: mode });
      const view = buildAppMenuSpec({ platform: "linux" }).find((section) => section.menu === "view");
      const pullOuts = (view?.groups ?? []).flat().filter((item) => item.submenu);
      expect(pullOuts).toHaveLength(1);
      const items = pullOuts[0]!.submenu!;
      expect(items.map((item) => item.id)).toEqual([
        stockId("editor-line-layout-off"), stockId("editor-toggle-wrap"), stockId("editor-toggle-pretty"),
      ]);
      expect(items.map((item) => item.checked)).toEqual(["off", "wrap", "pretty"].map((m) => m === mode));
    }
    expect(STOCK_FRAME.commands.filter((entry) => entry.id.includes("pretty") && entry.id.includes("apply"))).toEqual([]);
  });

  it("offers Off, Wrap and Pretty in order and shows the editor-wide choice", () => {
    resetEditorPrefsForTests({ wordWrap: true });
    const view = renderStatus();
    const pill = view.getByTestId("files-editor-line-layout");
    expect([...pill.querySelectorAll("button")].map((button) => button.dataset.value)).toEqual(["off", "wrap", "pretty"]);
    expect([...pill.querySelectorAll("button")].every((button) => !button.disabled)).toBe(true);
    expect(pressed(view, "wrap")).toBe("true");
    expect(pressed(view, "off")).toBe("false");
  });

  it("saves each choice and keeps the Wrap choice under Pretty", async () => {
    resetEditorPrefsForTests({ wordWrap: false });
    const view = renderStatus();

    fireEvent.click(view.getByTestId("files-editor-layout-pretty"));
    expect(editorLineLayoutPref()).toBe("pretty");
    expect(pressed(view, "pretty")).toBe("true");
    expect(editorWordWrapPref()).toBe(false);

    fireEvent.click(view.getByTestId("files-editor-layout-wrap"));
    expect([editorPrettyPref(), editorWordWrapPref()]).toEqual([false, true]);
    expect(pressed(view, "wrap")).toBe("true");

    await saveEditorLineLayout("pretty");
    expect(editorWordWrapPref()).toBe(true);
    fireEvent.click(view.getByTestId("files-editor-layout-off"));
    expect([editorPrettyPref(), editorWordWrapPref()]).toEqual([false, false]);
    expect(pressed(view, "off")).toBe("true");
  });

  it("keeps current-file tabs unmarked, preserves special tab icons, and never overrides editor identity to Preview", () => {
    const currentBuf = makeBuffer({ name: "extremely-long-configuration-filename.json", dirty: true });
    resetEditorPrefsForTests({ pretty: true });

    // Editable current-file identity stays "editing" (never "preview").
    expect(
      resolveEditorIdentity({
        historical: false,
        workerDraft: false,
        deleted: false,
        writable: true,
        preview: false,
        editable: true,
      }).id,
    ).toBe("editing");
    expect(fileTabKind(currentBuf, false)).toBeNull();
    expect(fileTabKind(currentBuf, true)).toEqual({ icon: "rewind", label: "Previous version" });

    const specialKinds = [
      [{ kind: "chat" as const, chatContent: { kind: "tool" as const } }, { icon: "tool", label: "Tool call details" }],
      [
        { kind: "chat" as const, chatContent: { kind: "approval" as const } },
        { icon: "settings-approvals", label: "Approval details" },
      ],
      [{ kind: "diff" as const }, { icon: "rewind", label: "File comparison" }],
      [{ kind: "walk" as const }, { icon: "file-group", label: "Group summary" }],
      [{ kind: "diffs" as const }, { icon: "diff", label: "All diffs" }],
      [{ kind: "chat" as const }, { icon: "session", label: "Chat content" }],
      [{ kind: "trust" as const }, { icon: "shield", label: "Trust changes" }],
    ] as const;
    for (const [identity, expected] of specialKinds) {
      expect(fileTabKind(identity, false)).toEqual(expected);
    }

    const noop = vi.fn();
    const tabView = render(() => (
      <div>
        <FilesTab
          buffer={currentBuf}
          name={{ name: currentBuf.name, qualifier: "", label: currentBuf.name, path: currentBuf.path }}
          stateLabels={["Unsaved"]}
          nameChange={() => null}
          active={true}
          pending={false}
          focusable={true}
          dragging={false}
          dropBefore={false}
          dropAfter={false}
          previousVersion={false}
          merging={false}
          onPointerDown={noop}
          onActivate={noop}
          onPromote={noop}
          onContextMenu={noop}
          onClose={noop}
        />
        <FilesTab
          buffer={makeBuffer({ key: "diff:1" as FileBufferKey, kind: "diff", name: " Diff " })}
          name={{ name: "Diff", qualifier: "", label: "Diff", path: "Diff" }}
          stateLabels={[]}
          nameChange={() => null}
          active={false}
          pending={false}
          focusable={false}
          dragging={false}
          dropBefore={false}
          dropAfter={false}
          previousVersion={false}
          merging={false}
          onPointerDown={noop}
          onActivate={noop}
          onPromote={noop}
          onContextMenu={noop}
          onClose={noop}
        />
      </div>
    ));

    const icons = tabView.container.querySelectorAll(".den-files-tab-kind-icon");
    expect(icons).toHaveLength(1);
    expect(icons[0]?.getAttribute("data-tip")).toBe("File comparison");
  });

  it("keeps status chips on the shared inset chrome", () => {
    const previewDomainCss = readFileSync(
      join(import.meta.dirname, "../../styling/files/preview-domain.css"),
      "utf8",
    );
    const shellUtilitiesCss = readFileSync(
      join(import.meta.dirname, "../../shell-utilities.css"),
      "utf8",
    );
    const baseRule = extractCssRuleBlock(
      previewDomainCss,
      ".den-files-editor__status-toggle",
    );
    const sharedInsetRecipe = shellUtilitiesCss.match(
      /@utility\s+den-inset-icon-btn\s*\{([\s\S]*?)\n\}/,
    )?.[1] ?? "";

    // Status toggles reuse shared den-inset-icon-btn chrome and only override compact sizing in preview-domain.css:
    expect(baseRule).toMatch(/width:\s*auto/);
    expect(baseRule).toMatch(/height:\s*auto/);
    expect(baseRule).toMatch(/padding:\s*2px 6px/);
    expect(baseRule).toMatch(/font-size:\s*var\(--text-den-caption\)/);
    expect(baseRule).not.toMatch(/--den-accent-signal/);

    // Shared den-inset-icon-btn recipe defines neutral-gray pressed/hover/focus/disabled states without accent-signal tint:
    expect(sharedInsetRecipe).toMatch(
      /&\[aria-pressed="true"\]:not\(:disabled\):not\([^)]+\)\s*\{[\s\S]*?background:\s*var\(--den-surface-offset\)[\s\S]*?box-shadow:\s*var\(--den-bevel-inset\)[\s\S]*?color:\s*var\(--den-text\)/,
    );
    expect(sharedInsetRecipe).toMatch(
      /&\[aria-pressed="true"\]:hover:not\(:disabled\):not\([^)]+\)\s*\{[\s\S]*?background:\s*var\(--den-tint-2\)[\s\S]*?box-shadow:\s*var\(--den-bevel-inset\)[\s\S]*?color:\s*var\(--den-text\)/,
    );
    expect(sharedInsetRecipe).not.toMatch(/--den-accent-signal/);
    expect(sharedInsetRecipe).toMatch(/&:focus-visible\s*\{[\s\S]*?box-shadow:/);
    expect(sharedInsetRecipe).toMatch(/&:disabled\s*\{[\s\S]*?opacity:\s*0\.4/);
  });
});
