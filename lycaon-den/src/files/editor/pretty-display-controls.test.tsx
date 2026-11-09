// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { createSignal } from "solid-js";
import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NATIVE_UI_HANDLER_IDS } from "../../contributions/native-ui-handlers.generated.ts";
import { STOCK_FRAME } from "../../contributions/stock-frame.generated.ts";
import { editorWordWrapPref, saveEditorWordWrap } from "../../settings/editor/editor-prefs.ts";
import { extractCssRuleBlock } from "../../styling/css-contract.ts";
import { fileBufferKey, type FileBufferKey } from "../components/project-files-model.ts";
import { resolveEditorIdentity } from "../documents/editor-identity.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import { FilesTab } from "../tabs/FilesTab.tsx";
import { fileTabKind } from "../tabs/files-tab-kind.ts";
import { FilesEditorStatus } from "./FilesEditorChrome.tsx";
import {
  clearPrettyDisplayTab,
  invokePrettyToggleForTab,
  isPrettyDisplayEnabled,
  prettyStatusToggleForTab,
  registerPrettySurfaceCapability,
  resetPrettyDisplayStateForTests,
  togglePrettyDisplayPref,
} from "./pretty-display-state.ts";

afterEach(() => {
  cleanup();
  resetPrettyDisplayStateForTests();
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

describe("Pretty display host contributions and status chrome (FMT-AC-06, FMT-INV-07)", () => {
  it("registers editor-toggle-pretty command, Alt+P keybinding, and View menu entry without an Apply command", () => {
    expect(NATIVE_UI_HANDLER_IDS).toContain("editor.togglePretty");
    const command = STOCK_FRAME.commands.find((entry) => entry.id === "painted-wolf/platform:editor-toggle-pretty");
    expect(command).toMatchObject({
      id: "painted-wolf/platform:editor-toggle-pretty",
      title: "Toggle pretty display",
      scope: "files",
      category: "navigation",
      action_kind: "native_ui",
      handler_id: "editor.togglePretty",
      when: { fact: "files_stage_active" },
    });

    const keybinding = STOCK_FRAME.keybindings.find((entry) => entry.id === "painted-wolf/platform:key-editor-toggle-pretty");
    expect(keybinding).toMatchObject({
      id: "painted-wolf/platform:key-editor-toggle-pretty",
      command: "painted-wolf/platform:editor-toggle-pretty",
      scope: "files",
      allow_in_input: true,
      bindings: {
        macos: ["Alt+P"],
        windows: ["Alt+P"],
        linux: ["Alt+P"],
      },
    });

    const menu = STOCK_FRAME.menus.find((entry) => entry.id === "painted-wolf/platform:app-menu-view-editor-toggle-pretty");
    expect(menu).toMatchObject({
      id: "painted-wolf/platform:app-menu-view-editor-toggle-pretty",
      slot: "app_menu.view",
      command: "painted-wolf/platform:editor-toggle-pretty",
      group: "2",
      order: 7,
      label: "Toggle pretty display",
    });

    const applyCommands = STOCK_FRAME.commands.filter((entry) => entry.id.includes("pretty") && entry.id.includes("apply"));
    expect(applyCommands).toEqual([]);
  });

  it("renders Pretty immediately left of Wrap, disabled until a surface supplies capability, and syncs per-tab state", () => {
    const projectId = "proj-pretty";
    const tabA = fileBufferKey("root", "a.json");
    const tabB = fileBufferKey("root", "b.json");
    const [activeTab, setActiveTab] = createSignal<FileBufferKey>(tabA);

    const view = render(() => (
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
        pretty={prettyStatusToggleForTab(projectId, activeTab())}
      />
    ));

    const prettyBtn = view.getByTestId("files-editor-pretty-toggle") as HTMLButtonElement;
    const wrapBtn = view.getByTestId("files-editor-wrap-toggle") as HTMLButtonElement;
    expect(prettyBtn.nextElementSibling).toBe(wrapBtn);
    expect(prettyBtn.disabled).toBe(true);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");
    expect(invokePrettyToggleForTab(projectId, tabA)).toBe(false);

    const unregisterA = registerPrettySurfaceCapability(projectId, tabA, {
      enabled: () => isPrettyDisplayEnabled(projectId, tabA),
      onToggle: () => {
        togglePrettyDisplayPref(projectId, tabA);
      },
    });
    const unregisterB = registerPrettySurfaceCapability(projectId, tabB, {
      enabled: () => isPrettyDisplayEnabled(projectId, tabB),
      onToggle: () => {
        togglePrettyDisplayPref(projectId, tabB);
      },
    });

    expect(prettyBtn.disabled).toBe(false);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(prettyBtn);
    expect(isPrettyDisplayEnabled(projectId, tabA)).toBe(true);
    expect(isPrettyDisplayEnabled(projectId, tabB)).toBe(false);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("true");

    // Switching tabs reflects tabB's independent Pretty state.
    setActiveTab(tabB);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");

    // Command/shortcut routing toggles the active tab and updates the button state.
    expect(invokePrettyToggleForTab(projectId, activeTab())).toBe(true);
    expect(isPrettyDisplayEnabled(projectId, tabB)).toBe(true);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("true");

    setActiveTab(tabA);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("true");

    clearPrettyDisplayTab(projectId, tabA);
    unregisterA();
    unregisterB();
    expect(prettyBtn.disabled).toBe(true);
    expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");
  });

  it("keeps current-file tabs unmarked, preserves special tab icons, and never overrides editor identity to Preview", () => {
    const projectId = "proj-tabs";
    const currentBuf = makeBuffer({ name: "extremely-long-configuration-filename.json", dirty: true });
    registerPrettySurfaceCapability(projectId, currentBuf.key, {
      enabled: () => isPrettyDisplayEnabled(projectId, currentBuf.key),
      onToggle: () => {
        togglePrettyDisplayPref(projectId, currentBuf.key);
      },
    });
    invokePrettyToggleForTab(projectId, currentBuf.key);
    expect(isPrettyDisplayEnabled(projectId, currentBuf.key)).toBe(true);

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

  it("drives persistent depressed styling for both Pretty and Wrap from aria-pressed while keeping their selection independent (FMT-AC-06)", async () => {
    const projectId = "proj-independent-toggles";
    const tabKey = fileBufferKey("root", "both.json");
    const initialWrap = editorWordWrapPref();
    await saveEditorWordWrap(false);

    try {
      const unregister = registerPrettySurfaceCapability(projectId, tabKey, {
        enabled: () => isPrettyDisplayEnabled(projectId, tabKey),
        onToggle: () => {
          togglePrettyDisplayPref(projectId, tabKey);
        },
      });

      const view = render(() => (
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
          pretty={prettyStatusToggleForTab(projectId, tabKey)}
        />
      ));

      const prettyBtn = view.getByTestId("files-editor-pretty-toggle") as HTMLButtonElement;
      const wrapBtn = view.getByTestId("files-editor-wrap-toggle") as HTMLButtonElement;

      // 1. Both off
      expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");
      expect(wrapBtn.getAttribute("aria-pressed")).toBe("false");

      // 2. Pretty on, Wrap off
      fireEvent.click(prettyBtn);
      expect(prettyBtn.getAttribute("aria-pressed")).toBe("true");
      expect(wrapBtn.getAttribute("aria-pressed")).toBe("false");

      // 3. Both Pretty and Wrap on simultaneously
      fireEvent.click(wrapBtn);
      expect(prettyBtn.getAttribute("aria-pressed")).toBe("true");
      expect(wrapBtn.getAttribute("aria-pressed")).toBe("true");

      // 4. Pretty off via Alt+P/menu handler while Wrap remains on
      expect(invokePrettyToggleForTab(projectId, tabKey)).toBe(true);
      expect(prettyBtn.getAttribute("aria-pressed")).toBe("false");
      expect(wrapBtn.getAttribute("aria-pressed")).toBe("true");

      unregister();
    } finally {
      await saveEditorWordWrap(initialWrap);
    }

    const previewDomainCss = readFileSync(
      join(import.meta.dirname, "../../styling/files/preview-domain.css"),
      "utf8",
    );
    const baseRule = extractCssRuleBlock(previewDomainCss, ".den-files-editor__status-toggle");
    const hoverRule = extractCssRuleBlock(
      previewDomainCss,
      ".den-files-editor__status-toggle:hover:not(:disabled)",
    );
    const pressedRule = extractCssRuleBlock(
      previewDomainCss,
      '.den-files-editor__status-toggle[aria-pressed="true"]:not(:disabled)',
    );
    const pressedHoverRule = extractCssRuleBlock(
      previewDomainCss,
      '.den-files-editor__status-toggle[aria-pressed="true"]:hover:not(:disabled)',
    );
    const focusRule = extractCssRuleBlock(
      previewDomainCss,
      ".den-files-editor__status-toggle:focus-visible",
    );
    const disabledRule = extractCssRuleBlock(
      previewDomainCss,
      ".den-files-editor__status-toggle:disabled",
    );

    expect(baseRule).toMatch(/border:\s*1px solid transparent/);
    expect(baseRule).toMatch(/padding:\s*1px 5px/);
    expect(hoverRule).toMatch(/border-color:\s*var\(--den-line\)/);
    expect(hoverRule).toMatch(/background:\s*var\(--den-tint-2\)/);
    expect(pressedRule).toMatch(/box-shadow:\s*var\(--den-bevel-inset\)/);
    expect(pressedRule).toMatch(/var\(--den-accent-signal\)/);
    expect(pressedRule).toMatch(/var\(--den-surface-offset\)/);
    expect(pressedRule).toMatch(/color:\s*var\(--den-text\)/);
    expect(pressedHoverRule).toMatch(/box-shadow:\s*var\(--den-bevel-inset\)/);
    expect(focusRule).toMatch(/outline:\s*1\.5px solid var\(--den-accent-signal\)/);
    expect(disabledRule).toMatch(/opacity:\s*0\.4/);
    expect(disabledRule).toMatch(/cursor:\s*not-allowed/);
  });
});
