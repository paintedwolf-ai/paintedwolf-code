// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";

const invoke = vi.fn();
vi.mock("@tauri-apps/api/core", () => ({ invoke: (...a: unknown[]) => invoke(...a) }));
vi.mock("../runtime.ts", () => ({ isTauriRuntime: vi.fn() }));
vi.mock("./native-path-dialog.ts", () => ({ nativePathDialog: vi.fn() }));

import { nativePathDialog } from "./native-path-dialog.ts";
import { isTauriRuntime } from "../runtime.ts";
import { downloadExport, saveBlobWithDialog } from "./save-file.ts";

const dialog = vi.mocked(nativePathDialog);
const runtime = vi.mocked(isTauriRuntime);

// jsdom's Blob has no arrayBuffer(); the save path only needs those bytes.
function blobOf(bytes: Uint8Array): Blob {
  return {
    arrayBuffer: () => Promise.resolve(bytes.buffer),
  } as unknown as Blob;
}

describe("downloadExport", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    invoke.mockResolvedValue(undefined);
  });

  it("reports native cancellation without saving", async () => {
    runtime.mockReturnValue(true);
    dialog.mockResolvedValue(null);
    await expect(downloadExport(blobOf(new Uint8Array([1])), "backup.zip"))
      .resolves.toEqual({ kind: "cancelled" });
    expect(invoke).not.toHaveBeenCalled();
  });

  it("reports a native save only after the write succeeds", async () => {
    runtime.mockReturnValue(true);
    dialog.mockResolvedValue({ path: "/tmp/backup.zip", grant: "grant" });
    invoke.mockRejectedValueOnce(new Error("disk full"));
    await expect(downloadExport(blobOf(new Uint8Array([1])), "backup.zip"))
      .rejects.toThrow("disk full");
    await expect(downloadExport(blobOf(new Uint8Array([1])), "backup.zip"))
      .resolves.toEqual({ kind: "saved", path: "/tmp/backup.zip" });
  });

  it("appends a temporary anchor for blob downloads", async () => {
    runtime.mockReturnValue(false);
    const appendSpy = vi.spyOn(document.body, "appendChild");
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => undefined);
    const blob = new Blob(["x"], { type: "text/plain" });
    await downloadExport(blob, "out.txt");
    expect(appendSpy).toHaveBeenCalled();
    expect(clickSpy).toHaveBeenCalled();
    appendSpy.mockRestore();
    clickSpy.mockRestore();
  });
});

describe("saveBlobWithDialog", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    runtime.mockReturnValue(true);
    invoke.mockResolvedValue(undefined);
  });

  it("writes through the grant and never sends a path", async () => {
    dialog.mockResolvedValue({ path: "/Users/x/Downloads/export.zip", grant: "handle-abc" });

    const saved = await saveBlobWithDialog(blobOf(new Uint8Array([1, 2, 3])), "export.zip");

    expect(saved).toBe("/Users/x/Downloads/export.zip");
    expect(invoke).toHaveBeenCalledTimes(1);
    const [command, args] = invoke.mock.calls[0] as [string, Record<string, unknown>];
    expect(command).toBe("save_bytes");
    expect(args.grant).toBe("handle-abc");
    // The host resolves the path locally.
    expect(args).not.toHaveProperty("path");
    expect(JSON.stringify(args)).not.toContain("/Users/x/Downloads/export.zip");
  });

  it("returns null on cancel without touching the filesystem", async () => {
    dialog.mockResolvedValue(null);

    await expect(saveBlobWithDialog(blobOf(new Uint8Array([120])), "export.zip")).resolves.toBeNull();
    expect(invoke).not.toHaveBeenCalled();
  });

  it("refuses to write when the pick carried no grant", async () => {
    dialog.mockResolvedValue({ path: "/Users/x/Downloads/export.zip", grant: null });

    await expect(saveBlobWithDialog(blobOf(new Uint8Array([120])), "export.zip")).rejects.toThrow(
      /no write grant/,
    );
    expect(invoke).not.toHaveBeenCalled();
  });
});
