import { describe, expect, it } from "vitest";
import { visualFromPart } from "./visual-artifact-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

function part(overrides: Partial<ToolPartView>): ToolPartView {
  return {
    id: "m:call_0",
    toolCallId: "call_0",
    assistantMessageId: "assistant-message",
    messageId: "m",
    tool: "emit_visual_fixture",
    kind: "generic",
    status: "completed",
    ...overrides,
  };
}

describe("visualFromPart", () => {
  const storeVisual = {
    id: "art-1",
    mime: "image/png",
    store_ref: true,
    source: "capture" as const,
    caption: "Fixture",
  };

  it("returns store-ref visual on completed tool rows", () => {
    const visual = visualFromPart(part({ visual: storeVisual }));
    expect(visual?.id).toBe("art-1");
    expect(visual?.store_ref).toBe(true);
  });

  it("keeps the host-stamped pixel size for the tool card frame", () => {
    const visual = visualFromPart(
      part({ visual: { ...storeVisual, width: 800, height: 600 } }),
    );
    expect(visual).toMatchObject({ width: 800, height: 600 });
  });

  it("returns the visual on error outcomes", () => {
    const visual = visualFromPart(
      part({ status: "error", visual: storeVisual }),
    );
    expect(visual?.id).toBe("art-1");
    expect(visual?.caption).toBe("Fixture");
  });

  it("returns a host-stamped visual while the part is still running", () => {
    expect(
      visualFromPart(part({ status: "running", visual: storeVisual }))?.id,
    ).toBe("art-1");
  });

  it("returns null when the part has no visual id", () => {
    expect(visualFromPart(part({ status: "running" }))).toBeNull();
    expect(visualFromPart(part({ status: "error" }))).toBeNull();
    expect(
      visualFromPart(
        part({ visual: { id: "  ", mime: "image/png", source: "capture" } }),
      ),
    ).toBeNull();
  });
});
