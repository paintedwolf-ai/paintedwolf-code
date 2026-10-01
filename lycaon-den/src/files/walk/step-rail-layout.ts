/** Spacing includes each 24px hit target and its halo. */
const STEP_RAIL_MIN_PITCH = 40;
export const STEP_RAIL_EDGE = 12;
const OVERSCAN = 3;

export function stepRailLayout(count: number, width: number) {
  const pitch = count > 1
    ? Math.max(STEP_RAIL_MIN_PITCH, (width - STEP_RAIL_EDGE * 2) / (count - 1))
    : 0;
  const extent = Math.max(width, STEP_RAIL_EDGE * 2 + Math.max(0, count - 1) * pitch);
  return { count, width, pitch, extent, maxScroll: Math.max(0, extent - width) };
}

export type StepRailLayout = ReturnType<typeof stepRailLayout>;

export function stepRailPosition(layout: StepRailLayout, index: number): number {
  return STEP_RAIL_EDGE + index * layout.pitch;
}

export function stepRailWindow(layout: StepRailLayout, offset: number) {
  if (layout.count === 0) return { first: 0, end: 0 };
  if (layout.pitch === 0) return { first: 0, end: 1 };
  return {
    first: Math.max(0, Math.floor((offset - STEP_RAIL_EDGE) / layout.pitch) - OVERSCAN),
    end: Math.min(layout.count, Math.ceil((offset + layout.width - STEP_RAIL_EDGE) / layout.pitch) + OVERSCAN + 1),
  };
}

export function stepRailReveal(layout: StepRailLayout, offset: number, index: number): number {
  const x = stepRailPosition(layout, index);
  // Visible targets keep their position under the pointer.
  if (x - STEP_RAIL_EDGE >= offset && x + STEP_RAIL_EDGE <= offset + layout.width) return offset;
  const margin = Math.min(layout.width / 2, STEP_RAIL_MIN_PITCH * 1.5);
  const next = x < offset + margin ? x - margin
    : x > offset + layout.width - margin ? x - layout.width + margin : offset;
  return Math.max(0, Math.min(layout.maxScroll, next));
}
