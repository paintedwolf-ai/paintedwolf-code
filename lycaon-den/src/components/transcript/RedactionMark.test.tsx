import { render } from "@solidjs/testing-library";
import { describe, expect, it } from "vitest";

import { RedactedText, RedactionMark } from "./RedactionMark.tsx";
import type { Message, RedactedSpan } from "../../api/types.ts";

function span(over: Partial<RedactedSpan> = {}): RedactedSpan {
  return {
    field: "content",
    start: 0,
    length: 10,
    kind: "secret",
    source: "shape_rule",
    rule_id: "kingfisher.gitea.1",
    rule_title: "Gitea Access Token",
    ...over,
  };
}

describe("RedactionMark", () => {
  it("keeps the marker as real text so a copy carries it, never the value", () => {
    const { container } = render(() => <RedactionMark span={span()} text="[REDACTED]" />);
    const mark = container.querySelector(".den-redaction-mark");
    expect(mark?.textContent).toBe("[REDACTED]");
  });

  it("offers no reveal affordance", () => {
    const { container } = render(() => <RedactionMark span={span()} text="[REDACTED]" />);
    const mark = container.querySelector(".den-redaction-mark");
    expect(mark?.querySelector("button")).toBeNull();
    expect(mark?.getAttribute("role")).toBeNull();
    // Nothing in the DOM may carry the original value, in text or in an attribute.
    expect(container.innerHTML).not.toContain("67ff");
  });

  it("reads a mask as a standing policy rather than a detection", () => {
    const { container } = render(() => (
      <RedactionMark span={span({ kind: "observer_mask", source: "policy" })} text="[redacted]" />
    ));
    const mark = container.querySelector(".den-redaction-mark");
    expect(mark?.classList.contains("den-redaction-mark--mask")).toBe(true);
    expect(mark?.getAttribute("data-tip")).toBe("Not shown to observers");
  });

  it("keeps a reference readable and says the source still holds the value", () => {
    const reference = "{{paintedwolf-secret:6f1c2b9e-0d4a-4c7e-9b1f-2a3d4e5f6a7b}}";
    const { container } = render(() => (
      <RedactionMark
        span={span({ kind: "managed_reference", source: "remembered_match", length: reference.length })}
        text={reference}
      />
    ));
    const mark = container.querySelector(".den-redaction-mark");
    expect(mark?.textContent).toBe(reference);
    expect(mark?.classList.contains("den-redaction-mark--reference")).toBe(true);
    expect(mark?.classList.contains("den-redaction-mark--mask")).toBe(false);
    expect(mark?.getAttribute("data-redaction-kind")).toBe("managed_reference");
    expect(mark?.getAttribute("data-tip")).toContain("the source still holds the value");
  });

  it("names the rule on a detection", () => {
    const { container } = render(() => <RedactionMark span={span()} text="[REDACTED]" />);
    expect(container.querySelector(".den-redaction-mark")?.getAttribute("data-tip")).toContain(
      "Gitea Access Token",
    );
  });
});

describe("RedactedText", () => {
  const message: Pick<Message, "host_secret_redaction"> = {
    host_secret_redaction: {
      spans: [span({ start: 13, length: 10 })],
    },
  };

  it("marks the host's span and leaves the rest as text", () => {
    const { container } = render(() => (
      <RedactedText message={message} field="content" text="GITEA_TOKEN=[REDACTED] ./release-cli" />
    ));
    const marks = container.querySelectorAll(".den-redaction-mark");
    expect(marks).toHaveLength(1);
    expect(container.textContent).toBe("GITEA_TOKEN=[REDACTED] ./release-cli");
  });

  it("renders plain text when the host recorded nothing for the field", () => {
    const { container } = render(() => (
      <RedactedText message={message} field="tool_result.content" text="[REDACTED] quoted in prose" />
    ));
    expect(container.querySelectorAll(".den-redaction-mark")).toHaveLength(0);
    expect(container.textContent).toBe("[REDACTED] quoted in prose");
  });
});
