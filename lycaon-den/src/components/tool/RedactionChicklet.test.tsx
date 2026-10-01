import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";

import { RedactionChicklet } from "./RedactionChicklet.tsx";
import type { RedactedSpan } from "../../api/types.ts";

function span(over: Partial<RedactedSpan> = {}): RedactedSpan {
  return { field: "tool_result.content", start: 0, length: 10, kind: "secret", ...over };
}

function chicklet(spans: RedactedSpan[]): string | null | undefined {
  const { container } = render(() => <RedactionChicklet message={{ host_secret_redaction: { spans } }} />);
  return container.querySelector(".den-redaction-chicklet")?.textContent;
}

describe("RedactionChicklet", () => {
  it("says a protected value was kept as its reference rather than removed", () => {
    expect(chicklet([span({ kind: "managed_reference", length: 58 })])).toBe("1 secret protected");
    expect(
      chicklet([span({ kind: "managed_reference" }), span({ kind: "managed_reference", start: 70 })]),
    ).toBe("2 secrets protected");
  });

  it("totals replacements of different kinds", () => {
    expect(chicklet([span(), span({ kind: "managed_reference", start: 20 })])).toBe("2 hidden");
  });

  it("keeps the single-kind labels", () => {
    expect(chicklet([span()])).toBe("1 secret removed");
    expect(chicklet([span({ kind: "observer_mask" }), span({ kind: "observer_mask", start: 20 })])).toBe(
      "2 values not shown",
    );
  });
});
