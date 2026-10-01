import { Decoration } from "@codemirror/view";
import { EditorSelection } from "@codemirror/state";
import type { ChatContentRow, SourceReaderRow } from "../../api/types.ts";
import type { ChatContentAccess } from "../../chat/transcript/content/chat-content-reader.ts";
import { redactionMarkModifier, redactionMarkTitle } from "../../chat/transcript/content/redaction-spans.ts";
import { ReaderEditor } from "../../components/source/reader/source-reader-editor.ts";
import { MAIN_SECTION, readerGap, type ReaderSlot } from "../../components/source/reader/source-reader-document.ts";
import type { EditorDisplayPrefs } from "../../components/source/editor/codemirror-theme.ts";
import type { ReaderTextAccess } from "../../components/source/reader/source-reader-selection.ts";

function sourceRow(row: ChatContentRow): SourceReaderRow {
  return { index: row.index, end: row.index+1, kind: "equal", text: row.text, before_line: 0, after_line: row.index+1, changed: [] };
}

/** Only nearby chat rows retain their text. */
export class ChatContentEditor {
  readonly reader: ReaderEditor;
  private readonly rows = new Map<number, ChatContentRow>();
  private readonly abort = new AbortController();
  private readonly pending = new Map<number, Promise<void>>();
  private readonly access: ChatContentAccess;
  private readonly error: (cause: unknown) => void;
  private current = 0;
  private revealGeneration = 0;

  constructor(parent: HTMLElement, access: ChatContentAccess, prefs: EditorDisplayPrefs, error: (cause: unknown) => void) {
    this.access = access; this.error = error;
    for (const row of access.reference.preview_rows ?? []) this.rows.set(row.index, row);
    const textAccess: ReaderTextAccess = {
      selection: async (start, end) => {
        const parts: string[] = [];
        for (let offset = start.row; offset <= end.row;) {
          const rows = await this.read(offset, this.abort.signal);
          if (!rows.length) throw new Error("Content could not be loaded.");
          for (const row of rows) {
            if (row.index > end.row) break;
            parts.push(row.text.slice(row.index === start.row ? start.offset : 0, row.index === end.row ? end.offset : row.text.length));
          }
          const next = (rows.at(-1)?.index ?? offset - 1) + 1;
          if (next <= offset) throw new Error("The selection range did not advance.");
          offset = next;
        }
        return parts.join("");
      },
      content: () => access.text(0, access.reference.total_runes, this.abort.signal),
    };
    this.reader = new ReaderEditor({ parent, surface: "diff-viewer", prefs, access: () => textAccess, fullSide: () => "after", changes: () => false,
      scrollPastEnd: false, commandBridge: true, facts: () => {}, hideFacts: () => {}, error,
      scroll: editor => {
        const top = editor.view.scrollDOM.getBoundingClientRect().top - editor.view.documentTop;
        const block = editor.view.lineBlockAtHeight(top);
        const entry = editor.document.at(block.from);
        if (entry) {
          const fraction = entry.slot.pending ? Math.max(0, Math.min(1, (top-block.top)/Math.max(1,block.height))) : 0;
          this.current = Math.min(access.reference.rows-1, entry.slot.index + Math.floor(fraction*(entry.slot.end-entry.slot.index)));
          access.rememberPosition(this.current);
        }
      },
      load: (_slot, offset) => { void this.load(offset ?? 0); },
      marks: document => document.entries.flatMap(entry => {
        const row = entry.row && this.rows.get(entry.row.index);
        if (!row) return [];
        const offsets = [0]; for (const rune of row.text) offsets.push((offsets.at(-1) ?? 0)+rune.length);
        return row.spans.flatMap(span => {
          const from = offsets[span.start], to = offsets[span.start+span.length];
          return from !== undefined && to !== undefined && to>from ? [Decoration.mark({
            class: ["den-redaction-mark", redactionMarkModifier(span)].filter(Boolean).join(" "),
            attributes: { "data-tip": redactionMarkTitle(span) },
          }).range(entry.from+from, entry.from+to)] : [];
        });
      }),
    });
    this.publish(true);
    if (access.position()>0) void this.revealRow(access.position());
  }

