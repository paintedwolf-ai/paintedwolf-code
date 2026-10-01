import { beforeEach, describe, expect, it, vi } from "vitest";
import { openInMenuItems } from "./open-in-menu-items.ts";
import { resetExternalOpenPrefsForTests } from "../settings/editor/external-open-prefs.ts";
import { resetHostIdentityForTest } from "../platform/connection/host-identity.ts";
import { testHostInfo } from "../platform/connection/host-identity-test.ts";
import { openLocalPath, type LocalPathTarget } from "../platform/navigation/open-local-path.ts";
const invoke = vi.hoisted(() => vi.fn(async () => {}));
const editor = vi.hoisted(() => vi.fn(async () => ({ status: "opened" })));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("../platform/runtime.ts", () => ({ isTauriRuntime: () => true, tauriPlatform: () => "macos" }));
vi.mock("../platform/navigation/external-source.ts", () => ({ openInExternalEditor: editor }));
const target: LocalPathTarget = { absolutePath: "/repo/report.pdf", projectRoots: ["/repo"], entryKind: "file" };
const destinations = (value = target) => openInMenuItems(value)[0]?.submenu ?? [];
beforeEach(() => { vi.clearAllMocks(); resetHostIdentityForTest(testHostInfo()); resetExternalOpenPrefsForTests(); });

describe("shared open destinations", () => {
  it("offers file destinations in one order and limits folders to meaningful destinations", () => {
    expect(destinations().map((item) => item.testId)).toEqual(["open-in-file-manager", "open-in-editor", "open-in-browser", "open-in-default-application"]);
    expect(destinations({ ...target, entryKind: "folder" }).map((item) => item.testId)).toEqual(["open-in-file-manager", "open-in-editor"]);
    expect(destinations({ ...target, absolutePath: "/repo/source.ts" }).map((item) => item.testId)).not.toContain("open-in-browser");
  });
  it("uses editor and browser preferences without changing the default in-app opener", async () => {
    resetExternalOpenPrefsForTests({ externalEditor: "vscode", browser: "firefox" });
    expect(destinations().map((item) => item.label)).toContain("VS Code");
    expect(destinations().map((item) => item.label)).toContain("Firefox");
    await openLocalPath({ ...target, line: 42 }, "editor");
    expect(editor).toHaveBeenCalledWith(expect.objectContaining({ absolutePath: target.absolutePath, line: 42, preset: "vscode" }));
    await openLocalPath(target, "browser");
    expect(invoke).toHaveBeenCalledWith("open_local_path", expect.objectContaining({ destination: "browser", browser: "firefox", absolutePath: target.absolutePath }));
  });
  it("disables unresolved or deleted locations and omits device actions for remote hosts", async () => {
    const unavailable = { ...target, unavailable: "This file is not present on disk." };
    expect(destinations(unavailable).every((item) => item.disabled && !item.onSelect)).toBe(true);
    await expect(openLocalPath(unavailable, "editor")).rejects.toThrow(unavailable.unavailable);
    await expect(openLocalPath({ ...target, absolutePath: "/other/file.pdf" }, "browser")).rejects.toThrow("outside");
    resetHostIdentityForTest(testHostInfo({ capabilities: [] }));
    expect(openInMenuItems(target)).toEqual([]);
    expect(invoke).not.toHaveBeenCalled();
  });
  it("requires a folder-compatible custom command without rewriting file arguments", async () => {
    const folder = { ...target, entryKind: "folder" as const };
    resetExternalOpenPrefsForTests({ externalEditor: "custom", customOpenCommand: "editor {path}:{line}" });
    expect(destinations(folder).find((item) => item.testId === "open-in-editor")?.disabled).toBe(true);
    resetExternalOpenPrefsForTests({ externalEditor: "custom", customOpenCommand: "editor {path}:{line}", customOpenFolderCommand: "editor {path}" });
    await openLocalPath(folder, "editor");
    expect(editor).toHaveBeenCalledWith(expect.objectContaining({ customTemplate: "editor {path}:{line}", customFolderTemplate: "editor {path}" }));
  });

  it("never offers browser opening for an executable or delegates a folder to the default application", async () => {
    await expect(openLocalPath({ ...target, absolutePath: "/repo/app.exe" }, "browser")).rejects.toThrow("kind of item");
    await expect(openLocalPath({ ...target, entryKind: "folder" }, "default-application")).rejects.toThrow("kind of item");
    expect(invoke).not.toHaveBeenCalled();
  });
});
