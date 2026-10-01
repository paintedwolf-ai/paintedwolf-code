import { describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import type { SearchHit } from "../../api/types.ts";
import { SearchResultGroup } from "./SearchResultGroup.tsx";
import { SearchResultRow } from "./SearchResultRow.tsx";

describe("SearchResultRow single-line list rows", () => {
  const citationHit = (): SearchHit => ({
    hit_id: "citation-1",
    hit_kind: "evidence",
    source: "message",
    project_id: "proj-a",
    project_name: "Alpha",
    session_id: "sess-a",
    source_ref: "read#1",
    title: "fn main()",
    snippet: "fn main()\nlet x = 1;",
    score: 1,
  });

  it("keeps rows one line — no inline preview or actions, ever", () => {
    render(() => (
      <SearchResultRow
        hit={citationHit()}
        selected={true}
        gridTemplate="120px 1fr 160px 80px"
        onSelect={() => undefined}
        onPivot={() => undefined}
        onNavigate={() => undefined}
      />
    ));
    expect(screen.getByText("fn main()")).toBeTruthy();
    expect(screen.queryByTestId("search-result-preview")).toBeNull();
    expect(screen.queryByTestId("search-result-open")).toBeNull();
  });

  it("marks host-interpreted terms in the title and shows age, not score", () => {
    const twoHoursAgo = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString();
    render(() => (
      <SearchResultRow
        hit={{ ...citationHit(), created_at: twoHoursAgo }}
        selected={false}
        gridTemplate="120px 1fr 160px 80px"
        highlightTerms={["main"]}
        onSelect={() => undefined}
        onPivot={() => undefined}
        onNavigate={() => undefined}
      />
    ));
    const mark = document.querySelector("mark.den-search-mark");
    expect(mark?.textContent).toBe("main");
    const row = screen.getByTestId("search-result-row");
    expect(row.querySelector(".den-search-row__time")?.textContent).toBe(
      "2h ago",
    );
    expect(row.textContent).not.toContain("1.00");
  });

  it.each(["code", "evidence"] as const)(
    "applies code case sensitivity only to %s results",
    (hitKind) => {
      const { container } = render(() => (
        <SearchResultRow
          hit={{ ...citationHit(), hit_kind: hitKind, title: "Maple maple MAPLE" }}
          selected={false}
          gridTemplate="1fr"
          highlightTerms={["Maple"]}
          codeCaseSensitive={true}
          onSelect={() => undefined}
          onPivot={() => undefined}
          onNavigate={() => undefined}
        />
      ));
      expect(Array.from(container.querySelectorAll("mark"), (mark) => mark.textContent))
        .toEqual(hitKind === "code" ? ["Maple"] : ["Maple", "maple", "MAPLE"]);
    },
  );

  it("links the trailing path for a host path-stamped hit of any kind", () => {
    render(() => (
      <SearchResultRow
        hit={{
          hit_id: "path-1",
          hit_kind: "evidence",
          source: "message",
          project_id: "proj-a",
          source_ref: "AGENTS.md",
          path: "AGENTS.md",
          title: "Agent policy",
          context: "AGENTS.md",
          snippet: "# Lycaon — agent policy",
          score: 1,
        }}
        selected={false}
        gridTemplate="120px 1fr 160px 80px"
        onSelect={() => undefined}
        onPivot={() => undefined}
        onNavigate={() => undefined}
      />
    ));
    expect(screen.getByTestId("source-path-link").textContent).toBe(
      "AGENTS.md",
    );
  });

  it("shows arg summary as secondary for tool calls", () => {
    render(() => (
      <SearchResultRow
        hit={{
          hit_id: "tool-1",
          hit_kind: "tool",
          source: "message",
          project_id: "proj-a",
          source_ref: "call_1",
          title: "read",
          context: "src/main.go",
          snippet: 'read {"path":"src/main.go"}',
          score: 1,
        }}
        selected={false}
        gridTemplate="120px 1fr 160px 80px"
        onSelect={() => undefined}
        onPivot={() => undefined}
        onNavigate={() => undefined}
      />
    ));
    expect(screen.getByText("read")).toBeTruthy();
    expect(screen.getByTestId("search-result-secondary").textContent).toBe(
      "src/main.go",
    );
  });
});

describe("SearchResultGroup selection identity", () => {
  it("selects only one row when snippets and source_ref collide", () => {
    const hit = (id: string, snippet: string): SearchHit => ({
      hit_id: id,
      hit_kind: "finding",
      source: "finding",
      project_id: "proj-a",
      source_ref: "scan-1",
      title: snippet,
      snippet,
      score: 1,
    });
    render(() => (
      <SearchResultGroup
        groups={[
          {
            projectId: "proj-a",
            projectName: "Alpha",
            isOrigin: true,
            hits: [
              hit("finding-1", "Remote script piped to shell — supply-chain risk."),
              hit("finding-2", "Remote script piped to shell — supply-chain risk."),
            ],
          },
        ]}
        selectedHitKey="finding-1"
        gridTemplate="120px 1fr 160px 80px"
        onSelectHit={() => undefined}
        onPivot={() => undefined}
        onNavigate={() => undefined}
      />
    ));
    const rows = screen.getAllByTestId("search-result-row");
    const items = screen.getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]?.classList.contains("den-search-row--selected")).toBe(true);
    expect(rows[1]?.classList.contains("den-search-row--selected")).toBe(false);
    expect(items[0]?.getAttribute("aria-current")).toBe("true");
    expect(items[1]?.getAttribute("aria-current")).toBeNull();
  });

  it("renders every row in the supplied result page", () => {
    const hits: SearchHit[] = Array.from({ length: 100 }, (_, index) => ({
      hit_id: `code-${index}`,
      hit_kind: "code",
      source: "code",
      project_id: "proj-a",
      path: `src/file-${index}.ts`,
      line: index + 1,
      title: `result ${index}`,
      snippet: `result ${index}`,
    }));
    render(() => (
      <SearchResultGroup
        groups={[
          {
            projectId: "proj-a",
            projectName: "Alpha",
            isOrigin: true,
            hits,
          },
        ]}
        selectedHitKey={null}
        gridTemplate="120px 1fr 160px 80px"
        onSelectHit={() => undefined}
        onPivot={() => undefined}
        onNavigate={() => undefined}
      />
    ));
    const rendered = screen.getAllByTestId("search-result-row");
    expect(rendered).toHaveLength(100);
    expect(rendered[0]?.textContent).toContain("result 0");
    expect(rendered[99]?.textContent).toContain("result 99");
  });
});
