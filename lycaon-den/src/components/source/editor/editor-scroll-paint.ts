import { recordEditorScrollPosition } from "./editor-scroll-position.ts";
import type { EditorView } from "@codemirror/view";
import { measureSync } from "../../../chat/stream/den-main-thread-perf.ts";
import { scrollportMotionForHost } from "../../../platform/scrolling/scrollport-motion.ts";

/** Records each committed offset and schedules a narrow-lookahead measure for gesture jumps. */
export function bindEditorScrollPaint(view: EditorView): () => void {
  const motion = scrollportMotionForHost(view.dom);
  return motion?.subscribeCommits((source, position) => {
    recordEditorScrollPosition(view, position);
    if (source !== "thumb_drag" && source !== "track_click") return;
    // The gesture's next frame replaces this viewport, so a far step renders a narrow lookahead.
    view.noteScrollJump();
    measureSync("editor.scrollPaint", () => view.requestMeasure(), { source });
  }) ?? (() => {});
}
