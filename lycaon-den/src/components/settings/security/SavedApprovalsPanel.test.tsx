import { stubClient } from "../../../test/client-fixture.ts";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import type { LycaonClient } from "../../../api/client.ts";
import type { ApprovalGrant } from "../../../api/types.ts";
import { invalidateApprovalGrantsCache } from "../../../settings/security/approval-grants-cache.ts";
import { SAVED_APPROVALS_COPY } from "../../../settings/security/saved-approvals-copy.ts";
import { SavedApprovalsPanel } from "./SavedApprovalsPanel.tsx";

function openSavedApprovalsOverflow() {
  fireEvent.click(screen.getByTestId("saved-approvals-overflow-trigger"));
}

function mockClient(
  overrides: Partial<LycaonClient> & {
    listApprovalGrants: LycaonClient["listApprovalGrants"];
  },
): LycaonClient {
  return stubClient({
    revokeApprovalGrants: vi.fn(),
    createApprovalGrant: vi.fn(),
    resolveSocketGrant: vi.fn(),
    listProjects: vi.fn().mockResolvedValue([]),
    ...overrides,
  });
}

function grant(
  overrides: Partial<ApprovalGrant> & { id: string },
): ApprovalGrant {
  return {
    title: overrides.title ?? "Allow read for this project",
    scope: overrides.scope ?? "project",
    category: overrides.category ?? "tool",
    pattern: overrides.pattern ?? "read",
    coverage: overrides.coverage ?? "the read tool",
    granted_at: overrides.granted_at ?? "2026-01-01T00:00:00Z",
    expires_when: overrides.expires_when ?? "in 7 days",
    reask_when: overrides.reask_when ?? "the project changes",
    ...overrides,
  };
}

function bulkRevokeAll(ids: readonly string[]) {
  return { results: ids.map((id) => ({ id, revoked: true })) };
}

