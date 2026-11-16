import { filesLocationPreviewEnabled } from "./files-captured-preview.ts";
import { Show } from "solid-js";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";

/** Presentation fixture only; never supplies source identity or permission. */
const locations: Record<string, { label: string; detail: string; remote?: boolean }> = {
  ops: {
    label: "Outside project",
    detail: "This Mac · Captured versions outside the project sandbox · Read only",
  },
  "staging-01": {
    label: "Remote",
    detail: "staging-01 · /etc/nginx · Captured versions only · No live connection",
    remote: true,
  },
};

export function FilesLocationPreview(props: { rootLabel: string; tree?: boolean }) {
  const location = () => filesLocationPreviewEnabled()
    ? locations[props.rootLabel] : undefined;
  return <Show when={location()} keyed>{entry =>
    <span
      class="den-status-mark"
      style={props.tree
        ? { position: "absolute", right: "4px", top: "4px", "pointer-events": "none" }
        : { "margin-inline": "6px", "flex-shrink": 0 }}
      data-tip={entry.detail}
      aria-label={entry.detail}
      data-testid="files-location-preview"
    >
      <Show when={entry.remote}><ThemeIcon slot="network" size={11} /></Show>
      {entry.label}
    </span>
  }</Show>;
}
