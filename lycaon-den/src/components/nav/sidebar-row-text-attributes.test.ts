import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { readSourceText } from "../../test/stylesheet-source.ts";

const src = join(import.meta.dirname, "../..");
const read = (rel: string) => readSourceText(join(src, rel), "utf8");

/**
 * WebKitGTK aborts its web process answering AT-SPI text attributes for a list item laid out as
 * the row's grid, or holding chrome text it cannot select; den:webkit:atspi exercises the real
 * engine, and these hold the shapes that avoid it in source.
 */
describe("sidebar rows WebKitGTK can describe", () => {
  it("lays out chat and folder rows on a wrapper inside the list item", () => {
    const chats = read("components/nav/FocusedSessionList.tsx");
    const folders = read("components/nav/ProjectFolderZone.tsx");
    expect(chats).toMatch(/<li\s+class="focused-session-list__item"[\s\S]*?>\s*(?:\{\/\*[\s\S]*?\*\/\}\s*)?<div class="den-list-row--nav den-list-row">/);
    expect(folders).toMatch(/<li\s+class="project-folders__item"[\s\S]*?>\s*(?:\{\/\*[\s\S]*?\*\/\}\s*)?<div class="project-folders__row den-list-row--nav den-list-row">/);
    expect(read("styling/chrome/session-navigation.css")).toMatch(/\.focused-session-list__item\s*\{[^}]*display:\s*block/);
    expect(read("styling/chrome/project-navigation.css")).toMatch(/\.project-folders__item\s*\{[^}]*display:\s*block/);
  });

  it("keeps chat titles and folder labels selectable inside chrome", () => {
    expect(read("styling/chrome/session-navigation.css")).toMatch(/\.focused-session-list__row\s*\{[^}]*user-select:\s*text/);
    expect(read("styling/chrome/project-navigation.css")).toMatch(/\.project-folders__path\s*\{[^}]*user-select:\s*text/);
  });
});
