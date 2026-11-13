import { readFileSync } from "node:fs";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";
import { parse } from "yaml";
import { LycaonApiError } from "../../../api/http.ts";
import type { CommandReceiptDocumentPage, CommandReceiptSearchPage } from "../../../api/types.ts";
import { PRETTY_MAX_INPUT } from "../../../components/source/pretty/pretty-layout.ts";
import { stubClient } from "../../../test/client-fixture.ts";
import { clearChatContentCache } from "./chat-content-reader.ts";
import {
  COMMAND_RECEIPT_COPY_MAX,
  COMMAND_RECEIPT_SEARCH_LIMIT,
  COMMAND_RECEIPT_STREAM_ROWS,
  COMMAND_RECEIPT_VIEWPORT_ROWS,
  CommandReceiptLimitError,
  openCommandReceiptAccess,
} from "./command-receipt-access.ts";

afterEach(clearChatContentCache);

type Window = { offset: number; limit: number; revision?: string };
const signal = () => new AbortController().signal;
const changed = () => new LycaonApiError("This receipt changed.", 409, "chat_content_changed");

/** A host document per segment: rows named by segment, revision and row, optionally padded. */
function host(totals: Record<string, number>, options: { settled?: boolean; pad?: number } = {}) {
  const revisions: Record<string, string> = {};
  const revisionOf = (scopes: readonly string[]) => revisions[scopes[0]!] ?? "r1".padEnd(64, "0");
  const text = (scopes: readonly string[], index: number) => `{"segment":"${scopes[0]}","revision":"${revisionOf(scopes).slice(0, 2)}","row":${index}${options.pad ? `,"pad":"${"x".repeat(options.pad)}"` : ""}}`;
  const get = vi.fn(async (_session: string, scopes: readonly string[], window: Window): Promise<CommandReceiptDocumentPage> => {
    const revision = revisionOf(scopes);
    if (window.revision && window.revision !== revision) throw changed();
    const total = totals[scopes[0]!]!;
    const end = Math.min(total, window.offset + window.limit);
    return { revision, total_rows: total, settled: options.settled ?? true, offset: window.offset, end_offset: end, complete: end === total,
      rows: Array.from({ length: end - window.offset }, (_, at) => ({ index: window.offset + at, text: text(scopes, window.offset + at) })) };
  });
  return { get, revisions, text };
}

async function open(get: ReturnType<typeof host>["get"], scopes = [{ programId: "p1" }], search?: (...args: never[]) => Promise<CommandReceiptSearchPage>) {
  return openCommandReceiptAccess({ client: stubClient({ getCommandReceiptDocument: get, ...(search ? { searchCommandReceiptDocument: search } : {}) }), sessionId: "s", scopes, signal: signal() });
}

