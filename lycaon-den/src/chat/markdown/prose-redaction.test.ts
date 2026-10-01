// @vitest-environment jsdom
import DOMPurify from "isomorphic-dompurify";
import { describe, expect, it } from "vitest";
import type { RedactedSpan } from "../../api/types.ts";
import { prepareMarkdownSource } from "./markdown-output.ts";
import { renderMarkdownHtml } from "./markdown-render.ts";
import { paintProseRedaction, planProseRedaction } from "./prose-redaction.ts";

const MARKER = "[REDACTED]";

function span(start: number, over: Partial<RedactedSpan> = {}): RedactedSpan {
  return {
    field: "content",
    start,
    length: MARKER.length,
    kind: "secret",
    source: "shape_rule",
    rule_id: "kingfisher.aws.1",
    rule_title: "AWS Access Key",
    ...over,
  };
}

/** The whole prose path: plan, normalize, render, paint. */
function renderProse(
  content: string,
  spans: RedactedSpan[],
  opts: Parameters<typeof renderMarkdownHtml>[1] = {},
): HTMLElement {
  const plan = planProseRedaction(content, spans);
  const html = paintProseRedaction(
    renderMarkdownHtml(prepareMarkdownSource(plan?.source ?? content), opts),
    plan,
  );
  const host = document.createElement("div");
  host.innerHTML = html;
  return host;
}

function marks(host: HTMLElement): HTMLElement[] {
  return [...host.querySelectorAll<HTMLElement>(".den-redaction-mark")];
}

describe("host spans in prose", () => {
  it("paints the span the host stamped, keeping the marker as real text", () => {
    const content = `Deploy with ${MARKER} now`;
    const host = renderProse(content, [span(content.indexOf(MARKER))]);

    expect(marks(host)).toHaveLength(1);
    expect(marks(host)[0]?.textContent).toBe(MARKER);
    expect(marks(host)[0]?.getAttribute("data-tip")).toContain("AWS Access Key");
    expect(host.textContent?.trim()).toBe("Deploy with [REDACTED] now");
  });

  it("survives the heading rewrite that shifts every later offset", () => {
    // prepareMarkdownSource inserts a space after `##`, moving the marker.
    const content = `##Head\nDeploy with ${MARKER} now`;
    const host = renderProse(content, [span(content.indexOf(MARKER))]);

    expect(host.querySelector("h2")?.textContent).toBe("Head");
    expect(marks(host)[0]?.textContent).toBe(MARKER);
  });

  it("paints inside markdown structure without breaking it", () => {
    const content = `- **bold ${MARKER}** tail`;
    const host = renderProse(content, [span(content.indexOf(MARKER))]);

    expect(host.querySelector("li strong .den-redaction-mark")?.textContent).toBe(
      MARKER,
    );
  });

  it("paints every span in a multi-span row", () => {
    const content = `a ${MARKER} b ${MARKER} c`;
    const first = content.indexOf(MARKER);
    const host = renderProse(content, [
      span(first),
      span(content.indexOf(MARKER, first + 1)),
    ]);

    expect(marks(host)).toHaveLength(2);
    expect(host.textContent?.trim()).toBe("a [REDACTED] b [REDACTED] c");
  });

  it("counts runes, not UTF-16 units", () => {
    const content = `key 🔑 is ${MARKER} here`;
    const runes = Array.from(content);
    const start = runes.indexOf("[");
    const host = renderProse(content, [span(start)]);

    expect(marks(host)[0]?.textContent).toBe(MARKER);
  });

  it("reads a mask as a policy rather than a detection", () => {
    const content = `hidden ${MARKER} here`;
    const host = renderProse(content, [
      span(content.indexOf(MARKER), { kind: "observer_mask", source: "policy" }),
    ]);

    const mark = marks(host)[0];
    expect(mark?.classList.contains("den-redaction-mark--mask")).toBe(true);
    expect(mark?.getAttribute("data-tip")).toBe("Not shown to observers");
  });

  it("paints a protected value's reference as a readable reference mark", () => {
    const reference = "{{paintedwolf-secret:6f1c2b9e-0d4a-4c7e-9b1f-2a3d4e5f6a7b}}";
    const content = `STRIPE_KEY=${reference} in .env`;
    const host = renderProse(content, [
      span(content.indexOf(reference), {
        length: reference.length,
        kind: "managed_reference",
        source: "remembered_match",
      }),
    ]);

    const mark = marks(host)[0];
    expect(mark?.textContent).toBe(reference);
    expect(mark?.classList.contains("den-redaction-mark--reference")).toBe(true);
  });
});

