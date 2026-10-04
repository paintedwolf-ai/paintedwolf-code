import { For, Show, createMemo, createSignal, createUniqueId, onCleanup, onMount } from "solid-js";
import { createAnchoredPopoverFocus } from "../../platform/interaction/modal-focus-trap.ts";
import { createRovingFocus } from "../../platform/interaction/roving-focus.ts";
import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { DenInput } from "../../components/primitives/DenInput.tsx";
import { Scrollport } from "../../components/primitives/Scrollport.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { searchWalkNavigation, walkNavigation } from "./walk-navigation.ts";
import type { Walk } from "./walk-model.ts";

type Props = { walk: Walk; at: number; onSelect: (index: number) => void };

function WalkNavigator(props: Props & { onClose: () => void; availableHeight: number }) {
  const navigation = createMemo(() => walkNavigation(props.walk));
  const [query, setQuery] = createSignal("");
  const searching = () => query().trim().length > 0;
  const entries = createMemo(() => searchWalkNavigation(navigation().entries, query()));
  const turns = createMemo(() => {
    const matching = new Set(entries());
    return navigation().chapters.map((chapter) => ({
      ...chapter,
      entries: navigation().entries.slice(chapter.start, chapter.end + 1).filter((entry) => matching.has(entry)),
    })).filter((chapter) => chapter.entries.length > 0);
  });
  const select = (index: number) => { props.onSelect(index); props.onClose(); };
  let list: HTMLDivElement | undefined;
  createRovingFocus(() => list, { items: "button[data-walk-destination]" });
  onMount(() => {
    const frame = requestAnimationFrame(() => {
      const current = list?.querySelector<HTMLElement>('[aria-current="step"]');
      if (list && current) list.scrollTop += current.getBoundingClientRect().top - list.getBoundingClientRect().top - list.clientHeight / 2 + current.clientHeight / 2;
    });
    onCleanup(() => cancelAnimationFrame(frame));
  });
  return <div class="den-walk-navigator" style={{ "max-height": `min(28rem, ${Math.max(0, props.availableHeight - 14)}px)` }}>
    <DenInput class="den-walk-navigator__search" type="search" aria-label="Find in walk" placeholder="Find in history…" value={query()}
      onInput={(event) => { setQuery(event.currentTarget.value); if (list) list.scrollTop = 0; }}
      onKeyDown={(event) => {
        if (event.key === "ArrowDown") { event.preventDefault(); list?.querySelector<HTMLButtonElement>("button")?.focus(); }
      }} />
    <Show when={searching()}><span class="shrink-0 text-den-text-muted text-den-caption" role="status">{entries().length} matching changes</span></Show>
    <Scrollport class="den-walk-navigator__list" contentClass="den-walk-navigator__rows" viewportRef={(el) => { list = el; }}
      viewport={{ role: "toolbar", "aria-label": "Changes by turn", "aria-orientation": "vertical" }}>
      <For each={turns()}>{(turn) => <>
        <div class="den-walk-navigator__turn">
          <div class="den-walk-navigator__row-head"><strong>{turn.title}</strong><span>{turn.entries.length} {turn.entries.length === 1 ? "change" : "changes"}</span></div>
          <div class="den-walk-navigator__description" data-tip={turn.prompt} data-tip-when-clipped>{turn.prompt}</div>
        </div>
        <For each={turn.entries}>{(entry) =>
          <button type="button" class="den-walk-navigator__row" data-walk-destination aria-current={entry.index === props.at ? "step" : undefined}
            onClick={() => select(entry.index)}>
            <span class="den-walk-navigator__row-head"><strong class="den-walk-navigator__path" data-file-change={entry.change} data-tip={entry.title} data-tip-when-clipped>{entry.title}</strong><span>{entry.index === props.at ? "Current step" : `Step ${entry.index + 1}`}</span></span>
            <span class="den-walk-navigator__description" data-tip={[entry.operation, entry.detail].filter(Boolean).join(" · ")} data-tip-when-clipped>{entry.operation}<Show when={entry.detail}>{" · "}{entry.detail}</Show></span>
          </button>
        }</For>
      </>}</For>
      <Show when={entries().length === 0}><span role="status" class="text-den-text-muted">No matching changes. Try a file name or words from a prompt.</span></Show>
    </Scrollport>
  </div>;
}

/** Labels whose widest bounds every position: hinted digit advances differ even with tabular figures. */
function countSizers(total: number): string[] {
  const digits = String(total).length;
  return Array.from({ length: 10 }, (_, digit) => `${String(digit).repeat(digits)} of ${total}`);
}

export function WalkPosition(props: Props) {
  const [open, setOpen] = createSignal(false);
  const [surface, setSurface] = createSignal<HTMLDivElement>();
  const [availableHeight, setAvailableHeight] = createSignal(window.innerHeight - 24);
  const id = createUniqueId();
  let trigger: HTMLButtonElement | undefined;
  createAnchoredPopoverFocus(open, surface, {
    trigger: () => trigger,
    initialFocus: () => surface()?.querySelector<HTMLInputElement>("input"),
    onEscape: () => setOpen(false),
  });
  return <>
    <button type="button" class="den-step-bar__read" data-testid="step-bar-read"
      ref={trigger} aria-haspopup="dialog" aria-expanded={open()} aria-controls={open() ? id : undefined}
      aria-label={`Step ${props.at + 1} of ${props.walk.steps.length}. Browse walk history`}
      onClick={() => setOpen(!open())}>
      <b class="den-step-bar__count">
        <For each={countSizers(props.walk.steps.length)}>{(sizer) =>
          <span class="den-step-bar__count-size" aria-hidden="true" data-sizer={sizer} />
        }</For>
        <span class="den-step-bar__count-value" aria-live="polite" aria-atomic="true">{props.at + 1} of {props.walk.steps.length}</span>
      </b>
      <ThemeIcon slot="chevron-down" size={12} />
    </button>
    <Show when={open()}>
      <AnchoredSurface id={id} ref={setSurface} anchor={() => trigger} role="dialog" ariaLabel="Jump in walk"
        class="den-status-popover den-walk-picker" preferredSide="top" align="start" dismissOnEscape={false} overflow="hidden"
        onPositioned={(_element, placement) => {
          const anchor = trigger?.getBoundingClientRect();
          if (!anchor) return;
          setAvailableHeight(Math.max(0, placement.side === "top" ? anchor.top - 16 : window.innerHeight - anchor.bottom - 16));
        }}
        onDismiss={() => setOpen(false)}>
        <WalkNavigator availableHeight={availableHeight()} walk={props.walk} at={props.at} onSelect={props.onSelect} onClose={() => setOpen(false)} />
      </AnchoredSurface>
    </Show>
  </>;
}
