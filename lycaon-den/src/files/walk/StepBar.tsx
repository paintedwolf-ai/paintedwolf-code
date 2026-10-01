import {
  For,
  Show,
  createEffect,
  on,
  createMemo,
  createSignal,
  onCleanup,
} from "solid-js";
import { relativeTimeLabel } from "../../time/time-copy.ts";
import type { TranscriptRevealTarget } from "../../chat/transcript/presentation/transcript-reveal-target.ts";
import { commandWindowLine } from "../source/source-command-window.ts";
import { gitChangeRefs } from "../source/source-git-change.ts";
import { fileBasename } from "../review/review-model.ts";
import {
  walkStepAction,
  walkStepActor,
  walkGroupFileSummary,
  walkObservedSpan,
  walkStepFiles,
  walkStepKind,
  walkStepOperation,
  type WalkCommandStep,
  type WalkGitStep,
  type WalkOutsideStep,
  type WalkStep,
} from "./walk-model.ts";
import {
  leaveWalk,
  setWalkAt,
  stepWalk,
  subscribeWalk,
  walkState,
  walkToLatest,
  walkToStart,
} from "./walk-store.ts";
import { WalkChapterHeading } from "./WalkChapterHeading.tsx";
import { walkChapterSegments } from "./walk-chapters.ts";
import { AnchoredSurface } from "../../components/primitives/AnchoredSurface.tsx";
import { ThemeIcon } from "../../components/primitives/ThemeIcon.tsx";
import { WalkPosition } from "./WalkPosition.tsx";
import { wholeFileOpChange } from "../../components/source/reader/source-reader-change.ts";
import { createStepRail } from "./create-step-rail.ts";
import { STEP_RAIL_EDGE } from "./step-rail-layout.ts";
import { ShowLatest } from "../../components/primitives/ShowLatest.tsx";
import { usePresentationParticipant } from "../../ui/presentation-context.tsx";
import { surfaceRevealDom, type SurfaceReveal } from "../../ui/surface-reveal.ts";

type Props = {
  projectId: string;
  presentation: SurfaceReveal;
  onRevealInTranscript?: (target: TranscriptRevealTarget) => void;
};

// Marker geometry at the current step's size, in px.
const MARKER_BOX_PX = 24;
const MARKER_BODY_PX = 5;
const MARKER_STROKE_PX = 1.5;
const MARKER_KNOCKOUT_PX = 6.5;
const MARKER_HALO_PX = 8;
const MARKER_HALO_STROKE_PX = 2;
const MARKER_HOVER_SCALE = 1.25;
const MARKER_DASHES = 6;

const CHAPTER_GAP_PX = 6;
const TOOLTIP_PATH_LIMIT = 3;

type HoveredStep = {
  key: string;
  anchor: DOMRect;
};

function stepOperationTone(step: WalkStep): "accent" | "positive" | "danger" {
  if (step.kind !== "effect") return "accent";
  if (step.effect.op === "create") return "positive";
  if (step.effect.op === "delete") return "danger";
  return "accent";
}

type MarkerDash = { array: string; offset: string };

function markerDash(radius: number): MarkerDash {
  const period = (2 * Math.PI * radius) / MARKER_DASHES;
  // Centring a dash on 3 o'clock puts gaps at the top and bottom.
  return { array: `${period * 0.6} ${period * 0.4}`, offset: `${period * 0.3}` };
}

function MarkerOutline(props: {
  class: string;
  square: boolean;
  /** Distance from the centre to the stroke's centre line. */
  half: number;
  strokeWidth?: number;
  dash?: MarkerDash;
}) {
  const centre = MARKER_BOX_PX / 2;
  return props.square ? (
    <rect
      class={props.class}
      x={centre - props.half}
      y={centre - props.half}
      width={props.half * 2}
      height={props.half * 2}
      // Concentric corners: every outline keeps the body's corner centre.
      rx={props.half - MARKER_BODY_PX / 2}
      stroke-width={props.strokeWidth}
    />
  ) : (
    <circle
      class={props.class}
      cx={centre}
      cy={centre}
      r={props.half}
      stroke-width={props.strokeWidth}
      stroke-dasharray={props.dash?.array}
      stroke-dashoffset={props.dash?.offset}
    />
  );
}

