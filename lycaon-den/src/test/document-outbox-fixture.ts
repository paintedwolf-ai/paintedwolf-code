import { beforeEach, vi } from "vitest";
import type { OutboxRecord } from "../files/documents/document-outbox.ts";

const preserved = vi.hoisted(() => new Map<string, OutboxRecord>());
const retained = vi.hoisted(() => new Map<string, Set<string>>());
vi.mock("../files/documents/document-outbox.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../files/documents/document-outbox.ts")>();
  const { memoryDocumentOutbox } = await import("./memory-document-outbox.ts");
  const outbox = memoryDocumentOutbox(preserved, retained);
  return { ...actual, documentOutbox: () => outbox };
});
beforeEach(() => { preserved.clear(); retained.clear(); });