describe("command receipt access", () => {
  it("opens with one viewport request and reads further rows on demand", async () => {
    const { get } = host({ p1: 65537 });
    const access = await open(get);
    expect(get).toHaveBeenCalledTimes(1);
    expect(get.mock.calls[0]?.slice(1, 3)).toEqual([["p1"], { offset: 0, limit: COMMAND_RECEIPT_VIEWPORT_ROWS }]);
    expect(access.rows()).toBe(65537);
    expect(access.previewRows()).toHaveLength(COMMAND_RECEIPT_VIEWPORT_ROWS);
    expect(access.peek(0)?.text.endsWith("\n")).toBe(true);
    const rows = await access.read(500, signal());
    expect(rows.map(row => row.index)).toEqual(Array.from({ length: COMMAND_RECEIPT_VIEWPORT_ROWS }, (_, at) => 500 + at));
    expect(get.mock.calls[1]?.[2]).toEqual({ offset: 500, limit: COMMAND_RECEIPT_VIEWPORT_ROWS, revision: "r1".padEnd(64, "0") });
  });

  it("reads more than one request's scopes as consecutive segments", async () => {
    const scopes = Array.from({ length: 130 }, (_, at) => ({ programId: `p${at}`, ...(at % 2 ? { activityId: `a${at}` } : {}) }));
    const totals = { p0: 3, p64: 2, p128: 4 };
    const { get } = host(totals);
    const access = await open(get, scopes);
    expect(get.mock.calls.map(call => [call[1].length, call[1][0]])).toEqual([[64, "p0"], [64, "p64"], [2, "p128"]]);
    expect(get.mock.calls[0]?.[1][1]).toBe("p1:a1");
    expect(access.rows()).toBe(9);
    expect(access.previewRows().map(row => [row.index, JSON.parse(row.text).segment])).toEqual([
      [0, "p0"], [1, "p0"], [2, "p0"], [3, "p64"], [4, "p64"], [5, "p128"], [6, "p128"], [7, "p128"], [8, "p128"],
    ]);
  });

  it("heals a read of a changed receipt with one unpinned read", async () => {
    const { get, revisions } = host({ p1: 200 }, { settled: false });
    const access = await open(get);
    const before = access.revision();
    revisions.p1 = "r2".padEnd(64, "0");
    const rows = await access.read(100, signal());
    expect(get).toHaveBeenCalledTimes(3);
    expect(get.mock.calls[2]?.[2]).toEqual({ offset: 100, limit: COMMAND_RECEIPT_VIEWPORT_ROWS });
    expect(access.revision()).not.toBe(before);
    expect(JSON.parse(rows[0]!.text).revision).toBe("r2");
    expect(access.peek(0)).toBeUndefined();
    expect(access.settled()).toBe(false);
  });

  it("copies the whole receipt in stream pages and refuses one over the cap", async () => {
    const { get, text } = host({ p1: 1200 });
    const access = await open(get);
    const copied = await access.copyAll(signal());
    expect(copied).toBe(Array.from({ length: 1200 }, (_, at) => `${text(["p1"], at)}\n`).join(""));
    expect(get.mock.calls.slice(1).map(call => call[2].offset)).toEqual([0, 500, 1000]);
    expect(get.mock.calls.slice(1).every(call => call[2].limit === COMMAND_RECEIPT_STREAM_ROWS)).toBe(true);

    const large = host({ p1: 3000 }, { pad: Math.ceil(COMMAND_RECEIPT_COPY_MAX / 2000) });
    const big = await open(large.get, [{ programId: "p1" }]);
    await expect(big.copyAll(signal())).rejects.toBeInstanceOf(CommandReceiptLimitError);
  });

  it("stops reading for Pretty past its input limit and remembers the outcome", async () => {
    const { get } = host({ p1: 3000 }, { pad: Math.ceil(PRETTY_MAX_INPUT / 1000) });
    const access = await open(get);
    const first = await access.prettySnapshot(signal());
    expect(first && "limit" in first && first.limit > PRETTY_MAX_INPUT).toBe(true);
    const requests = get.mock.calls.length;
    expect(requests).toBeLessThan(1 + 3000 / COMMAND_RECEIPT_STREAM_ROWS);
    expect(await access.prettySnapshot(signal())).toEqual(first);
    expect(get).toHaveBeenCalledTimes(requests);

    const small = host({ p1: 3 });
    const smallAccess = await open(small.get);
    const snapshot = await smallAccess.prettySnapshot(signal());
    expect(snapshot && "text" in snapshot && snapshot.text.split("\n")).toHaveLength(4);
  });

  it("restarts a whole-document read once when the receipt changes, then stops", async () => {
    const { get, revisions } = host({ p1: 10 }, { settled: false });
    const access = await open(get);
    revisions.p1 = "r2".padEnd(64, "0");
    const copied = await access.copyAll(signal());
    expect(copied).toContain('"revision":"r2"');

    let serial = 3;
    get.mockImplementation(async (_session, _scopes, window) => {
      if (window.revision) throw changed();
      const revision = `r${serial++}`.padEnd(64, "0");
      return { revision, total_rows: 10, settled: false, offset: 0, end_offset: 10, complete: true, rows: [] };
    });
    const calls = get.mock.calls.length;
    await expect(access.copyAll(signal())).rejects.toBeInstanceOf(CommandReceiptLimitError);
    // A pinned read, a refresh, and the pinned read that changed again.
    expect(get.mock.calls.length - calls).toBe(3);
  });

  it("searches segments in order and places matches by document row", async () => {
    const scopes = Array.from({ length: 65 }, (_, at) => ({ programId: `p${at}` }));
    const { get } = host({ p0: 5, p64: 3 });
    const search = vi.fn(async (_session: string, segment: readonly string[], options: { cursor?: string; limit: number }): Promise<CommandReceiptSearchPage> => {
      expect(options.limit).toBe(COMMAND_RECEIPT_SEARCH_LIMIT);
      if (segment[0] === "p0") return options.cursor ? { matches: [{ row: 4, column: 1, length: 2 }] } : { matches: [{ row: 1, column: 3, length: 2 }], next_cursor: "host-page-2" };
      return { matches: [{ row: 2, column: 0, length: 2 }] };
    });
    const access = await open(get, scopes, search as never);
    const pages = [];
    for (let cursor: string | undefined = undefined; ;) {
      const page = await access.find("ab", cursor, false, signal());
      pages.push(page.matches);
      if (!page.next_cursor) break;
      cursor = page.next_cursor;
    }
    expect(pages.flat()).toEqual([{ row: 1, column: 3, length: 2 }, { row: 4, column: 1, length: 2 }, { row: 7, column: 0, length: 2 }]);
    expect(search.mock.calls.map(call => call[2].cursor)).toEqual([undefined, "host-page-2", undefined]);
    const target = await access.revealTarget({ row: 7, column: 0, length: 2 }, signal());
    expect(target).toMatchObject({ row: 7, from: 0, to: 2 });
  });

  it("requests limits the host accepts", () => {
    const spec = parse(readFileSync(join(import.meta.dirname, "../../../../../docs/openapi/paths/sessions/activity.yaml"), "utf8"));
    const limit = (path: string) => spec.paths[path].get.parameters.find((parameter: { name?: string }) => parameter.name === "limit").schema;
    const within = (value: number, schema: { minimum: number; maximum: number }) => value >= schema.minimum && value <= schema.maximum;
    const read = limit("/v1/sessions/{id}/program-receipt-document");
    const find = limit("/v1/sessions/{id}/program-receipt-document/search");
    expect(within(COMMAND_RECEIPT_VIEWPORT_ROWS, read) && within(COMMAND_RECEIPT_STREAM_ROWS, read) && within(COMMAND_RECEIPT_SEARCH_LIMIT, find)).toBe(true);
  });
});
