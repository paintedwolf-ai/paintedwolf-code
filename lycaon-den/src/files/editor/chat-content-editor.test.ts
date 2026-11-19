// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChatContentReference, ChatContentRow, ChatContentPage } from "../../api/types.ts";
import { createChatContentAccess, clearChatContentCache } from "../../chat/transcript/content/chat-content-reader.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { editorDisplayPrefs } from "../../components/source/editor/editor-display-prefs.ts";
import type { ChatReadingPlace } from "./chat-content-viewport.ts";
import { ChatContentEditor } from "./chat-content-editor.ts";

const row = (index: number): ChatContentRow => ({index,offset:index*8,text:"content\n",spans:[]});
const reference: ChatContentReference = {field:"tool_output",sha256:"fixture",total_runes:2400,size_bytes:2400,rows:300,
  preview_rows:Array.from({length:64},(_,index)=>row(index))};
const cleanup: (()=>void)[] = [];
afterEach(() => { for(const dispose of cleanup.splice(0)) dispose(); clearChatContentCache(); });

function fixture(place?: ChatReadingPlace) {
  let finish: (page:ChatContentPage)=>void = () => {};
  const getRows = vi.fn(() => new Promise<ChatContentPage>(resolve => {finish=resolve;}));
  const client = stubClient({getChatContent:getRows});
  const parent = document.createElement("div");
  document.body.append(parent);
  const error = vi.fn();
  const editor = new ChatContentEditor(parent,createChatContentAccess({client,sessionId:"session",messageId:"message",reference}),editorDisplayPrefs(),error,place);
  cleanup.push(() => {editor.destroy();parent.remove();});
  return {editor,getRows,error,finish:() => finish({reference,rows:Array.from({length:64},(_,index)=>row(index+100)),
    offset:800,end_offset:1312,complete:false,text:"",spans:[]})};
}

describe("chat content navigation", () => {
  it("awaits a shared row request and applies the latest requested column", async () => {
    const f = fixture();
    const first = f.editor.revealRow(100,2);
    const second = f.editor.revealRow(100,5);
    expect(f.getRows).toHaveBeenCalledTimes(1);
    f.finish();
    await Promise.all([first,second]);
    const position = f.editor.reader.view.state.selection.main.head;
    const entry = f.editor.reader.document.at(position);
    expect(entry?.slot.index).toBe(100);
    expect(position-entry!.from).toBe(4);
    expect(f.error).not.toHaveBeenCalled();
  });

  it("does not reveal a search result that was canceled while loading", async () => {
    const f = fixture();
    const reveal = vi.spyOn(f.editor.reader,"reveal");
    const pending = f.editor.revealMatch({offset:800,length:7});
    f.editor.clearMatch();
    f.finish();
    await pending;
    expect(reveal).not.toHaveBeenCalled();
    expect(f.error).not.toHaveBeenCalled();
  });
});

it("resolves the visible rank inside a placeholder to its retained rune offset", async () => {
  const f = fixture();
  Object.defineProperty(f.editor.reader.view.scrollDOM, "clientHeight", { value: 200, configurable: true });
  try {
    vi.spyOn(f.editor.reader, "viewportAnchor").mockReturnValue({ rank: 100, fraction: 0 });
    const pending = f.editor.capturePlace(new AbortController().signal);
    expect(f.getRows.mock.calls[0]?.slice(3, 4)).toEqual([{ row: 100 }]);
    f.finish();
    expect(await pending).toEqual({ offset: 0, left: 0, target: { offset: 800, length: 0 } });
  } finally { Reflect.deleteProperty(f.editor.reader.view.scrollDOM, "clientHeight"); }
});

it("uses the measured character beside a gap whose estimated rank differs", async () => {
  const f = fixture();
  const { view } = f.editor.reader;
  Object.defineProperty(view.scrollDOM, "clientHeight", { value: 200, configurable: true });
  vi.spyOn(view.scrollDOM, "getBoundingClientRect").mockReturnValue(new DOMRect(10, 20, 300, 200));
  vi.spyOn(view, "posAtCoords").mockReturnValue(18);
  vi.spyOn(view, "coordsAtPos").mockReturnValue({ top: 19, left: 30, bottom: 35, right: 31 });
  vi.spyOn(f.editor.reader, "viewportAnchor").mockReturnValue({ rank: 100, fraction: 0 });
  expect(await f.editor.capturePlace(new AbortController().signal)).toEqual({
    offset: -1, left: 20, target: { offset: 18, length: 0 },
  });
  expect(f.getRows).not.toHaveBeenCalled();
});

it("materializes a retained handoff without moving the selection", async () => {
  const f = fixture({ target: { offset: 804, length: 0 }, offset: -1, left: 20 });
  expect(f.getRows.mock.calls[0]?.slice(3, 4)).toEqual([{ locate: 804 }]);
  const { reader } = f.editor;
  const initial = reader.view.state.selection.main.head;
  f.finish();
  await vi.waitFor(() => expect(reader.document.rowAt("", 100)).toBeDefined());
  expect(reader.view.state.selection.main.head).toBe(initial);
  expect(f.error).not.toHaveBeenCalled();
});

it("selects the matched text when the latest navigation finishes", async () => {
  const f = fixture();
  const pending = f.editor.revealMatch({ offset: 804, length: 3 });
  f.finish();
  await pending;
  const { reader } = f.editor;
  const selection = reader.view.state.selection.main;
  expect(reader.document.at(selection.from)?.row?.index).toBe(100);
  expect(reader.view.state.sliceDoc(selection.from, selection.to)).toBe("ent");
});
