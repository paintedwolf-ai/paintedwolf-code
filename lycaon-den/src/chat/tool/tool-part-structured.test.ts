import { afterEach, describe, expect, it } from "vitest";
import {
  buildStructuredToolPresentation,
  resetStructuredToolPresentationCacheForTests,
} from "./tool-part-structured.ts";
import type { ToolPartView } from "./tool-part-model.ts";

function part(overrides: Partial<ToolPartView> = {}): ToolPartView {
  return {
    id: "tc1",
    toolCallId: "tc1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "workflow_advance",
    kind: "generic",
    status: "completed",
    ...overrides,
  };
}

describe("buildStructuredToolPresentation", () => {
  afterEach(() => {
    resetStructuredToolPresentationCacheForTests();
  });

  it("reuses the cached presentation when content is unchanged across part objects", () => {
    const args = { path: "src/main.go" };
    const output = JSON.stringify({
      ok: true,
      phase: "stub",
      message: "cached",
      paths: Array.from({ length: 20 }, (_, i) => `f${i}.ts`),
    });
    const first = buildStructuredToolPresentation(
      part({ id: "cache-a", args, output }),
    );
    const second = buildStructuredToolPresentation(
      part({ id: "cache-a", args, output }),
    );
    expect(second).toBe(first);

    const updated = buildStructuredToolPresentation(
      part({ id: "cache-a", args, output: JSON.stringify({ ok: true, phase: "next" }) }),
    );
    expect(updated).not.toBe(first);
  });

  it("presents http_request credentials, jar, form, and receipt without values", () => {
    const view = buildStructuredToolPresentation(
      part({
        id: "http-1",
        tool: "http_request",
        args: {
          url: "http://localhost:5555/api/v1/crates/new",
          method: "PUT",
          query: [{ name: "dry_run", value: "yes" }],
          auth: { scheme: "basic", username: "admin", password: "hunter2-secret" },
          form: [
            { name: "version", value: "1.2.3" },
            { name: "package", path: "dist/app.crate" },
          ],
          cookie_jar: "registry",
          response_path: "tmp/receipt.json",
        },
        output: JSON.stringify({
          status: 201,
          final_url: "http://localhost:5555/api/v1/crates/new?dry_run=yes",
          bytes: 12,
          sha256: "ab".repeat(32),
          response_path: "tmp/receipt.json",
          written: true,
          cookies: {
            jar: "registry",
            reference: "{{paintedwolf-secret:0}}",
            sent: 1,
            stored: 2,
            names: ["sessionid", "csrftoken"],
            persisted: true,
          },
        }),
      }),
    );
    const rendered = JSON.stringify(view);
    expect(rendered).not.toContain("hunter2-secret");
    // Basic-auth usernames can contain API keys.
    expect(rendered).not.toContain("admin");
    const labels = view.argsFacts.map((fact) => `${fact.label}=${fact.value}`);
    expect(labels).toContain("Query=dry_run");
    expect(labels).toContain("Auth=Basic");
    expect(labels).toContain("Form fields=version");
    expect(labels).toContain("Form file · package=dist/app.crate");
    expect(labels).toContain("Cookie jar=registry");
    expect(labels).toContain("Response file=tmp/receipt.json");
    const facts = view.sections.flatMap((section) =>
      section.kind === "facts" ? section.facts : [],
    );
    const factLabels = facts.map((fact) => `${fact.label}=${fact.value}`);
    expect(factLabels).toContain("Cookie jar=registry · 1 sent · 2 stored");
    expect(factLabels).toContain("Cookies held=sessionid, csrftoken");
  });

  it("marks a cookie jar the exchange could not write back", () => {
    const view = buildStructuredToolPresentation(
      part({
        id: "http-2",
        tool: "http_request",
        args: { url: "http://localhost:5555/login", method: "POST", cookie_jar: "registry" },
        output: JSON.stringify({
          status: 200,
          final_url: "http://localhost:5555/login",
          cookies: {
            jar: "registry",
            sent: 0,
            stored: 1,
            persisted: false,
            error: "managed secret store is unavailable",
          },
        }),
      }),
    );
    const facts = view.sections
      .flatMap((section) => (section.kind === "facts" ? section.facts : []))
      .map((fact) => `${fact.label}=${fact.value}`);
    expect(facts).toContain("Cookie jar=registry · 0 sent · 1 stored · not persisted");
  });

  it("names the host file holding a body too large to inline", () => {
    const view = buildStructuredToolPresentation(
      part({
        id: "http-3",
        tool: "http_request",
        args: { url: "https://api.example.test/dump" },
        output: JSON.stringify({
          status: 200,
          final_url: "https://api.example.test/dump",
          bytes: 918_273,
          body_omitted: true,
          body_encoding: "utf-8",
          body_spill_path: "tool-output/abc123.txt",
        }),
      }),
    );
    const facts = view.sections
      .flatMap((section) => (section.kind === "facts" ? section.facts : []))
      .map((fact) => `${fact.label}=${fact.value}`);
    expect(facts).toContain("Body file=tool-output/abc123.txt");
  });

  it("renders a recalled observation with its provenance and staleness", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "recall",
        args: { query: "resolveWriteRoots" },
        output: JSON.stringify({
          resolution: "matched",
          scope_stated: "this session and its worker legs, finished ones included",
          count: { value: 1, relation: "exact" },
          hits: [
            {
              handle: "read#3",
              hit_id: "h1",
              kind: "read",
              agent_type: "scout",
              observed_at: "2026-08-18T10:00:00Z",
              path: "internal/confine/confine.go",
              line: 214,
              currency: "changed",
              body: ["resolveWriteRoots skips the jail check"],
            },
          ],
        }),
      }),
    );
    const rendered = JSON.stringify(view.sections);
    // An excerpt alone would read as current.
    expect(rendered).toContain("scout");
    expect(rendered).toContain("read#3");
    expect(rendered).toContain("internal/confine/confine.go:214");
    expect(rendered).toContain("changed");
    expect(rendered).toContain("resolveWriteRoots skips the jail check");
    // Session ids are routing detail, not card copy.
    expect(rendered).not.toContain("hit_id");
  });

  // A message-kind hit from this chat carries neither handle nor agent type.
  it("renders a recalled row that names no tool and no agent", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "recall",
        args: { query: "config dir" },
        output: JSON.stringify({
          resolution: "matched",
          count: { value: 1, relation: "exact" },
          hits: [
            {
              hit_id: "hit-2",
              kind: "note",
              mine: true,
              observed_at: "2026-08-19T20:52:04Z",
              snippet: "config dir resolves before the store opens",
            },
          ],
        }),
      }),
    );
    const rendered = JSON.stringify(view.sections);
    expect(rendered).toContain("this chat");
    expect(rendered).toContain("config dir resolves before the store opens");
    expect(rendered).not.toContain("hit-2");
  });

  it("stays silent about staleness when nothing wrote the path since", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "recall",
        args: { query: "needle" },
        output: JSON.stringify({
          resolution: "matched",
          count: { value: 1, relation: "exact" },
          hits: [
            {
              handle: "read#1",
              hit_id: "h2",
              agent_type: "scout",
              observed_at: "2026-08-18T10:00:00Z",
              path: "a.go",
              currency: "unchanged",
              snippet: "needle here",
            },
          ],
        }),
      }),
    );
    expect(JSON.stringify(view.sections)).not.toContain("unchanged");
  });

  it("preserves source text in a content document", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "read",
        kind: "read",
        args: { path: "src/main.go" },
        output: "package main\n",
      }),
    );
    expect(view.argsFacts.some((f) => f.label === "Path")).toBe(true);
    expect(view.sections[0]).toMatchObject({
      kind: "content",
      text: "package main\n",
    });
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("marks input path args as typed path facts for SourcePathLink", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "read",
        kind: "read",
        args: { path: "src/main.go", file_path: "src/x.ts" },
        output: "package main\n",
      }),
    );
    const pathFact = view.argsFacts.find((f) => f.label === "Path");
    expect(pathFact?.path).toEqual({ path: "src/main.go", entryKind: "file" });
    // Both path and file_path display as "Path".
    expect(view.argsFacts.some((f) => f.path?.path === "src/main.go")).toBe(true);
    expect(view.argsFacts.some((f) => f.path?.path === "src/x.ts")).toBe(true);
  });

  it("marks JSON path/file_path fields as typed path facts", () => {
    const view = buildStructuredToolPresentation(
      part({
        output: JSON.stringify({ ok: true, path: "src/foo.ts", status: "clean" }),
      }),
    );
    const facts = view.sections.find((s) => s.kind === "facts");
    const pathFact = (facts?.kind === "facts" ? facts.facts : []).find(
      (f) => f.label === "Path",
    );
    expect(pathFact?.path).toEqual({ path: "src/foo.ts" });
  });

  it.each(["state_query", "state_update"])("keeps %s scaffold paths out of source navigation", (tool) => {
    const view = buildStructuredToolPresentation(part({ tool, args: { path: "progress.current" }, output: JSON.stringify({ path: "progress.current", value: "review" }) }));
    expect(view.argsFacts.find((fact) => fact.value === "progress.current")?.path).toBeUndefined();
    for (const section of view.sections) {
      if (section.kind === "facts") expect(section.facts.every((fact) => !fact.path)).toBe(true);
    }
  });

  it("marks list_dir input as a folder even without a settled result", () => {
    const view = buildStructuredToolPresentation(part({ tool: "list_dir", args: { path: "src" }, output: "", status: "running" }));
    expect(view.argsFacts.find((fact) => fact.path)?.path).toEqual({ path: "src", entryKind: "folder" });
  });

  it("marks a jq_edit document path as a file from the generated presentation catalog", () => {
    const view = buildStructuredToolPresentation(part({ tool: "jq_edit", kind: "write", args: { path: "package.json", query: ".version = \"2\"" }, output: "", status: "running" }));
    expect(view.argsFacts.find((fact) => fact.path)?.path).toEqual({ path: "package.json", entryKind: "file" });
  });

  it("types only the path argument, not a destination the catalog does not describe", () => {
    const view = buildStructuredToolPresentation(part({ tool: "extract_archive", args: { path: "dist.zip", dest: "out" }, output: "", status: "running" }));
    expect(view.argsFacts.find((fact) => fact.path?.path === "dist.zip")?.path).toEqual({ path: "dist.zip", entryKind: "file" });
    expect(view.argsFacts.find((fact) => fact.path?.path === "out")?.path).toEqual({ path: "out" });
  });

  it("preserves root and directory metadata from path records", () => {
    const view = buildStructuredToolPresentation(part({ args: { path: "src", root_id: "other" }, output: JSON.stringify({ path: "src", root_id: "other", is_dir: true }) }));
    expect(view.argsFacts.find((fact) => fact.path)?.path).toEqual({ path: "src", rootId: "other" });
    const facts = view.sections.find((section) => section.kind === "facts");
    expect(facts?.kind === "facts" && facts.facts.find((fact) => fact.path)?.path).toEqual({ path: "src", rootId: "other", entryKind: "folder" });
  });

  it("presents survey receipt facts with a navigable path", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "scan",
        kind: "generic",
        output: JSON.stringify({
          receipt: { tool: "grep", path: "src/scan.go", bytes_returned: 42, paths_touched: 3, truncated: true },
        }),
      }),
    );
    const facts = view.sections.find((s) => s.kind === "facts");
    const pathFact = (facts?.kind === "facts" ? facts.facts : []).find(
      (f) => f.label === "Path",
    );
    expect(pathFact?.path).toEqual({ path: "src/scan.go" });
    expect(facts?.kind === "facts" ? facts.facts : []).toEqual(expect.arrayContaining([
      { label: "Survey", value: "grep" },
      { label: "Bytes", value: "42" },
      { label: "Paths touched", value: "3" },
      { label: "Truncated", value: "Yes" },
    ]));
  });

  it("does not mark the wire spill path as a typed path fact (host-data-relative)", () => {
    // Spill paths resolve under host data, outside project source navigation.
    const output = JSON.stringify({ ok: true, wire_spill_path: "tool-spills/tc1.json" });
    const view = buildStructuredToolPresentation(
      part({ tool: "command", kind: "command", args: { command: "x" }, output }),
    );
    const factsSection = view.sections.find((s) => s.kind === "facts");
    const facts = factsSection?.kind === "facts" ? factsSection.facts : [];
    const spill = facts.find((f) => f.hostDataSpill);
    expect(spill).toBeTruthy();
    expect(spill?.value).toBe("tool-spills/tc1.json");
    expect(spill?.path).toBeUndefined();
  });

  it("labels read-range documents with their source line range", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "read",
        kind: "read",
        args: { path: "src/foo.go" },
        output: JSON.stringify({
          path: "src/foo.go",
          ranges: [
            { offset: 42, end_line: 44, content: "func foo() {}\n" },
          ],
        }),
      }),
    );
    const rangeSection = view.sections.find(
      (s) => s.kind === "content" && s.label === "lines 42–44",
    );
    expect(rangeSection).toMatchObject({ text: "func foo() {}\n" });
  });

  it("labels substance documents with their source path and range", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "outline",
        kind: "generic",
        output: JSON.stringify({
          substance: [
            { path: "src/bar.ts", start_line: 10, end_line: 12, body: "x\n" },
          ],
        }),
      }),
    );
    const subSection = view.sections.find(
      (s) => s.kind === "content" && s.label === "src/bar.ts:10–12",
    );
    expect(subSection).toMatchObject({ text: "x" });
  });

  it("parses compacted read JSON into readable markdown without escaped wire", () => {
    const output = `[compacted tool_result — original ~100 tokens; map + verbatim head/tail below are the working set]\n\nverbatim head/tail:\n[read#1]\n${JSON.stringify({
      path: "README.md",
      content: "1\t# Hello\n2\t\n3\tWorld\n",
    })}`;
    const view = buildStructuredToolPresentation(
      part({ tool: "read", kind: "read", args: { path: "README.md" }, output }),
    );
    expect(view.evidenceHandle).toBe("read#1");
    expect(view.sections.some((s) => s.kind === "compaction")).toBe(true);
    const body = view.sections.find(
      (s) => s.kind === "content",
    );
    expect(body).toMatchObject({
      kind: "content",
      text: "# Hello\n\nWorld\n",
    });
    expect(view.sections.every((s) => s.kind !== "content" || !s.text.includes('{"content"'))).toBe(
      true,
    );
  });

  it("extracts workflow JSON into facts and tucks full payload under raw", () => {
    const view = buildStructuredToolPresentation(
      part({
        output: JSON.stringify({
          ok: true,
          phase: "stub",
          message: "Gate plan_stub_valid not satisfied",
          failed_gate: "plan_stub_valid",
        }),
      }),
    );
    const facts = view.sections.find((s) => s.kind === "facts");
    expect(facts && facts.kind === "facts" ? facts.facts : []).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Result", value: "OK" }),
        expect.objectContaining({ label: "Phase", value: "stub" }),
        expect.objectContaining({ label: "Failed gate", value: "plan_stub_valid" }),
      ]),
    );
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("does not promote opaque command network rows into wire presentation", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        output: JSON.stringify({
          command: "npm install",
          ok: true,
          network: [
            { host: "registry.npmjs.org", allowed: true },
            { host: "telemetry.evil", allowed: false },
          ],
        }),
      }),
    );
    const net = view.sections.find((s) => s.kind === "network");
    expect(net).toBeUndefined();
  });

  it("uses host-stamped external access as the only network section", () => {
    const externalAccess = {
      modes: ["mediated_http" as const],
      visibility_summary: "observed" as const,
      endpoints: [
        {
          host: "registry.npmjs.org",
          port: 443,
          transport: "http_connect" as const,
          visibility: "observed" as const,
          decision: "allow" as const,
          attempt_count: 1,
        },
      ],
    };
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        output: JSON.stringify({
          network: [{ host: "wrong.example", allowed: true }],
        }),
        externalAccess,
      }),
    );
    const sections = view.sections.filter(
      (section) => section.kind === "network",
    );
    expect(sections).toEqual([{ kind: "network", externalAccess }]);
  });

  it("states what the local AI ranked inside a call, and nothing when it answered nothing", () => {
    const factsOf = (output: string) => {
      const view = buildStructuredToolPresentation(part({ tool: "summarize", kind: "generic", output }));
      const facts = view.sections.find((s) => s.kind === "facts");
      return facts && facts.kind === "facts" ? facts.facts : [];
    };
    expect(
      factsOf(JSON.stringify({ task: "retry", rerank: { engine: "Bialy", sites: ["summarize_definitions", "summarize_structure"], candidates: 70, scored: 48, elapsed_ms: 150 } })),
    ).toEqual(expect.arrayContaining([{ label: "Local AI", value: "Ranked 48 of 70 candidates across 2 lists · 150 ms", wide: true }]));
    const withoutEngine = factsOf(JSON.stringify({ task: "retry", ok: true }));
    expect(withoutEngine.some((fact) => fact.label === "Local AI")).toBe(false);
  });

  it("lists the tools a request loaded once, beside the model's need", () => {
    const view = buildStructuredToolPresentation(part({
      tool: "request_tools", kind: "generic", args: { need: "git tools" },
      output: JSON.stringify({ need: "git tools", loaded: ["git_commit", "git_status"], already_loaded: ["git_diff"], note: "Loaded schemas are on the next model call." }),
    }));
    const facts = view.sections.find((s) => s.kind === "facts");
    expect(facts && facts.kind === "facts" ? facts.facts : []).toEqual([
      { label: "Loaded", value: "git_commit, git_status", wide: true },
      { label: "Already loaded", value: "git_diff", wide: true },
    ]);
    expect(view.sections.some((s) => s.kind === "facts" && s.facts.some((f) => f.label === "Need"))).toBe(false);
    expect(view.sections.some((s) => s.kind === "note" && s.text === "Loaded schemas are on the next model call.")).toBe(true);
  });

  it("surfaces sandbox confinement as a fact, and flags unconfined runs", () => {
    const factsOf = (output: string) => {
      const view = buildStructuredToolPresentation(
        part({ tool: "command", kind: "command", output }),
      );
      const facts = view.sections.find((s) => s.kind === "facts");
      return facts && facts.kind === "facts" ? facts.facts : [];
    };
    expect(
      factsOf(JSON.stringify({ command: "x", ok: true, confined: true, network_posture: "observe" })),
    ).toEqual(expect.arrayContaining([{ label: "Containment", value: "on · observe" }]));
    // Broker posture describes mediated traffic; the mode describes confinement.
    expect(
      factsOf(
        JSON.stringify({
          command: "x",
          ok: true,
          confined: true,
          network_mode: "proxy_only",
          network_posture: "observe",
        }),
      ),
    ).toEqual(
      expect.arrayContaining([{ label: "Containment", value: "on · proxy_only · observe" }]),
    );
    expect(
      factsOf(JSON.stringify({ command: "x", ok: true, confined: false })).some(
        (f) => f.label === "Containment" && f.value.includes("unconfined"),
      ),
    ).toBe(true);
  });

  it("surfaces the complete downloaded-code boundary as a durable fact", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        output: JSON.stringify({
          ok: true,
          confined: true,
          network_mode: "proxy_only",
          remote_package_execution: {
            environment: "reduced",
            ambient_credentials_removed: true,
            protected_reads_denied: true,
            network_scope: "registry_only",
            allowed_hosts: ["proxy.golang.org", "sum.golang.org"],
          },
        }),
      }),
    );
    const section = view.sections.find((candidate) => candidate.kind === "facts");
    const facts = section?.kind === "facts" ? section.facts : [];
    expect(facts).toEqual(
      expect.arrayContaining([{
        label: "Downloaded code",
        value:
          "reduced environment · no inherited credentials · protected reads require approval · network only to proxy.golang.org, sum.golang.org",
      }]),
    );
  });

  it.each(["/fixture/cacert.pem", "/fixture/credentials"])(
    "shows the approved protected read exception %s in command results",
    (path) => {
      const view = buildStructuredToolPresentation(part({
        tool: "command", kind: "command",
        output: JSON.stringify({
          ok: true,
          remote_package_execution: {
            protected_reads_denied: true,
            approved_read_paths: [path],
          },
        }),
      }));
      const section = view.sections.find((candidate) => candidate.kind === "facts");
      expect(section?.kind === "facts" ? section.facts : []).toContainEqual({
        label: "Downloaded code",
        value: `approved read access to ${path} · other protected reads require approval`,
      });
    },
  );

  it("surfaces verify host-confirmation as a fact", () => {
    const factsOf = (output: string) => {
      const view = buildStructuredToolPresentation(
        part({ tool: "verify", kind: "generic", output }),
      );
      const facts = view.sections.find((s) => s.kind === "facts");
      return facts && facts.kind === "facts" ? facts.facts : [];
    };
    expect(
      factsOf(JSON.stringify({ command: "./task check", exit_code: 0, passed: true, confirmed: true })),
    ).toEqual(expect.arrayContaining([{ label: "Verification", value: "host-confirmed" }]));
    expect(
      factsOf(JSON.stringify({ command: "echo ok", exit_code: 0, passed: true, confirmed: false })),
    ).toEqual(expect.arrayContaining([{ label: "Verification", value: "not host-confirmed" }]));
  });

  it("preserves generic prose with raw output access", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "record_finding",
        kind: "generic",
        output: "Summary:\n- done",
      }),
    );
    expect(view.sections[0]).toMatchObject({
      kind: "content",
      text: "Summary:\n- done",
    });
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("preserves write-result prose with raw output access", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "write",
        kind: "write",
        output: "# Plan\n\n- step",
      }),
    );
    expect(view.sections[0]).toMatchObject({
      kind: "content",
      text: "# Plan\n\n- step",
    });
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("extracts empty JSON arrays as a readable note with raw tucked away", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "grep",
        kind: "generic",
        output: JSON.stringify({ entries: [] }),
      }),
    );
    expect(
      view.sections.some((s) => s.kind === "note" && s.text.includes("none")),
    ).toBe(true);
    expect(view.rawOutputAvailable).toBe(true);
    expect(
      view.sections.every(
        (s) =>
          !(
            s.kind === "content" &&
            s.text.trim().startsWith("{")
          ),
      ),
    ).toBe(true);
  });

  it("formats find results as a list with raw JSON disclosure", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "find",
        kind: "generic",
        output: JSON.stringify({
          results: [
            { path: "src/main.go", type: "file" },
            { path: "internal", type: "dir" },
          ],
          truncated: false,
        }),
      }),
    );
    const content = view.sections.find((s) => s.kind === "content");
    expect(content && content.kind === "content" ? content.text : "").toContain(
      "src/main.go",
    );
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("renders digests as content, not multi-line facts", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "find",
        kind: "generic",
        output: JSON.stringify({
          results: [{ path: "a.go", type: "file", size: 12 }],
          digest: "find total=1\n- a.go",
          highlights: [{ path: "a.go", why: "match" }],
        }),
      }),
    );
    const facts = view.sections.find((s) => s.kind === "facts");
    expect(
      facts && facts.kind === "facts"
        ? facts.facts.every((f) => !f.value.includes("\n"))
        : true,
    ).toBe(true);
    const digest = view.sections.find(
      (s) => s.kind === "content" && s.text.includes("find total=1"),
    );
    expect(digest).toBeTruthy();
    const list = view.sections
      .filter((s) => s.kind === "content")
      .map((s) => (s.kind === "content" ? s.text : ""))
      .join("\n");
    expect(list).toContain("a.go (file");
    expect(list).toContain("a.go — match");
  });

  it("coalesces command_output chunks into readable output", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command_output",
        kind: "generic",
        output: JSON.stringify({
          handle: "h1",
          chunks: [
            { stream: "stdout", text: "hello\n" },
            { stream: "stderr", text: "oops\n" },
          ],
        }),
      }),
    );
    const out = view.sections.find(
      (s) => s.kind === "content",
    );
    expect(out && out.kind === "content" ? out.text : "").toContain("hello");
    expect(out && out.kind === "content" ? out.text : "").toContain("stderr");
    expect(out && out.kind === "content" ? out.text : "").not.toMatch(/Cursor:/);
  });

  it("includes web_search snippets and overlay path lists", () => {
    const search = buildStructuredToolPresentation(
      part({
        tool: "web_search",
        kind: "generic",
        output: JSON.stringify({
          results: [
            {
              title: "Docs",
              url: "https://example.com",
              snippet: "Useful docs",
            },
          ],
        }),
      }),
    );
    expect(
      search.sections
        .filter((s) => s.kind === "content")
        .map((s) => (s.kind === "content" ? s.text : ""))
        .join("\n"),
    ).toContain("Useful docs");

    const promote = buildStructuredToolPresentation(
      part({
        tool: "promote_overlay",
        kind: "generic",
        output: JSON.stringify({
          merge_status: "merged",
          applied: ["a.go", "b.go"],
          clean_paths: ["a.go"],
        }),
      }),
    );
    const paths = promote.sections
      .filter((s) => s.kind === "content")
      .map((s) => (s.kind === "content" ? s.text : ""))
      .join("\n");
    expect(paths).toContain("a.go");
    expect(paths).toContain("b.go");
  });

  it("formats command JSON into tail output and raw disclosure", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        output: JSON.stringify({
          stages: [{ command: "go test ./...", exit_code: 0 }],
          exit_code: 0,
          tail: "ok\n",
          ok: true,
        }),
      }),
    );
    const content = view.sections.find(
      (s) => s.kind === "content",
    );
    expect(content && content.kind === "content" ? content.text.trim() : "").toBe(
      "ok",
    );
    const stages = view.sections.find(
      (s) => s.kind === "content" && s.text.includes("go test ./..."),
    );
    expect(stages).toBeTruthy();
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("formats command pipeline stages with per-stage exit codes", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        output: JSON.stringify({
          stages: [
            { command: "git log --oneline", exit_code: 0 },
            { command: "head -20", exit_code: 0 },
          ],
          exit_code: 0,
          ok: true,
        }),
      }),
    );
    const stageSection = view.sections.find(
      (s) => s.kind === "content" && s.text.includes("git log --oneline"),
    );
    expect(stageSection && stageSection.kind === "content" ? stageSection.text : "").toContain(
      "head -20",
    );
  });

  it("names a sequenced stage the operator passed over as skipped", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        output: JSON.stringify({
          stages: [
            { command: "go build ./...", exit_code: 1 },
            { command: "./deploy", skipped: true, connector: "&&" },
          ],
          exit_code: 1,
          ok: false,
        }),
      }),
    );
    const stageSection = view.sections.find(
      (s) => s.kind === "content" && s.text.includes("go build ./..."),
    );
    const text =
      stageSection && stageSection.kind === "content" ? stageSection.text : "";
    // Skipped stages have no exit code.
    expect(text).toContain("./deploy → skipped");
    expect(text).not.toContain("./deploy → exit");
  });

  it("formats background command start with handle panel", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        args: { command: "npm run dev", background: true },
        // The panel follows the stamp, not the payload that mirrors it.
        process: { handle: "h-1", running: true },
        output: JSON.stringify({
          background: true,
          handle: "h-1",
          stages: [{ command: "npm run dev", exit_code: -1 }],
        }),
      }),
    );
    expect(
      view.sections.some(
        (s) =>
          s.kind === "background_process" &&
          (s as { handle: string }).handle === "h-1",
      ),
    ).toBe(true);
  });

  it("formats foreground yield (running:true) with handle panel", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        status: "running",
        args: { command: "./task test:short" },
        process: { handle: "h-yield", running: true },
        output: JSON.stringify({
          running: true,
          handle: "h-yield",
          stages: [{ command: "./task test:short", exit_code: -1 }],
          waited_ms: 30_000,
        }),
      }),
    );
    expect(
      view.sections.some(
        (s) =>
          s.kind === "background_process" &&
          (s as { handle: string }).handle === "h-yield",
      ),
    ).toBe(true);
  });

  it("surfaces command I/O args and result metadata", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "command",
        kind: "command",
        args: {
          command: "grep needle",
          stdin_from: "input.txt",
          stdout_to: "out.log",
          env: { MY_FLAG: "1" },
        },
        output: JSON.stringify({
          stages: [{ command: "grep needle", exit_code: 0 }],
          exit_code: 0,
          stdin_provided: true,
          stdin_from: "input.txt",
          stdout_to: "out.log",
          env_keys: ["MY_FLAG"],
          ok: true,
        }),
      }),
    );
    expect(view.argsFacts).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Stdin from", value: "input.txt" }),
        expect.objectContaining({ label: "Stdout to", value: "out.log" }),
        expect.objectContaining({ label: "Env", value: "MY_FLAG" }),
      ]),
    );
    const facts = view.sections.find((s) => s.kind === "facts");
    expect(facts && facts.kind === "facts" ? facts.facts : []).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Stdin from", value: "input.txt" }),
        expect.objectContaining({ label: "Stdout to", value: "out.log" }),
      ]),
    );
  });

  it("formats git_status files and branch facts", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "git_status",
        kind: "generic",
        output: JSON.stringify({
          available: true,
          branch: "main",
          dirty: true,
          staged_count: 1,
          unstaged_count: 2,
          files: [{ path: "src/a.go", status: "M" }],
        }),
      }),
    );
    const facts = view.sections.find((s) => s.kind === "facts");
    expect(facts && facts.kind === "facts" ? facts.facts : []).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Branch", value: "main" }),
        expect.objectContaining({ label: "Dirty", value: "Yes" }),
      ]),
    );
    const content = view.sections.find((s) => s.kind === "content");
    expect(content && content.kind === "content" ? content.text : "").toContain(
      "src/a.go [M]",
    );
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("formats list_dir entries and nested git_show content", () => {
    const listDir = buildStructuredToolPresentation(
      part({
        tool: "list_dir",
        kind: "generic",
        output: JSON.stringify({
          path: "src",
          entries: [{ name: "main.go", type: "file", mode: "644" }],
          total_entries: 1,
          truncated: false,
        }),
      }),
    );
    expect(
      listDir.sections.find((s) => s.kind === "content")?.kind === "content"
        ? (listDir.sections.find((s) => s.kind === "content") as { text: string })
            .text
        : "",
    ).toContain("main.go (file 644)");

    const gitShow = buildStructuredToolPresentation(
      part({
        tool: "git_show",
        kind: "generic",
        output: JSON.stringify({
          available: true,
          show: {
            ref: "HEAD",
            kind: "commit",
            content: "commit message body",
          },
        }),
      }),
    );
    const showContent = gitShow.sections.find((s) => s.kind === "content");
    expect(
      showContent && showContent.kind === "content" ? showContent.text : "",
    ).toContain("commit message body");
  });

  it("formats read batch ranges as labeled documents", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "read",
        kind: "read",
        output: JSON.stringify({
          path: "main.go",
          mode: "content",
          total_lines: 20,
          truncated: false,
          ranges: [
            { offset: 1, limit: 2, end_line: 2, content: "package main\n" },
          ],
        }),
      }),
    );
    const code = view.sections.find(
      (s) => s.kind === "content",
    );
    expect(code).toMatchObject({
      kind: "content",
      text: "package main\n",
      label: "lines 1–2",
    });
  });

  it("surfaces wire-compacted find residue with banner and spill path", () => {
    const output = `[compacted tool_result — list page]\n[find#1]\n${JSON.stringify({
      results: [{ path: "a.go", type: "file" }],
      results_total: 500,
      results_truncated: true,
      results_omitted_range: { from: 40, to: 460 },
      wire_spill_path: "tool-output/find.json",
    })}`;
    const view = buildStructuredToolPresentation(
      part({ tool: "find", kind: "generic", output }),
    );
    const compaction = view.sections.find((s) => s.kind === "compaction");
    expect(compaction).toMatchObject({
      kind: "compaction",
      banner: "[compacted tool_result — list page]",
      spillPath: "tool-output/find.json",
    });
    const facts = view.sections.find((s) => s.kind === "facts");
    expect(facts && facts.kind === "facts" ? facts.facts : []).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Results total", value: "500" }),
        expect.objectContaining({ label: "Omitted", value: "40–460" }),
      ]),
    );
  });

  it("renders guidance rejects as markdown", () => {
    const text =
      "Rejected: COORDINATOR_READ_OUTSIDE_SCOPE\nCode: COORDINATOR_READ_OUTSIDE_SCOPE";
    const view = buildStructuredToolPresentation(
      part({
        output: text,
        status: "error",
        outcome: "rejected",
        codes: ["COORDINATOR_READ_OUTSIDE_SCOPE"],
      }),
    );
    expect(view.sections[0]).toMatchObject({ kind: "content" });
    expect(view.rawOutputAvailable).toBe(false);
  });

  it("renders read log_digest as structured outline section", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "read",
        kind: "read",
        args: { path: "var/log/app.log" },
        output: JSON.stringify({
          path: "var/log/app.log",
          mode: "outline",
          outline_kind: "log_digest",
          total_lines: 100,
          log_digest: {
            format: "logfmt",
            record_count: 10,
            parsed_count: 10,
            truncated: false,
            facets: [
              { key: "severity", values: [{ value: "warn", count: 2 }] },
            ],
            clusters: [
              {
                template: "timeout",
                count: 2,
                first_line: 7,
                last_line: 7,
              },
            ],
          },
        }),
      }),
    );
    expect(view.sections[0]).toMatchObject({ kind: "log_digest" });
    expect(view.rawOutputAvailable).toBe(true);
  });

  it("pretty-prints summarize pack tiers, anchors, and next_actions", () => {
    const view = buildStructuredToolPresentation(
      part({
        tool: "summarize",
        kind: "generic",
        args: { path: "internal/config", task: "how env vars load" },
        output: `[summarize#2]\n${JSON.stringify({
          task: "how env vars load",
          pack: {
            identity: [
              {
                path: "internal/config/env.go",
                kind: "file",
                line_count: 120,
                parse_health: "ok",
              },
            ],
            skeleton: [
              {
                path: "internal/config/env.go",
                kind: "function",
                name: "FromEnv",
                line: 42,
              },
            ],
            substance: [
              {
                path: "internal/config/env.go",
                start_line: 42,
                end_line: 44,
                symbol: "FromEnv",
                body: "42: func FromEnv() Config {\n43:   return Config{}\n44: }",
              },
            ],
          },
          anchors: [
            {
              handle: "summarize#1",
              path: "internal/config/env.go",
              line: 42,
              excerpt: "func FromEnv()",
            },
          ],
          next_actions: [
            {
              tool: "read",
              path: "internal/config/env.go",
              lines: "30-60",
              why: "see every env key",
            },
          ],
          selected: 1,
          total: 4,
          truncated: false,
        })}`,
      }),
    );
    const facts = view.sections.find((s) => s.kind === "facts");
    expect(facts && facts.kind === "facts" ? facts.facts : []).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ label: "Task", value: "how env vars load" }),
        expect.objectContaining({ label: "Coverage", value: "1 of 4" }),
      ]),
    );
    const byLabel = Object.fromEntries(
      view.sections
        .filter((s) => s.kind === "content" && s.label)
        .map((s) =>
          s.kind === "content" ? [s.label!, { text: s.text }] : [],
        ),
    );
    expect(byLabel.Identity?.text).toContain(
      "internal/config/env.go · file · 120 lines · ok",
    );
    expect(byLabel.Skeleton?.text).toContain(
      "internal/config/env.go L42 function FromEnv",
    );
    expect(byLabel.Anchors?.text).toContain(
      "summarize#1 internal/config/env.go:42:",
    );
    expect(byLabel["Next actions"]?.text).toContain(
      "read internal/config/env.go 30-60 — see every env key",
    );
    const substance = view.sections.find(
      (s) =>
        s.kind === "content" &&
        (s.label ?? "").includes("env.go:42"),
    );
    expect(substance && substance.kind === "content" ? substance.text : "").toContain(
      "func FromEnv() Config",
    );
    expect(view.sections.some((s) => s.kind === "content" && s.label === "Pack")).toBe(
      false,
    );
    expect(view.rawOutputAvailable).toBe(true);
    expect(view.evidenceHandle).toBe("summarize#2");
  });
});

it("refreshes the target when the resolved subject arrives", () => {
  const args = { handle: "exact-handle" };
  const initial = part({ id: "subject-update", tool: "command_output", kind: "generic", args, output: "done" });
  const before = buildStructuredToolPresentation(initial);
  const after = buildStructuredToolPresentation({ ...initial, displaySubject: "./task den:test:fast" });
  expect(after).not.toBe(before);
  expect(after.argsFacts[0]).toMatchObject({ label: "Target", value: "./task den:test:fast" });
  expect(after.argsFacts).toContainEqual(expect.objectContaining({ label: "Handle", value: "exact-handle" }));
});
