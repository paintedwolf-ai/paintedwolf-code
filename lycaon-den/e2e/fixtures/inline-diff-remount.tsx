import { render } from "solid-js/web";
import { createSignal, Show } from "solid-js";
import { SourceReader } from "../../src/components/source/reader/SourceReader.tsx";
import type { SourceReaderAccess } from "../../src/api/source-reader.ts";
import type { SourceComparisonSession } from "../../src/api/source-comparison-session.ts";
import type { SourceReaderRow } from "../../src/api/types.ts";
import "../../src/tokens.generated.css";
import "../../src/tokens-derived.css";
import "../../src/global.css";
import "../../src/file-edit-diff.css";
import "../../src/components/source/reader/source-reader.css";

const rows: SourceReaderRow[] = Array.from({ length: 90 }, (_, index) => ({
  index, end: index + 1, kind: "insert", text: `const changed${index} = 'a wrapped comparison line';\n`,
  before_line: 0, after_line: index + 1, changed: [],
}));
const side = { path: "example.ts", sha256: "fixture", lines: 90, availability: "available" as const };
const summary = { before: side, after: side, rows: rows.length, added: rows.length, removed: 0, change_areas: [], change_area_count: 0 };
let waitForRows: Promise<void> = Promise.resolve();
let release = () => {};
const access = {
  summary: async () => summary,
  presentation: async () => {
    const state = { id: "fixture", state: "ready", intent_revision: 1, projection_revision: 1,
      extent: { rows: rows.length }, comparison: { summary } };
    return {
      state: () => state, ready: async () => state, attach: () => () => {}, subscribe: () => () => {}, close: async () => {},
      frame: async () => { await waitForRows; return { view_id: "fixture", intent_revision: 1, projection_revision: 1,
        span: { start: 0, end: rows.length }, extent: { rows: rows.length }, rows }; },
    } as unknown as SourceComparisonSession;
  },
} as SourceReaderAccess;

function Fixture() {
  const [mounted, setMounted] = createSignal(true);
  const [ready, setReady] = createSignal(false);
  let scroller!: HTMLDivElement;
  let heldHeight = 0;
  return <>
    <button onClick={() => {
      heldHeight = scroller.querySelector<HTMLElement>(".den-file-edit-diff")!.getBoundingClientRect().height;
      waitForRows = new Promise(resolve => { release = resolve; });
      setMounted(false);
    }}>Unmount comparison</button>
    <button onClick={() => setMounted(true)}>Remount comparison</button>
    <button onClick={() => release()}>Release comparison</button>
    <output data-testid="ready">{String(ready())}</output>
    <div ref={scroller} data-testid="scroll" style={{ width: "440px", height: "500px", overflow: "auto", "overflow-anchor": "none" }}>
      <Show when={mounted()} fallback={<div style={{ height: `${heldHeight}px` }} />}>
        <div class="den-file-edit-diff"><SourceReader access={access} path="example.ts" wrap scrollPastEnd={false} onReady={setReady} /></div>
      </Show>
      {[0, 1, 2].map(index => <img data-testid={`image-${index}`} width="400" height="272" alt={`Capture ${index + 1}`}
        src={`data:image/svg+xml,${encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="400" height="272"><rect width="400" height="272" fill="teal"/></svg>')}`} />)}
      <div style={{ height: "800px" }}>Following conversation</div>
    </div>
  </>;
}
render(() => <Fixture />, document.getElementById("fixture")!);
