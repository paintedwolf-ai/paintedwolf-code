import { describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { DecisionRow, decisionRowLabel } from "./DecisionRow.tsx";

describe("DecisionRow", () => {
  it("reads as the word, then its detail", () => {
    render(() => (
      <DecisionRow
        tone="approved"
        label="Approved"
        details={[
          { text: "bash", testId: "d-tool" },
          { text: "rm -rf build", emphasis: "subject", testId: "d-subject" },
        ]}
      />
    ));
    const row = document.querySelector(".den-decision")!;
    expect(row.getAttribute("data-tone")).toBe("approved");
    expect(screen.getByTestId("d-tool").textContent).toBe("bash");
    expect(screen.getByTestId("d-subject").getAttribute("data-emphasis")).toBe(
      "subject",
    );
    // Separators are decorative.
    expect(row.querySelectorAll(".den-decision__sep")).toHaveLength(2);
    expect(
      row.querySelector(".den-decision__sep")?.getAttribute("aria-hidden"),
    ).toBe("true");
  });

  it("drops empty details and their separator", () => {
    render(() => (
      <DecisionRow
        tone="rejected"
        label="Rejected"
        details={[{ text: "" }, { text: "   " }, { text: "plan.md" }]}
      />
    ));
    const row = document.querySelector(".den-decision")!;
    expect(row.querySelectorAll(".den-decision__detail")).toHaveLength(1);
    expect(row.querySelectorAll(".den-decision__sep")).toHaveLength(1);
  });

  it("titles a truncated detail so the full value stays reachable", () => {
    render(() => (
      <DecisionRow
        tone="neutral"
        label="Expired"
        details={[{ text: "a-very-long-subject", truncate: true, testId: "d" }]}
      />
    ));
    expect(screen.getByTestId("d").getAttribute("data-tip")).toBe(
      "a-very-long-subject",
    );
  });

  it("reads the same line as a sentence for a screen reader", () => {
    expect(decisionRowLabel("Approved", ["bash", undefined, " ", "one"])).toBe(
      "Approved · bash · one",
    );
  });
});
