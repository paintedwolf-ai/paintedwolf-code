import { afterEach, describe, expect, it } from "vitest";
import {
  blueprintIsTitleOnly,
  blueprintPreviewSource,
  markBlueprintWorkspaceOffered,
  blueprintWorkspaceOffered,
  blueprintWorkspaceOfferKey,
  resetBlueprintWorkspaceOffersForTests,
} from "./blueprint-workspace-modal.ts";

afterEach(() => {
  resetBlueprintWorkspaceOffersForTests();
});

describe("blueprint workspace offers", () => {
  it("puts the workspace up once per ask", () => {
    const key = blueprintWorkspaceOfferKey("run-1", "2026-08-10T00:00:00Z");
    expect(blueprintWorkspaceOffered(key)).toBe(false);
    markBlueprintWorkspaceOffered(key);
    expect(blueprintWorkspaceOffered(key)).toBe(true);
  });

  // A revised blueprint is a new ask.
  it("treats a new revision, and a new run, as new asks", () => {
    const first = blueprintWorkspaceOfferKey("run-1", "2026-08-10T00:00:00Z");
    markBlueprintWorkspaceOffered(first);
    expect(
      blueprintWorkspaceOffered(blueprintWorkspaceOfferKey("run-1", "2026-08-10T01:00:00Z")),
    ).toBe(false);
    expect(
      blueprintWorkspaceOffered(blueprintWorkspaceOfferKey("run-2", "2026-08-10T00:00:00Z")),
    ).toBe(false);
  });

  it("keys a run with no revision stamp yet", () => {
    expect(blueprintWorkspaceOfferKey("run-1")).toBe("run-1:0");
    expect(blueprintWorkspaceOfferKey("run-1", "  ")).toBe("run-1:0");
  });
});

describe("blueprintPreviewSource", () => {
  it("drops leading frontmatter the card chrome already shows", () => {
    expect(
      blueprintPreviewSource("---\nstatus: draft\n---\n## Goal\n\nShip it\n"),
    ).toBe("## Goal\n\nShip it\n");
  });

  it("leaves a body with no frontmatter alone", () => {
    expect(blueprintPreviewSource("## Goal\n\nShip it\n")).toBe(
      "## Goal\n\nShip it\n",
    );
  });
});

describe("blueprintIsTitleOnly", () => {
  // The card's title row already says everything a stub carries.
  it("recognizes a stub of frontmatter and at most a title heading", () => {
    expect(blueprintIsTitleOnly("")).toBe(true);
    expect(blueprintIsTitleOnly("---\ntitle: Ship it\n---\n")).toBe(true);
    expect(blueprintIsTitleOnly("---\ntitle: Ship it\n---\n\n# Ship it\n\n")).toBe(
      true,
    );
    expect(blueprintIsTitleOnly("# Ship it\n")).toBe(true);
  });

  it("treats any body beyond the title as content", () => {
    expect(blueprintIsTitleOnly("---\ntitle: Ship it\n---\n## Goal\n")).toBe(
      false,
    );
    expect(blueprintIsTitleOnly("# Ship it\n\nShip it.\n")).toBe(false);
    expect(blueprintIsTitleOnly("## Goal\n\nShip it\n")).toBe(false);
  });
});
