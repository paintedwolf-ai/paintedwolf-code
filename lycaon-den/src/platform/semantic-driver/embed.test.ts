// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { afterEach, expect, it } from "vitest";

afterEach(() => document.body.replaceChildren());

it("the embedded driver preserves observed control identity and checked state", () => {
  document.body.innerHTML = '<input id="task" value="private"><input id="done" type="checkbox">';
  document.querySelectorAll("input").forEach((input) => {
    Object.defineProperty(input, "offsetParent", { get: () => document.body });
  });
  const bundle = readFileSync("../lycaon/internal/browser/embed/semantic_driver.js", "utf8");
  new Function(bundle)();
  const state = window.__lycaonDriver?.state();
  expect(state).toMatchObject({ interactive: [
    { id: "task", type: "text" },
    { id: "done", type: "checkbox", checked: false },
  ] });
  expect(JSON.stringify(state)).not.toContain("private");
});
