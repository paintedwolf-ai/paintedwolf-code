import { describe, expect, it } from "vitest";
import {
  NAV_WIDTH_DEFAULT_PX,
  NAV_WIDTH_MAX_PX,
  NAV_WIDTH_MIN_PX,
} from "../../shared/app-state-types.ts";
import {
  clampNavWidthPx,
  maxNavWidthPx,
  resolveNavWidthPx,
} from "./shell-layout-model.ts";

describe("shell-layout-model", () => {
  it("clamps nav width to configured bounds", () => {
    expect(clampNavWidthPx(NAV_WIDTH_MIN_PX - 10)).toBe(NAV_WIDTH_MIN_PX);
    expect(clampNavWidthPx(NAV_WIDTH_MAX_PX + 100)).toBe(NAV_WIDTH_MAX_PX);
    expect(clampNavWidthPx(240)).toBe(240);
  });

  it("limits max width to 45% of viewport", () => {
    expect(maxNavWidthPx(800)).toBe(360);
    expect(clampNavWidthPx(400, 800)).toBe(360);
  });

  it("resolves persisted width with defaults", () => {
    expect(resolveNavWidthPx(undefined)).toBe(NAV_WIDTH_DEFAULT_PX);
    expect(resolveNavWidthPx({ navWidthPx: 280 })).toBe(280);
    expect(resolveNavWidthPx({ navWidthPx: Number.NaN })).toBe(
      NAV_WIDTH_DEFAULT_PX,
    );
  });
});
