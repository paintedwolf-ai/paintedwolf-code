import { describe, expect, it } from "vitest";
import { render, screen } from "@solidjs/testing-library";
import { SummarizerModelHint } from "./SummarizerModelHint.tsx";
import { MODELS_SETTINGS_COPY } from "../../settings/providers/models-settings-copy.ts";

describe("SummarizerModelHint", () => {
  it("names the unset label as one token that never wraps", () => {
    render(() => (
      <p data-testid="hint">
        <SummarizerModelHint />
      </p>
    ));
    const token = screen.getByTestId("summarizer-unset-label");
    expect(token.textContent).toBe(MODELS_SETTINGS_COPY.unsetModel);
    expect(token.classList.contains("whitespace-nowrap")).toBe(true);
    expect(screen.getByTestId("hint").textContent).toBe(
      `${MODELS_SETTINGS_COPY.summarizerModelHintLead} --- ${MODELS_SETTINGS_COPY.summarizerModelHintTail}`,
    );
  });
});
