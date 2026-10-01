import { describe, expect, it } from "vitest";
import { editorActionOutput } from "../inline-edit/editor-action-client.ts";
import { seedStockFrame } from "../../../contributions/stock-frame-test.ts";
import {
  SELECTION_VERB_IDS,
  SELECTION_VERBS,
  askSelectionVerbs,
  selectionVerbById,
} from "./selection-verbs.ts";

describe("selection verbs", () => {
  it("contains exactly the six selection verbs", () => {
    expect(SELECTION_VERB_IDS).toEqual([
      "explain",
      "summarize",
      "document",
      "addTest",
      "rewriteStructurally",
      "fixFinding",
    ]);
    expect(SELECTION_VERBS).toHaveLength(6);
  });

  it("lists every selection verb when a finding is present", () => {
    const labels = askSelectionVerbs({ hasFinding: true }).map((v) => v.id);
    expect(labels).toEqual([
      "explain",
      "summarize",
      "document",
      "addTest",
      "rewriteStructurally",
      "fixFinding",
    ]);
  });

  it("omits Fix this finding when no finding annotates the range", () => {
    const labels = askSelectionVerbs({ hasFinding: false }).map((v) => v.id);
    expect(labels).toEqual([
      "explain",
      "summarize",
      "document",
      "addTest",
      "rewriteStructurally",
    ]);
  });

  it("routes prose vs edits by the declared preset", () => {
    seedStockFrame();
    const byOutput = (want: "prose" | "edits") =>
      SELECTION_VERBS.filter((v) => editorActionOutput(v.commandId) === want).map(
        (v) => v.id,
      );
    expect(byOutput("prose")).toEqual(["explain", "summarize"]);
    expect(byOutput("edits")).toEqual([
      "document",
      "addTest",
      "rewriteStructurally",
      "fixFinding",
    ]);
  });

  it("distinguishes contextual explanation from a brief summary", () => {
    expect(selectionVerbById("explain")).toMatchObject({
      label: "Explain in context",
      menuLabel: "Explain in context",
    });
    expect(selectionVerbById("summarize")).toMatchObject({
      label: "Give me the gist",
      menuLabel: "Give me the gist",
    });
  });
});
