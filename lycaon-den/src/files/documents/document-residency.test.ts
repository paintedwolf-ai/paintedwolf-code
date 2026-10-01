import { required } from "../../test/at.ts";
import { describe, expect, it, vi } from "vitest";
import { DOCUMENT_MEMORY_BUDGET, DOCUMENT_WINDOW_FLOOR, DocumentCapacityError, DocumentResidency, EDITABLE_SOURCE_MAX_BYTES, documentAdmissionBytes, documentResourceKey, windowDocumentBudget, type DocumentReservation } from "./document-residency.ts";

function admitted(value: DocumentReservation | null): DocumentReservation {
  return required(value);
}

const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};

describe("document residency", () => {
  it("bounds hundreds of admitted documents by the retained working set", async () => {
    const residency = new DocumentResidency(100);
    let resident = 0;
    let peak = 0;
    for (let index = 0; index < 500; index++) {
      const reservation = await residency.acquire(String(index), 25, "foreground");
      resident++;
      peak = Math.max(peak, resident);
      admitted(reservation).retain(25, async () => { resident--; return true; });
      admitted(reservation).release();
    }
    expect(peak).toBe(4);
    expect(residency.snapshot.bytes).toBe(100);
    expect(residency.snapshot.documents).toBe(4);
  });

  it("keeps a renamed read-only file in the same protected admission", async () => {
    const residency = new DocumentResidency(100);
    const address = { rootId: "root", path: "old.txt", fileId: "file" };
    const first = admitted(await residency.acquire(documentResourceKey("project", address), 60, "foreground"));
    const release = vi.fn(async () => true);
    first.retain(60, release);
    first.release();
    address.path = "renamed.txt";
    residency.protect(documentResourceKey("project", address), true);
    const reopened = admitted(await residency.acquire(documentResourceKey("project", address), 60, "foreground"));
    expect(residency.snapshot.documents).toBe(1);
    expect(residency.snapshot.bytes).toBe(60);
    reopened.release();
    await expect(residency.acquire("another", 60, "foreground")).rejects.toBeInstanceOf(DocumentCapacityError);
    expect(release).not.toHaveBeenCalled();
  });

  it("charges a document until asynchronous release finishes", async () => {
    const residency = new DocumentResidency(10);
    const release = deferred();
    const started = deferred();
    const first = await residency.acquire("first", 10, "foreground");
    admitted(first).retain(10, async () => { started.resolve(); await release.promise; return true; });
    admitted(first).release();
    const onAdmission = vi.fn();
    const second = residency.acquire("second", 10, "foreground").then(reservation => { onAdmission(); return reservation; });
    await started.promise;
    expect(residency.snapshot).toMatchObject({ bytes: 10, releasing: 1 });
    expect(onAdmission).not.toHaveBeenCalled();
    release.resolve();
    admitted(await second).release();
    expect(onAdmission).toHaveBeenCalledOnce();
  });

  it("preserves a document when its durable commit fails", async () => {
    const residency = new DocumentResidency(10);
    const first = await residency.acquire("first", 10, "foreground");
    admitted(first).retain(10, async () => {
      throw new Error("Storage unavailable");
    });
    admitted(first).release();
    await expect(residency.acquire("second", 10, "foreground")).rejects.toBeInstanceOf(DocumentCapacityError);
    expect(residency.snapshot).toMatchObject({ bytes: 10, documents: 1 });
  });

  it("keeps independent presentations protected until both release", async () => {
    const residency = new DocumentResidency(10);
    const first = await residency.acquire("first", 10, "foreground");
    const release = vi.fn(async () => true);
    admitted(first).retain(10, release);
    admitted(first).release();
    const one = Symbol(), two = Symbol();
    residency.protect("first", true, one);
    residency.protect("first", true, two);
    residency.protect("first", false, one);
    await expect(residency.acquire("second", 10, "foreground")).rejects.toBeInstanceOf(DocumentCapacityError);
    expect(release).not.toHaveBeenCalled();
    residency.protect("first", false, two);
    admitted(await residency.acquire("second", 10, "foreground")).release();
    expect(release).toHaveBeenCalledOnce();
  });

  it("allows two acquisitions with only one speculative acquisition", async () => {
    const residency = new DocumentResidency(100);
    const background = await residency.acquire("one", 10, "background");
    const next = vi.fn();
    const waiting = residency.acquire("two", 10, "background").then(value => { next(); return value; });
    const foreground = await residency.acquire("three", 10, "foreground");
    expect(residency.snapshot.acquiring).toBe(2);
    expect(next).not.toHaveBeenCalled();
    admitted(background).release();
    admitted(await waiting).release();
    admitted(foreground).release();
  });

  it("coalesces provisional reservations after learning a document identity", async () => {
    const residency = new DocumentResidency(100);
    const first = await residency.acquire("path", 40, "foreground");
    const second = await residency.acquire("document", 40, "foreground");
    admitted(first).identify("document");
    admitted(first).retain(30, async () => true);
    admitted(second).retain(30, async () => true);
    admitted(first).release();
    expect(residency.snapshot).toMatchObject({ bytes: 40, documents: 1, acquiring: 1 });
    admitted(second).release();
    expect(residency.snapshot).toMatchObject({ acquiring: 0, bytes: 30 });
  });

  it("does not release a replacement reservation from an earlier controller lifetime", async () => {
    const residency = new DocumentResidency(100);
    const previous = await residency.acquire("same", 40, "foreground");
    residency.reset();
    const current = await residency.acquire("same", 40, "foreground");
    admitted(previous).release();
    expect(residency.snapshot).toMatchObject({ bytes: 40, documents: 1, acquiring: 1 });
    admitted(current).release();
    expect(residency.snapshot.bytes).toBe(0);
  });

  it("reserves recovery expansion before releasing another document", async () => {
    const residency = new DocumentResidency(100);
    const retained = admitted(await residency.acquire("retained", 50, "foreground"));
    const disposed = vi.fn(async () => true);
    retained.retain(50, disposed);
    retained.release();
    const incoming = admitted(await residency.acquire("incoming", 25, "foreground"));
    await incoming.expand(75);
    expect(disposed).toHaveBeenCalledOnce();
    expect(residency.snapshot.bytes).toBe(75);
    await expect(incoming.expand(101)).rejects.toBeInstanceOf(DocumentCapacityError);
    expect(residency.snapshot.bytes).toBe(75);
    incoming.release();
  });

  it("never apportions a window less than one switch between limit-sized documents", () => {
    const limit = documentAdmissionBytes(EDITABLE_SOURCE_MAX_BYTES, Math.ceil((EDITABLE_SOURCE_MAX_BYTES * 4) / 3));
    expect(windowDocumentBudget(1)).toBe(DOCUMENT_MEMORY_BUDGET);
    for (const windows of [2, 4, 8, 32]) {
      expect(windowDocumentBudget(windows)).toBeGreaterThanOrEqual(2 * limit);
      expect(windowDocumentBudget(windows)).toBeGreaterThanOrEqual(DOCUMENT_WINDOW_FLOOR);
    }
    expect(windowDocumentBudget(2)).toBe(Math.max(DOCUMENT_WINDOW_FLOOR, DOCUMENT_MEMORY_BUDGET / 2));
    expect(windowDocumentBudget(1000)).toBe(DOCUMENT_WINDOW_FLOOR);
  });

  it("discards superseded demand before body acquisition", async () => {
    const residency = new DocumentResidency(100);
    await expect(residency.acquire("obsolete", 10, "foreground", () => false)).resolves.toBeNull();
    expect(residency.snapshot.bytes).toBe(0);
  });
});
