import { fireEvent, render, screen, waitFor, cleanup } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ElevatedAccessSummary } from "../../api/types.ts";
import { setStatusChipNavigationSink } from "../../chat/status/status-navigation-sink.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { resetSurfaceQueriesForTests } from "../../ui/surface-query.ts";
import { createComposerElevatedAccess, ElevatedAccessButton } from "./ComposerElevatedAccess.tsx";

function summary(overrides: Partial<ElevatedAccessSummary> = {}): ElevatedAccessSummary {
  return { root_session_id: "chat", approvals_enabled: true, total: 1, shared_scopes: [],
    records: [{ id: "grant_host", kind: "grant", title: "Host execution", scope: "chat", effects: ["host_execution"] }], ...overrides };
}

function setup(get = vi.fn(async () => summary())) {
  const navigate = vi.fn();
  setStatusChipNavigationSink(navigate);
  const client = stubClient({ getElevatedAccess: get, revokeElevatedAccess: vi.fn() });
  const [sessionId, setSessionId] = createSignal("chat");
  const [connected, setConnected] = createSignal(true);
  const [revision, setRevision] = createSignal(0);
  render(() => {
    let input!: HTMLTextAreaElement;
    const state = createComposerElevatedAccess({ client: () => client, connected, sessionId,
      revision: () => `${revision()}:${connected()}`, focus: () => input.focus() });
    return <><textarea ref={input} /><ElevatedAccessButton state={state} /></>;
  });
  return { get, navigate, client, setSessionId, setConnected, refresh: () => setRevision((v) => v + 1) };
}

afterEach(() => { cleanup(); resetSurfaceQueriesForTests(); setStatusChipNavigationSink(null); });

describe("composer elevated access", () => {
  it("opens Project configuration approvals without revoking on click or keyboard activation", async () => {
    const h = setup();
    const button = await screen.findByRole("button", { name: "Review 1 elevated-access approval in Project configuration" });
    expect(button.getAttribute("data-tip")).toContain("Project configuration → Approvals");
    fireEvent.click(button);
    button.focus();
    fireEvent.keyDown(button, { key: "Enter" });
    expect(h.navigate).toHaveBeenCalledWith({ kind: "project-configuration", section: "approvals" });
    expect(h.client.revokeElevatedAccess).not.toHaveBeenCalled();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("hides the lock when approvals are disabled or no live elevated records remain", async () => {
    let current = summary();
    const h = setup(vi.fn(async () => current));
    await screen.findByTestId("composer-elevated-access");
    current = summary({ approvals_enabled: false });
    h.refresh();
    await waitFor(() => expect(screen.queryByTestId("composer-elevated-access")).toBeNull());
    current = summary({ total: 0, records: [] });
    h.refresh();
    await waitFor(() => expect(h.get).toHaveBeenCalledTimes(3));
    expect(screen.queryByTestId("composer-elevated-access")).toBeNull();
  });

  it("keeps a visible lock disabled while disconnected", async () => {
    const h = setup();
    const button = await screen.findByTestId("composer-elevated-access") as HTMLButtonElement;
    h.setConnected(false);
    expect(button.disabled).toBe(true);
    expect(button.getAttribute("aria-label")).toContain("unavailable");
    fireEvent.click(button);
    expect(h.navigate).not.toHaveBeenCalled();
  });

  it("returns focus to the composer when the last approval expires", async () => {
    const expires = new Date(Date.now() + 250).toISOString();
    let reads = 0;
    setup(vi.fn(async () => ++reads === 1
      ? summary({ records: [{ ...summary().records[0]!, expires_at: expires }] })
      : summary({ total: 0, records: [] })));
    const button = await screen.findByTestId("composer-elevated-access");
    button.focus();
    await waitFor(() => expect(screen.queryByTestId("composer-elevated-access")).toBeNull());
    expect(document.activeElement).toBe(screen.getByRole("textbox"));
  });
});
