import { expect, it, vi } from "vitest";
import { currentComparisonAccess, loadComparisonSnapshot, sourceReaderAccess } from "./source-reader.ts";
import { stubClient } from "../test/client-fixture.ts";
import { sourceReaderFixture } from "../test/source-reader-fixture.ts";

it("releases abandoned comparison preparation before the host finishes", async () => {
  const host = sourceReaderFixture();
  const prepared = host.prepare("before\n", "after\n");
  const preparing = { ...prepared.view, state: "preparing" as const, comparison: undefined };
  const release = vi.fn(host.methods.releaseSourceView);
  const read = vi.fn(async () => preparing);
  const client = stubClient({ ...host.methods, createSourceView: async () => preparing,
    getSourceView: read, releaseSourceView: release });
  const controller = new AbortController();
  const pending = loadComparisonSnapshot(client, "project", prepared.source, undefined, controller.signal);
  const rejected = expect(pending).rejects.toMatchObject({ name: "AbortError" });
  await vi.waitFor(() => expect(read).toHaveBeenCalled());
  controller.abort();
  await rejected;
  expect(release).toHaveBeenCalledWith("project", preparing.id);
});

it("releases a late acceptance after the file open was abandoned", async () => {
  const host = sourceReaderFixture();
  const prepared = host.prepare("before\n", "after\n");
  let accept!: (view: typeof prepared.view) => void;
  const open = new Promise<typeof prepared.view>(resolve => { accept = resolve; });
  const release = vi.fn(host.methods.releaseSourceView);
  const client = stubClient({ ...host.methods, createSourceView: () => open, releaseSourceView: release });
  const controller = new AbortController();
  const pending = loadComparisonSnapshot(client, "project", prepared.source, undefined, controller.signal);
  const rejected = expect(pending).rejects.toMatchObject({ name: "AbortError" });
  controller.abort();
  await rejected;
  accept(prepared.view);
  await vi.waitFor(() => expect(release).toHaveBeenCalledWith("project", prepared.view.id));
});

it("pins current content separately for each opening", async () => {
  const host = sourceReaderFixture();
  const first = host.prepare("current one\n", "historical\n");
  const second = host.prepare("current two\n", "historical\n");
  const open = vi.fn().mockResolvedValueOnce(first.view).mockResolvedValueOnce(second.view);
  const client = stubClient({ ...host.methods, createSourceView: open });
  const request = { kind: "retained" as const, view_id: "retained", comparison: "current" as const };
  const a = sourceReaderAccess(client, "project", request);
  expect(await a.summary()).toBe(first.view.comparison?.summary);
  const b = sourceReaderAccess(client, "project", request);
  expect(await b.summary()).toBe(second.view.comparison?.summary);
  expect(await a.summary()).toBe(first.view.comparison?.summary);
  expect(open).toHaveBeenCalledTimes(2);
  expect(open.mock.calls[0]?.[1].source).not.toHaveProperty("before");
});

it("shares preparation while inline and fullscreen presentations remain independent", async () => {
  const host = sourceReaderFixture();
  const reader = host.prepare("before\n", "after\n");
  const open = vi.fn(host.createSourceView);
  const client = stubClient({ ...host.methods, createSourceView: open });
  const first = sourceReaderAccess(client, "project", reader);
  const second = sourceReaderAccess(client, "project", reader);
  expect(first).toBe(second);
  const [inline, fullscreen] = await Promise.all([first.presentation(), second.presentation({ mode: "full" })]);
  await Promise.all([inline.ready(), fullscreen.ready()]);
  expect(inline.state()?.id).not.toBe(fullscreen.state()?.id);
  await fullscreen.unfold(0, 1);
  expect(inline.state()?.intent).toEqual({ mode: "changes" });
  expect(fullscreen.state()?.intent).toEqual({ mode: "full", expanded: [{ start: 0, end: 1 }] });
  expect(open).toHaveBeenCalledTimes(2);
  await inline.close(); await fullscreen.close();
});

it("preserves reference session identity when current comparison has no explicit session", async () => {
  const host = sourceReaderFixture();
  const reader = { ...host.prepare("old\n", "retained\n", "file.ts"), session_id: "chat-session-123" };
  const open = vi.fn(host.createSourceView);
  const client = stubClient({ ...host.methods, createSourceView: open });
  const original = sourceReaderAccess(client, "project", reader);
  const current = currentComparisonAccess(original, client, "project", "root-1", "file.ts");
  await current.summary();
  expect(open).toHaveBeenCalled();
  const currentCall = open.mock.calls.find(call => call[1]?.kind === "comparison" && call[1]?.source.kind === "retained" && call[1]?.source.comparison === "current");
  expect(currentCall?.[1]?.session_id).toBe("chat-session-123");
});

