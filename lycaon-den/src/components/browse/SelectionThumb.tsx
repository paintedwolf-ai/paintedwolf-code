import { createEffect, createMemo, on, onCleanup } from "solid-js";
import {
  playSelectionTravel,
  shouldSnapSelectionTravel,
} from "../../ui/selection-travel.ts";

type Box = { x: number; y: number; width: number; height: number };

type Travel = {
  animation: Animation;
  from: Box;
  to: Box;
};

type Props = {
  active: string;
};

function boxInTrack(track: HTMLElement, target: HTMLElement): Box {
  const trackRect = track.getBoundingClientRect();
  const targetRect = target.getBoundingClientRect();
  return {
    x: targetRect.left - trackRect.left - track.clientLeft + track.scrollLeft,
    y: targetRect.top - trackRect.top - track.clientTop + track.scrollTop,
    width: targetRect.width,
    height: targetRect.height,
  };
}

function sameBox(a: Box, b: Box): boolean {
  return (
    a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height
  );
}

function travelTransform(box: Box): string {
  return `translate(${box.x}px, ${box.y}px)`;
}

function applyBox(el: HTMLElement, box: Box): void {
  el.style.transform = travelTransform(box);
  el.style.width = `${box.width}px`;
  el.style.height = `${box.height}px`;
}

function travelFrames(from: Box, to: Box): [Keyframe, Keyframe] {
  return [
    {
      transform: travelTransform(from),
      width: `${from.width}px`,
      height: `${from.height}px`,
    },
    {
      transform: travelTransform(to),
      width: `${to.width}px`,
      height: `${to.height}px`,
    },
  ];
}

export function SelectionThumb(props: Props) {
  let thumbRef: HTMLSpanElement | undefined;
  let destination: Box | null = null;
  let travel: Travel | null = null;
  let shown: boolean | null = null;
  let placementFrame: number | undefined;
  let animatePlacement = false;
  const schedulePlace = (animate = false) => {
    animatePlacement ||= animate;
    placementFrame ??= requestAnimationFrame(() => {
      placementFrame = undefined;
      const travel = animatePlacement;
      animatePlacement = false;
      place(travel);
    });
  };

  const cancelTravel = () => {
    travel?.animation.cancel();
    travel = null;
  };

  const startTravel = (thumb: HTMLElement, from: Box, to: Box) => {
    applyBox(thumb, from);
    let next: Travel | null = null;
    const animation = playSelectionTravel(thumb, travelFrames(from, to), () => {
      if (!next || travel !== next) return;
      applyBox(thumb, next.to);
      destination = next.to;
      travel = null;
    });
    next = { animation, from, to };
    travel = next;
    destination = to;
  };

  const retargetTravel = (to: Box) => {
    const current = travel;
    if (!current || sameBox(current.to, to)) return;
    (current.animation.effect as KeyframeEffect).setKeyframes(
      travelFrames(current.from, to),
    );
    current.to = to;
    destination = to;
  };

  const place = (animate: boolean) => {
    const thumb = thumbRef;
    if (!thumb) return;
    const track = thumb.parentElement;
    if (!track) return;

    const hide = () => {
      cancelTravel();
      if (shown !== false) {
        shown = false;
        thumb.style.opacity = "0";
      }
      destination = null;
    };

    const target = Array.from(
      track.querySelectorAll<HTMLElement>(".den-browse-segment"),
    ).find((segment) => segment.dataset.value === props.active);
    if (!target) {
      hide();
      return;
    }

    // The box matches the segment; CSS insets the painted fill.
    const box = boxInTrack(track, target);
    if (shown !== true) {
      shown = true;
      thumb.style.opacity = "1";
    }
    if (destination && sameBox(destination, box)) return;

    if (!animate && travel) {
      // Preserve travel while selected text changes the track geometry.
      retargetTravel(box);
      return;
    }

    if (!animate || destination === null || shouldSnapSelectionTravel(thumb)) {
      cancelTravel();
      applyBox(thumb, box);
      destination = box;
      return;
    }

    const from = travel ? boxInTrack(track, thumb) : destination;
    cancelTravel();
    startTravel(thumb, from, box);
  };

  // Selection and geometry notifications share one placement before paint.
  const active = createMemo(() => props.active);
  createEffect(on(active, () => schedulePlace(true)));

  createEffect(() => {
    const thumb = thumbRef;
    if (!thumb) return;
    const track = thumb.parentElement;
    if (!track || typeof ResizeObserver === "undefined") return;

    const resizeObserver = new ResizeObserver(() => schedulePlace());
    resizeObserver.observe(track);
    const mutationObserver =
      typeof MutationObserver === "undefined"
        ? null
        : new MutationObserver(() => schedulePlace());
    mutationObserver?.observe(track, { childList: true, subtree: true });
    onCleanup(() => {
      resizeObserver.disconnect();
      mutationObserver?.disconnect();
    });
  });

  onCleanup(() => {
    cancelTravel();
    if (placementFrame !== undefined) cancelAnimationFrame(placementFrame);
  });

  return (
    <span
      class="den-browse-segment-thumb"
      aria-hidden="true"
      ref={(el) => {
        thumbRef = el;
      }}
    >
      <span class="den-browse-segment-thumb__fill" />
    </span>
  );
}
