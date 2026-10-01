import { describe, expect, it } from "vitest";
import { parseAppearance } from "./app-state-parse.ts";
import { SYSTEM_FONT } from "../../fonts/font-catalog.ts";

describe("parseAppearance fonts", () => {
  it("keeps both selections", () => {
    expect(parseAppearance({ uiFont: "Literata", monoFont: SYSTEM_FONT })).toEqual({
      uiFont: "Literata",
      monoFont: SYSTEM_FONT,
    });
  });

  it("keeps a family this machine does not have", () => {
    // Whether a font exists is a property of the machine, not of the choice.
    // Rewriting it here would lose the choice on every sync to another machine.
    expect(parseAppearance({ monoFont: "Berkeley Mono" })).toEqual({
      monoFont: "Berkeley Mono",
    });
  });

  it("trims and collapses like the picker does", () => {
    expect(parseAppearance({ uiFont: "  IBM   Plex Sans " })).toEqual({
      uiFont: "IBM Plex Sans",
    });
  });

  it("drops a value that could break out of the declaration", () => {
    // App state is a file on disk, so it gets the same validation as the field.
    expect(parseAppearance({ uiFont: 'Inter"; background: url(http://x)' })).toBeUndefined();
    expect(parseAppearance({ monoFont: "Mono; color: red" })).toBeUndefined();
  });

  it("drops a non-string", () => {
    expect(parseAppearance({ uiFont: 42 })).toBeUndefined();
    expect(parseAppearance({ uiFont: null })).toBeUndefined();
  });

  it("leaves the theme selection alone", () => {
    expect(
      parseAppearance({ mode: "dark", darkTheme: "acme/kit:night", uiFont: "DM Sans" }),
    ).toEqual({ mode: "dark", darkTheme: "acme/kit:night", uiFont: "DM Sans" });
  });
});
