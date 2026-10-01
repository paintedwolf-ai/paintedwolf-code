import { render } from "@solidjs/testing-library";
import { beforeEach, describe, expect, it } from "vitest";
import { resetStructuredToolPresentationCacheForTests } from "../../chat/tool/tool-part-structured.ts";
import type { Message, RedactedSpan, ToolCall } from "../../api/types.ts";
import { toolPartFromCall } from "../../chat/tool/tool-part-model.ts";
import { StructuredToolBody } from "./StructuredToolBody.tsx";

const SECRET_LEN = 10;

function span(field: string, start: number): RedactedSpan {
  return {
    field,
    start,
    length: SECRET_LEN,
    kind: "secret",
    source: "shape_rule",
    rule_id: "kingfisher.aws.1",
    rule_title: "AWS Access Key",
  };
}

const ASSISTANT_FIELDS = {
  role: "assistant" as const,
  origin: "model" as const,
  authority: "none" as const,
  trust_tier: "trusted" as const,
  created_at: "2026-01-01T00:00:00Z",
};

const TOOL_FIELDS = {
  role: "tool" as const,
  origin: "tool" as const,
  authority: "none" as const,
  trust_tier: "untrusted" as const,
  created_at: "2026-01-01T00:00:01Z",
};

const COMMAND = "curl -H 'Auth: [REDACTED]' https://api.example.com";
type CommandToolCall = ToolCall & { args: { command: string } };

const call: CommandToolCall = {
  id: "call-1",
  name: "command",
  args: { command: COMMAND },
};

const MARKER_AT = COMMAND.indexOf("[REDACTED]");

function renderPart(part: ReturnType<typeof toolPartFromCall>) {
  return render(() => <StructuredToolBody part={part} projectId="proj-1" />);
}

const READ_PATH = "src/config/[REDACTED]/app.ts";

function readCallPart(spans: RedactedSpan[]) {
  const readCall: ToolCall = {
    id: "call-read",
    name: "read",
    args: { path: READ_PATH },
  };
  const assistant: Message = {
    id: "a-read",
    ...ASSISTANT_FIELDS,
    content: "",
    tool_calls: [readCall],
    ...(spans.length > 0 ? { host_secret_redaction: { spans } } : {}),
  };
  return toolPartFromCall(readCall, undefined, assistant.id, undefined, {
    message: assistant,
    index: 0,
  });
}

