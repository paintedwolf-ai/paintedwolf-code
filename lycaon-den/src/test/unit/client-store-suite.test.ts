/** Client, store, and event integration contracts. */
import { describe, expect, it, vi } from "vitest";
import { createLycaonClient } from "../../api/client-impl.ts";
import { TOPIC_STORE_INVALIDATION } from "../../api/events.ts";
import { ALL_EVENT_TOPICS } from "../../api/event-topics.generated.ts";
import { createAppStore } from "../../store/app-state.ts";

describe("client + store suite", () => {
  it("TOPIC_STORE_INVALIDATION covers every EventTopic", () => {
    for (const topic of ALL_EVENT_TOPICS) {
      // Payload-carried topics need no store refetch.
      if (
        topic === "message" ||
        topic === "worker" ||
        topic === "board" ||
        topic === "process" ||
        topic === "preview" ||
        topic === "attention" ||
        topic === "activity" ||
        topic === "oar" ||
        topic === "cli_open" ||
        topic === "source_changed" ||
        topic === "source_operation" ||
        topic === "source_view" ||
        topic === "file_briefing" ||
        topic === "editor_document" ||
        topic === "artifact" ||
        topic === "turn_clock" ||
        topic === "turn_load" ||
        topic === "agent_presence" ||
        topic === "chat_vault" ||
        // Settings invalidates by facet.
        topic === "settings"
      ) {
        expect(TOPIC_STORE_INVALIDATION[topic]).toEqual([]);
        continue;
      }
      expect(TOPIC_STORE_INVALIDATION[topic]?.length).toBeGreaterThan(0);
    }
    expect(Object.keys(TOPIC_STORE_INVALIDATION)).toHaveLength(
      ALL_EVENT_TOPICS.length,
    );
  });

  it("LycaonClient sends MCP routes with bearer", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify([{ id: "scan", enabled: false }]), {
          status: 200,
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify([{ provider_id: "scan", ok: true }]), {
          status: 200,
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    const client = createLycaonClient({
      baseUrl: "http://127.0.0.1:8787",
      apiToken: "tok",
    });
    await client.listMcpProviders();
    await client.checkMcpProviders();

    const urls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(urls[0]).toContain("/v1/mcp/providers");
    expect(urls[1]).toContain("/v1/mcp/providers/check");
    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(new Headers(init.headers).get("Authorization")).toBe("Bearer tok");
  });

  it("app store merges session SSE events into current session", () => {
    const store = createAppStore();
    store.actions.setCurrentSession({
      id: "sess-1",
      owner_person_id: "00000000-0000-4000-8000-000000000002",
      project_id: "00000000-0000-4000-8000-000000000001",
      workspace_path: "/tmp/p",
      posture: "build",
      status: "idle",
      created_at: "t",
      activity_at: "t",
      updated_at: "t",
    });
    store.actions.mergeSession({ id: "sess-1", status: "busy" });
    expect(store.state.currentSession?.status).toBe("busy");
  });
});
