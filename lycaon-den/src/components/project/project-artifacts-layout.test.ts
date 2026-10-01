import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

const styles = readFileSync(
  join(import.meta.dirname, "../../artifacts-domain.css"),
  "utf8",
);

describe("project artifact gallery layout", () => {
  it("makes the content host a bounded flex column with a scrolling grid", () => {
    expect(styles).toMatch(
      /\.project-artifacts-view \.den-browse-main\s*\{[^}]*display:\s*flex[^}]*overflow:\s*hidden/s,
    );
    expect(styles).toMatch(/\.den-artifacts-grid\s*\{[^}]*flex:\s*1[^}]*min-height:\s*0/s);
    expect(styles).toMatch(
      /\.den-artifacts-grid__content\s*\{[^}]*display:\s*grid[^}]*grid-template-columns:\s*repeat\(/s,
    );
  });

  it("constrains previews to thumbnail frames", () => {
    expect(styles).toMatch(
      /\.project-artifacts-tile \.den-transcript-visual__frame\s*\{[^}]*aspect-ratio:\s*16 \/ 10/s,
    );
    expect(styles).toMatch(
      /\.project-artifacts-tile \.den-transcript-visual__img\s*\{[^}]*height:\s*100%[^}]*object-fit:\s*contain/s,
    );
  });
});
