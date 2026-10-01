import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { toolSchemaNames } from "./tool-catalog-tools.ts";
import { toolPartFromCall, toolPartSummaryTitle } from "./tool-part-model.ts";
import { resolveChickletTitle } from "./tool-chicklet-titles.ts";
import {
  TOOL_CHICKLET_RUNNING_LABELS,
  TOOL_CHICKLET_TITLE_KEYS,
} from "./tool-presentation.generated.ts";

const SAMPLE_ARGS: Record<string, Record<string, unknown>> = {
  read: { path: "src/main.go" },
  write: { path: "README.md" },
  edit: { path: "pkg/foo.go" },
  replace_lines: { path: "pkg/foo.go" },
  code_rewrite: { path: "pkg/foo.go", pattern: "fmt.Println($A)", rewrite: "log.Info($A)" },
  request_tools: { need: "decode and hash helpers" },
  find: { name_glob: "**/*.go" },
  grep: { pattern: "TODO" },
  stat: { paths: ["go.mod"] },
  wc: { paths: ["internal"] },
  chmod: { paths: ["script.sh"], mode: "+x" },
  delete: { paths: ["tmp.txt"] },
  chown: { paths: ["scripts/run.sh"], owner: "current" },
  diff: { path_a: "internal/foo/old.go", path_b: "internal/foo/new.go" },
  jq: { path: "package.json", query: ".scripts.test" },
  extract_archive: { path: "vendor/sdk.zip", dest: "vendor/sdk" },
  copy: { copies: [{ from: "fixtures/a.json", to: "internal/testdata/a.json" }] },
  move: { moves: [{ from: "old.go", to: "newpkg/new.go" }] },
  mkdir: { paths: ["internal/newpkg/sub"] },
  list_dir: { path: "internal" },
  command: { command: "./task check-fast" },
  command_output: { handle: "handle-1" },
  command_stop: { handle: "handle-1" },
  verify: { command: "./task check-fast" },
  git_diff: { paths: ["README.md"] },
  git_log: { path: "cmd/main.go" },
  git_show: { ref: "HEAD", path: "main.go" },
  git_blame: { path: "main.go" },
  git_restore: { paths: ["main.go"] },
  git_ref: { refs: ["HEAD"] },
  git_commit: { message: "fix: tests" },
  web_search: { query: "solidjs signals" },
  fetch_url: { url: "https://example.com" },
  handoff_reserve: { paths: ["internal/foo.go"] },
  promote_overlay: { overlay_id: "job-overlay-1" },
  reject_overlay: { overlay_id: "job-overlay-1", reason: "superseded" },
  preview_overlay: { overlay_id: "job-overlay-1", path: "main.go" },
  extend_worker_budget: { job_id: "job-1", max_tool_loops: 80 },
  task: {
    agent_type: "implementer",
    description: "Add login form",
  },
  delegate_dispatch: {
    subagent_type: "repo-researcher",
    description: "Survey auth flow",
  },
  record_finding: { summary: "Auth middleware blocks token refresh" },
  update_progress: { content: "- [ ] Wire login form\n- [x] Add tests" },
  render_view: {
    markup: "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"40\" height=\"20\"><rect width=\"40\" height=\"20\" fill=\"#333\"/></svg>",
    mime: "svg",
    caption: "Login mockup",
  },
  view_image: {
    path: "assets/logo.svg",
  },
  view_video: {
    path: "prompt-attachments/ab/bug.mp4",
  },
  capture_page: {
    project_dir: ".",
    caption: "Home",
  },
  measure_page: {
    project_dir: ".",
    selectors: ["#box-a"],
    caption: "Geometry",
  },
  page_open: {
    project_dir: ".",
  },
  page_act: {
    id: "page-1",
    // actions is required by the schema; a handle-only call cannot happen.
    actions: [{ type: "click", text: "Save" }],
  },
  page_snapshot: {
    id: "page-1",
    caption: "Held",
  },
  terminal_open: {
    command: "python3 -i",
  },
  terminal_send: {
    id: "pty-1",
  },
  terminal_read: {
    id: "pty-1",
  },
  terminal_snapshot: {
    id: "pty-1",
    caption: "TUI",
  },
  terminal_close: {
    id: "pty-1",
  },
};

