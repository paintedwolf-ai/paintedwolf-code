import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@solidjs/testing-library";
import { EditorSettingsPanel } from "./EditorSettingsPanel.tsx";
import { EMPTY_APP_STATE_V1 } from "../../../../shared/app-state-types.ts";
import { resetAppStateSnapshotForTests } from "../../../store/app-state-snapshot.ts";
import { resetExternalOpenPrefsForTests } from "../../../settings/editor/external-open-prefs.ts";
import { resetEditorPrefsForTests } from "../../../settings/editor/editor-prefs.ts";
import { detectEditors } from "../../../platform/files/detect-editors.ts";
import { loadAppState, patchAppState } from "../../../platform/persistence/app-state.ts";

vi.mock("../../../platform/persistence/app-state.ts", () => ({
  loadAppState: vi.fn(async () => ({
    ...EMPTY_APP_STATE_V1,
    externalOpen: { externalEditor: "cursor" },
  })),
  patchAppState: vi.fn(async (_patch, fallback) => fallback),
}));

vi.mock("../../../platform/files/detect-editors.ts", () => ({
  detectEditors: vi.fn(),
}));

vi.mock("../../../platform/runtime.ts", () => ({
  tauriPlatform: () => "macos",
  detectedPlatform: () => "macos",
  isTauriRuntime: () => true,
}));

function renderedOptions(el: HTMLElement): { values: (string | null)[]; labels: string[] } {
  el.click();
  const options = screen.getAllByRole("option");
  const result = {
    values: options.map((option) => option.getAttribute("data-value")),
    labels: options.map(
      (option) =>
        option.querySelector(".den-select__option-label")?.textContent?.trim() ?? "",
    ),
  };
  screen.getByRole("listbox").dispatchEvent(
    new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
  );
  return result;
}

