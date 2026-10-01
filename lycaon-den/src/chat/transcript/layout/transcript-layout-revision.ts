import { createSignal } from "solid-js";

// The build replaces this token with the renderer fingerprint.
const [revision, setRevision] = createSignal(
  "__TRANSCRIPT_LAYOUT_REVISION__",
);
export const transcriptLayoutRevision = revision;

if (import.meta.hot) {
  let pending: string | undefined;
  import.meta.hot.on("transcript-layout-revision", (value: string) => { pending = value; });
  // The generation changes after updated styles and components are installed.
  import.meta.hot.on("vite:afterUpdate", () => {
    if (pending === undefined) return;
    setRevision(pending);
    pending = undefined;
  });
}
