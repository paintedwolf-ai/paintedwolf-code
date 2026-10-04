import { recordEditorScrollPosition } from "./editor-scroll-position.ts";
import type { EditorView } from "@codemirror/view";
import { measureSync } from "../../../chat/stream/den-main-thread-perf.ts";
import { scrollportMotionForHost } from "../../../platform/scrolling/scrollport-motion.ts";

/** CodeMirror's synchronous measure, which its typings keep internal. */
type SynchronousMeasure = { measure(): void };

/** Records each committed offset and renders gesture jumps at their destination before the frame paints. */
export function bindEditorScrollPaint(view: EditorView): () => void {
  const motion = scrollportMotionForHost(view.dom);
  return motion?.subscribeCommits((source, position) => {
    recordEditorScrollPosition(view, position);
    if (source !== "thumb_drag" && source !== "track_click") return;
    // The gesture's next frame replaces this viewport, so a far step renders a narrow lookahead.
    view.noteScrollJump();
    // A requested measure runs a frame later, after the jump has painted blank lines.
    measureSync("editor.scrollPaint", () => (view as unknown as SynchronousMeasure).measure(), { source });
  }) ?? (() => {});
}
