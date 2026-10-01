import { describe, expect, it, vi } from "vitest";
import { stubClient } from "./client-fixture.ts";

// Type checking rejects unknown client methods.
if (false) {
  // @ts-expect-error getAttentoin is not a LycaonClient method.
  stubClient({ getAttentoin: vi.fn() });
}

describe("stubClient", () => {
  it("returns declared methods", async () => {
    const getAttention = vi.fn().mockResolvedValue({ items: [] });
    const client = stubClient({ getAttention });

    await expect(client.getAttention()).resolves.toEqual({ items: [] });
    expect(getAttention).toHaveBeenCalledOnce();
  });

  it("fails when code reaches an undeclared method", () => {
    const client = stubClient();
    expect(() => client.getAttention).toThrow(
      "Test client did not stub getAttention",
    );
  });
});