describe("SavedApprovalsPanel", () => {
  beforeEach(() => {
    invalidateApprovalGrantsCache();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("puts host-classified elevated approvals first and limits the project view to applicable records", async () => {
    const elevated = ["host_execution"] as const;
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({ grants: [
        grant({ id: "ordinary", scope: "project", project_id: "proj-1", pattern: "ordinary" }),
        grant({ id: "host", scope: "chat", chat_session_id: "chat-1", title: "Host execution", coverage: "Commands outside the sandbox", pattern: "host", elevated_effects: [...elevated] }),
        grant({ id: "worker", scope: "chat", chat_session_id: "worker-1", pattern: "worker", elevated_effects: [...elevated] }),
        grant({ id: "shared", scope: "device", pattern: "shared", elevated_effects: ["direct_network"] }),
        grant({ id: "other-project", scope: "project", project_id: "proj-2", pattern: "other-project", elevated_effects: [...elevated] }),
        grant({ id: "other-chat", scope: "chat", chat_session_id: "chat-2", pattern: "other-chat", elevated_effects: [...elevated] }),
      ] }),
      getElevatedAccess: vi.fn().mockResolvedValue({
        root_session_id: "chat-1", approvals_enabled: true, total: 3, shared_scopes: ["device"],
        records: [{ id: "worker", kind: "grant", title: "Worker", scope: "chat", effects: ["host_execution"] }],
      }),
    });
    render(() => <SavedApprovalsPanel client={client} projectId="proj-1" sessionId="chat-1" />);
    const band = await screen.findByTestId("saved-approvals-band-elevated");
    await waitFor(() => expect(band.querySelector('[data-grant-id="worker"]')).toBeTruthy());
    expect(band.querySelector('[data-grant-id="host"]')).toBeTruthy();
    expect(band.querySelector('[data-grant-id="host"]')?.textContent).toContain("Host execution");
    expect(band.querySelector('[data-grant-id="host"]')?.textContent).toContain("This chat");
    expect(band.querySelector('[data-grant-id="host"]')?.textContent).toContain("Commands outside the sandbox");
    expect(band.querySelector('[data-grant-id="host"]')?.querySelector('[data-testid="saved-approvals-revoke"]')).toBeTruthy();
    expect(band.compareDocumentPosition(screen.getByTestId("saved-approvals-toolbar")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(band.querySelector('[data-grant-id="shared"]')).toBeTruthy();
    expect(screen.queryByText("other-project")).toBeNull();
    expect(screen.queryByText("other-chat")).toBeNull();
    expect(screen.getByTestId("saved-approvals-band-project").querySelector('[data-grant-id="ordinary"]')).toBeTruthy();
    expect(screen.queryByTestId("saved-approvals-add-socket")).toBeNull();
    expect(screen.getByTestId("saved-approvals-toolbar").textContent).toContain("All · 1");
    fireEvent.input(screen.getByTestId("saved-approvals-search"), { target: { value: "no match" } });
    expect(screen.getByTestId("saved-approvals-band-elevated").querySelector('[data-grant-id="host"]')).toBeTruthy();
    expect(screen.getByTestId("saved-approvals-no-matches")).toBeTruthy();
  });

  it("confirms and revokes live elevated access from the approvals page", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    let rows = [grant({ id: "grant_host", scope: "chat", chat_session_id: "chat-1",
      pattern: "host", elevated_effects: ["host_execution"] })];
    const listApprovalGrants = vi.fn().mockImplementation(async () => ({ grants: [...rows] }));
    const getElevatedAccess = vi.fn().mockImplementation(async () => ({
      root_session_id: "chat-1", approvals_enabled: true, total: rows.length, shared_scopes: [],
      records: rows.map((row) => ({ id: row.id, kind: "grant", title: row.title, scope: row.scope, effects: row.elevated_effects })),
    }));
    const revokeElevatedAccess = vi.fn().mockImplementation(async () => {
      rows = [];
      return { results: [{ id: "grant_host", disposition: "revoked" }], remaining: {
        root_session_id: "chat-1", approvals_enabled: true, total: 0, records: [], shared_scopes: [],
      } };
    });
    const client = mockClient({ listApprovalGrants, getElevatedAccess, revokeElevatedAccess });
    render(() => <SavedApprovalsPanel client={client} projectId="proj-1" sessionId="chat-1" />);
    await screen.findByTestId("saved-approvals-revoke-elevated");
    expect(revokeElevatedAccess).not.toHaveBeenCalled();
    expect(screen.queryByTestId("saved-approvals-toolbar")).toBeNull();
    fireEvent.click(await screen.findByTestId("saved-approvals-revoke-elevated"));
    await waitFor(() => expect(revokeElevatedAccess).toHaveBeenCalledWith("chat-1"));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining("Shared project and device approvals"));
    await waitFor(() => expect(screen.queryByTestId("saved-approvals-band-elevated")).toBeNull());
    expect(screen.getByTestId("saved-approvals-action-notice").textContent).toContain("Running processes may retain access");
  });

  it("shows readable secret names and recipients in the revocation inventory", async () => {
    const client = mockClient({ listApprovalGrants: vi.fn().mockResolvedValue({ grants: [grant({
      id: "secret-service", category: "secret", scope: "chat", chat_session_id: "chat-1",
      title: "Send for this chat", pattern: "this secret", secret_names: ["Service password"],
      secret_recipients: [
        { label: "Processes in this chat", surface: "command", kind: "process" },
        { label: "http://127.0.0.1:8080", surface: "http_request", kind: "service" },
      ],
    })] }) });
    render(() => <SavedApprovalsPanel client={client} />);
    await waitFor(() => expect(screen.getByText("Service password")).toBeTruthy());
    expect(screen.getByTestId("saved-secret-recipients").textContent).toContain("http://127.0.0.1:8080");
    expect(screen.getByTestId("saved-secret-recipients").textContent).toContain("Processes in this chat");
    const search = screen.getByTestId("saved-approvals-search");
    fireEvent.input(search, { target: { value: "SERVICE PASSWORD" } });
    await waitFor(() => expect(screen.getByTestId("saved-secret-recipients")).toBeTruthy());
    fireEvent.input(search, { target: { value: "127.0.0.1:8080" } });
    await waitFor(() => expect(screen.getByTestId("saved-secret-recipients")).toBeTruthy());
    fireEvent.input(search, { target: { value: "different service" } });
    await waitFor(() => expect(screen.queryByTestId("saved-secret-recipients")).toBeNull());
  });

  it("groups rows by scope with chat, project, and device bands", async () => {
    const listApprovalGrants = vi.fn().mockResolvedValue({
      grants: [
        grant({
          id: "t1",
          scope: "chat",
          chat_session_id: "sess-1",
          session_title: "Wire LocalStack into the harness",
          category: "action_set",
          pattern: "digest",
          action_count: 1,
        }),
        grant({
          id: "p1",
          scope: "project",
          project_dir: "/Users/me/proj",
          category: "write_root",
          pattern: "/Users/me/Library/Caches/pnpm",
        }),
        grant({
          id: "d1",
          scope: "device",
          category: "host",
          pattern: "api.anthropic.com",
          project_dir: "/Users/me/proj",
        }),
      ],
    });
    const client = mockClient({ listApprovalGrants });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-band-chat")).toBeTruthy();
    });
    expect(listApprovalGrants).toHaveBeenCalled();
    expect(screen.getByTestId("saved-approvals-band-project")).toBeTruthy();
    expect(screen.getByTestId("saved-approvals-band-device")).toBeTruthy();
    expect(
      screen.getByTestId("saved-approvals-session").textContent,
    ).toContain("Wire LocalStack into the harness");
    expect(screen.getByTestId("saved-approvals-count").textContent).toBe(
      "3 saved approvals",
    );
    expect(screen.getByTestId("saved-approvals-band-device").textContent).toContain(
      SAVED_APPROVALS_COPY.deviceProjectOnly,
    );
    // Exact action identities stay host-only; the row renders only their count.
    expect(
      screen.getByTestId("saved-approvals-actionset-summary").textContent,
    ).toContain(SAVED_APPROVALS_COPY.actionSetSummary(1));
    expect(screen.queryByText("digest")).toBeNull();
  });

  it("renders a direct-IP lease as the command with an unobserved-destinations detail", async () => {
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({
        grants: [
          grant({
            id: "grant_direct",
            scope: "chat",
            category: "direct_ip",
            pattern: "ntpdate time.nist.gov",
            coverage: "`ntpdate time.nist.gov`, reaching the network unobserved",
            title: "Allow direct network for 1 day",
            chat_session_id: "sess-1",
            expires_when: "in 1 day or when this chat is deleted",
          }),
        ],
      }),
    });
    render(() => <SavedApprovalsPanel client={client} />);

    const row = await screen.findByTestId("saved-approvals-row");
    expect(row.getAttribute("data-grant-category")).toBe("direct_ip");
    expect(row.textContent).toContain("ntpdate time.nist.gov");
    expect(row.textContent).toContain(SAVED_APPROVALS_COPY.directIPDetail);
  });

  it("derives category chips from present rows with counts", async () => {
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({
        grants: [
          grant({ id: "g1", category: "tool" }),
          grant({ id: "g2", category: "tool", pattern: "write" }),
          grant({
            id: "g3",
            category: "write_root",
            pattern: "/tmp/x",
          }),
        ],
      }),
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(3);
    });
    expect(screen.getByTestId("saved-approvals-category-all").textContent).toBe(
      "All · 3",
    );
    expect(
      screen.getByTestId("saved-approvals-category-tool").textContent,
    ).toBe("Tool · 2");
    expect(
      screen.getByTestId("saved-approvals-category-write_root").textContent,
    ).toBe("Write access · 1");
    expect(screen.queryByTestId("saved-approvals-category-socket_path")).toBeNull();

    fireEvent.click(screen.getByTestId("saved-approvals-category-write_root"));
    expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(1);
    expect(screen.getByTestId("saved-approvals-count").textContent).toBe(
      "1 of 3",
    );
  });

  it("filters by search across structured fields", async () => {
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({
        grants: [
          grant({ id: "g1", category: "tool" }),
          grant({
            id: "g2",
            category: "host_resource",
            pattern: "screen.capture,audio.input",
            resource_ids: ["screen.capture", "audio.input"],
          }),
        ],
      }),
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(2);
    });
    fireEvent.input(screen.getByTestId("saved-approvals-search"), {
      target: { value: "audio.input" },
    });
    expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(1);
    expect(screen.getAllByText("audio.input")).toBeTruthy();
  });

  it("revokes a row through the bulk endpoint and hides it optimistically", async () => {
    let rows = [
      grant({ id: "g1" }),
      grant({ id: "g2", pattern: "write" }),
    ];
    const listApprovalGrants = vi.fn().mockImplementation(async () => ({
      grants: [...rows],
    }));
    const revokeApprovalGrants = vi
      .fn()
      .mockImplementation(async ({ ids }: { ids: string[] }) => {
        rows = rows.filter((g) => !ids.includes(g.id));
        return bulkRevokeAll(ids);
      });
    const client = mockClient({ listApprovalGrants, revokeApprovalGrants });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(2);
    });
    fireEvent.click(screen.getAllByTestId("saved-approvals-revoke")[0]!);

    await waitFor(() => {
      expect(revokeApprovalGrants).toHaveBeenCalledWith({ ids: ["g1"] });
    });
    await waitFor(() => {
      expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(1);
    });
  });

  it("revokes a whole group through the bulk endpoint after confirm", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let rows = [
      grant({ id: "p1", project_dir: "/proj" }),
      grant({ id: "p2", project_dir: "/proj", pattern: "write" }),
      grant({ id: "d1", scope: "device" }),
    ];
    const listApprovalGrants = vi.fn().mockImplementation(async () => ({
      grants: [...rows],
    }));
    const revokeApprovalGrants = vi
      .fn()
      .mockImplementation(async ({ ids }: { ids: string[] }) => {
        rows = rows.filter((g) => !ids.includes(g.id));
        return bulkRevokeAll(ids);
      });
    const client = mockClient({ listApprovalGrants, revokeApprovalGrants });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-band-project")).toBeTruthy();
    });
    const projectBand = screen.getByTestId("saved-approvals-band-project");
    fireEvent.click(
      projectBand.querySelector(
        '[data-testid="saved-approvals-revoke-group"]',
      )!,
    );

    await waitFor(() => {
      expect(revokeApprovalGrants).toHaveBeenCalledWith({ ids: ["p1", "p2"] });
    });
    await waitFor(() => {
      expect(screen.queryByTestId("saved-approvals-band-project")).toBeNull();
      expect(screen.getByTestId("saved-approvals-band-device")).toBeTruthy();
    });
  });

  it("clears expired and revokes missing through the overflow menu", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let rows = [
      grant({ id: "ok" }),
      grant({
        id: "old",
        expired: true,
        expires_at: "2026-01-01T00:00:00Z",
      }),
      grant({
        id: "gone",
        category: "write_root",
        pattern: "/gone",
        unavailable: true,
      }),
    ];
    const listApprovalGrants = vi.fn().mockImplementation(async () => ({
      grants: [...rows],
    }));
    const revokeApprovalGrants = vi
      .fn()
      .mockImplementation(async ({ ids }: { ids: string[] }) => {
        rows = rows.filter((g) => !ids.includes(g.id));
        return bulkRevokeAll(ids);
      });
    const client = mockClient({ listApprovalGrants, revokeApprovalGrants });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(3);
    });
    openSavedApprovalsOverflow();
    fireEvent.click(screen.getByTestId("saved-approvals-clear-expired"));
    await waitFor(() => {
      expect(revokeApprovalGrants).toHaveBeenCalledWith({ ids: ["old"] });
    });

    await waitFor(() => {
      expect(screen.getAllByTestId("saved-approvals-row")).toHaveLength(2);
    });
    openSavedApprovalsOverflow();
    fireEvent.click(screen.getByTestId("saved-approvals-revoke-missing"));
    await waitFor(() => {
      expect(revokeApprovalGrants).toHaveBeenCalledWith({ ids: ["gone"] });
    });
  });

  it("does not bulk revoke when confirm is cancelled", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const revokeApprovalGrants = vi.fn();
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({
        grants: [grant({ id: "p1", project_dir: "/proj" })],
      }),
      revokeApprovalGrants,
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-band-project")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("saved-approvals-revoke-group"));

    await waitFor(() => {
      expect(window.confirm).toHaveBeenCalled();
    });
    expect(revokeApprovalGrants).not.toHaveBeenCalled();
  });

  it("surfaces partial bulk failures", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({
        grants: [
          grant({ id: "p1", project_dir: "/proj" }),
          grant({ id: "p2", project_dir: "/proj", pattern: "write" }),
        ],
      }),
      revokeApprovalGrants: vi.fn().mockResolvedValue({
        results: [
          { id: "p1", revoked: true },
          { id: "p2", revoked: false, error: "store busy" },
        ],
      }),
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-band-project")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("saved-approvals-revoke-group"));

    await waitFor(() => {
      expect(
        screen.getByTestId("saved-approvals-action-error").textContent,
      ).toBe(SAVED_APPROVALS_COPY.revokeSomeFailed(1, 2));
    });
  });

  it("shows empty copy when there are no grants", async () => {
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }),
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-empty").textContent).toBe(
        SAVED_APPROVALS_COPY.empty,
      );
    });
  });

  it("renders socket rows with the missing badge and quiet detail line", async () => {
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({
        grants: [
          grant({
            id: "grant_sock",
            title: "Allow local service for this project",
            category: "socket_path",
            scope: "project",
            project_dir: "/proj",
            pattern: "/tmp/svc.sock",
            approved_path: "/tmp/svc.sock",
            resolved_path: "/private/tmp/svc.sock",
            unavailable: true,
            revoke_applies_to: SAVED_APPROVALS_COPY.socketRevokeConfirm,
            source: "settings",
          }),
        ],
      }),
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-unavailable").textContent).toBe(
        SAVED_APPROVALS_COPY.unavailableSocketBadge,
      );
    });
    expect(screen.getByText(SAVED_APPROVALS_COPY.socketMissingHint)).toBeTruthy();
    // Full-authority warnings appear in create and confirm surfaces.
    expect(screen.queryByTestId("saved-approvals-authority-warning")).toBeNull();
  });

  it("asks before revoking a live socket row", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const revokeApprovalGrants = vi
      .fn()
      .mockImplementation(async ({ ids }: { ids: string[] }) =>
        bulkRevokeAll(ids),
      );
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({
        grants: [
          grant({
            id: "grant_sock",
            category: "socket_path",
            pattern: "/tmp/svc.sock",
            approved_path: "/tmp/svc.sock",
            resolved_path: "/tmp/svc.sock",
            revoke_applies_to: "Applies to the next command.",
          }),
        ],
      }),
      revokeApprovalGrants,
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-revoke")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("saved-approvals-revoke"));

    await waitFor(() => {
      expect(confirmSpy).toHaveBeenCalledWith("Applies to the next command.");
      expect(revokeApprovalGrants).toHaveBeenCalledWith({
        ids: ["grant_sock"],
      });
    });
  });

  it("creates a durable socket grant behind the add affordance with a project picker", async () => {
    const resolveSocketGrant = vi.fn().mockResolvedValue({
      approved_path: "/tmp/svc.sock",
      resolved_path: "/private/tmp/svc.sock",
      effective_authority: "outside_sandbox_daemon",
      authority_warning: "outside sandbox warning",
    });
    const createApprovalGrant = vi.fn().mockResolvedValue(
      grant({
        id: "grant_new",
        category: "socket_path",
        pattern: "/tmp/svc.sock",
      }),
    );
    const listProjects = vi.fn().mockResolvedValue([
      {
        id: "proj-1",
        name: "Harness",
        roots: [{ path: "/Users/me/proj" }],
      },
    ]);
    const client = mockClient({
      listApprovalGrants: vi.fn().mockResolvedValue({ grants: [] }),
      resolveSocketGrant,
      createApprovalGrant,
      listProjects,
    });

    render(() => <SavedApprovalsPanel client={client} />);

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-empty")).toBeTruthy();
    });
    expect(screen.queryByTestId("saved-approvals-socket-create")).toBeNull();
    fireEvent.click(screen.getByTestId("saved-approvals-add-socket"));
    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-socket-create")).toBeTruthy();
    });

    fireEvent.input(screen.getByTestId("saved-approvals-socket-path"), {
      target: { value: "/tmp/svc.sock" },
    });
    fireEvent.click(screen.getByTestId("saved-approvals-socket-resolve"));

    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-socket-confirm")).toBeTruthy();
    });
    expect(resolveSocketGrant).toHaveBeenCalledWith({
      socket_path: "/tmp/svc.sock",
    });
    expect(
      screen.getByTestId("saved-approvals-socket-authority").textContent,
    ).toBe("outside sandbox warning");
    await waitFor(() => {
      expect(screen.getByTestId("saved-approvals-socket-project")).toBeTruthy();
    });

    fireEvent.click(screen.getByTestId("saved-approvals-socket-confirm-create"));

    await waitFor(() => {
      expect(createApprovalGrant).toHaveBeenCalledWith({
        category: "socket_path",
        scope: "project",
        socket_path: "/tmp/svc.sock",
        project_id: "proj-1",
      });
    });
    await waitFor(() => {
      expect(screen.queryByTestId("saved-approvals-socket-create")).toBeNull();
    });
  });
});
