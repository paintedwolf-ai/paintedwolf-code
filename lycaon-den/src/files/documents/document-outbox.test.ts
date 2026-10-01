import { required } from "../../test/at.ts";
import { DocumentResidency, DocumentCapacityError } from "./document-residency.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DocumentStorageError, readReservedRecovery, base64FromBytes, bytesFromBase64, decodeUpdate, documentOutbox, encodeUpdate } from "./document-outbox.ts";

const invoke = vi.hoisted(() => vi.fn());
vi.mock("../../platform/runtime.ts", () => ({ isTauriRuntime: () => true }));
vi.mock("@tauri-apps/api/core", () => ({ invoke }));

describe("native preservation errors", () => {
  beforeEach(() => invoke.mockReset());

  it("does not cross IPC for an empty preservation transaction", async () => {
    await documentOutbox().commit([]);
    await documentOutbox().commit([], []);
    expect(invoke).not.toHaveBeenCalled();
  });

  it("refuses an oversized recovery before requesting its bytes", async () => {
    const residency = new DocumentResidency(4 * 1024 * 1024);
    const reservation = required(await residency.acquire("document", 1024, "foreground"));
    invoke.mockResolvedValueOnce({ documentId: "document", bytes: 8 * 1024 * 1024 });
    await expect(readReservedRecovery(documentOutbox(), "document", reservation)).rejects.toBeInstanceOf(DocumentCapacityError);
    expect(invoke.mock.calls.map(call => call[0])).toEqual(["inspect_document_outbox"]);
    reservation.release();
  });

  it("refuses an unknown checkpoint shape without mutating storage", async () => {
    invoke.mockResolvedValueOnce([{ kind: "checkpoint", documentId: "document", state: "old" }]);
    await expect(documentOutbox().read("document")).rejects.toThrow("unknown format");
    expect(invoke.mock.calls.map(call => call[0])).toEqual(["read_document_outbox"]);
  });

  it("exposes a structured storage failure for automatic opening recovery", async () => {
    invoke.mockRejectedValueOnce("The preserved file is temporarily unavailable");
    await expect(documentOutbox().read("document")).rejects.toBeInstanceOf(DocumentStorageError);
    invoke.mockResolvedValueOnce([]);
    await expect(documentOutbox().read("document")).resolves.toEqual([]);
  });
});

describe("update encoding", () => {
  const sample = (length: number) => Uint8Array.from({ length }, (_, i) => (i * 131 + 7) & 255);

  it("matches standard base64 for every padding remainder", () => {
    for (const length of [0, 1, 2, 3, 4, 5, 6, 7, 100, 1000, 65537]) {
      const bytes = sample(length);
      const reference = Buffer.from(bytes).toString("base64");
      expect(base64FromBytes(bytes)).toBe(reference);
      expect(encodeUpdate(bytes)).toBe(reference);
      expect(Buffer.from(bytesFromBase64(reference))).toEqual(Buffer.from(bytes));
      expect(Buffer.from(decodeUpdate(reference))).toEqual(Buffer.from(bytes));
    }
  });

  it("round-trips high bytes without text decoding", () => {
    const bytes = Uint8Array.from([0, 127, 128, 200, 255, 254, 1]);
    expect(Buffer.from(bytesFromBase64(base64FromBytes(bytes)))).toEqual(Buffer.from(bytes));
  });
});
