import { afterEach, describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import {
  findController,
  openFind,
  resetFindControllerForTests,
  setFindQuery,
} from "./find-controller.ts";
import { FIND_MARK_ATTR } from "./find-match.ts";
import { SearchResultGroup } from "../components/search/SearchResultGroup.tsx";
import type { SearchHit } from "../api/types.ts";
import type { ProjectHitGroup } from "../search/search-query-model.ts";
import { bindFindableView } from "./use-findable-view.ts";
import { createSignal } from "solid-js";

afterEach(() => {
  resetFindControllerForTests();
  document.body.replaceChildren();
});

function ResultsOnlyHost(props: { hits: SearchHit[] }) {
  const [root, setRoot] = createSignal<HTMLElement | null>(null);
  bindFindableView({ id: "search-results", root });
  const groups: ProjectHitGroup[] = [
    {
      projectId: "proj-a",
      projectName: "Alpha",
      hits: props.hits,
      isOrigin: true,
    },
  ];
  return (
    <div data-testid="search-results-list" ref={setRoot}>
      <SearchResultGroup
        groups={groups}
        selectedHitKey={null}
        gridTemplate="120px 1fr 160px 80px"
        onSelectHit={() => {}}
        onPivot={() => {}}
        onNavigate={() => {}}
      />
    </div>
  );
}

describe("search results findable adapter", () => {
  it("matches inside the results list root", async () => {
    const hits: SearchHit[] = [
      {
        hit_id: "find-1",
        hit_kind: "message",
        source: "message",
        project_id: "proj-a",
        project_name: "Alpha",
        session_id: "sess-a",
        title: "SEARCH_FIND_NEEDLE in snippet",
        snippet: "SEARCH_FIND_NEEDLE in snippet",
      },
    ];
    render(() => <ResultsOnlyHost hits={hits} />);
    const results = await screen.findByTestId("search-results-list");
    results.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
    openFind();
    setFindQuery("SEARCH_FIND_NEEDLE");
    expect(findController.matches().length).toBeGreaterThanOrEqual(1);
    expect(
      document.querySelectorAll(`span[${FIND_MARK_ATTR}="1"]`).length,
    ).toBeGreaterThanOrEqual(1);
  });
});