describe("EditorSettingsPanel — in-app prefs", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetExternalOpenPrefsForTests();
    resetEditorPrefsForTests();
    vi.mocked(detectEditors).mockResolvedValue([]);
  });

  it("changes the walk default and clears the saved location", async () => {
    vi.mocked(loadAppState).mockResolvedValueOnce({
      ...EMPTY_APP_STATE_V1,
      editor: { walkControlsLocation: { placement: "floating", position: { x: 150, y: 200 } } },
    });
    render(() => <EditorSettingsPanel />);
    await waitFor(() => expect((screen.getByTestId("editor-walk-controls-docked") as HTMLButtonElement).disabled).toBe(false));
    screen.getByTestId("editor-walk-controls-docked").click();
    await waitFor(() => expect(vi.mocked(patchAppState)).toHaveBeenCalledWith(
      expect.objectContaining({ editor: expect.objectContaining({ walkControlsDefault: "docked", walkControlsLocation: undefined }) }),
      expect.anything(),
    ));
    expect((screen.getByTestId("editor-walk-controls-reset") as HTMLButtonElement).disabled).toBe(true);
  });

  it("changes visible tree levels and turns sticky rows off", async () => {
    render(() => <EditorSettingsPanel />);
    const control = screen.getByTestId("editor-tree-visible-levels") as HTMLButtonElement;
    await waitFor(() => expect(control.disabled).toBe(false));
    expect(control.textContent).toContain("5 levels (default)");
    for (const value of ["3", "10", "0"]) {
      control.click();
      screen.getAllByRole("option").find(option => option.getAttribute("data-value") === value)!.click();
      await waitFor(() => expect(vi.mocked(patchAppState)).toHaveBeenCalledWith(
        expect.objectContaining({ editor: expect.objectContaining({ treeVisibleLevels: Number(value) }) }), expect.anything(),
      ));
    }
    expect(control.textContent).toContain("Off");
  });

  it("renders every in-app editor pref control", async () => {
    render(() => <EditorSettingsPanel />);
    await waitFor(() =>
      expect(
        (screen.getByTestId("editor-font-size-inc") as HTMLButtonElement).disabled,
      ).toBe(false),
    );
    for (const testId of [
      "editor-font-family",
      "editor-font-size",
      "editor-line-height",
      "editor-word-wrap",
      "editor-reveal-in-tree",
      "editor-tree-visible-levels",
      "editor-line-numbers",
      "editor-indent-style",
      "editor-indent-width",
      "editor-indent-guides",
      "editor-whitespace",
      "editor-version-comparison",
      "editor-walk-controls-default",
      "editor-walk-controls-reset",
      "editor-scrollbar-ticks",
      "editor-scrollbar-ticks-symbols",
      "editor-scrollbar-ticks-changes",
      "editor-scrollbar-ticks-matches",
      "editor-scrollbar-ticks-cursor",
      "editor-scrollbar-ticks-windows",
      "editor-scrollbar-ticks-agents",
      "editor-agent-activity",
      "editor-agent-activity-reads",
      "editor-agent-activity-changes",
      "editor-agent-activity-fileNames",
    ]) {
      expect(screen.getByTestId(testId)).toBeTruthy();
    }
  });

  it("defaults to automatic reveal and saves the opt-out", async () => {
    render(() => <EditorSettingsPanel />);
    const toggle = screen.getByTestId("editor-reveal-in-tree") as HTMLInputElement;
    await waitFor(() => expect(toggle.disabled).toBe(false));
    expect(toggle.checked).toBe(true);
    toggle.click();
    await waitFor(() => expect(patchAppState).toHaveBeenCalledWith(
      expect.objectContaining({ editor: expect.objectContaining({ revealInTree: false }) }), expect.anything(),
    ));
    expect(toggle.checked).toBe(false);
  });

  it("picks tick kinds and dims them while the master switch is off", async () => {
    render(() => <EditorSettingsPanel />);
    await waitFor(() =>
      expect(
        (screen.getByTestId("editor-font-size-inc") as HTMLButtonElement).disabled,
      ).toBe(false),
    );
    screen.getByTestId("editor-scrollbar-ticks-matches").click();
    const { editorScrollbarTickKindsPref, editorScrollbarTicksPref } =
      await import("../../../settings/editor/editor-prefs.ts");
    expect(editorScrollbarTickKindsPref().matches).toBe(false);
    const windows = screen.getByTestId("editor-scrollbar-ticks-windows") as HTMLInputElement;
    expect(windows.checked).toBe(true);
    windows.click();
    expect(editorScrollbarTickKindsPref().windows).toBe(false);

    screen.getByTestId("editor-scrollbar-ticks").click();
    expect(editorScrollbarTicksPref()).toBe(false);
    const group = () =>
      screen.getByTestId("editor-scrollbar-ticks-symbols").closest("fieldset");
    await waitFor(() => expect(group()?.disabled).toBe(true));
    expect(group()?.getAttribute("data-inset")).toBe("true");
    expect(editorScrollbarTickKindsPref().matches).toBe(false);
  });

  it("saves agent activity kinds and keeps them while the master switch is off", async () => {
    render(() => <EditorSettingsPanel />);
    const master = screen.getByTestId("editor-agent-activity") as HTMLInputElement;
    await waitFor(() => expect(master.disabled).toBe(false));
    const { editorAgentActivityKindsPref, editorShownAgentActivity } =
      await import("../../../settings/editor/editor-prefs.ts");
    expect(master.checked).toBe(true);
    screen.getByTestId("editor-agent-activity-reads").click();
    await waitFor(() => expect(patchAppState).toHaveBeenCalledWith(
      expect.objectContaining({ editor: expect.objectContaining({ agentActivityKinds: expect.objectContaining({ reads: false }) }) }),
      expect.anything(),
    ));
    master.click();
    expect(editorShownAgentActivity()).toEqual({ reads: false, changes: false, fileNames: false });
    expect(editorAgentActivityKindsPref()).toEqual({ reads: false, changes: true, fileNames: true });
    const group = () => screen.getByTestId("editor-agent-activity-reads").closest("fieldset");
    await waitFor(() => expect(group()?.disabled).toBe(true));
  });

  it("changes the default historical-version comparison", async () => {
    render(() => <EditorSettingsPanel />);
    await waitFor(() =>
      expect(
        (screen.getByTestId("editor-version-comparison-before") as HTMLButtonElement)
          .disabled,
      ).toBe(false),
    );

    screen.getByTestId("editor-version-comparison-before").click();

    const { editorVersionComparisonPref } =
      await import("../../../settings/editor/editor-prefs.ts");
    expect(editorVersionComparisonPref()).toBe("before");
  });

  it("mutates font size and indent prefs live", async () => {
    render(() => <EditorSettingsPanel />);
    await waitFor(() =>
      expect(
        (screen.getByTestId("editor-font-size-inc") as HTMLButtonElement).disabled,
      ).toBe(false),
    );
    expect(screen.getByTestId("editor-font-preview").textContent).toContain("13px");
    screen.getByTestId("editor-font-size-inc").click();
    await waitFor(() =>
      expect(screen.getByTestId("editor-font-preview").textContent).toContain("14px"),
    );
    screen.getByTestId("editor-indent-tabs").click();
    screen.getByTestId("editor-indent-width-2").click();
    screen.getByTestId("editor-indent-guides").click();
    screen.getByTestId("editor-whitespace").click();
    const { editorIndentStylePref, editorIndentWidthPref, editorIndentGuidesPref, editorWhitespacePref } =
      await import("../../../settings/editor/editor-prefs.ts");
    expect(editorIndentStylePref()).toBe("tabs");
    expect(editorIndentWidthPref()).toBe(2);
    expect(editorIndentGuidesPref()).toBe(false);
    expect(editorWhitespacePref()).toBe(true);
  });
});