  private async read(index: number, signal: AbortSignal): Promise<ChatContentRow[]> {
    const cached: ChatContentRow[] = [];
    for (let i=index; i<Math.min(index+64,this.access.reference.rows); i++) {
      const row=this.access.peek(i); if (!row) break; cached.push(row);
    }
    return cached.length ? cached : this.access.read(index,signal);
  }

  private publish(reset = false): void {
    const selection=this.reader.view.state.selection;
    const capturePosition = (position: number) => {
      const entry = this.reader.document.at(position);
      return entry?.row ? { index: entry.row.index, offset: position-entry.from } : undefined;
    };
    const positions = selection.ranges.map(range => ({
      anchor: capturePosition(range.anchor), head: capturePosition(range.head),
    }));
    const pinned = this.reader.view.state.selection.ranges.flatMap(range => [
      this.reader.document.at(range.from)?.slot.index, this.reader.document.at(range.to)?.slot.index,
    ]);
    const ordered = [...this.rows.values()].sort((a,b) => Math.abs(a.index-this.current)-Math.abs(b.index-this.current));
    let bytes=0;
    ordered.forEach((row,index) => {
      bytes+=row.text.length*2+128;
      if ((index>=256 || bytes>512*1024) && !pinned.includes(row.index)) this.rows.delete(row.index);
    });
    const slots: ReaderSlot[]=[]; let cursor=0;
    for (const row of [...this.rows.values()].sort((a,b)=>a.index-b.index)) {
      if (row.index>cursor) slots.push(readerGap(cursor,row.index,true,row.index-cursor));
      slots.push(sourceRow(row)); cursor=row.index+1;
    }
    if (cursor<this.access.reference.rows) slots.push(readerGap(cursor,this.access.reference.rows,true,this.access.reference.rows-cursor));
    this.reader.setRows(slots,reset);
    if (!reset) {
      const ranges = positions.flatMap(({ anchor, head }) => {
        if (!anchor || !head) return [];
        const start = this.reader.document.rowAt(MAIN_SECTION, anchor.index);
        const end = this.reader.document.rowAt(MAIN_SECTION, head.index);
        if (!start || !end) throw new Error("Selected content is unavailable.");
        return [EditorSelection.range(
          start.from + Math.min(anchor.offset, start.to-start.from),
          end.from + Math.min(head.offset, end.to-end.from),
        )];
      });
      if (ranges.length === selection.ranges.length) {
        this.reader.view.dispatch({ selection: EditorSelection.create(ranges, selection.mainIndex) });
      }
    }
  }

  private load(index: number): Promise<void> {
    const pending = this.pending.get(index);
    if (pending) return pending;
    if (this.abort.signal.aborted) return Promise.resolve();
    const request = (async () => {
      try {
        const rows = await this.read(index, this.abort.signal);
        if (this.abort.signal.aborted) return;
        if (!rows.length) throw new Error("Content could not be loaded.");
        this.current = index;
        for (const row of rows) this.rows.set(row.index, row);
        this.publish();
      } catch (cause) {
        if (!this.abort.signal.aborted) this.error(cause);
      } finally {
        this.pending.delete(index);
      }
    })();
    this.pending.set(index, request);
    return request;
  }

  async revealRow(index: number, column = 1): Promise<void> {
    const generation=++this.revealGeneration;
    await this.load(index);
    if (!this.abort.signal.aborted && generation===this.revealGeneration) this.reader.reveal(MAIN_SECTION,index,{row:index,from:column-1,to:column-1});
  }

  async reveal(offset: number, length = 0): Promise<void> {
    const generation=++this.revealGeneration;
    try {
      const rows=await this.access.locate(offset,this.abort.signal);
      if (this.abort.signal.aborted || generation!==this.revealGeneration || !rows[0]) return;
      this.current=rows[0].index;
      for (const row of rows) this.rows.set(row.index,row);
      this.publish();
      const row=rows[0], runes=Array.from(row.text);
      const from=runes.slice(0,offset-row.offset).join("").length;
      const to=from+runes.slice(offset-row.offset,offset-row.offset+length).join("").length;
      this.reader.reveal(MAIN_SECTION,row.index,{ row:row.index,from,to });
    } catch (cause) { if (!this.abort.signal.aborted && generation===this.revealGeneration) this.error(cause); }
  }

  clearMatch(): void { this.revealGeneration++;this.reader.clearMatch(); }
  destroy(): void { this.abort.abort(); this.reader.destroy(); }
}
