import { describe, expect, it } from "vitest";
import type { ExtensionSuggestionsResponse } from "../../api/types.ts";
import {
  extensionSuggestionDetail,
  installableExtensionSuggestions,
} from "./extension-suggestions-model.ts";

const response: ExtensionSuggestionsResponse = {
  project_id: "p1",
  suggestion_revision: "rev-1",
  suggestions: [
    { id: "available", source: "registry", version: "^2", status: "available" },
    { id: "installed", source: "registry", status: "installed" },
    { id: "declined", source: "registry", status: "declined" },
    {
      id: "mismatch",
      source: "registry",
      status: "version_mismatch",
      contributes: "You're on 1.0.0, this project wants ^2",
    },
  ],
};

describe("extension suggestions", () => {
  it("offers only suggestions that can change installation", () => {
    expect(installableExtensionSuggestions(response).map((item) => item.id)).toEqual([
      "available",
      "mismatch",
    ]);
  });

  it("shows the requested source or version mismatch", () => {
    expect(extensionSuggestionDetail(response.suggestions[0]!)).toBe("registry @ ^2");
    expect(extensionSuggestionDetail(response.suggestions[3]!)).toBe(
      "You're on 1.0.0, this project wants ^2",
    );
  });
});