describe("EditorSettingsPanel — detected editors", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetExternalOpenPrefsForTests();
    resetEditorPrefsForTests();
    vi.mocked(detectEditors).mockResolvedValue([]);
  });

  it("keeps destination settings available without a default-opener preference", async () => {
    render(() => <EditorSettingsPanel />);
    await waitFor(() => expect((screen.getByTestId("editor-external-editor") as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByTestId("editor-open-target")).toBeNull();
  });

  it("renders only editors detected on this machine plus Custom", async () => {
    vi.mocked(detectEditors).mockResolvedValue([
      { id: "cursor", displayName: "Cursor", installPath: "/Applications/Cursor.app" },
    ]);

    render(() => <EditorSettingsPanel />);

    const select = await waitFor(async () => {
      const el = screen.getByTestId("editor-external-editor") as HTMLButtonElement;
      expect(el.disabled).toBe(false);
      return el;
    });

    expect(renderedOptions(select)).toEqual({
      values: ["cursor", "custom"],
      labels: ["Cursor", "Custom"],
    });
  });

  it("lists detected editors in catalog order before Custom", async () => {
    vi.mocked(detectEditors).mockResolvedValue([
      { id: "cursor", displayName: "Cursor", installPath: "cli:cursor" },
      { id: "vscode", displayName: "VS Code", installPath: "cli:code" },
    ]);

    render(() => <EditorSettingsPanel />);

    const select = await waitFor(async () => {
      const el = screen.getByTestId("editor-external-editor") as HTMLButtonElement;
      expect(el.disabled).toBe(false);
      return el;
    });

    expect(renderedOptions(select).values).toEqual(["cursor", "vscode", "custom"]);
  });

  it("surfaces a stale saved preset as not-detected instead of silently dropping it", async () => {
    vi.mocked(detectEditors).mockResolvedValue([]);

    render(() => <EditorSettingsPanel />);

    const select = await waitFor(async () => {
      const el = screen.getByTestId("editor-external-editor") as HTMLButtonElement;
      expect(el.disabled).toBe(false);
      return el;
    });

    expect(renderedOptions(select)).toEqual({
      values: ["cursor", "custom"],
      labels: ["Cursor — not detected", "Custom"],
    });
  });

  it("badges a newly-added editor's stale saved choice with its label", async () => {
    vi.mocked(loadAppState).mockResolvedValueOnce({
      ...EMPTY_APP_STATE_V1,
      externalOpen: { externalEditor: "sublime" },
    });
    vi.mocked(detectEditors).mockResolvedValue([]);

    render(() => <EditorSettingsPanel />);

    const select = await waitFor(async () => {
      const el = screen.getByTestId("editor-external-editor") as HTMLButtonElement;
      expect(el.disabled).toBe(false);
      return el;
    });

    expect(renderedOptions(select)).toEqual({
      values: ["sublime", "custom"],
      labels: ["Sublime Text — not detected", "Custom"],
    });
  });

  it("labels a failed probe as unavailable, never as not-detected", async () => {
    // Failed probes provide no detection result.
    vi.mocked(detectEditors).mockRejectedValue(new Error("tauri command failed"));

    render(() => <EditorSettingsPanel />);

    const select = await waitFor(async () => {
      const el = screen.getByTestId("editor-external-editor") as HTMLButtonElement;
      expect(el.disabled).toBe(false);
      return el;
    });

    expect(renderedOptions(select)).toEqual({
      values: ["cursor", "custom"],
      labels: ["Cursor — detection unavailable", "Custom"],
    });
  });

  it("makes no detection claim in runtimes where the probe cannot run", async () => {
    vi.mocked(detectEditors).mockResolvedValue(null);

    render(() => <EditorSettingsPanel />);

    const select = await waitFor(async () => {
      const el = screen.getByTestId("editor-external-editor") as HTMLButtonElement;
      expect(el.disabled).toBe(false);
      return el;
    });

    expect(renderedOptions(select)).toEqual({
      values: ["cursor", "custom"],
      labels: ["Cursor", "Custom"],
    });
  });
});
