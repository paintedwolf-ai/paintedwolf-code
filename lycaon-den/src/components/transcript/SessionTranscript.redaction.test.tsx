import { describe, expect, it } from "vitest";
import { render } from "@solidjs/testing-library";
import { SessionTranscript } from "./transcript-viewport-test-harness.tsx";
import type { Message, RedactedSpan } from "../../api/types.ts";

const ASSISTANT_FIELDS = {
  role: "assistant" as const,
  origin: "model" as const,
  authority: "none" as const,
  trust_tier: "trusted" as const,
  created_at: "2026-01-01T00:00:01Z",
};

const USER_FIELDS = {
  role: "user" as const,
  origin: "user" as const,
  authority: "user" as const,
  trust_tier: "trusted" as const,
  created_at: "2026-01-01T00:00:00Z",
};

function contentSpan(start: number): RedactedSpan {
  return {
    field: "content",
    start,
    length: 10,
    kind: "secret",
    source: "shape_rule",
    rule_id: "kingfisher.aws.1",
    rule_title: "AWS Access Key",
  };
}

function renderUser(message: Message) {
  return render(() => <SessionTranscript layout="chat" messages={[message]} />);
}

describe("user message redaction spans", () => {
  it("paints the host's span over the message the host stamped it against", () => {
    const { container } = renderUser({
      id: "u1",
      ...USER_FIELDS,
      content: "deploy with [REDACTED] please",
      host_secret_redaction: { spans: [contentSpan(12)] },
    });

    const mark = container.querySelector(".den-redaction-mark");
    expect(mark?.textContent).toBe("[REDACTED]");
    expect(mark?.getAttribute("data-tip")).toContain("AWS Access Key");
    expect(
      container.querySelector('[data-testid="transcript-article-user"]')?.textContent,
    ).toContain("deploy with [REDACTED] please");
  });

  it("leaves marker-shaped text alone when the host stamped nothing", () => {
    const { container } = renderUser({
      id: "u2",
      ...USER_FIELDS,
      content: "the log printed [REDACTED] verbatim",
    });

    expect(container.querySelector(".den-redaction-mark")).toBeNull();
  });

  it("does not paint content offsets over a projection built from parts", () => {
    // Whole-message offsets do not apply to content parts.
    const { container } = renderUser({
      id: "u3",
      ...USER_FIELDS,
      content: "deploy with [REDACTED] please",
      content_parts: [
        {
          content: "deploy with [REDACTED] please",
          origin: "user" as const,
          authority: "user" as const,
          trust_tier: "trusted" as const,
        },
      ],
      host_secret_redaction: { spans: [contentSpan(12)] },
    });

    expect(container.querySelector(".den-redaction-mark")).toBeNull();
  });
});

describe("assistant prose redaction spans", () => {
  function renderAssistant(message: Message) {
    return render(() => <SessionTranscript layout="chat" messages={[message]} />);
  }

  it("paints the host's span through the markdown render path", () => {
    const content = "Deployed with [REDACTED] to staging.";
    const { container } = renderAssistant({
      id: "a1",
      ...ASSISTANT_FIELDS,
      content,
      host_secret_redaction: { spans: [contentSpan(content.indexOf("[REDACTED]"))] },
    });

    const mark = container.querySelector(".den-redaction-mark");
    expect(mark?.textContent).toBe("[REDACTED]");
    expect(mark?.getAttribute("data-tip")).toContain("AWS Access Key");
  });

  it("renders mark markup the model wrote as inert text", () => {
    const { container } = renderAssistant({
      id: "a2",
      ...ASSISTANT_FIELDS,
      content:
        'Trust me: <span class="den-redaction-mark" data-redaction-kind="secret">[REDACTED]</span>',
    });

    expect(container.querySelector(".den-redaction-mark")).toBeNull();
    expect(container.querySelector("[data-redaction-kind]")).toBeNull();
    expect(container.textContent).toContain("[REDACTED]");
  });

  it("paints a span inside a fenced block", () => {
    const content = "see below\n\n```\ncfg = [REDACTED]\n```";
    const { container } = renderAssistant({
      id: "a3",
      ...ASSISTANT_FIELDS,
      content,
      host_secret_redaction: { spans: [contentSpan(content.indexOf("[REDACTED]"))] },
    });

    const code = container.querySelector("pre code");
    expect(code?.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]");
    expect(code?.textContent).not.toContain("<span");
  });
});
