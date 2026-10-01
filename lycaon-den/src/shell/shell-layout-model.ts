import {
  NAV_WIDTH_DEFAULT_PX,
  NAV_WIDTH_MAX_PX,
  NAV_WIDTH_MIN_PX,
  type DenLayoutPrefs,
} from "../../shared/app-state-types.ts";

/** Upper bound for nav width given viewport (never more than 45% of window). */
export function maxNavWidthPx(viewportWidth: number): number {
  return Math.min(
    NAV_WIDTH_MAX_PX,
    Math.max(NAV_WIDTH_MIN_PX, Math.floor(viewportWidth * 0.45)),
  );
}

export function clampNavWidthPx(
  value: number,
  viewportWidth?: number,
): number {
  const max =
    viewportWidth !== undefined
      ? maxNavWidthPx(viewportWidth)
      : NAV_WIDTH_MAX_PX;
  if (!Number.isFinite(value)) return NAV_WIDTH_DEFAULT_PX;
  return Math.round(Math.min(max, Math.max(NAV_WIDTH_MIN_PX, value)));
}

export function resolveNavWidthPx(
  prefs?: DenLayoutPrefs,
  viewportWidth?: number,
): number {
  return clampNavWidthPx(prefs?.navWidthPx ?? NAV_WIDTH_DEFAULT_PX, viewportWidth);
}