describe("tool-chicklet-titles", () => {
  it("covers every tool in tools/schemas", () => {
    const names = toolSchemaNames();
    expect(names.length).toBeGreaterThan(0);
    for (const name of names) {
      expect(TOOL_CHICKLET_TITLE_KEYS[name], name).toBeDefined();
    }
  });

  it("shows the pipeline when command is absent", () => {
    expect(
      resolveChickletTitle("command", {
        pipeline: ["git log --oneline", "head -20"],
      }),
    ).toBe("git log --oneline | head -20");
  });

  it("covers runtime task tools beyond tools/schemas", () => {
    expect(TOOL_CHICKLET_RUNNING_LABELS.task).toBeDefined();
    expect(TOOL_CHICKLET_RUNNING_LABELS.delegate_dispatch).toBeDefined();
    expect(
      resolveChickletTitle("task", {
        agent_type: "implementer",
        description: "Add login",
      }),
    ).toBe("implementer · Add login");
    expect(
      resolveChickletTitle("delegate_dispatch", {
        subagent_type: "repo-researcher",
        description: "Survey auth",
      }),
    ).toBe("repo-researcher · Survey auth");
  });

  it("builds contextual chicklet subtitles for representative tools", () => {
    for (const [tool, args] of Object.entries(SAMPLE_ARGS)) {
      const part = toolPartFromCall(
        { id: `tc-${tool}`, name: tool, args },
        {
          id: `tr-${tool}`,
          role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
          content: "ok",
          tool_result: { content: "ok", outcome: "completed" },
          created_at: "t",
        },
        "asst",
      );
      const title = toolPartSummaryTitle(part);
      expect(title, tool).not.toBe(tool);
      expect(title.length).toBeGreaterThan(0);
    }
  });

  it("allows no-arg tools to show only the tool name", () => {
    const part = toolPartFromCall(
      { id: "tc-git", name: "git_status", args: {} },
      {
        id: "tr-git",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "{}",
        tool_result: { content: "{}", outcome: "completed" },
        created_at: "t",
      },
      "asst",
    );
    expect(toolPartSummaryTitle(part)).toBe("git_status");
  });

  it("shows only the tool name for a handle-only call", () => {
    const part = toolPartFromCall(
      { id: "tc-close", name: "page_close", args: { id: "page-1" } },
      {
        id: "tr-close",
        role: "tool", origin: "tool" as const, authority: "none" as const, trust_tier: "untrusted" as const,
        content: "{}",
        tool_result: { content: "{}", outcome: "completed" },
        created_at: "t",
      },
      "asst",
    );
    expect(toolPartSummaryTitle(part)).toBe("page_close");
  });

  describe("declared title keys are exhaustive", () => {
    // A partially streamed call can carry the body before the path.
    it("shows no subtitle rather than a file body", () => {
      expect(
        resolveChickletTitle("write", { content: "package main\n\nfunc main()" }),
      ).toBeUndefined();
      expect(
        resolveChickletTitle("edit", { old_string: "a", new_string: "b" }),
      ).toBeUndefined();
      expect(
        resolveChickletTitle("replace_lines", { new_content: "x" }),
      ).toBeUndefined();
    });

    it("does not promote arbitrary runtime arguments", () => {
      expect(resolveChickletTitle("mcp__thing__do", { note: "hi", id: "opaque-id" })).toBeUndefined();
    });

    it("takes the first line of a multi-line value", () => {
      expect(
        resolveChickletTitle("git_commit", {
          message: "Fix the thing\n\nLonger body that must not run on.",
        }),
      ).toBe("Fix the thing");
      const progress = resolveChickletTitle("update_progress", {
        content: "- [ ] Wire login form\n- [ ] Add tests",
      });
      expect(progress).toBe("- [ ] Wire login form");
      expect(progress).not.toContain("\n");
    });

    // An object or closed-enum key could only print nothing or a fixed token.
    it("declares no key where none could produce content", () => {
      expect(resolveChickletTitle("submit_verdict", { verdict: { ok: true } }))
        .toBeUndefined();
      expect(resolveChickletTitle("render_view", { mime: "svg", markup: "<svg/>" }))
        .toBe("Visual artifact");
      expect(resolveChickletTitle("render_view", { mime: "svg", caption: "The flow" }))
        .toBe("The flow");
      expect(resolveChickletTitle("render_view", { handle: "flow-1" }))
        .toBe("Visual artifact");
      expect(resolveChickletTitle("render_view", { dest: "mockups/flow.png" }))
        .toBe("mockups/flow.png");
      expect(resolveChickletTitle("view_image", { handle: "flow-1" }))
        .toBe("Image artifact");
      expect(resolveChickletTitle("view_image", { path: "assets/logo.svg" }))
        .toBe("assets/logo.svg");
    });

    // The collapsed card is the only place a cross-chat reach is visible.
    it("names the scope when a recall reaches past this chat", () => {
      expect(resolveChickletTitle("recall", { query: "auth middleware" }))
        .toBe("auth middleware");
      expect(resolveChickletTitle("recall", { query: "auth middleware", widen: "subtree" }))
        .toBe("auth middleware");
      expect(resolveChickletTitle("recall", { query: "auth middleware", widen: "project" }))
        .toBe("all chats in this project · auth middleware");
      expect(resolveChickletTitle("recall", { query: "auth middleware", widen: "all" }))
        .toBe("all chats, all projects · auth middleware");
    });

    it("cuts long titles on code points", () => {
      const title = resolveChickletTitle("command", { command: "🙂".repeat(200) });
      expect(title).toBeDefined();
      expect(title).not.toContain("�");
      expect(Array.from(title!)).toHaveLength(96);
    });

    // Page and terminal handles are routing detail, not titles.
    it("titles a page drive by what it did, never the page handle", () => {
      expect(
        resolveChickletTitle("page_act", {
          id: "417aaab9-ed22-4cc3-9dbc-c0c7de54c3b8",
          actions: [
            { type: "wait", wait: "idle" },
            { type: "click", text: "Save" },
          ],
        }),
      ).toBe("click Save");
      expect(
        resolveChickletTitle("page_act", {
          id: "417aaab9-ed22-4cc3-9dbc-c0c7de54c3b8",
          actions: [
            { type: "fill", label: "Email", value: "a@b.c" },
            { type: "click", text: "Submit" },
          ],
        }),
      ).toBe("fill Email +1 more");
      // A wait-only script did nothing worth naming.
      expect(
        resolveChickletTitle("page_act", {
          id: "417aaab9-ed22-4cc3-9dbc-c0c7de54c3b8",
          actions: [{ type: "wait", wait: "idle" }],
        }),
      ).toBeUndefined();
      expect(
        resolveChickletTitle("page_close", {
          id: "417aaab9-ed22-4cc3-9dbc-c0c7de54c3b8",
        }),
      ).toBeUndefined();
    });

    it("titles each drive input by its target", () => {
      const title = (step: Record<string, unknown>, extra: Record<string, unknown> = {}) =>
        resolveChickletTitle("page_act", { id: "p", actions: [step], ...extra });
      expect(title({ type: "hover", selector: "#menu" })).toBe("hover #menu");
      expect(title({ type: "drag", testid: "card", to: { selector: "#zone" } })).toBe("drag card → #zone");
      expect(title({ type: "drag", selector: "#handle", by: { x: 40, y: 0 } })).toBe("drag #handle");
      expect(title({ type: "scroll", selector: "#list", by: { y: 300 } })).toBe("scroll #list");
      expect(title({ type: "scroll", by: { y: 300 } })).toBe("scroll page");
      expect(title({ type: "press", key: "Mod+K" })).toBe("press Mod+K");
      expect(title({ type: "type", value: "hello" })).toBe("type hello");
      expect(title({ type: "route", routes: [{ url: "/api/items", status: 500 }, { url: "/api/me" }] })).toBe("route /api/items +1");
      expect(title({ type: "wait_for", text: "Saved" })).toBeUndefined();
      expect(title({ type: "click", role: "button", text: "Save" }, { record: { tail_ms: 800 } })).toBe("click Save · recorded");
    });

    it("titles a snapshot from its caption, never its handle", () => {
      expect(
        resolveChickletTitle("page_snapshot", {
          id: "73d7f3fa-1c27-412c-a426-1762b1995954",
          caption: "Status UI before reload",
        }),
      ).toBe("Status UI before reload");
      expect(
        resolveChickletTitle("page_snapshot", {
          id: "73d7f3fa-1c27-412c-a426-1762b1995954",
        }),
      ).toBeUndefined();
      expect(
        resolveChickletTitle("terminal_snapshot", {
          id: "0f9a2c11-5f0b-4a77-9a2e-1d3c8f6b4e02",
          caption: "watcher rebuilding",
        }),
      ).toBe("watcher rebuilding");
    });
  });
});

it("matches the host title contract", () => {
  const cases = JSON.parse(readFileSync(new URL("../../../../lycaon/internal/toolpresentation/testdata/titles.json", import.meta.url), "utf8")) as { tool: string; args: Record<string, unknown>; title: string }[];
  for (const item of cases) expect(resolveChickletTitle(item.tool, item.args) ?? "", item.tool).toBe(item.title);
});

it("prefers the resolved result target over the provisional call label", () => {
  const handle = "7441efdd-29d7-4cd9-899e-3c542d62d8ba";
  const part = toolPartFromCall({ id: "call", name: "command_output", args: { handle, cursor: 0 }, display_title: "Output from a command" }, {
    id: "result", role: "tool", origin: "tool", authority: "none", trust_tier: "untrusted", content: "done", created_at: "t",
    tool_result: { content: "done", display_title: "./task den:test:fast", display_subject: "./task den:test:fast", outcome: "completed" },
  }, "assistant");
  expect(toolPartSummaryTitle(part)).toBe("./task den:test:fast");
  expect(part.args?.handle).toBe(handle);
});
