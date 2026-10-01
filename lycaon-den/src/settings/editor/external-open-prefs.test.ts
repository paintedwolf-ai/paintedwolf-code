import { beforeEach, describe, expect, it } from "vitest";
import {
  EMPTY_APP_STATE_V1,
  type ExternalEditorPreset,
} from "../../../shared/app-state-types.ts";
import {
  resetAppStateSnapshotForTests,
  setAppStateSnapshot,
} from "../../store/app-state-snapshot.ts";
import {
  DEFAULT_BROWSER,
  DEFAULT_EXTERNAL_EDITOR,
  EDITOR_LABELS,
  normalizeExternalOpenPrefs,
  resetExternalOpenPrefsForTests,
  resolveExternalOpenPrefs,
  saveExternalOpenPrefs,
  syncExternalOpenPrefsFromSnapshot,
  externalOpenPrefs,
} from "./external-open-prefs.ts";

describe("external-open-prefs", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({ ...EMPTY_APP_STATE_V1 });
    resetExternalOpenPrefsForTests();
  });

  it("defaults browser to system-default", () => {
    const got = resolveExternalOpenPrefs(undefined);
    expect(got.browser).toBe(DEFAULT_BROWSER);
    expect(got.browser).toBe("system-default");
    expect(got.externalEditor).toBe(DEFAULT_EXTERNAL_EDITOR);
    expect(got.externalEditor).toBe("cursor");
  });

  it("preserves known overrides and drops unknown enums", () => {
    expect(
      resolveExternalOpenPrefs({
        browser: "chrome",
        externalEditor: "vscode",
      }),
    ).toMatchObject({
      browser: "chrome",
      externalEditor: "vscode",
    });
    // Newly-added macOS editors are accepted; unknown ids still drop.
    expect(
      resolveExternalOpenPrefs({
        externalEditor: "sublime",
      }),
    ).toMatchObject({ externalEditor: "sublime" });
    expect(
      normalizeExternalOpenPrefs({
        browser: "chrome",
      }),
    ).toEqual({ browser: "chrome" });
    expect(
      normalizeExternalOpenPrefs({
        externalEditor: "totally-made-up" as ExternalEditorPreset,
      }),
    ).toBeUndefined();
  });

  it("EDITOR_LABELS has a non-empty label for every preset", () => {
    for (const preset of Object.keys(EDITOR_LABELS) as ExternalEditorPreset[]) {
      const label = EDITOR_LABELS[preset];
      expect(typeof label).toBe("string");
      expect(label.trim().length).toBeGreaterThan(0);
    }
    // Spot-check a few that the labels match the Rust display names.
    expect(EDITOR_LABELS["intellij-idea"]).toBe("IntelliJ IDEA");
    expect(EDITOR_LABELS["vscode-insiders"]).toBe("VS Code Insiders");
    expect(EDITOR_LABELS["custom"]).toBe("Custom");
  });

  it("preserves custom file and folder commands independently", async () => {
    await saveExternalOpenPrefs({ externalEditor: "custom", customOpenCommand: "editor {path}:{line}", customOpenFolderCommand: "editor {path}" });
    syncExternalOpenPrefsFromSnapshot();
    expect(externalOpenPrefs()).toMatchObject({ customOpenCommand: "editor {path}:{line}", customOpenFolderCommand: "editor {path}" });
    await saveExternalOpenPrefs({ browser: "firefox" });
    expect(externalOpenPrefs().customOpenFolderCommand).toBe("editor {path}");
  });

  it("syncs from app-state snapshot and saves patches", async () => {
    setAppStateSnapshot({
      ...EMPTY_APP_STATE_V1,
      externalOpen: { browser: "firefox" },
    });
    syncExternalOpenPrefsFromSnapshot();
    expect(externalOpenPrefs().browser).toBe("firefox");

    await saveExternalOpenPrefs({ browser: "safari" });
    expect(externalOpenPrefs().browser).toBe("safari");
  });
});

