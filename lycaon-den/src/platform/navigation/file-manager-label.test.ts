import { beforeEach, describe, expect, it, vi } from "vitest";

const platform = vi.hoisted(() => ({ name: "linux" as string, tauri: true }));
const invoke = vi.hoisted(() => vi.fn<(command: string) => Promise<unknown>>());
vi.mock("@tauri-apps/api/core", () => ({ invoke }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime: () => platform.tauri, tauriPlatform: () => platform.name }));

const { loadFileManagerName, localPathDestinationLabel } = await import("./open-local-path.ts");

beforeEach(() => {
  platform.name = "linux";
  platform.tauri = true;
  invoke.mockReset();
});

describe("file manager label", () => {
  it("names the desktop's own file manager on Linux once the shell answers", async () => {
    expect(localPathDestinationLabel("file-manager")).toBe("File manager");
    invoke.mockResolvedValueOnce("Dolphin");
    await loadFileManagerName();
    expect(invoke).toHaveBeenCalledWith("file_manager_name");
    expect(localPathDestinationLabel("file-manager")).toBe("Dolphin");
    invoke.mockResolvedValueOnce(null);
    await loadFileManagerName();
    expect(localPathDestinationLabel("file-manager")).toBe("File manager");
  });

  it("keeps the label when the shell cannot answer, and the platform names elsewhere", async () => {
    invoke.mockRejectedValueOnce(new Error("ipc closed"));
    await loadFileManagerName();
    expect(localPathDestinationLabel("file-manager")).toBe("File manager");
    for (const [name, label] of [["macos", "Finder"], ["windows", "File Explorer"]] as const) {
      platform.name = name;
      expect(localPathDestinationLabel("file-manager")).toBe(label);
    }
    invoke.mockClear();
    await loadFileManagerName();
    expect(invoke).not.toHaveBeenCalled();
  });
});
