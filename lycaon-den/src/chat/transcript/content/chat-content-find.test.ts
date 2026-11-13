import { createRoot, createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../../api/http.ts";
import { openFind, resetFindControllerForTests, setFindQuery } from "../../../find/find-controller.ts";
import { getFindRevealHost, listFindRevealHosts } from "../../../find/find-reveal.ts";
import { bindChatContentFind } from "./chat-content-find.ts";
import type { ChatContentAccess, ChatContentFindPage } from "./chat-content-reader.ts";

afterEach(() => { resetFindControllerForTests(); vi.useRealTimers(); });

const expired = () => new LycaonApiError("Expired", 409, "cursor_generation_expired");
const changed = () => new LycaonApiError("Changed", 409, "chat_content_changed");

function bind(find: ChatContentAccess["find"], refresh?: (access: { setRevision: (value: string) => void }) => void) {
  const [revision, setRevision] = createSignal("r1");
  const access = { revision, find, refresh: vi.fn(async () => refresh?.({ setRevision })) } as unknown as ChatContentAccess;
  const onError = vi.fn();
  const host = document.createElement("div");
  const dispose = createRoot(dispose => {
    bindChatContentFind({ access: () => access, host: () => host, collapsed: () => false, expand: () => () => {}, reveal: () => {}, onError });
    return dispose;
  });
  const matches = () => {
    const found = listFindRevealHosts().find(candidate => candidate.hostEl() === host);
    return found ? getFindRevealHost(found.id)?.remoteMatches?.() ?? [] : [];
  };
  return { access, onError, dispose, matches };
}

async function search(query: string) {
  openFind(); setFindQuery(query);
  await vi.advanceTimersByTimeAsync(200);
}

describe("chat content Find", () => {
  it("starts over once when the engine expires its cursor", async () => {
    vi.useFakeTimers();
    let firstRun = true;
    const find = vi.fn(async (_query: string, cursor: string | undefined): Promise<ChatContentFindPage> => {
      if (!cursor) return { matches: [{ offset: firstRun ? 1 : 2, length: 1 }], next_cursor: "page-2" };
      if (firstRun) { firstRun = false; throw expired(); }
      return { matches: [{ offset: 9, length: 1 }] };
    });
    const bound = bind(find);
    await search("x");
    expect(find.mock.calls.map(call => call[1])).toEqual([undefined, "page-2", undefined, "page-2"]);
    expect(bound.matches()).toHaveLength(2);
    bound.dispose();
  });

  it("stops after a second expired cursor", async () => {
    vi.useFakeTimers();
    const find = vi.fn(async (_query: string, cursor: string | undefined): Promise<ChatContentFindPage> => {
      if (cursor) throw expired();
      return { matches: [{ offset: 1, length: 1 }], next_cursor: "page-2" };
    });
    const bound = bind(find);
    await search("x");
    expect(find).toHaveBeenCalledTimes(4);
    expect(bound.matches()).toHaveLength(0);
    bound.dispose();
  });

  it("refreshes once when the content changes under a search, then reports it", async () => {
    vi.useFakeTimers();
    const find = vi.fn(async (): Promise<ChatContentFindPage> => { throw changed(); });
    let revision = 1;
    const bound = bind(find, ({ setRevision }) => setRevision(`r${++revision}`));
    await search("x");
    await vi.advanceTimersByTimeAsync(200);
    expect(bound.access.refresh).toHaveBeenCalledTimes(1);
    expect(find).toHaveBeenCalledTimes(2);
    expect(bound.onError).toHaveBeenCalledTimes(1);
    bound.dispose();
  });
});
