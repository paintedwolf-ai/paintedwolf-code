import { For, Show, onCleanup, onMount, type JSX } from "solid-js";
import { AnchoredSurface } from "../../primitives/AnchoredSurface.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { ThemeIcon } from "../../primitives/ThemeIcon.tsx";

export type RemovedLinesPreview = {
  anchor: DOMRect;
  /** 1-based lines in the start of the range the removal held. */
  fromLine: number;
  toLine: number;
  /** One row per removed line, highlighted the way the editor draws them. */
  rows: readonly HTMLElement[];
  /** The editor's theme scope, so highlight classes resolve outside it. */
  themeClasses: string;
  font: JSX.CSSProperties;
};

type Props = {
  preview: RemovedLinesPreview | null;
  /** Reject writes against the saved file, so unsaved typing holds it. */
  canReject: boolean;
  onShowInPlace: () => void;
  onReject: () => void;
  onKeepOpen: () => void;
  /** The pointer left the card. A drag or selection inside it holds it open. */
  onLeave: () => void;
  /** A click elsewhere, Escape, or a scroll closes the card now. */
  onDismiss: () => void;
};

function removedLabel(p: RemovedLinesPreview): { title: string; lines: string } {
  const count = p.toLine - p.fromLine + 1;
  return count === 1
    ? { title: "1 line removed", lines: `was line ${p.fromLine}` }
    : { title: `${count} lines removed`, lines: `was lines ${p.fromLine}–${p.toLine}` };
}

/** Previews a folded removal over the code, so opening it moves nothing. */
export function RemovedLinesPopover(props: Props) {
  let surface: HTMLDivElement | undefined;
  let pressed = false;
  const release = () => {
    pressed = false;
  };
  onMount(() => document.addEventListener("pointerup", release, true));
  onCleanup(() => document.removeEventListener("pointerup", release, true));

  const selecting = (): boolean => {
    if (pressed) return true;
    const selection = document.getSelection();
    return Boolean(
      surface && selection && !selection.isCollapsed && surface.contains(selection.anchorNode),
    );
  };

  return (
    <Show when={props.preview} keyed>
      {(p) => {
        const label = removedLabel(p);
        return (
          <AnchoredSurface
            ref={(element) => {
              surface = element;
            }}
            class="den-menu-surface files-removed-lines"
            role="dialog"
            ariaLabel={label.title}
            testId="files-removed-lines"
            anchor={() => p.anchor}
            preferredSide="bottom"
            align="start"
            gap={4}
            dismissOnScroll
            onDismiss={() => props.onDismiss()}
            onMouseEnter={() => props.onKeepOpen()}
            onMouseDown={() => {
              pressed = true;
            }}
            onMouseLeave={() => {
              if (!selecting()) props.onLeave();
            }}
          >
            <div class="files-removed-lines__head">
              <span class="files-removed-lines__title">{label.title}</span>
              <span class="files-removed-lines__lines">{label.lines}</span>
              <div class="files-removed-lines__actions">
                <DenButton
                  variant="ghost"
                  compact
                  data-testid="files-removed-lines-show"
                  onClick={() => props.onShowInPlace()}
                >
                  <ThemeIcon slot="unfold" size={14} />
                  Show in place
                </DenButton>
                <DenButton
                  variant="ghost"
                  compact
                  disabled={!props.canReject}
                  data-testid="files-removed-lines-reject"
                  data-tip={
                    props.canReject
                      ? "Restore these lines"
                      : "Save first. Reject writes against the file on disk."
                  }
                  onClick={() => props.onReject()}
                >
                  <ThemeIcon slot="rewind" size={14} />
                  Reject
                </DenButton>
              </div>
            </div>
            <div
              class={`files-removed-lines__body ${p.themeClasses}`}
              style={p.font}
            >
              <For each={p.rows}>
                {(row, i) => (
                  <div class="files-removed-lines__row">
                    <span class="files-removed-lines__num">{p.fromLine + i()}</span>
                    {row}
                  </div>
                )}
              </For>
            </div>
          </AnchoredSurface>
        );
      }}
    </Show>
  );
}
