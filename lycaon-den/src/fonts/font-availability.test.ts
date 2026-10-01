// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { probeFontAvailability } from "./font-availability.ts";

/** Supplies measured widths for the canvas probe. */
function stubCanvas(widths: (font: string) => number): void {
  const context = {
    font: "",
    measureText() {
      return { width: widths(context.font) };
    },
  };
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
    context as unknown as CanvasRenderingContext2D,
  );
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("probeFontAvailability", () => {
  it("reports a family that changes the measurement as available", () => {
    stubCanvas((font) => (font.includes("Berkeley Mono") ? 420 : 400));
    expect(probeFontAvailability("Berkeley Mono")).toBe("available");
  });

  it("reports a family that measures exactly like every sentinel as missing", () => {
    stubCanvas(() => 400);
    expect(probeFontAvailability("Fira Code")).toBe("unavailable");
  });

  it("does not call a family missing on one sentinel collision", () => {
    stubCanvas((font) => {
      if (font.includes("serif") && font.includes("Twin")) return 512;
      return 400;
    });
    expect(probeFontAvailability("Twin")).toBe("available");
  });

  it("says unknown when it cannot measure", () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
    expect(probeFontAvailability("Inter")).toBe("unknown");
  });

  it("says unknown when the sentinel itself measures nothing", () => {
    stubCanvas(() => 0);
    expect(probeFontAvailability("Inter")).toBe("unknown");
  });

  it("says unknown for an empty name", () => {
    stubCanvas(() => 400);
    expect(probeFontAvailability("   ")).toBe("unknown");
  });

  it("escapes a quote rather than breaking the font shorthand", () => {
    const seen: string[] = [];
    stubCanvas((font) => {
      seen.push(font);
      return 400;
    });
    probeFontAvailability('Ba"d');
    expect(seen.some((font) => font.includes('"Ba\\"d"'))).toBe(true);
  });
});
