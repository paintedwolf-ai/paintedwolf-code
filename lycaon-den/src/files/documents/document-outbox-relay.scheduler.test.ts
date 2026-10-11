// @vitest-environment jsdom
import { expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import { memoryDocumentOutbox } from "../../test/memory-document-outbox.ts";
import { startDocumentOutboxRelay } from "./document-outbox-relay.ts";
const storage = vi.hoisted(() => vi.fn());
vi.mock("./document-outbox.ts", async (original) => ({
  ...await original<typeof import("./document-outbox.ts")>(), documentOutbox: storage,
}));

it.each(["dispose", "replace"] as const)("stops an in-flight relay after window %s", async (boundary) => {
  vi.useFakeTimers();
  const outbox = memoryDocumentOutbox(); storage.mockReturnValue(outbox);
  let finish!: (entries: Awaited<ReturnType<typeof outbox.list>>) => void;
  const list = vi.spyOn(outbox, "list").mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
  const read = vi.spyOn(outbox, "read");
  let client = stubClient(); const stop = startDocumentOutboxRelay(() => client, () => false);
  try {
    await vi.waitFor(() => expect(finish).toBeTypeOf("function"));
    if (boundary === "dispose") stop(); else client = stubClient();
    finish([{ documentId: "pending", projectId: "project", rootId: "root", fileId: "file", path: "a.txt" }]);
    await vi.advanceTimersByTimeAsync(0);
    expect(read).not.toHaveBeenCalled();
    stop(); await vi.advanceTimersByTimeAsync(10_000);
    expect(list).toHaveBeenCalledOnce();
  } finally { stop(); vi.useRealTimers(); }
});
