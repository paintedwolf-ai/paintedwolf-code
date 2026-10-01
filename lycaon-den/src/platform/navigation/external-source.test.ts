import { describe, expect, it, vi } from "vitest";
import { openInExternalEditor } from "./external-source.ts";

describe("external-source", () => {
  it("rejects editor opening outside the desktop app", async () => {
    const got = await openInExternalEditor(
      {
        absolutePath: "/repo/a.ts",
        line: 9,
        projectRoots: ["/repo"],
        preset: "cursor",
      },
      { isTauri: false },
    );
    expect(got).toEqual({ status: "rejected", reason: "Open this path from the desktop app." });
  });

  it("invokes open_in_editor on Tauri", async () => {
    const invokeOpen = vi.fn().mockResolvedValue(undefined);
    const got = await openInExternalEditor(
      {
        absolutePath: "/repo/a.ts",
        line: 2,
        projectRoots: ["/repo"],
        preset: "vscode",
      },
      { isTauri: true, invokeOpen },
    );
    expect(got).toEqual({ status: "opened" });
    expect(invokeOpen).toHaveBeenCalledWith({
      absPath: "/repo/a.ts",
      line: 2,
      preset: "vscode",
      customTemplate: undefined,
      customFolderTemplate: undefined,
      projectRoots: ["/repo"],
    });
  });
});