describe("forged marks", () => {
  const forged = `A <span class="den-redaction-mark" data-redaction-kind="secret" data-tip="AWS Access Key">${MARKER}</span> b`;

  it("the sanitizer alone would let the model's own markup through", () => {
    const sanitized = DOMPurify.sanitize(renderMarkdownHtml(forged, {}));
    expect(sanitized).toContain("den-redaction-mark");
  });

  it("renders model-authored mark markup as inert text with no host metadata", () => {
    const host = renderProse(forged, []);

    expect(marks(host)).toHaveLength(0);
    expect(host.querySelector("[data-redaction-kind]")).toBeNull();
    expect(host.querySelector("[data-redaction-rule]")).toBeNull();
    // The words survive; only the claim to be a host mark is removed.
    expect(host.textContent).toContain(MARKER);
  });

  it("neutralizes forged markup in every spelling the sanitizer preserves", () => {
    const spellings = [
      `<SPAN CLASS="den-redaction-mark">${MARKER}</SPAN>`,
      `<span class='x den-redaction-mark y' data-redaction-rule='forged'>${MARKER}</span>`,
      `<span class="den-redaction-mark den-redaction-mark--mask">${MARKER}</span>`,
      `<span class="den-redaction-mark--reference">${MARKER}</span>`,
    ].join("\n\n");

    const host = renderProse(spellings, []);

    expect(marks(host)).toHaveLength(0);
    expect(host.querySelectorAll(".den-redaction-mark--mask")).toHaveLength(0);
    expect(host.querySelectorAll(".den-redaction-mark--reference")).toHaveLength(0);
    expect(host.querySelector("[data-redaction-rule]")).toBeNull();
  });

  it("keeps a forged mark inert on a row that also has a real one", () => {
    const content = `${forged} and real ${MARKER} end`;
    const runes = Array.from(content);
    // The host stamped only the trailing occurrence.
    const start = runes.length - Array.from(` ${MARKER} end`).length + 1;
    const host = renderProse(content, [span(start)]);

    expect(marks(host)).toHaveLength(1);
    expect(host.querySelectorAll("[data-redaction-kind]")).toHaveLength(1);
  });

  it("ignores a sentinel-shaped string the model wrote itself", () => {
    // Private-use delimiters with a guessed body: not a token this render wrote.
    const guessed = `x \uE000${"0".repeat(36)}\uE001 y`;
    const host = renderProse(`${guessed} and ${MARKER}`, [
      span(Array.from(`${guessed} and `).length),
    ]);

    expect(marks(host)).toHaveLength(1);
    expect(marks(host)[0]?.textContent).toBe(MARKER);
  });
});

describe("code blocks", () => {
  it("paints inside a fenced block without emitting literal markup", () => {
    const content = `intro\n\n\`\`\`\ncfg = ${MARKER}\n\`\`\``;
    const host = renderProse(content, [span(content.indexOf(MARKER))]);

    const code = host.querySelector("pre code");
    expect(code?.querySelector(".den-redaction-mark")?.textContent).toBe(MARKER);
    // The block reads as text, with no injected tag surfacing in it.
    expect(code?.textContent).toBe(`cfg = ${MARKER}\n`);
    expect(code?.textContent).not.toContain("<span");
  });

  it("paints inside an indented block", () => {
    const content = `intro\n\n    cfg = ${MARKER}\n`;
    const host = renderProse(content, [span(content.indexOf(MARKER))]);

    expect(host.querySelector("pre code .den-redaction-mark")?.textContent).toBe(
      MARKER,
    );
  });

  it("paints inside a language-tagged fence", () => {
    const content = `\`\`\`yaml\ntoken: ${MARKER}\n\`\`\``;
    const host = renderProse(content, [span(content.indexOf(MARKER))]);

    expect(host.querySelector("pre code .den-redaction-mark")?.textContent).toBe(
      MARKER,
    );
  });

  it("paints through the prose-path block renderer", () => {
    // An unlanguaged fence with a project id takes the path-linkifying override.
    const content = `\`\`\`\nsrc/app.ts\ncfg = ${MARKER}\n\`\`\``;
    const host = renderProse(content, [span(content.indexOf(MARKER))], {
      projectId: "proj-1",
    });

    expect(host.querySelector(".den-redaction-mark")?.textContent).toBe(MARKER);
  });

  it("paints inside an inline codespan", () => {
    const content = `use \`${MARKER}\` now`;
    const host = renderProse(content, [span(content.indexOf(MARKER))], {
      projectId: "proj-1",
    });

    expect(host.querySelector("code .den-redaction-mark")?.textContent).toBe(MARKER);
  });

  it("keeps a fenced mark copyable as its marker text", () => {
    // Keep marker text available to copy.
    const content = `\`\`\`\ncfg = ${MARKER}\n\`\`\``;
    const host = renderProse(content, [span(content.indexOf(MARKER))]);

    expect(host.querySelector("pre")?.textContent).toContain(MARKER);
  });

  it("paints a prose span and a fenced span on the same row", () => {
    const content = `see ${MARKER} here\n\n\`\`\`\ncfg = ${MARKER}\n\`\`\``;
    const first = content.indexOf(MARKER);
    const host = renderProse(content, [
      span(first),
      span(content.indexOf(MARKER, first + 1)),
    ]);

    expect(marks(host)).toHaveLength(2);
    expect(host.querySelector("pre code .den-redaction-mark")).toBeTruthy();
  });
});

describe("planProseRedaction", () => {
  it("plans nothing when the host stamped nothing", () => {
    expect(planProseRedaction("plain prose", [])).toBeNull();
  });

  it("leaves content untouched apart from the spans it replaces", () => {
    const content = `a ${MARKER} b`;
    const plan = planProseRedaction(content, [span(content.indexOf(MARKER))]);
    const token = [...(plan?.marks.keys() ?? [])][0] ?? "";

    expect(plan?.source).toBe(`a ${token} b`);
    expect(plan?.marks.get(token)?.text).toBe(MARKER);
  });

  it("uses a fresh token per render so one cannot be replayed", () => {
    const content = `a ${MARKER} b`;
    const at = content.indexOf(MARKER);
    const first = [...planProseRedaction(content, [span(at)])!.marks.keys()];
    const second = [...planProseRedaction(content, [span(at)])!.marks.keys()];

    expect(first[0]).not.toBe(second[0]);
  });
});
