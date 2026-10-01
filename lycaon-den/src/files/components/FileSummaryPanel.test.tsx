import { cleanup, fireEvent, render, screen } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { LiveFileBriefing } from "./file-briefing-live.ts";
import {
  FileSummaryPanel,
  fileSummaryFallbackMarkdown,
  fileSummaryLocationsByName,
  splitFileSummaryInline,
} from "./FileSummaryPanel.tsx";

const briefing: LiveFileBriefing = {
  target_key: "target-1",
  attempt_id: "11111111-1111-4111-8111-111111111111",
  root_id: "root-1",
  path: "src/main.ts",
  presentation: "current",
  source_sha256: "after",
  status: "complete",
  preview: {
    language: "typescript",
    line_count: 80,
  },
  locations: [{ line: 31, name: "buildReview", kind: "function" }],
  sections: [{ kind: "purpose", text: "Builds the `buildReview` surface." }],
  fallback_text: "",
  truncated: false,
  stale: false,
  updated_at: "2026-08-17T00:00:01Z",
};

describe("FileSummaryPanel", () => {
  afterEach(cleanup);

  it.each([0, 1, 2])("labels a %i-line preview", (count) => {
    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={{ ...briefing, preview: { ...briefing.preview!, line_count: count } }}
        requestFailed={false} contextLabel={null} loading={false}
        onRetry={() => {}} onOpenLocation={() => {}}
      />
    ));
    expect(screen.getByTestId("file-summary-preview").textContent).toContain(
      `${count} ${count === 1 ? "line" : "lines"}`,
    );
  });

  it("navigates exact host-known declarations where the prose names them", () => {
    const onOpenLocation = vi.fn();
    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={briefing}
        requestFailed={false}
        contextLabel={null}
        loading={false}
        onRetry={() => {}}
        onOpenLocation={onOpenLocation}
      />
    ));

    fireEvent.click(screen.getByRole("button", { name: "src/main.ts" }));
    expect(onOpenLocation).toHaveBeenLastCalledWith(
      { root_id: "root-1", path: "src/main.ts", presentation: "current" },
      1,
    );

    expect(screen.getByTestId("file-summary-sections").textContent).toContain(
      "Builds the buildReview surface.",
    );
    expect(screen.queryByRole("button", { name: "More" })).toBeNull();
    expect(screen.queryByText("Source evidence")).toBeNull();

    fireEvent.click(
      screen.getByRole("button", { name: "Open buildReview at line 31" }),
    );
    expect(onOpenLocation).toHaveBeenLastCalledWith(
      { root_id: "root-1", path: "src/main.ts", presentation: "current" },
      31,
    );
  });

  it("holds the explanation until generation finishes, then renders markdown", () => {
    const { unmount } = render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={{
          ...briefing,
          status: "streaming",
          sections: [],
          streamText: "- **Purpose:** Builds the Review surface.",
        }}
        requestFailed={false}
        contextLabel={null}
        loading={false}
        onRetry={() => {}}
        onOpenLocation={() => {}}
      />
    ));

    expect(screen.getByText("Summarizing this file…")).toBeTruthy();
    expect(screen.queryByTestId("file-summary-sections")).toBeNull();
    unmount();

    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={{
          ...briefing,
          status: "preview",
          sections: [],
          fallback_text: "- **Purpose:** Builds the Review surface.",
        }}
        requestFailed={false}
        contextLabel={null}
        loading={false}
        onRetry={() => {}}
        onOpenLocation={() => {}}
      />
    ));

    expect(screen.queryByText("Summarizing this file…")).toBeNull();
    expect(screen.getByTestId("file-summary-fallback").textContent).toContain(
      "Builds the Review surface.",
    );
  });

  it("uses fallback markdown only after a terminal briefing", () => {
    expect(
      fileSummaryFallbackMarkdown({
        ...briefing,
        status: "streaming",
        sections: [],
        streamText: "- **Purpose:** Builds the Review surface.",
      }),
    ).toBe("");
    expect(fileSummaryFallbackMarkdown(briefing)).toBe("");
    expect(fileSummaryFallbackMarkdown({
      ...briefing,
      status: "preview",
      sections: [],
      fallback_text: "A bounded fallback explanation.",
    })).toBe("A bounded fallback explanation.");
  });

  it("offers manual detail when automatic generation leaves facts only", () => {
    const onRetry = vi.fn();
    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={{
          ...briefing,
          status: "preview",
          sections: [],
          fallback_text: "",
        }}
        requestFailed={false}
        contextLabel={null}
        loading={false}
        onRetry={onRetry}
        onOpenLocation={() => {}}
      />
    ));

    fireEvent.click(screen.getByRole("button", { name: "Summarize current file" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("shows one loading state while retrying a terminal result", () => {
    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={{
          ...briefing,
          status: "failed",
          sections: [],
          error: "The first attempt failed.",
        }}
        requestFailed={false}
        contextLabel={null}
        loading={true}
        onRetry={() => {}}
        onOpenLocation={() => {}}
      />
    ));

    expect(screen.getByText("Summarizing this file…")).toBeTruthy();
    expect(screen.queryByText("The first attempt failed.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Summarize current file" })).toBeNull();
    expect(screen.getByTestId("file-summary-content").getAttribute("aria-busy")).toBe(
      "true",
    );
  });

  it("offers retry for a complete response without an explanation", () => {
    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={{ ...briefing, status: "complete", sections: [] }}
        requestFailed={false}
        contextLabel={null}
        loading={false}
        onRetry={() => {}}
        onOpenLocation={() => {}}
      />
    ));

    expect(screen.getByText("Detailed summary is not available yet.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Summarize current file" })).toBeTruthy();
  });

  it("renders unknown code names as inert code", () => {
    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "src/main.ts", presentation: "current" }}
        summary={{
          ...briefing,
          sections: [
            { kind: "purpose", text: "Builds the `UnknownReview` surface." },
          ],
        }}
        requestFailed={false}
        contextLabel={null}
        loading={false}
        onRetry={() => {}}
        onOpenLocation={() => {}}
      />
    ));

    expect(screen.getByTestId("file-summary-sections").textContent).toContain(
      "Builds the UnknownReview surface.",
    );
    expect(screen.queryByRole("button", { name: /UnknownReview/ })).toBeNull();
    expect(screen.getByText("UnknownReview").tagName).toBe("CODE");
  });

  it("presents generic text as Plain text, never Vim help", () => {
    render(() => (
      <FileSummaryPanel
        target={{ root_id: "root-1", path: "notes.txt", presentation: "current" }}
        summary={{
          ...briefing,
          path: "notes.txt",
          preview: {
            language: "vimdoc",
            line_count: 2,
          },
        }}
        requestFailed={false}
        contextLabel={null}
        loading={false}
        onRetry={() => {}}
        onOpenLocation={() => {}}
      />
    ));

    expect(screen.getByText("Plain text")).toBeTruthy();
    expect(screen.queryByText("vimdoc")).toBeNull();
  });

  it("keeps version context in navigation and retry copy", () => {
    const target = {
      root_id: "root-1",
      path: "src/main.ts",
      presentation: "version" as const,
      version_id: "version-1",
    };
    const onOpenLocation = vi.fn();
    render(() => (
      <FileSummaryPanel
        target={target}
        summary={{ ...briefing, presentation: "version", stale: true }}
        requestFailed={false}
        contextLabel="Version · Edited · 2h ago"
        loading={false}
        onRetry={() => {}}
        onOpenLocation={onOpenLocation}
      />
    ));

    expect(screen.getByTestId("file-summary-context").textContent).toBe(
      "Version · Edited · 2h ago",
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Open buildReview at line 31" }),
    );
    expect(onOpenLocation).toHaveBeenLastCalledWith(target, 31);
    expect(screen.getByRole("button", { name: "Summarize this version" })).toBeTruthy();
  });

  it("leaves duplicate declaration names inert instead of guessing", () => {
    const locations = fileSummaryLocationsByName([
      { line: 10, name: "render", kind: "function" },
      { line: 40, name: "render", kind: "method" },
    ]);
    expect(locations.get("render")).toBeNull();
  });

  it("keeps duplicate outline records for one source location navigable", () => {
    const locations = fileSummaryLocationsByName([
      { line: 10, name: "render", kind: "function" },
      { line: 10, name: "render", kind: "method" },
    ]);
    expect(locations.get("render")?.line).toBe(10);
  });

  it("parses only single-backtick inline code spans", () => {
    expect(splitFileSummaryInline("Calls `buildReview` safely.")).toEqual([
      { kind: "text", text: "Calls " },
      { kind: "code", text: "buildReview" },
      { kind: "text", text: " safely." },
    ]);
    expect(splitFileSummaryInline("Keeps ```raw``` literal.")).toEqual([
      { kind: "text", text: "Keeps ```raw``` literal." },
    ]);
  });
});
