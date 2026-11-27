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

  it("loads without Vitest module mocking when VITEST is absent", async () => {
    vi.stubEnv("VITEST", "");
    vi.resetModules();
    const importActual = vi.spyOn(vi, "importActual");
    try {
      const browserFixture = await import("./client-fixture.ts");
      const getAttention = vi.fn().mockResolvedValue({ items: [] });
      const client = browserFixture.stubClient({ getAttention });

      expect(importActual).not.toHaveBeenCalled();
      expect(browserFixture.stubbedFunction(client.getAttention)).toBe(getAttention);
      await expect(client.getAttention()).resolves.toEqual({ items: [] });
      expect(() => client.listProjects).toThrow(
        "Test client did not stub listProjects",
      );
    } finally {
      importActual.mockRestore();
      vi.unstubAllEnvs();
      vi.resetModules();
    }
  });
});
