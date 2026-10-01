import { describe, expect, it, vi } from "vitest";
import { createLycaonClient } from "./client-impl.ts";

describe("LycaonClient integration", () => {
  it("createSession round-trip via mock fetch", async () => {
    const session = {
      id: "sess-new",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    };

    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify(session), {
          status: 201,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "tok",
    });

    const created = await client.createSession({
      project_id: "00000000-0000-4000-8000-000000000001",
      posture: "build",
    });
    expect(created.id).toBe("sess-new");
  });
});
