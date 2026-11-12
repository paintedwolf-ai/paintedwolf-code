import { For, Show, createEffect, createSignal, on, type JSX } from "solid-js";
import type { BriefingEvidenceRef, BriefingSnapshot, BriefingSummarySection } from "../api/types.ts";
import { RandomLetterReveal, markLetterRevealOnceKey } from "../components/primitives/RandomLetterReveal.tsx";
import { announce } from "../platform/interaction/announcer.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { openBriefingEvidence, type BriefingReturnAnchor } from "./briefing-evidence.ts";
import { briefingRevealText, splitBriefingInline } from "./briefing-inline.ts";

/** One section as a surface shows it; no text means it is still being written. */
export type SummarySection = {
  id: string;
  text?: string;
  source?: BriefingSummarySection["source"];
  /** Names the section before its text, as a labelled summary does. */
  label?: string;
};

/**
 * The sections a snapshot shows: its results in order, then a waiting
 * section for each one still being written. A stalled or settled attempt
 * shows no waiting text.
 */
export function snapshotSections(snapshot: BriefingSnapshot | null, stalled = false): SummarySection[] {
  if (!snapshot) return [];
  const done: SummarySection[] = (snapshot.result?.summary ?? []).map((section) => ({ id: section.section_id, text: section.text, source: section.source }));
  if (snapshot.status !== "pending" || stalled) return done;
  const shown = new Set(done.map((section) => section.id));
  return [...done, ...snapshot.pending_sections.filter((id) => !shown.has(id)).map((id) => ({ id }))];
}

type Props = {
  sections: readonly SummarySection[];
  /** The evidence record; arrivals reveal once per record and section. */
  recordId: string | null;
  /** Replaces the general disclaimer, as the approval card does. */
  disclaimer?: string;
  class?: string;
  "data-testid"?: string;
};

function Mark(props: { disclaimer: string }) {
  return (
    <>
      <span class="den-briefing-mark" data-tip={props.disclaimer} aria-hidden="true">✦</span>
      <span class="den-visually-hidden">{props.disclaimer}</span>
    </>
  );
}

function Inline(props: { text: string }) {
  return (
    <For each={splitBriefingInline(props.text)}>
      {(part) => (part.kind === "code" ? <code class="den-briefing-summary__code">{part.text}</code> : part.text)}
    </For>
  );
}

// The reveal animates plain characters; code spans take their style once it ends.
function RevealedText(props: { text: string; onceKey: string }) {
  const [revealed, setRevealed] = createSignal(false);
  createEffect(on(() => props.text, () => setRevealed(false), { defer: true }));
  return (
    <Show when={revealed()} fallback={<RandomLetterReveal text={briefingRevealText(props.text)} onceKey={props.onceKey} onRevealed={() => setRevealed(true)} />}>
      <Inline text={props.text} />
    </Show>
  );
}

// The label flows with its text so a narrow pane wraps the sentence, not the label.
function Labelled(props: { label?: string; children: JSX.Element }) {
  return (
    <span class="den-briefing-summary__text">
      <Show when={props.label}>{(label) => <><strong class="den-briefing-summary__label">{label()}:</strong>{" "}</>}</Show>
      {props.children}
    </span>
  );
}

/**
 * Shows model-written text with its mark, waiting text while a section is
 * written, and deterministic text plainly. The mark follows each section's
 * source, never the surface.
 */
export function BriefingSummary(props: Props) {
  const disclaimer = () => props.disclaimer ?? briefingCopy.disclaimer;
  const watched = new Set<string>();
  const onceKey = (id: string) => `${props.recordId ?? ""}\0${id}`;
  createEffect(on(() => props.sections.some((section) => section.text === undefined), (waiting, was) => {
    if (waiting && !was) announce(briefingCopy.waiting);
  }));
  return (
    <div class={`den-briefing-summary ${props.class ?? ""}`} data-testid={props["data-testid"] ?? "briefing-summary"}>
      <For each={props.sections}>
        {(section) => (
          <Show
            when={section.text !== undefined}
            fallback={(() => {
              watched.add(section.id);
              return (
                <p class="den-briefing-summary__section" data-section={section.id} data-state="waiting">
                  <Mark disclaimer={disclaimer()} />
                  <Labelled label={section.label}><span class="den-briefing-summary__waiting">{briefingCopy.waiting}</span></Labelled>
                </p>
              );
            })()}
          >
            <p class="den-briefing-summary__section" data-section={section.id} data-source={section.source}>
              <Show when={section.source === "summarizer"} fallback={<Labelled label={section.label}><Inline text={section.text ?? ""} /></Labelled>}>
                <Mark disclaimer={disclaimer()} />
                <Labelled label={section.label}>
                  {(() => {
                    // Text already settled when this view opened shows at once.
                    if (!watched.has(section.id)) markLetterRevealOnceKey(onceKey(section.id));
                    return <RevealedText text={section.text ?? ""} onceKey={onceKey(section.id)} />;
                  })()}
                </Labelled>
              </Show>
            </p>
          </Show>
        )}
      </For>
    </div>
  );
}

/**
 * Opens a summary's evidence in Files. The summary text is never itself a
 * link, so it stays selectable.
 */
export function BriefingEvidenceLink(props: { reference: BriefingEvidenceRef; returnAnchor?: BriefingReturnAnchor }) {
  return (
    <button
      type="button"
      class="den-briefing-evidence-link"
      aria-label={briefingCopy.evidenceLinkName}
      data-testid="briefing-evidence-link"
      onClick={() => openBriefingEvidence(props.reference, props.returnAnchor)}
    >
      {briefingCopy.evidenceLink}
    </button>
  );
}
