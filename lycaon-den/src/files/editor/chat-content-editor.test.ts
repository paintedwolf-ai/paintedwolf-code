// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChatContentReference, ChatContentRow, ChatContentPage } from "../../api/types.ts";
import { createChatContentAccess, clearChatContentCache } from "../../chat/transcript/content/chat-content-reader.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { editorDisplayPrefs } from "../../components/source/editor/editor-display-prefs.ts";
import { ChatContentEditor } from "./chat-content-editor.ts";

const row = (index: number): ChatContentRow => ({index,offset:index*8,text:"content\n",spans:[]});
const reference: ChatContentReference = {field:"tool_output",sha256:"fixture",total_runes:2400,size_bytes:2400,rows:300,
  preview_rows:Array.from({length:64},(_,index)=>row(index))};
const cleanup: (()=>void)[] = [];
afterEach(() => { for(const dispose of cleanup.splice(0)) dispose(); clearChatContentCache(); });

function fixture() {
  let finish: (page:ChatContentPage)=>void = () => {};
  const getRows = vi.fn(() => new Promise<ChatContentPage>(resolve => {finish=resolve;}));
  const client = stubClient({getChatContent:getRows});
  const parent = document.createElement("div");
  document.body.append(parent);
  const error = vi.fn();
  const editor = new ChatContentEditor(parent,createChatContentAccess({client,sessionId:"session",messageId:"message",reference}),editorDisplayPrefs(),error);
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
    const pending = f.editor.reveal(800,7);
    f.editor.clearMatch();
    f.finish();
    await pending;
    expect(reveal).not.toHaveBeenCalled();
    expect(f.error).not.toHaveBeenCalled();
  });
});
