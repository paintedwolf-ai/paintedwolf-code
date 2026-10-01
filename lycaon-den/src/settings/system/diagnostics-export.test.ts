// @vitest-environment jsdom
import { describe, expect, it, vi, beforeEach } from "vitest";
import {
  diagnosticsBundleFilename,
  saveDiagnosticsBundle,
  saveStartupDiagnosticsBundle,
} from "./diagnostics-export.ts";
import type { BackendConnection } from "../../platform/connection/backend.ts";

const downloadExport = vi.fn(async (): Promise<
  | { kind: "saved"; path: string }
  | { kind: "cancelled" }
> => ({
  kind: "saved",
  path: "/tmp/painted-wolf-code-diagnostics.zip",
}));
const lycaonFetch = vi.fn();
const invoke = vi.fn();

vi.mock("../../platform/files/save-file.ts", () => ({
  downloadExport: (...args: unknown[]) => downloadExport(...(args as [])),
}));
vi.mock("../../api/http.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/http.ts")>()),
  lycaonFetch: (...args: unknown[]) => lycaonFetch(...(args as [])),
}));
vi.mock("@tauri-apps/api/core", () => ({
  invoke: (...args: unknown[]) => invoke(...(args as [])),
}));

const connection = {
  baseUrl: "http://127.0.0.1:8787",
  apiToken: "t",
} as BackendConnection;

describe("diagnosticsBundleFilename", () => {
  it("stamps the file so two saves do not collide", () => {
    const name = diagnosticsBundleFilename(new Date("2026-07-25T18:04:05Z"));
    expect(name).toBe("painted-wolf-code-diagnostics-20260725-180405.zip");
  });
});

describe("saveDiagnosticsBundle", () => {
  beforeEach(() => {
    downloadExport.mockClear();
    downloadExport.mockResolvedValue({
      kind: "saved",
      path: "/tmp/painted-wolf-code-diagnostics.zip",
    });
    lycaonFetch.mockReset();
    invoke.mockReset();
  });

  it("fetches the bundle and hands it to the save dialog — never uploads it", async () => {
    const blob = new Blob(["zip"], { type: "application/zip" });
    lycaonFetch.mockResolvedValue({ ok: true, blob: async () => blob });

    const result = await saveDiagnosticsBundle(connection, new Date("2026-07-25T00:00:00Z"));

    expect(result).toEqual({
      kind: "saved",
      path: "/tmp/painted-wolf-code-diagnostics.zip",
    });
    // The only outbound call is to the local sidecar.
    expect(lycaonFetch).toHaveBeenCalledWith(connection, "/v1/diagnostics/export");
    expect(downloadExport).toHaveBeenCalledTimes(1);
  });

  it("surfaces cancel when the save dialog is dismissed", async () => {
    const blob = new Blob(["zip"], { type: "application/zip" });
    lycaonFetch.mockResolvedValue({ ok: true, blob: async () => blob });
    downloadExport.mockResolvedValue({ kind: "cancelled" });

    const result = await saveDiagnosticsBundle(connection);

    expect(result).toEqual({ kind: "cancelled" });
  });

  it("reports a server failure without throwing into the panel", async () => {
    lycaonFetch.mockResolvedValue(new Response(
      JSON.stringify({ code: "internal_error", message: "The diagnostics file could not be built." }),
      { status: 500, headers: { "Content-Type": "application/json" } },
    ));

    const result = await saveDiagnosticsBundle(connection);

    expect(result.kind).toBe("failed");
    expect(downloadExport).not.toHaveBeenCalled();
  });

  it("reports a transport failure without throwing", async () => {
    lycaonFetch.mockImplementation(() => {
      throw new Error("offline");
    });

    const result = await saveDiagnosticsBundle(connection);

    expect(result.kind).toBe("failed");
    if (result.kind === "failed") expect(result.detail).toContain("offline");
  });
});

describe("saveStartupDiagnosticsBundle", () => {
  beforeEach(() => {
    downloadExport.mockClear();
    downloadExport.mockResolvedValue({
      kind: "saved",
      path: "/tmp/painted-wolf-code-diagnostics.zip",
    });
    invoke.mockReset();
  });

  it("uses the native pre-spawn collector and the usual save dialog", async () => {
    invoke.mockResolvedValue([80, 75, 3, 4]);

    const result = await saveStartupDiagnosticsBundle(
      new Date("2026-07-25T00:00:00Z"),
    );

    expect(result).toEqual({
      kind: "saved",
      path: "/tmp/painted-wolf-code-diagnostics.zip",
    });
    expect(invoke).toHaveBeenCalledWith("export_startup_diagnostics");
    expect(downloadExport).toHaveBeenCalledWith(
      expect.any(Blob),
      "painted-wolf-code-diagnostics-20260725-000000.zip",
    );
  });

  it("does not offer an empty native response as a report", async () => {
    invoke.mockResolvedValue([]);

    const result = await saveStartupDiagnosticsBundle();

    expect(result).toEqual({ kind: "failed", detail: "startup report was empty" });
    expect(downloadExport).not.toHaveBeenCalled();
  });

  it("contains a native collector failure without opening the save dialog", async () => {
    invoke.mockRejectedValue(new Error("startup collector failed"));

    const result = await saveStartupDiagnosticsBundle();

    expect(result).toEqual({ kind: "failed", detail: "startup collector failed" });
    expect(downloadExport).not.toHaveBeenCalled();
  });
});