describe("tool-call argument redaction", () => {
  beforeEach(() => resetStructuredToolPresentationCacheForTests());

  it("paints args spans the assistant row carries for a call in flight", () => {
    const assistant: Message = {
      id: "a1",
      ...ASSISTANT_FIELDS,
      content: "",
      tool_calls: [call],
      host_secret_redaction: {
        spans: [span("tool_calls.0.args.command", MARKER_AT)],
      },
    };
    const part = toolPartFromCall(call, undefined, assistant.id, undefined, {
      message: assistant,
      index: 0,
    });

    const { container } = renderPart(part);

    const mark = container.querySelector(".den-redaction-mark");
    expect(mark?.textContent).toBe("[REDACTED]");
    expect(mark?.getAttribute("data-tip")).toContain("AWS Access Key");
  });

  it("uses the call's own index, not the first call's spans", () => {
    const other: ToolCall = { id: "call-0", name: "read", args: { path: "a.txt" } };
    const assistant: Message = {
      id: "a1",
      ...ASSISTANT_FIELDS,
      content: "",
      tool_calls: [other, call],
      host_secret_redaction: {
        spans: [span("tool_calls.1.args.command", MARKER_AT)],
      },
    };
    const painted = toolPartFromCall(call, undefined, assistant.id, undefined, {
      message: assistant,
      index: 1,
    });
    const misindexed = toolPartFromCall(call, undefined, assistant.id, undefined, {
      message: assistant,
      index: 0,
    });

    expect(renderPart(painted).container.querySelector(".den-redaction-mark")).toBeTruthy();
    expect(
      renderPart(misindexed).container.querySelector(".den-redaction-mark"),
    ).toBeNull();
  });

  it("reads the result row's own args spans once the call finishes", () => {
    const assistant: Message = {
      id: "a1",
      ...ASSISTANT_FIELDS,
      content: "",
      tool_calls: [call],
    };
    const result: Message = {
      id: "t1",
      ...TOOL_FIELDS,
      content: "done",
      tool_result: {
        content: "done",
        tool_call_id: call.id,
        tool: call.name,
        tool_args: call.args,
      },
      host_secret_redaction: {
        spans: [span("tool_result.tool_args.command", MARKER_AT)],
      },
    };
    const part = toolPartFromCall(call, result, assistant.id, undefined, {
      message: assistant,
      index: 0,
    });

    const { container } = renderPart(part);

    expect(container.querySelector(".den-redaction-mark")?.textContent).toBe(
      "[REDACTED]",
    );
  });

  it("leaves marker-shaped argument text alone when the host stamped nothing", () => {
    const assistant: Message = {
      id: "a1",
      ...ASSISTANT_FIELDS,
      content: "",
      tool_calls: [call],
    };
    const part = toolPartFromCall(call, undefined, assistant.id, undefined, {
      message: assistant,
      index: 0,
    });

    const { container } = renderPart(part);

    expect(container.textContent).toContain("[REDACTED]");
    expect(container.querySelector(".den-redaction-mark")).toBeNull();
  });

  it("refuses to paint an argument the card reformatted", () => {
    // Trimming changes offsets, so the card omits field provenance.
    const paddedCommand = `  curl [REDACTED] end`;
    const padded: CommandToolCall = {
      id: "call-2",
      name: "command",
      args: { command: paddedCommand },
    };
    const assistant: Message = {
      id: "a1",
      ...ASSISTANT_FIELDS,
      content: "",
      tool_calls: [padded],
      host_secret_redaction: {
        spans: [span("tool_calls.0.args.command", paddedCommand.indexOf("[REDACTED]"))],
      },
    };
    const part = toolPartFromCall(padded, undefined, assistant.id, undefined, {
      message: assistant,
      index: 0,
    });

    const { container } = renderPart(part);

    expect(container.textContent).toContain("[REDACTED]");
    expect(container.querySelector(".den-redaction-mark")).toBeNull();
  });

  it("carries no args provenance when neither row stamped anything", () => {
    const assistant: Message = {
      id: "a1",
      ...ASSISTANT_FIELDS,
      content: "",
      tool_calls: [call],
    };
    expect(
      toolPartFromCall(call, undefined, assistant.id, undefined, {
        message: assistant,
        index: 0,
      }).argsRedaction,
    ).toBeNull();
  });
});

describe("a redacted path is not an openable link", () => {
  beforeEach(() => resetStructuredToolPresentationCacheForTests());

  it("renders a clean path as a source link", () => {
    const { container } = renderPart(readCallPart([]));

    expect(container.querySelector('[data-testid="source-path-link"]')).toBeTruthy();
    expect(container.querySelector(".den-redaction-mark")).toBeNull();
  });

  it("renders a path carrying a replacement as marked text, not a link", () => {
    const at = Array.from(READ_PATH).indexOf("[");
    const { container } = renderPart(
      readCallPart([span("tool_calls.0.args.path", at)]),
    );

    expect(container.querySelector('[data-testid="source-path-link"]')).toBeNull();
    expect(container.querySelector(".den-redaction-mark")?.textContent).toBe(
      "[REDACTED]",
    );
  });
});

it("renders provenance on the resolved target without losing its handle", () => {
  const handle = "command-handle";
  const outputCall: ToolCall = { id: "call-output", name: "command_output", args: { handle, cursor: 0 } };
  const result: Message = {
    id: "output", ...TOOL_FIELDS, content: "done",
    tool_result: { tool: "command_output", tool_args: outputCall.args, content: "done", display_subject: COMMAND },
    host_secret_redaction: { spans: [span("tool_result.display_subject", MARKER_AT)] },
  };
  const { container, getByText } = renderPart(toolPartFromCall(outputCall, result, "assistant"));
  expect(getByText(handle)).toBeTruthy();
  expect(container.querySelector(".den-redaction-mark")?.textContent).toBe("[REDACTED]");
});
