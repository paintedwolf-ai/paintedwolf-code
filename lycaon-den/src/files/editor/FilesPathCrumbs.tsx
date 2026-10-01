import {
  For,
  Show,
  type JSX,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
} from "solid-js";
import { observeCrumbClip } from "./files-editor-crumb-overflow.ts";

export type PathCrumbSegment = { name: string; path: string; isDir: boolean };

/** Cumulative segments of a root-relative path; the last one is the file. */
export function pathCrumbSegments(path: string): PathCrumbSegment[] {
  const parts = path.split("/").filter(Boolean);
  const out: PathCrumbSegment[] = [];
  let acc = "";
  for (const [i, name] of parts.entries()) {
    acc = acc ? `${acc}/${name}` : name;
    out.push({ name, path: acc, isDir: i < parts.length - 1 });
  }
  return out;
}

type Props = {
  rootId: string;
  rootLabel: string;
  path: string;
  testId?: string;
  onRevealSegment: (rootId: string, path: string, isDir: boolean) => void;
  /** Replaces the default file segment. */
  fileSegment?: (seg: PathCrumbSegment) => JSX.Element;
  /** Rendered after the path, inside the clip. */
  children?: JSX.Element;
};

/** Toolbar breadcrumb: segments never wrap or truncate; the trailing clip fades. */
export function FilesPathCrumbs(props: Props) {
  const [clipped, setClipped] = createSignal(false);
  let crumbEl: HTMLElement | undefined;
  let sentinelEl: HTMLElement | undefined;
  const segments = createMemo(() => pathCrumbSegments(props.path));

  onMount(() => {
    if (!crumbEl || !sentinelEl) return;
    onCleanup(observeCrumbClip(crumbEl, sentinelEl, setClipped));
  });

  const segmentButton = (seg: PathCrumbSegment) => (
    <button
      type="button"
      class="den-files-editor__crumb-seg"
      classList={{ "den-files-editor__crumb-file": !seg.isDir }}
      data-files-ctx="breadcrumb"
      data-root={props.rootId}
      data-path={seg.path}
      data-isdir={seg.isDir ? "true" : "false"}
      onClick={() => props.onRevealSegment(props.rootId, seg.path, seg.isDir)}
    >
      {seg.name}
    </button>
  );

  return (
    <nav
      ref={(el) => {
        crumbEl = el;
      }}
      class="den-files-editor__crumb"
      aria-label="File path"
      data-testid={props.testId}
    >
      <Show when={props.rootLabel}>
        <button
          type="button"
          class="den-files-editor__crumb-seg den-files-editor__root"
          data-files-ctx="breadcrumb"
          data-root={props.rootId}
          data-path="."
          data-isdir="true"
          onClick={() => props.onRevealSegment(props.rootId, ".", true)}
        >
          @{props.rootLabel}
        </button>
      </Show>
      <For each={segments()}>
        {(seg, index) => (
          <>
            <Show when={props.rootLabel || index() > 0 || props.path.startsWith("/")}>
              <span class="den-files-editor__crumb-sep" aria-hidden="true">/</span>
            </Show>
            {!seg.isDir && props.fileSegment
              ? props.fileSegment(seg)
              : segmentButton(seg)}
          </>
        )}
      </For>
      {props.children}
      <span
        ref={(el) => {
          sentinelEl = el;
        }}
        class="den-files-editor__crumb-sentinel"
        aria-hidden="true"
      />
      <span
        class="den-files-editor__crumb-fade"
        classList={{ "den-files-editor__crumb-fade--visible": clipped() }}
        aria-hidden="true"
      />
    </nav>
  );
}
