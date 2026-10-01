import { execSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  genericTranscriptToolNames,
  lycaonCatalogToolNames,
  toolEvidenceKindMap,
  toolSchemaNames,
} from "./tool-catalog-tools.ts";
import { buildStructuredToolPresentation } from "./tool-part-structured.ts";
import {
  readablePresentationIssues,
  violatesStructuredToolPresentation,
} from "./tool-presentation-contract.ts";
import {
  classifyToolKind,
  isTaskToolName,
  type ToolPartView,
} from "./tool-part-model.ts";
import { TOOL_CARD_KINDS } from "./tool-presentation.generated.ts";
import {
  GIT_UNAVAILABLE_OUTPUT_FIXTURE,
  TOOL_GUIDANCE_OUTPUT_FIXTURE,
  TOOL_STRUCTURED_OUTPUT_FIXTURES,
} from "./tool-structured-output-fixtures.ts";
import { peelEvidenceHandleTag } from "./tool-output-handle.ts";

const toolSrc = import.meta.dirname;
const denSrc = join(toolSrc, "../..");

function read(path: string): string {
  return readFileSync(path, "utf8");
}

function rgForbidden(pattern: string, paths: string, extra = ""): string {
  return execSync(
    `rg -n '${pattern}' ${paths} ${extra} -g '!*invariant.test.ts' 2>/dev/null || true`,
    { encoding: "utf8", cwd: toolSrc },
  ).trim();
}

function part(
  tool: string,
  output: string,
  overrides: Partial<ToolPartView> = {},
): ToolPartView {
  return {
    id: `tc-${tool}`,
    toolCallId: `tc-${tool}`,
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool,
    kind: classifyToolKind(tool),
    status: "completed",
    output,
    error: null,
    ...overrides,
  };
}

function fixtureEvidenceHandle(
  tool: string,
  kindMap: Readonly<Record<string, string>>,
): string {
  return `${kindMap[tool] ?? "read"}#1`;
}

function tagFixtureOutput(handle: string, body: string): string {
  return `[${handle}]\n${body}`;
}

