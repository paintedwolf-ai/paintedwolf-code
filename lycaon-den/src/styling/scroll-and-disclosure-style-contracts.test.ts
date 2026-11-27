import { describe, expect, it } from "vitest";
import { denStylesheetRules, parseStylesheet, stylesheetRules } from "../test/style-contracts/parsed-stylesheets.ts";
import { splitTopLevel } from "../test/style-contracts/stylesheet-parser.ts";
import { VITEST_REPOSITORY_SCAN_TIMEOUT_MS } from "../test/vitest-timeouts.ts";

const STATE = String.raw`\[open\]|\[data-animating\]|\[data-expanded="true"\]`;
const stateful = new RegExp(STATE);
const statefulShell = new RegExp(String.raw`(?:&|\.[a-zA-Z][\w-]*)(?:${STATE})\s*$`);
const frame = /den-chat-stream-frame|\[data-den-scrollport\]|\.den-scrollport(?!\w)/;
const physicalPadding = /^padding(?:-(?:top|bottom|left|right))?$/;
const shellPaint = /^(?:background|border(?:-[a-z-]+)?)$/;

describe("scrollport and disclosure style contracts", { timeout: VITEST_REPOSITORY_SCAN_TIMEOUT_MS }, () => {
  const rules = denStylesheetRules();

  it("keeps disclosure corners and shadows independent of open state", () => {
    const violations = rules.filter((rule) =>
      stateful.test(rule.selector) && rule.declarations.some(({ prop }) =>
        prop === "border-radius" || prop === "box-shadow",
      ),
    );
    expect(violations.map(({ location, selector }) => `${location}: ${selector}`)).toEqual([]);
  });

  it("keeps disclosure shell paint independent of open state", () => {
    const violations = rules.filter((rule) =>
      splitTopLevel(rule.selector, /,/).some((selector) => statefulShell.test(selector)) &&
      rule.declarations.some(({ prop }) => shellPaint.test(prop)),
    );
    expect(violations.map(({ location, selector }) => `${location}: ${selector}`)).toEqual([]);
  });

  it("keeps scrollport frame padding on the content extent", () => {
    const violations = rules.flatMap(({ selector, declarations, location }) => {
      if (!splitTopLevel(selector, /,/).some((part) => frame.test(part) && !/[>\s]/.test(part))) return [];
      return declarations
        .filter(({ prop, value }) => physicalPadding.test(prop) && !/^(?:0|0px|none)$/.test(value))
        .map(({ prop, value }) => `${location}: ${selector} declares ${prop}: ${value}`);
    });
    expect(violations).toEqual([]);
  });
});

it("reads only actual declarations from each nested rule", () => {
  const rel = "fixture.css";
  const file = {
    rel,
    path: rel,
    text: `@layer shell {
  .den-scrollport {
    /* padding: 10px; */
    --label: "border-radius: 5px";
    &[open] { border-radius: 5px; }
  }
}`,
  };
  expect(parseStylesheet(file)).toBe(parseStylesheet(file));
  expect(stylesheetRules(file)).toEqual([
    { selector: ".den-scrollport", location: "fixture.css:2", declarations: [{ prop: "--label", value: '"border-radius: 5px"' }] },
    { selector: "&[open]", location: "fixture.css:5", declarations: [{ prop: "border-radius", value: "5px" }] },
  ]);
});
