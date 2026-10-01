/** Active theme glyph overrides. */
import { createSignal } from "solid-js";
import type { ContributionIconNode, ContributionIconStroke } from "../api/types.ts";
import type { IconSlot } from "./theme-vocabulary.generated.ts";

type IconOverrides = Readonly<Record<string, ContributionIconNode[]>>;

export const THEME_ICON_STROKE_VARS = [
  "--den-icon-stroke-scale",
  "--den-icon-stroke-cap",
  "--den-icon-stroke-join",
] as const;

const [overrides, setOverrides] = createSignal<IconOverrides>({});

/** Replaces the glyph set wholesale. */
export function setThemeGlyphs(icons: IconOverrides | undefined): void {
  setOverrides(icons ?? {});
}

/** Returns the theme-wide icon stroke properties. */
export function iconStrokeProperties(
  stroke: ContributionIconStroke,
): Record<string, string> {
  return {
    [THEME_ICON_STROKE_VARS[0]]: String(stroke.weight),
    [THEME_ICON_STROKE_VARS[1]]: stroke.cap,
    [THEME_ICON_STROKE_VARS[2]]: stroke.join,
  };
}

/** The theme's geometry for a slot, or null to draw the host glyph. */
export function themedIcon(slot: IconSlot): ContributionIconNode[] | null {
  const nodes = overrides()[slot];
  return nodes && nodes.length > 0 ? nodes : null;
}
