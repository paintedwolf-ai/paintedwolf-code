import { For, Show, createSignal, onCleanup, onMount } from "solid-js";
import type { BriefingRecordItem } from "../api/types.ts";
import { briefingCopy } from "./briefing-copy.ts";
import { EvidenceText } from "./EvidenceText.tsx";
import { evidenceInputIsProse, evidenceInputParts } from "./evidence-inputs.ts";

const copy = briefingCopy.evidence;

/** One input a summary was given: its name, then the text that was sent. Long text is held until expanded, and can be held again. */
export function EvidenceInput(props: { item: BriefingRecordItem }) {
  const parts = () => evidenceInputParts(props.item);
  const name = () => copy.inputNames[props.item.key] ?? props.item.key;
  const note = () => {
    if (props.item.inclusion === "truncated") return copy.sentInPart;
    return props.item.key === "recent_results" && parts().some((part) => part.head) ? copy.resultCount(parts().length) : "";
  };
  let body: HTMLDivElement | undefined;
  const [overflows, setOverflows] = createSignal(false);
  const [expanded, setExpanded] = createSignal(false);
  let root: HTMLDivElement | undefined;
  // Collapsing a long input keeps it on screen, so the reader is not left below where it ended.
  const toggle = () => {
    const open = !expanded();
    setExpanded(open);
    if (!open) root?.scrollIntoView?.({ block: "nearest" });
  };
  onMount(() => {
    const el = body;
    if (!el || typeof ResizeObserver === "undefined") return;
    // The held height is a style rule, so overflow is read from the element, never computed from text.
    const check = () => {
      if (!expanded() && el.clientHeight > 0) setOverflows(el.scrollHeight > el.clientHeight + 1);
    };
    const observer = new ResizeObserver(check);
    observer.observe(el);
    check();
    onCleanup(() => observer.disconnect());
  });
  return (
    <div ref={(el) => (root = el)} class="den-briefing-evidence__input" data-testid="briefing-evidence-input" data-key={props.item.key} data-inclusion={props.item.inclusion}>
      <p class="den-briefing-evidence__item-head">
        <span class="den-walk-page__name">{name()}</span>
        <Show when={note()}><span class="den-walk-page__note">{note()}</span></Show>
      </p>
      <div
        ref={(el) => (body = el)}
        class="den-briefing-evidence__body"
        data-expanded={expanded() ? "" : undefined}
        data-clamped={overflows() && !expanded() ? "" : undefined}
      >
        <For each={parts()}>
          {(part) => (
            <div class="den-briefing-evidence__item">
              <Show when={part.head} keyed>
                {(head) => (
                  <p class="den-briefing-evidence__item-head den-briefing-evidence__part-head">
                    <span>{head[0]}</span>
                    <Show when={head[1]}><span class="den-walk-page__note">{head[1]}</span></Show>
                  </p>
                )}
              </Show>
              <Show when={evidenceInputIsProse(props.item.key)} fallback={<EvidenceText text={part.text} language={part.language} label={name()} />}>
                <p class="den-briefing-evidence__prose">{part.text}</p>
              </Show>
            </div>
          )}
        </For>
      </div>
      <Show when={overflows()}>
        <button type="button" class="den-chat-content-link den-briefing-evidence__more" data-testid="briefing-evidence-show-all"
          aria-expanded={expanded()} aria-label={`${expanded() ? briefingCopy.showLess : briefingCopy.showAll}: ${name()}`}
          onClick={toggle}>{expanded() ? briefingCopy.showLess : briefingCopy.showAll}</button>
      </Show>
    </div>
  );
}
