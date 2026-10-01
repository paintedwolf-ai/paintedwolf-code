import { afterEach, describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { ThemeIcon } from "./ThemeIcon.tsx";
import {
  iconStrokeProperties,
  setThemeGlyphs,
} from "../../contributions/theme-icons.ts";
import {
  ICON_SLOTS,
  ICON_VIEWBOX,
} from "../../contributions/theme-vocabulary.generated.ts";

function apply(icons: Parameters<typeof setThemeGlyphs>[0]) {
  setThemeGlyphs(icons);
}

afterEach(() => apply({}));

function hostGlyph() {
  return <ThemeIcon slot="check" />;
}

/** Counts the stock check paths. */
function hostPathCount(container: HTMLElement) {
  return container.querySelectorAll("path").length;
}

describe("ThemeIcon", () => {
  it("draws the host glyph when no theme overrides the slot", () => {
    const { container } = render(hostGlyph);
    expect(hostPathCount(container)).toBeGreaterThan(0);
    expect(container.querySelector("circle")).toBeNull();
  });

  it("draws the theme's geometry instead when the slot is overridden", () => {
    apply({ check: [{ tag: "circle", attrs: { cx: "8", cy: "8", r: "5" } }] });
    const { container } = render(hostGlyph);
    expect(hostPathCount(container)).toBe(0);
    const circle = container.querySelector("circle");
    expect(circle?.getAttribute("r")).toBe("5");
  });

  it("returns to the host glyph when a later theme overrides nothing", () => {
    // Wholesale replacement, not a merge.
    apply({ check: [{ tag: "circle", attrs: { r: "5" } }] });
    apply({});
    const { container } = render(hostGlyph);
    expect(hostPathCount(container)).toBeGreaterThan(0);
    expect(container.querySelector("circle")).toBeNull();
  });

  it("keeps the frame host-managed, so a themed glyph cannot move anything", () => {
    // Size and viewBox stay host-managed.
    apply({
      check: [{ tag: "path", attrs: { d: "M1 1 L15 15" } }],
    });
    const { container } = render(() => <ThemeIcon slot="check" size={22} />);
    const svg = container.querySelector("svg")!;
    expect(svg.getAttribute("viewBox")).toBe(ICON_VIEWBOX);
    expect(svg.getAttribute("width")).toBe("22");
    expect(svg.getAttribute("height")).toBe("22");
  });

  it("paints from the frame, so a themed glyph follows the palette", () => {
    apply({ check: [{ tag: "path", attrs: { d: "M1 1" } }] });
    const { container } = render(hostGlyph);
    const svg = container.querySelector("svg")!;
    expect(svg.getAttribute("stroke")).toBe("currentColor");
    expect(container.querySelector("path")?.getAttribute("fill")).toBeNull();
  });

  it("takes paint and weight from the slot table, not from a prop", () => {
    // Fill mode is part of the slot contract.
    const { container } = render(hostGlyph);
    const svg = container.querySelector("svg")!;
    expect(svg.style.getPropertyValue("--den-icon-slot-weight")).toBe(
      String(ICON_SLOTS.check.weight),
    );
    const dots = render(() => <ThemeIcon slot="more" />);
    const solid = dots.container.querySelector("svg")!;
    expect(ICON_SLOTS.more.paint).toBe("fill");
    expect(solid.getAttribute("fill")).toBe("currentColor");
    expect(solid.getAttribute("stroke")).toBe("none");
  });

  it("carries the theme's hand to glyphs it never redrew", () => {
    // Stroke treatment reaches stock glyphs.
    const root = document.createElement("div");
    for (const [cssVar, value] of Object.entries(
      iconStrokeProperties({ weight: 1.6, cap: "butt", join: "miter" }),
    )) {
      root.style.setProperty(cssVar, value);
    }
    expect(root.style.getPropertyValue("--den-icon-stroke-scale")).toBe("1.6");
    expect(root.style.getPropertyValue("--den-icon-stroke-cap")).toBe("butt");
    expect(root.style.getPropertyValue("--den-icon-stroke-join")).toBe("miter");
    // CSS combines theme and slot weights.
    const { container } = render(hostGlyph);
    const svg = container.querySelector("svg")!;
    expect(svg.classList.contains("den-theme-icon")).toBe(true);
    expect(svg.style.getPropertyValue("--den-icon-slot-weight")).toBe(
      String(ICON_SLOTS.check.weight),
    );
  });

  it("drops unknown tags", () => {
    apply({
      check: [{ tag: "script" as "path", attrs: { d: "M1 1" } }],
    });
    const { container } = render(hostGlyph);
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("path")).toBeNull();
  });

  it("renders a group's children", () => {
    apply({
      check: [
        {
          tag: "g",
          attrs: { opacity: "0.5" },
          children: [{ tag: "circle", attrs: { r: "2" } }],
        },
      ],
    });
    const { container } = render(hostGlyph);
    expect(container.querySelector("g")?.getAttribute("opacity")).toBe("0.5");
    expect(container.querySelector("g circle")?.getAttribute("r")).toBe("2");
  });
});
