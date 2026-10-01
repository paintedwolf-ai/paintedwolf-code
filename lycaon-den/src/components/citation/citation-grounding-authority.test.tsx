import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";
import type { CitationGrounding } from "../../api/types.ts";
import { buildCitationGroundingView } from "../../chat/grounding/citation-grounding-model.ts";
import { CitationGroundingBadge } from "./CitationGroundingBadge.tsx";
import { CitationGroundingPanel } from "./CitationGroundingPanel.tsx";

describe("citation provenance across presentation surfaces", () => {
  for (const traced of [false, true]) {
    for (const hostAssembled of [false, true]) {
      for (const hasCitations of [false, true]) {
        it(`presents traced=${traced}, host=${hostAssembled}, citations=${hasCitations}`, () => {
          const grounding: CitationGrounding = {
            traced,
            host_assembled: hostAssembled,
            hint_code: traced ? undefined : "INVEST_CITATIONS_REQUIRED",
            checks: [{ id: "typed_citations", label: "Typed citations", kind: "citation",
              status: "passed", vacuous: !hasCitations }],
            cited_evidence: hasCitations ? [{ handle: "http_response#1", verdict: "matched" }] : [],
            evidence_records: [{ handle: "http_response#1", kind: "http_response", shape: "surface_snapshot" }],
          };
          const view = buildCitationGroundingView(grounding);
          render(() => <>
            <CitationGroundingBadge grounding={grounding} onOpenDetail={() => {}} />
            <CitationGroundingPanel view={view} />
          </>);
          const badge = screen.queryByTestId("citation-grounding-badge");
          const panel = screen.getByTestId("citation-grounding-panel");
          if (traced && !hasCitations) {
            expect(badge).toBeNull();
            expect(panel.textContent).toContain("No citations to trace");
            return;
          }
          const positive = traced || hostAssembled;
          expect(view.outcome).toBe(traced ? "traced" : "partial");
          expect(badge?.textContent).toContain(positive ? "Grounded" : "Partial");
          expect(badge?.getAttribute("aria-label")).toContain(positive ? "Citations grounded" : "couldn't be traced");
          expect(panel.classList.contains("den-citation-grounding--traced")).toBe(positive);
          expect(panel.classList.contains("den-citation-grounding--partial")).toBe(!positive);
          expect(view.headline).toContain(positive ? "All citations grounded" : "couldn't be traced");
        });
      }
    }
  }
});
