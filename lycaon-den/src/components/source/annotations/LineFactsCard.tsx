import { createEffect, createMemo, For, Show } from "solid-js";
import { AnchoredSurface } from "../../primitives/AnchoredSurface.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import type { LineFacts, LineFactRow } from "./line-facts.ts";

export type LineFactsCardState = {
  anchor: HTMLElement;
  line: number;
  path: string;
  facts: LineFacts;
  enter: boolean;
};
type Props = {
  state: LineFactsCardState | null;
  onActivate: (row: LineFactRow, line: number) => void;
  onKeepOpen: () => void;
  onLeave: () => void;
  onDismiss: () => void;
  onReturnFocus: () => void;
};
export function LineFactsCard(props: Props) {
  let surface: HTMLDivElement | undefined;
  const rows = () => [...(surface?.querySelectorAll<HTMLButtonElement>(".files-line-facts__row") ?? [])];
  const entryLine = createMemo(() => props.state?.enter ? props.state.line : null);
  createEffect(() => {
    const line = entryLine();
    if (line == null) return;
    // Keyboard entry also focuses a card that is already positioned.
    queueMicrotask(() => {
      if (entryLine() === line && !surface?.contains(document.activeElement)) rows()[0]?.focus({ preventScroll: true });
    });
  });
  return <ShowLatest when={props.state}>{state => (
    <AnchoredSurface
      ref={element => { surface = element; }}
      class="den-menu-surface files-line-facts"
      role="dialog"
      ariaLabel={`Line ${state().line} · ${state().path}`}
      testId="files-line-facts"
      anchor={() => state().anchor}
      preferredSide="right"
      align="start"
      gap={4}
      dismissOnScroll
      dismissWhenAnchorHidden
      onDismiss={props.onDismiss}
      onEscape={props.onReturnFocus}
      onMouseEnter={props.onKeepOpen}
      onMouseLeave={() => { if (!surface?.contains(document.activeElement)) props.onLeave(); }}
      onPositioned={() => { if (state().enter && !surface?.contains(document.activeElement)) rows()[0]?.focus({ preventScroll: true }); }}
      onKeyDown={event => {
        const items = rows(), at = items.indexOf(document.activeElement as HTMLButtonElement);
        let next = -1;
        if (event.key === "ArrowDown") next = Math.min(items.length - 1, at + 1);
        else if (event.key === "ArrowUp") next = Math.max(0, at - 1);
        else if (event.key === "Home") next = 0;
        else if (event.key === "End") next = items.length - 1;
        else if (event.key === "ArrowLeft" || event.key === "Escape") {
          event.preventDefault(); event.stopPropagation(); props.onReturnFocus(); return;
        }
        if (next < 0) return;
        event.preventDefault(); event.stopPropagation(); items[next]?.focus();
      }}
    >
      <div class="files-line-facts__content" onFocusIn={props.onKeepOpen} onFocusOut={event => {
        if (!(event.relatedTarget instanceof Node) || (!surface?.contains(event.relatedTarget) && !state().anchor.contains(event.relatedTarget))) props.onDismiss();
      }}>
        <div class="files-line-facts__head">
          <span class="files-line-facts__title">Line {state().line}</span>
          <span class="files-line-facts__path" data-tip={state().path} data-tip-when-clipped>{state().path}</span>
        </div>
        <div class="files-line-facts__body">
          <For each={["now", "changed", "findings"] as const}>{section => {
            const sectionRows = () => state().facts[section];
            const label = () => section === "now" ? "Now" : section === "changed" ? "Changed" : sectionRows().length === 1 ? "Finding" : "Findings";
            return <Show when={sectionRows().length}>
            <section aria-label={label()}>
              <div class="files-line-facts__section">{label()}</div>
              <For each={sectionRows().map(row => row.key)}>{key => {
                const initial = sectionRows().find(item => item.key === key);
                if (!initial) return null;
                const row = () => sectionRows().find(item => item.key === key) ?? initial;
                return (
                <button type="button" class="files-line-facts__row" onClick={() => props.onActivate(row(), state().line)}>
                  <span class="files-line-facts__swatch" classList={{ "files-line-facts__swatch--finding": row().target.kind === "finding" }}
                    style={{ "--line-fact-color": row().color ?? "color-mix(in srgb, var(--den-text-muted) 55%, var(--den-background))" }} aria-hidden="true" />
                  <span class="files-line-facts__main">
                    <span class="files-line-facts__who" data-tip={row().title} data-tip-when-clipped>{row().title}</span>
                    <span class="files-line-facts__fact" classList={{ "files-line-facts__fact--waiting": row().waiting }}>
                      {row().fact}<Show when={row().tool}> · <span class="files-line-facts__tool">{row().tool}</span></Show>
                    </span>
                  </span>
                  <span class="files-line-facts__go" classList={{ "files-line-facts__go--waiting": row().waiting }}>
                    {row().target.kind === "finding" ? "Open finding" : row().target.kind === "worker" ? "Show worker" : row().waiting ? "Review" : "Open in chat"}
                    <span aria-hidden="true">›</span>
                  </span>
                </button>
              ); }}</For>
            </section>
            </Show>;
          }}</For>
        </div>
      </div>
    </AnchoredSurface>
  )}</ShowLatest>;
}