/** Edits are filled, groups hollow; commands square, outside runs dashed. */
function StepMarker(props: { step: WalkStep }) {
  const square = () => props.step.kind === "command";
  const hollow = () => props.step.kind !== "effect";
  const bodyHalf = () =>
    hollow() ? MARKER_BODY_PX - MARKER_STROKE_PX / 2 : MARKER_BODY_PX;
  return (
    <svg
      class="den-step-marker"
      classList={{ "den-step-marker--hollow": hollow() }}
      width={MARKER_BOX_PX}
      height={MARKER_BOX_PX}
      viewBox={`0 0 ${MARKER_BOX_PX} ${MARKER_BOX_PX}`}
      aria-hidden="true"
    >
      <MarkerOutline
        class="den-step-marker__halo"
        square={square()}
        half={MARKER_HALO_PX}
        strokeWidth={MARKER_HALO_STROKE_PX}
      />
      <MarkerOutline class="den-step-marker__knockout" square={square()} half={MARKER_KNOCKOUT_PX} />
      <MarkerOutline
        class="den-step-marker__body"
        square={square()}
        half={bodyHalf()}
        dash={props.step.kind === "outside" ? markerDash(bodyHalf()) : undefined}
      />
    </svg>
  );
}

function stepHeadline(step: WalkStep): string {
  if (step.kind === "git") {
    return gitChangeRefs(step.change) || walkStepOperation(step);
  }
  if (step.kind === "command") return commandWindowLine(step.command);
  if (step.kind === "outside") {
    const files = walkStepFiles(step);
    const only = files.length === 1 ? files[0] : undefined;
    return only ? fileBasename(only.path) : walkGroupFileSummary(step);
  }
  return fileBasename(step.effect.path);
}

function stepWhen(step: WalkStep): string {
  switch (step.kind) {
    case "git":
      return relativeTimeLabel(step.change.observed_at);
    case "command":
      return relativeTimeLabel(step.command.started_at);
    case "outside":
      return walkObservedSpan(step.effects);
    case "effect":
      return relativeTimeLabel(step.effect.observed_at);
  }
}

function stepEffect(step: WalkStep) {
  return step.kind === "effect" ? step.effect : null;
}

function stepGit(step: WalkStep): WalkGitStep | null {
  return step.kind === "git" ? step : null;
}

function stepCommand(step: WalkStep): WalkCommandStep | null {
  return step.kind === "command" ? step : null;
}

function stepOutside(step: WalkStep): WalkOutsideStep | null {
  return step.kind === "outside" ? step : null;
}

function stepAriaLabel(step: WalkStep): string {
  if (step.kind === "git") {
    const details = [
      walkStepOperation(step),
      gitChangeRefs(step.change),
      walkGroupFileSummary(step),
      step.change.detail?.trim() ?? "",
      stepWhen(step),
    ].filter(Boolean);
    return details.join(". ");
  }
  if (step.kind === "command") {
    const details = [
      walkStepOperation(step),
      commandWindowLine(step.command),
      walkGroupFileSummary(step),
      stepWhen(step),
    ].filter(Boolean);
    return details.join(". ");
  }
  if (step.kind === "outside") {
    const details = [
      walkStepOperation(step),
      walkGroupFileSummary(step),
      stepWhen(step),
    ].filter(Boolean);
    return details.join(". ");
  }
  const details = [
    `${walkStepOperation(step)} ${step.effect.path}`,
    walkStepActor(step),
    step.effect.turn > 0 ? `turn ${step.effect.turn}` : "",
    walkStepAction(step),
    stepWhen(step),
  ].filter(Boolean);
  return details.join(". ");
}