describe("tool structured output invariant", () => {
  it("routes generic tool cards only through StructuredToolBody", () => {
    const generic = read(join(denSrc, "components/tool/GenericToolCard.tsx"));
    expect(generic).toMatch(/StructuredToolBody/);
    expect(generic).not.toMatch(/ToolResultBody|ToolPartRawOutput/);

    const structured = read(join(denSrc, "components/tool/StructuredToolBody.tsx"));
    expect(structured).toMatch(/buildStructuredToolPresentation/);
    expect(structured).toMatch(/tool-presentation-contract/);
    expect(structured).toMatch(/ToolContentLink/);
    expect(structured).toMatch(/tool-related-search-link/);
    expect(structured).not.toMatch(/den-tool-part-card-nested-raw/);

    const builder = read(join(toolSrc, "tool-part-structured.ts"));
    expect(builder).toMatch(/tool-presentation-contract/);
    expect(builder).toMatch(/normalizeToolWireOutput|parseToolJsonObject/);

    const contract = read(join(toolSrc, "tool-presentation-contract.ts"));
    expect(contract).toMatch(/normalizeToolWireOutput/);
    expect(contract).toMatch(/stripReadLineNumbers/);
  });

  it("forbids parallel tool output render components", () => {
    for (const dir of ["components", "files"]) {
      expect(rgForbidden("ToolResultBody|ToolPartRawOutput", join(denSrc, dir))).toBe("");
    }
  });

  it("ToolPartCard sends non-task tools to GenericToolCard", () => {
    const card = read(join(denSrc, "components/tool/ToolPartCard.tsx"));
    expect(card).toMatch(/isTaskToolPart/);
    expect(card).toMatch(/GenericToolCard/);
  });

  it("renders every kind the catalog declares", () => {
    for (const [tool, kind] of Object.entries(TOOL_CARD_KINDS)) {
      expect(classifyToolKind(tool), `${tool} declares kind ${kind}`).toBe(kind);
    }
  });

  it("has structured-output fixtures for every tools/schemas tool", () => {
    for (const tool of toolSchemaNames()) {
      if (isTaskToolName(tool)) continue;
      expect(TOOL_STRUCTURED_OUTPUT_FIXTURES[tool], tool).toBeDefined();
    }
  });

  it("has structured-output fixtures for every lycaon-tools.yaml catalog tool", () => {
    for (const tool of lycaonCatalogToolNames()) {
      if (tool === "delegate_dispatch") continue;
      expect(TOOL_STRUCTURED_OUTPUT_FIXTURES[tool], tool).toBeDefined();
    }
  });

  it("keeps fixture keys aligned with generic transcript tool union", () => {
    const catalog = new Set(genericTranscriptToolNames());
    const fixtureKeys = Object.keys(TOOL_STRUCTURED_OUTPUT_FIXTURES).sort();
    for (const tool of fixtureKeys) {
      expect(catalog.has(tool), `orphan fixture: ${tool}`).toBe(true);
    }
    for (const tool of catalog) {
      expect(
        TOOL_STRUCTURED_OUTPUT_FIXTURES[tool],
        `missing fixture: ${tool}`,
      ).toBeDefined();
    }
  });

  it("formats agent guidance without raw disclosure", () => {
    const view = buildStructuredToolPresentation(
      part("read", TOOL_GUIDANCE_OUTPUT_FIXTURE, {
        status: "error",
        outcome: "rejected",
        codes: ["PATH_OUTSIDE_JAIL"],
      }),
    );
    expect(violatesStructuredToolPresentation(view, TOOL_GUIDANCE_OUTPUT_FIXTURE)).toBeNull();
    expect(view.rawOutputAvailable).toBe(false);
  });

  it("does not infer guidance from arbitrary tool output markers", () => {
    const output = "Rejected: this text came from an external tool";
    const view = buildStructuredToolPresentation(part("read", output));
    expect(view.guidance).toBe(false);
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("formats git unavailable JSON without inline JSON body", () => {
    const view = buildStructuredToolPresentation(
      part("git_status", GIT_UNAVAILABLE_OUTPUT_FIXTURE),
    );
    expect(
      violatesStructuredToolPresentation(view, GIT_UNAVAILABLE_OUTPUT_FIXTURE),
    ).toBeNull();
  });

  it("renders managed-secret lifecycle metadata without a value surface", () => {
    for (const tool of [
      "secret_generate",
      "secret_list",
      "secret_revoke",
    ] as const) {
      const output = TOOL_STRUCTURED_OUTPUT_FIXTURES[tool]!;
      const view = buildStructuredToolPresentation(part(tool, output));
      const rendered = JSON.stringify(view.sections);
      expect(rendered, tool).toContain("{{paintedwolf-secret:");
      expect(output, tool).not.toMatch(/"value"\s*:/);
      expect(view.rawOutputAvailable, tool).toBe(false);
      expect(view.rawOutputProtected, tool).toBe(true);
    }
  });

  it("renders http_request arguments and response fields as a first-class tool", () => {
    const output = TOOL_STRUCTURED_OUTPUT_FIXTURES.http_request!;
    const view = buildStructuredToolPresentation(
      part("http_request", output, {
        args: {
          url: "https://api.example.com/v1/jobs",
          method: "POST",
          headers: [
            { name: "Content-Type", value: "application/json" },
            { name: "Authorization", value: "Bearer do-not-render" },
          ],
          body_text: "private request body",
          timeout_ms: 5000,
          redirects: "none",
          response_body: "include",
          capability_request: { loopback_connect: { ports: [8080] } },
          unredact: "receipt-do-not-render",
        },
      }),
    );

    expect(view.argsFacts).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          label: "URL",
          value: "https://api.example.com/v1/jobs",
        }),
        expect.objectContaining({ label: "Method", value: "POST" }),
        expect.objectContaining({
          label: "Headers",
          value: "Content-Type, Authorization",
        }),
        expect.objectContaining({
          label: "Body",
          value: "Text supplied · 20 characters",
        }),
        expect.objectContaining({ label: "Timeout", value: "5000 ms" }),
        expect.objectContaining({ label: "Loopback ports", value: "8080" }),
        expect.objectContaining({
          label: "Redaction receipt",
          value: "Provided",
        }),
      ]),
    );
    expect(JSON.stringify(view.argsFacts)).not.toContain("do-not-render");
    expect(JSON.stringify(view.argsFacts)).not.toContain("private request body");

    const resultFacts = view.sections.find((section) => section.kind === "facts");
    expect(resultFacts?.kind === "facts" ? resultFacts.facts : []).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Status", value: "202" }),
        expect.objectContaining({
          label: "Final URL",
          value: "https://api.example.com/v1/jobs",
        }),
        expect.objectContaining({ label: "Duration", value: "84 ms" }),
        expect.objectContaining({ label: "Bytes", value: "17 B" }),
        expect.objectContaining({
          label: "Content type",
          value: "application/json",
        }),
        expect.objectContaining({ label: "Body encoding", value: "utf-8" }),
      ]),
    );
    expect(view.sections).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ kind: "content", label: "Headers" }),
        expect.objectContaining({ kind: "content", label: "Body" }),
      ]),
    );
  });

  it.each(genericTranscriptToolNames())(
    "structured presentation for %s",
    (tool) => {
      const output = TOOL_STRUCTURED_OUTPUT_FIXTURES[tool]!;
      const view = buildStructuredToolPresentation(part(tool, output));
      expect(
        violatesStructuredToolPresentation(view, output),
        `${tool} fixture violates structured presentation contract`,
      ).toBeNull();
      expect(
        readablePresentationIssues(view, output),
        `${tool} fixture readability`,
      ).toEqual([]);
    },
  );

  it.each(genericTranscriptToolNames())(
    "evidence-tagged structured presentation for %s",
    (tool) => {
      const output = TOOL_STRUCTURED_OUTPUT_FIXTURES[tool]!;
      const kindMap = toolEvidenceKindMap();
      const handle = fixtureEvidenceHandle(tool, kindMap);
      const tagged = tagFixtureOutput(handle, output);
      const view = buildStructuredToolPresentation(part(tool, tagged));
      const baseline = buildStructuredToolPresentation(part(tool, output));

      expect(view.evidenceHandle).toBe(handle);
      expect(peelEvidenceHandleTag(tagged).body).toBe(output);
      expect(
        violatesStructuredToolPresentation(view, tagged),
        `${tool} tagged fixture violates structured presentation contract`,
      ).toBeNull();
      expect(
        readablePresentationIssues(view, tagged),
        `${tool} tagged fixture readability`,
      ).toEqual([]);
      if (view.rawOutputProtected) {
        expect(view.rawOutputAvailable).toBe(false);
      } else {
        expect(view.rawOutputAvailable).toBe(true);
      }
      expect(view.argsFacts).toEqual(baseline.argsFacts);
      expect(view.sections).toEqual(baseline.sections);

      for (const section of view.sections) {
        if (section.kind !== "content") continue;
        expect(section.text, `${tool} must not show escaped JSON newlines`).not.toMatch(
          /\\n/,
        );
      }
    },
  );

  it.each(genericTranscriptToolNames())(
    "universal section pipeline for %s — labeled content without a Pack pane",
    (tool) => {
      const output = TOOL_STRUCTURED_OUTPUT_FIXTURES[tool]!;
      const view = buildStructuredToolPresentation(part(tool, output));
      for (const section of view.sections) {
        if (section.kind !== "content") continue;
        expect(section.label ?? "", `${tool} must not use a Pack pane`).not.toBe(
          "Pack",
        );
        expect(
          section.text.includes("## Identity") ||
            section.text.includes("## Substance") ||
            section.text.includes("## Skeleton"),
          `${tool} must not glue pack tiers into one markdown dump`,
        ).toBe(false);
      }
    },
  );

  it("read and summarize fixtures use universal labeled body panels", () => {
    const readView = buildStructuredToolPresentation(
      part("read", TOOL_STRUCTURED_OUTPUT_FIXTURES.read!),
    );
    const readBody = readView.sections.find(
      (s) => s.kind === "content",
    );
    expect(readBody).toBeTruthy();
    expect(readBody && readBody.kind === "content" ? readBody.text : "").toContain(
      "# Hello",
    );
    expect(readBody && readBody.kind === "content" ? readBody.text : "").not.toContain(
      "1\t",
    );

    const summarizeView = buildStructuredToolPresentation(
      part("summarize", TOOL_STRUCTURED_OUTPUT_FIXTURES.summarize!),
    );
    const facts = summarizeView.sections.find((s) => s.kind === "facts");
    expect(facts && facts.kind === "facts" ? facts.facts : []).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Task" }),
        expect.objectContaining({ label: "Coverage", value: "2 of 12" }),
      ]),
    );
    const labeled = summarizeView.sections.filter(
      (s) => s.kind === "content" && !!s.label,
    );
    expect(labeled.map((s) => (s.kind === "content" ? s.label : ""))).toEqual(
      expect.arrayContaining([
        "Identity",
        "Skeleton",
        "Anchors",
        "Next actions",
        "Sources touched",
      ]),
    );
    const substance = summarizeView.sections.filter(
      (s) => s.kind === "content",
    );
    expect(substance.length).toBeGreaterThanOrEqual(1);
    expect(
      substance.some(
        (s) =>
          s.kind === "content" &&
          s.text.includes("func FromEnv()") &&
          (s.label ?? "").includes("env.go"),
      ),
    ).toBe(true);
    expect(
      summarizeView.sections.some(
        (s) => s.kind === "content" && s.label === "Pack",
      ),
    ).toBe(false);
  });

  it("nested pack objects lift through the same pipeline as top-level arrays", () => {
    const view = buildStructuredToolPresentation(
      part(
        "find",
        JSON.stringify({
          pack: {
            identity: [{ path: "a.go", kind: "file", line_count: 3 }],
            substance: [
              {
                path: "a.go",
                start_line: 1,
                end_line: 2,
                body: "package main\n",
              },
            ],
          },
          selected: 1,
          total: 2,
        }),
      ),
    );
    expect(
      view.sections.some(
        (s) => s.kind === "facts" && s.facts.some((f) => f.label === "Coverage"),
      ),
    ).toBe(true);
    expect(
      view.sections.some(
        (s) =>
          s.kind === "content" &&
          s.label === "Identity" &&
          s.text.includes("a.go"),
      ),
    ).toBe(true);
    expect(
      view.sections.some(
        (s) =>
          s.kind === "content" &&
          s.text.includes("package main"),
      ),
    ).toBe(true);
  });
});
