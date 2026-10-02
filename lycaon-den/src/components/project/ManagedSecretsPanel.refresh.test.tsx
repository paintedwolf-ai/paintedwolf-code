import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import type { ManagedSecret, ManagedSecretUseList } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { ResidentPresenceProvider } from "../../ui/resident-presence-context.tsx";
import type { ResidentPresence } from "../../ui/resident-surfaces.ts";
import { ManagedSecretsPanel } from "./ManagedSecretsPanel.tsx";

const id = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const secret: ManagedSecret = {
  reference: `{{paintedwolf-secret:${id}}}`, name: "Local service", purpose: "Validation",
  scope: "project", origin: "settings_entered", format: "base64url", entropy_bits: 0,
  created_at: "2026-09-01T10:00:00Z", state: "active", version: 1, use_count: 0, reveal_count: 0,
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("managed secret refresh boundaries", () => {
  it("refreshes summary and selected history when returning to the resident panel", async () => {
    const [presence, setPresence] = createSignal<ResidentPresence>("active");
    let current = secret;
    const client = stubClient({
      listProjectManagedSecrets: vi.fn(async () => ({ secrets: [current], count: 1 })),
      listProjectManagedSecretUses: vi.fn(async () => ({ uses: [], count: 0 })),
    });
    render(() => <ResidentPresenceProvider presence={presence()}><ManagedSecretsPanel client={client} projectId="project-1" /></ResidentPresenceProvider>);
    fireEvent.click(await screen.findByTestId(`managed-secret-${id}`));
    await screen.findByTestId("managed-secret-card");
    setPresence("idle");
    current = { ...secret, use_count: 6, last_used_at: "2026-09-01T11:00:00Z" };
    setPresence("active");
    await waitFor(() => expect(screen.getByTestId("managed-secret-card").textContent).toContain("6 uses"));
    expect(client.listProjectManagedSecretUses).toHaveBeenCalledTimes(2);
  });

  it("ignores a previous project's list after switching project", async () => {
    const old = deferred<{ secrets: ManagedSecret[]; count: number }>();
    const [project, setProject] = createSignal("old-project");
    const client = stubClient({
      listProjectManagedSecrets: vi.fn((projectId: string) => projectId === "old-project" ? old.promise : Promise.resolve({ secrets: [{ ...secret, name: "Current project key" }], count: 1 })),
      listProjectManagedSecretUses: vi.fn(async () => ({ uses: [], count: 0 })),
    });
    render(() => <ManagedSecretsPanel client={client} projectId={project()} />);
    setProject("new-project");
    await screen.findByText("Current project key");
    old.resolve({ secrets: [{ ...secret, name: "Previous project key" }], count: 1 });
    await old.promise;
    await waitFor(() => expect(screen.queryByText("Previous project key")).toBeNull());
    expect(screen.getByText("Current project key")).toBeTruthy();
  });

  it("ignores history from a previously selected secret", async () => {
    const old = deferred<ManagedSecretUseList>();
    const secondId = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
    const second = { ...secret, reference: `{{paintedwolf-secret:${secondId}}}`, name: "Second service" };
    const client = stubClient({
      listProjectManagedSecrets: vi.fn(async () => ({ secrets: [secret, second], count: 2 })),
      listProjectManagedSecretUses: vi.fn((_projectId: string, secretId: string) => secretId === id ? old.promise : Promise.resolve({ uses: [], count: 0 })),
    });
    render(() => <ManagedSecretsPanel client={client} projectId="project-1" />);
    fireEvent.click(await screen.findByTestId(`managed-secret-${id}`));
    fireEvent.click(await screen.findByTestId("managed-secrets-back"));
    fireEvent.click(await screen.findByTestId(`managed-secret-${secondId}`));
    await waitFor(() => expect(screen.getByTestId("managed-secret-card").textContent).toContain("Second service"));
    old.resolve({ uses: [{ session_id: "old-session", tool_call_id: "old-call", tool_name: "old-tool", outcome: "resolved", delivery: "handed_off", used_at: "2026-09-01T11:00:00Z" }] });
    await old.promise;
    await waitFor(() => expect(screen.queryByText(/old-tool/)).toBeNull());
  });
});