export function StepBar(props: Props) {
  const [tick, setTick] = createSignal(0);
  const [hovered, setHovered] = createSignal<HoveredStep | null>(null);
  const [focusedKey, setFocusedKey] = createSignal<string | null>(null);
  const dotEls = new Map<string, HTMLButtonElement>();
  let hoverCloseTimer: ReturnType<typeof setTimeout> | undefined;

  onCleanup(
    subscribeWalk((id) => {
      if (id === props.projectId.trim()) setTick((n) => n + 1);
    }),
  );

  const state = createMemo(() => {
    void tick();
    return walkState(props.projectId);
  });

  const steps = createMemo(() => state().walk.steps);
  const stepIndices = createMemo(() => new Map(steps().map((step, index) => [step.key, index])));
  const stepsByKey = createMemo(() => new Map(steps().map((step) => [step.key, step])));
  const segments = createMemo(() => walkChapterSegments(state().walk));

  const segmentsByKey = createMemo(() => new Map(segments().map((segment) => [segment.key, segment])));
  const at = createMemo(() => state().at);
  const count = createMemo(() => steps().length);
  const hoveredStep = createMemo(() => {
    const hover = hovered();
    return hover ? (stepsByKey().get(hover.key) ?? null) : null;
  });

  const rail = createStepRail({
    count, at,
    selection: () => `${props.projectId}:${state().sessionId}:${steps()[at()]?.key}:${state().selectionRevision}`,
    focused: () => stepIndices().get(focusedKey() ?? "") ?? null,
    onScroll: () => closeStepDetails(),
  });
  const stepKeys = createMemo(() => rail.indices().flatMap((index) => { const step = steps()[index]; return step ? [step.key] : []; }));
  const segmentKeys = createMemo(() => segments()
    .filter((segment) => segment.end >= rail.window().first && segment.start < rail.window().end)
    .map((segment) => segment.key));
  const position = rail.position;
  usePresentationParticipant("walk-rail", () => state().status !== "loading" && (count() === 0 || rail.layout().width > 0));

  const showStepDetails = (index: number, element: HTMLElement) => {
    if (hoverCloseTimer !== undefined) clearTimeout(hoverCloseTimer);
    hoverCloseTimer = undefined;
    const step = steps()[index];
    if (step) setHovered({ key: step.key, anchor: element.getBoundingClientRect() });
  };

  const hideStepDetails = (index: number, element: HTMLElement) => {
    if (document.activeElement === element) return;
    if (hoverCloseTimer !== undefined) clearTimeout(hoverCloseTimer);
    hoverCloseTimer = setTimeout(() => {
      hoverCloseTimer = undefined;
      if (hovered()?.key === steps()[index]?.key) setHovered(null);
    }, 140);
  };

  const keepStepDetailsOpen = () => {
    if (hoverCloseTimer !== undefined) clearTimeout(hoverCloseTimer);
    hoverCloseTimer = undefined;
  };

  const closeStepDetails = () => {
    if (hoverCloseTimer !== undefined) clearTimeout(hoverCloseTimer);
    hoverCloseTimer = undefined;
    setHovered(null);
  };

  onCleanup(() => {
    if (hoverCloseTimer !== undefined) clearTimeout(hoverCloseTimer);
  });

  // Horizontal gestures browse; clicking the line selects a step.
  let pointerSelected = false;
  const onRailClick = (event: MouseEvent) => {
    if (pointerSelected) { pointerSelected = false; return; }
    if (event.target instanceof Element && event.target.closest("button")) return;
    setWalkAt(props.projectId, rail.seek(event.clientX));
  };
  let dragPointer: number | null = null;
  const onRailPointerDown = (event: PointerEvent) => {
    pointerSelected = false;
    if (event.pointerType !== "mouse" || event.button !== 0) return;
    if (event.target instanceof Element && event.target.closest("button")) return;
    event.preventDefault();
    pointerSelected = true;
    dragPointer = event.pointerId;
    (event.currentTarget as HTMLElement).setPointerCapture?.(event.pointerId);
    setWalkAt(props.projectId, rail.seek(event.clientX));
  };

  let focusAfterSelection = false;
  const selectionRevision = createMemo(() => state().selectionRevision);
  createEffect(on(selectionRevision, () => {
    if (!focusAfterSelection) return;
    focusAfterSelection = false;
    queueMicrotask(() => {
      if (!(document.activeElement instanceof Element) || !document.activeElement.closest(".den-step-bar__dot")) return;
      const step = steps()[at()];
      if (step) dotEls.get(step.key)?.focus({ preventScroll: true });
    });
  }));

  const onKeyDown = (event: KeyboardEvent) => {
    if (event.target instanceof Element && event.target.closest("input, [role=dialog]")) return;
    const startedOnDot =
      event.target instanceof Element &&
      event.target.closest(".den-step-bar__dot") !== null;
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    if (startedOnDot && ["ArrowLeft", "ArrowDown", "ArrowRight", "ArrowUp", "Home", "End"].includes(event.key)) focusAfterSelection = true;
    if (event.key === "Escape") {
      event.preventDefault();
      leaveWalk(props.projectId);
      return;
    }
    if (event.key === "ArrowLeft" || event.key === "ArrowDown") {
      event.preventDefault();
      stepWalk(props.projectId, -1);
      return;
    }
    if (event.key === "ArrowRight" || event.key === "ArrowUp") {
      event.preventDefault();
      stepWalk(props.projectId, 1);
      return;
    }
    if (event.key === "Home") {
      event.preventDefault();
      walkToStart(props.projectId);
      return;
    }
    if (event.key === "End") {
      event.preventDefault();
      walkToLatest(props.projectId);
    }
  };

  return (
    <div
      class="den-step-bar"
      {...surfaceRevealDom(props.presentation)}
      data-testid="step-bar"
      role="group"
      aria-label="Walk file changes"
      onKeyDown={onKeyDown}
    >
      <div class="den-walk-heading">
        <WalkChapterHeading projectId={props.projectId} onRevealInTranscript={props.onRevealInTranscript} />
      </div>
      <Show when={state().notice}>
        <span class="den-step-bar__note" role="status" data-testid="walk-history-notice">{state().notice}</span>
      </Show>
      <Show when={state().status === "ready" && count() === 0}>
        <span class="den-step-bar__note" data-testid="step-bar-empty">
          This chat has no file changes yet.
        </span>
      </Show>

      <Show when={count() > 0}>
        <WalkPosition walk={state().walk} at={at()} onSelect={(index) => setWalkAt(props.projectId, index)} />

        <div class="den-step-bar__viewport">
          <button type="button" class="den-step-bar__browse" data-testid="step-bar-earlier"
            aria-label="Browse earlier steps"
            disabled={rail.offset() <= 0.5} onClick={() => rail.browse(-1)}>
            <ThemeIcon slot="walk-previous" size={14} />
          </button>
        <div
          class="den-step-bar__rail"
          ref={rail.attach}
          data-testid="step-bar-rail"
          onClick={onRailClick}
          onPointerDown={onRailPointerDown}
          onPointerMove={(event) => {
            if (dragPointer === event.pointerId) setWalkAt(props.projectId, rail.seek(event.clientX));
          }}
          onPointerUp={() => { dragPointer = null; }}
          onPointerCancel={() => { dragPointer = null; pointerSelected = false; }}
          onLostPointerCapture={() => { dragPointer = null; }}
          onScroll={rail.onScroll}
        >
          <div
            class="den-step-bar__track"
            data-testid="step-bar-track"
            style={{
              width: `${rail.layout().extent}px`,
              "--den-step-marker-stroke": String(MARKER_STROKE_PX),
              "--den-step-marker-hover": String(MARKER_HOVER_SCALE),
            }}
          >
            <div
              class="den-step-bar__line"
              data-testid="step-bar-line"
              aria-hidden="true"
              style={{
                left: `${STEP_RAIL_EDGE}px`,
                width: `${Math.max(0, count() - 1) * rail.layout().pitch}px`,
              }}
            />
            <For each={segmentKeys()}>
              {(key) => <Show when={segmentsByKey().get(key)}>{(segment) => {
                const left = () => Math.max(position(segment().start), rail.offset());
                // A label runs to the next chapter, or to the rail's end for the last.
                const width = () => {
                  const next = segment().end + 1;
                  const right = next < count() ? position(next) - CHAPTER_GAP_PX : rail.layout().extent;
                  return Math.max(1, right - left());
                };
                return <span class="den-step-bar__chapter" aria-hidden="true" style={{
                  left: `${left()}px`, width: `${width()}px`,
                }}><Show when={width() >= 48}>{segment().title}</Show></span>;
              }}</Show>}
            </For>
            <For each={stepKeys()}>
              {(key) => {
                const index = () => stepIndices().get(key) ?? -1;
                onCleanup(() => dotEls.delete(key));
                return <ShowLatest when={stepsByKey().get(key)}>{(step) => {
                const label = () => stepAriaLabel(step());
                return (
                  <button
                    type="button"
                    class="den-step-bar__dot"
                    classList={{
                      "den-step-bar__dot--past": index() < at(),
                      "den-step-bar__dot--current": index() === at(),
                      "den-step-bar__dot--create": walkStepKind(step()) === "create",
                      "den-step-bar__dot--write": walkStepKind(step()) === "write",
                      "den-step-bar__dot--delete": walkStepKind(step()) === "delete",
                      "den-step-bar__dot--git": walkStepKind(step()) === "git",
                      "den-step-bar__dot--command": walkStepKind(step()) === "command",
                      "den-step-bar__dot--outside": walkStepKind(step()) === "outside",
                      "den-step-bar__dot--new": state().unseenStepKeys.includes(key),
                    }}
                    style={{ left: `${position(index())}px` }}
                    data-testid="step-bar-dot"
                    aria-current={index() === at() ? "step" : undefined}
                    aria-label={`Step ${index() + 1} of ${count()}. ${label()}`}
                    tabIndex={index() === at() ? 0 : -1}
                    ref={(element) => {
                      dotEls.set(key, element);
                    }}
                    onClick={() => setWalkAt(props.projectId, index())}
                    onMouseEnter={(event) =>
                      showStepDetails(index(), event.currentTarget)
                    }
                    onMouseLeave={(event) =>
                      hideStepDetails(index(), event.currentTarget)
                    }
                    onFocus={(event) => {
                      setFocusedKey(key);
                      rail.reveal(index());
                      showStepDetails(index(), event.currentTarget);
                    }}
                    onBlur={() => {
                      setFocusedKey(null);
                      if (hovered()?.key === key) setHovered(null);
                    }}
                  >
                    <StepMarker step={step()} />
                  </button>
                );
                }}</ShowLatest>;
              }}
            </For>
          </div>
        </div>

          <button type="button" class="den-step-bar__browse" data-testid="step-bar-later"
            aria-label="Browse later steps"
            disabled={rail.offset() >= rail.layout().maxScroll - 0.5} onClick={() => rail.browse(1)}>
            <ThemeIcon slot="walk-next" size={14} />
          </button>
        </div>

      </Show>


      <Show when={hoveredStep()}>
        {(step) => (
          <AnchoredSurface
            class="den-status-popover den-step-tooltip"
            role="tooltip"
            testId="step-bar-tooltip"
            anchor={() => hovered()?.anchor}
            preferredSide="top"
            align="center"
            gap={8}
            dismissOnScroll
            onDismiss={closeStepDetails}
            onMouseEnter={keepStepDetailsOpen}
            onMouseLeave={closeStepDetails}
          >
            <div class="den-status-popover__hint den-step-tooltip__head">
              <span>
                Step {steps().findIndex((step) => step.key === hovered()?.key) + 1} of {count()}
              </span>
              <span
                class="den-status-mark"
                data-tone={stepOperationTone(step())}
              >
                {walkStepOperation(step())}
              </span>
            </div>
            <strong class="den-status-popover__title den-step-tooltip__file">
              {stepHeadline(step())}
            </strong>
            <Show when={stepEffect(step())}>
              {(effect) => (
                <>
                  <span class="den-status-popover__hint den-step-tooltip__path" data-file-change={wholeFileOpChange(effect().op)}>
                    {effect().path}
                  </span>
                  <Show when={effect().op === "rename" && effect().from_path}>
                    <span class="den-status-popover__hint den-step-tooltip__rename">
                      From {effect().from_path}
                    </span>
                  </Show>
                </>
              )}
            </Show>
            <Show when={stepGit(step())}>
              {(git) => (
                <>
                  <span class="den-status-popover__hint den-step-tooltip__path">
                    {walkGroupFileSummary(git())}
                  </span>
                  <Show when={git().change.detail?.trim()}>
                    {(subject) => (
                      <span class="den-status-popover__hint den-step-tooltip__path">
                        {subject()}
                      </span>
                    )}
                  </Show>
                </>
              )}
            </Show>
            <Show when={stepCommand(step())}>
              {(command) => (
                <span class="den-status-popover__hint den-step-tooltip__path">
                  {walkGroupFileSummary(command())} observed while it ran
                </span>
              )}
            </Show>
            <Show when={stepOutside(step())}>
              {(outside) => {
                const files = () => walkStepFiles(outside());
                return (
                  <>
                    <For each={files().slice(0, TOOLTIP_PATH_LIMIT)}>
                      {(file) => (
                        <span class="den-status-popover__hint den-step-tooltip__path">
                          {file.path}
                        </span>
                      )}
                    </For>
                    <Show when={files().length > TOOLTIP_PATH_LIMIT}>
                      <span class="den-status-popover__hint">
                        and {files().length - TOOLTIP_PATH_LIMIT} more
                      </span>
                    </Show>
                  </>
                );
              }}
            </Show>
            <div class="den-status-popover__hint den-step-tooltip__meta">
              <Show when={stepEffect(step())}>
                {(effect) => (
                  <>
                    <span>{walkStepActor(step())}</span>
                    <Show when={effect().turn > 0}>
                      <span>Turn {effect().turn}</span>
                    </Show>
                    <span>{walkStepAction(step())}</span>
                    <Show
                      when={
                        effect().capture_quality &&
                        effect().capture_quality !== "exact"
                      }
                    >
                      <span>{effect().capture_quality}</span>
                    </Show>
                  </>
                )}
              </Show>
              <Show when={stepWhen(step())}>
                {(time) => <span>{time()}</span>}
              </Show>
            </div>
          </AnchoredSurface>
        )}
      </Show>
    </div>
  );
}
