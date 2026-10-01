import { describe, expect, it, vi } from "vitest";
import {
  openInSearch,
  registerOpenInSearchSink,
} from "./search-nav.ts";

describe("search-nav openInSearch", () => {
  it("delivers origin project and seeded DSL to the registered sink", () => {
    const sink = vi.fn();
    const unregister = registerOpenInSearchSink(sink);
    openInSearch("proj-origin", "handle:read#1");
    expect(sink).toHaveBeenCalledWith({
      originProjectId: "proj-origin",
      query: "handle:read#1",
    });
    unregister();
  });

  it("buffers requests until a sink is registered", () => {
    const sink = vi.fn();
    openInSearch("proj-buffered", "session:sess-1");
    const unregister = registerOpenInSearchSink(sink);
    expect(sink).toHaveBeenCalledWith({
      originProjectId: "proj-buffered",
      query: "session:sess-1",
    });
    unregister();
  });
});
