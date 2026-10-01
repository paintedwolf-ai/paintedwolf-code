import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { McpSettingsPanel } from "./McpSettingsPanel.tsx";
import type { McpCheckRow, McpProvider } from "../../../api/types.ts";
import { LycaonApiError } from "../../../api/http.ts";
import { MCP_SETTINGS_COPY } from "../../../settings/mcp/mcp-settings-copy.ts";

const confirmAndOpenExternalLink = vi.hoisted(() =>
  vi.fn().mockResolvedValue(true),
);
vi.mock("../../../platform/desktop/external-link.ts", () => ({
  confirmAndOpenExternalLink,
}));

const servers: McpProvider[] = [
  { id: "opengrep", enabled: false, class: "local", tool_loading: "auto" },
  {
    id: "fetch",
    enabled: true,
    class: "web",
    tool_loading: "auto",
    last_error: "mcp_provider_unreachable",
  },
];

const connectionRows: McpCheckRow[] = [
  { provider_id: "opengrep", status: "healthy" },
  { provider_id: "fetch", status: "error", code: "mcp_provider_unreachable",
    notice: { title: "Provider unreachable", message: "The provider did not answer." } },
];

function mockClient(overrides: Record<string, unknown> = {}) {
  return {
    listMcpProviders: vi.fn().mockResolvedValue(servers),
    listMcpRecipes: vi.fn().mockResolvedValue({ recipes: [] }),
    createMcpProvider: vi.fn(),
    updateMcpProvider: vi.fn(),
    deleteMcpProvider: vi.fn(),
    listMcpProviderTools: vi.fn().mockResolvedValue([]),
    refreshMcpProvider: vi.fn(),
    startMcpOAuth: vi.fn(),
    cancelMcpOAuth: vi.fn().mockResolvedValue(undefined),
    completeMcpOAuth: vi.fn(),
    revokeMcpOAuth: vi.fn(),
    checkMcpProviders: vi.fn(),
    ...overrides,
  };
}

async function openCustomAdd(kind: "local" | "web" = "local") {
  fireEvent.click(screen.getByTestId("mcp-add-provider"));
  await waitFor(() => {
    expect(
      screen.getByTestId(
        kind === "web" ? "mcp-recipe-custom-web" : "mcp-recipe-custom-local",
      ),
    ).toBeTruthy();
  });
  fireEvent.click(
    screen.getByTestId(
      kind === "web" ? "mcp-recipe-custom-web" : "mcp-recipe-custom-local",
    ),
  );
  await waitFor(() => {
    expect(screen.getByTestId("mcp-editor-id")).toBeTruthy();
  });
}

describe("McpSettingsPanel", () => {
  it("always shows class threat ledes and access explainer", async () => {
    const client = mockClient({ listMcpProviders: vi.fn().mockResolvedValue([]) });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-threat-local").textContent).toContain(
        MCP_SETTINGS_COPY.threatLocal.slice(0, 40),
      );
    });
    expect(getByTestId("mcp-threat-web").textContent).toContain(
      MCP_SETTINGS_COPY.threatWeb.slice(0, 40),
    );
    expect(getByTestId("mcp-threat-banner").textContent).toContain(
      MCP_SETTINGS_COPY.threatDocs,
    );
    expect(getByTestId("mcp-access-explainer").textContent).toBe(
      MCP_SETTINGS_COPY.accessExplainer,
    );
    expect(getByTestId("mcp-path-hints").textContent).toContain(
      MCP_SETTINGS_COPY.pathHintUser,
    );
  });

  it("shows empty list copy when no servers", async () => {
    const client = mockClient({ listMcpProviders: vi.fn().mockResolvedValue([]) });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-empty-providers")).toBeTruthy();
    });
    expect(getByTestId("mcp-empty-providers").textContent).toBe(
      MCP_SETTINGS_COPY.emptyProviders,
    );
  });

  it("test connection renders ok and error rows", async () => {
    const client = mockClient({
      checkMcpProviders: vi.fn().mockResolvedValue(connectionRows),
    });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-opengrep")).toBeTruthy();
    });

    fireEvent.click(getByTestId("mcp-test-connection"));

    await waitFor(() => {
      expect(client.checkMcpProviders).toHaveBeenCalledTimes(1);
      expect(getByTestId("mcp-connection-results")).toBeTruthy();
    });

    const okRow = getByTestId("mcp-connection-row-opengrep");
    expect(okRow.textContent).toContain(MCP_SETTINGS_COPY.statusOk);
    const errRow = getByTestId("mcp-connection-row-fetch");
    expect(errRow.textContent).toContain(MCP_SETTINGS_COPY.statusError);
    expect(errRow.textContent).toContain("did not answer");
  });

  it("enable toggle calls updateMcpProvider", async () => {
    const client = mockClient({
      updateMcpProvider: vi
        .fn()
        .mockResolvedValue({
          id: "opengrep",
          enabled: true,
        }),
    });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-opengrep")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-provider-opengrep"));

    await waitFor(() => {
      expect(getByTestId("mcp-enable-opengrep")).toBeTruthy();
    });

    fireEvent.click(getByTestId("mcp-enable-opengrep"));

    await waitFor(() => {
      expect(client.updateMcpProvider).toHaveBeenCalledWith(
        "opengrep",
        { enabled: true },
        undefined,
      );
    });
  });

  it("always-load toggle updates the provider loading mode", async () => {
    const client = mockClient({
      updateMcpProvider: vi.fn().mockResolvedValue({
        id: "opengrep",
        enabled: false,
        class: "local",
        tool_loading: "always",
      }),
    });
    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));
    await waitFor(() => expect(getByTestId("mcp-provider-opengrep")).toBeTruthy());
    fireEvent.click(getByTestId("mcp-provider-opengrep"));
    await waitFor(() => expect(getByTestId("mcp-always-load-opengrep")).toBeTruthy());
    fireEvent.click(getByTestId("mcp-always-load-opengrep"));
    await waitFor(() => {
      expect(client.updateMcpProvider).toHaveBeenCalledWith(
        "opengrep",
        { tool_loading: "always" },
        undefined,
      );
    });
  });

  it("navigates into server detail and back in the same card", async () => {
    const client = mockClient();
    const { getByTestId, queryByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-list")).toBeTruthy();
    });
    expect(queryByTestId("mcp-provider-detail-opengrep")).toBeNull();

    fireEvent.click(getByTestId("mcp-provider-opengrep"));
    await waitFor(() => {
      expect(getByTestId("mcp-provider-detail-opengrep")).toBeTruthy();
      expect(getByTestId("mcp-back")).toBeTruthy();
      expect(queryByTestId("mcp-provider-list")).toBeNull();
    });

    fireEvent.click(getByTestId("mcp-back"));
    await waitFor(() => {
      expect(queryByTestId("mcp-provider-detail-opengrep")).toBeNull();
      expect(getByTestId("mcp-provider-list")).toBeTruthy();
    });
  });

  it("renders rejected rows separately", async () => {
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([
        ...servers,
        {
          id: "evil",
          enabled: false,
          class: "local",
          status: "rejected",
          last_error: "project_stdio_forbidden",
          connection_source: "project",
          notice: {
            title: "This project cannot run a local MCP program",
            message:
              "A project overlay may not start a stdio MCP provider. Local programs are a device setting.",
            suggested_action:
              "Add the provider under Settings → MCP providers.",
          },
        },
      ]),
    });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-group-local")).toBeTruthy();
      expect(getByTestId("mcp-rejected-row")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-rejected-row"));
    await waitFor(() => {
      expect(getByTestId("mcp-rejected-notice").textContent).toContain(
        "cannot run a local MCP program",
      );
    });
  });

  it("add provider dialog posts create disabled by default", async () => {
    const client = mockClient({
      createMcpProvider: vi.fn().mockResolvedValue({
        id: "local-http",
        enabled: false,
        transport: "http",
      }),
    });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-add-provider")).toBeTruthy();
    });
    await openCustomAdd();

    fireEvent.input(screen.getByTestId("mcp-editor-id"), {
      target: { value: "local-http" },
    });
    fireEvent.input(screen.getByTestId("mcp-editor-url"), {
      target: { value: "127.0.0.1:8765/mcp" },
    });
    expect(screen.getByTestId("mcp-editor-url").getAttribute("placeholder")).toBe(
      MCP_SETTINGS_COPY.urlPlaceholder,
    );
    expect(screen.getByText(/Local provider on this computer/).textContent).toContain(
      "http://127.0.0.1:8765/mcp",
    );
    expect(screen.queryByTestId("mcp-editor-allow-remote")).toBeNull();
    fireEvent.click(screen.getByTestId("mcp-editor-save"));

    await waitFor(() => {
      expect(client.createMcpProvider).toHaveBeenCalledWith(
        {
          source: "custom",
          id: "local-http",
          enabled: false,
          url: "http://127.0.0.1:8765/mcp",
        },
        undefined,
      );
    });
  });

  it("refuses remote HTTP on the URL field without posting", async () => {
    const client = mockClient({
      createMcpProvider: vi.fn().mockResolvedValue({ id: "x", enabled: false }),
    });

    render(() => <McpSettingsPanel client={client as never} />);
    await waitFor(() => {
      expect(screen.getByTestId("mcp-add-provider")).toBeTruthy();
    });
    await openCustomAdd("web");
    fireEvent.input(screen.getByTestId("mcp-editor-id"), {
      target: { value: "coropa" },
    });
    fireEvent.input(screen.getByTestId("mcp-editor-url"), {
      target: { value: "http://172.17.0.2:8080/mcp" },
    });
    expect(screen.getByText(MCP_SETTINGS_COPY.urlErrorRemoteHTTP)).toBeTruthy();
    fireEvent.click(screen.getByTestId("mcp-editor-save"));
    expect(client.createMcpProvider).not.toHaveBeenCalled();
  });

  it("add provider dialog renders host notice copy on refusal", async () => {
    const client = mockClient({
      createMcpProvider: vi.fn().mockRejectedValue(
        new LycaonApiError(
          "A provider with this id is already in the catalog.",
          409,
          "duplicate_id",
          {
            title: "That provider id is already in use",
            suggestedAction: "Choose a different id, or edit the existing provider.",
          },
        ),
      ),
    });

    render(() => <McpSettingsPanel client={client as never} />);
    await waitFor(() => {
      expect(screen.getByTestId("mcp-add-provider")).toBeTruthy();
    });
    await openCustomAdd();
    fireEvent.input(screen.getByTestId("mcp-editor-id"), {
      target: { value: "coropa" },
    });
    fireEvent.input(screen.getByTestId("mcp-editor-url"), {
      target: { value: "http://127.0.0.1:8765/mcp" },
    });
    fireEvent.click(screen.getByTestId("mcp-editor-save"));

    await waitFor(() => {
      const el = screen.getByTestId("mcp-editor-error");
      expect(el.getAttribute("data-code")).toBe("duplicate_id");
      expect(el.textContent).toContain("already in use");
    });
  });

  it("keeps focus in Name while typing in the add dialog", async () => {
    const client = mockClient();

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-add-provider")).toBeTruthy();
    });
    await openCustomAdd();

    const idInput = screen.getByTestId("mcp-editor-id") as HTMLInputElement;
    idInput.focus();
    expect(document.activeElement).toBe(idInput);

    fireEvent.input(idInput, { target: { value: "a" } });
    expect(document.activeElement).toBe(idInput);
    expect(idInput.value).toBe("a");

    fireEvent.input(idInput, { target: { value: "ab" } });
    expect(document.activeElement).toBe(idInput);
    expect(idInput.value).toBe("ab");
  });

  it("opens custom add as a stacked form, not the picker sheet", async () => {
    render(() => <McpSettingsPanel client={mockClient() as never} />);
    await waitFor(() => {
      expect(screen.getByTestId("mcp-add-provider")).toBeTruthy();
    });
    await openCustomAdd("web");

    const dialog = screen.getByRole("dialog");
    expect(dialog.className.split(/\s+/)).not.toContain("den-dialog--sheet");
    expect(screen.getByTestId("mcp-editor-form")).toBeTruthy();
    expect(screen.getByTestId("mcp-editor-url")).toBeTruthy();
    expect(screen.getByTestId("mcp-editor-token")).toBeTruthy();
    expect(screen.getByTestId("mcp-editor-credential-wire")).toBeTruthy();
  });

  it("locks project Custom local to loopback HTTP", async () => {
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([
        {
          id: "loopback",
          enabled: false,
          class: "local",
          transport: "http",
          url: "http://127.0.0.1:8765/mcp",
          source: "override",
        },
      ]),
    });
    render(() => (
      <McpSettingsPanel
        client={client as never}
        alwaysProjectScope
        projectId="proj-1"
      />
    ));
    await waitFor(() => {
      expect(screen.getByTestId("mcp-add-provider")).toBeTruthy();
    });
    await openCustomAdd("local");

    const transport = screen.getByTestId(
      "mcp-editor-transport",
    ) as HTMLInputElement;
    expect(transport.value).toBe(MCP_SETTINGS_COPY.transportHttp);
    expect(transport.disabled).toBe(true);
    expect(screen.queryByTestId("mcp-editor-command")).toBeNull();
    expect(screen.queryByTestId("mcp-editor-token")).toBeNull();
  });

  it("tools browser loads discovered tools", async () => {
    const client = mockClient({
      listMcpProviderTools: vi.fn().mockResolvedValue([
        { name: "mcp_fetch_get", description: "GET" },
      ]),
    });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-fetch")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-provider-fetch"));
    fireEvent.click(getByTestId("mcp-tools-fetch"));

    await waitFor(() => {
      expect(client.listMcpProviderTools).toHaveBeenCalledWith("fetch", undefined);
      expect(getByTestId("mcp-tool-fetch-mcp_fetch_get")).toBeTruthy();
    });
  });

  it("a failed tool listing never renders the no-tools-discovered answer", async () => {
    // "No tools discovered" is a listing result; a failed listing is not one.
    const client = mockClient({
      listMcpProviderTools: vi.fn().mockRejectedValue(new Error("provider offline")),
    });

    const { getByTestId, queryByText } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-fetch")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-provider-fetch"));
    fireEvent.click(getByTestId("mcp-tools-fetch"));

    await waitFor(() => {
      expect(getByTestId("mcp-tools-unavailable-fetch")).toBeTruthy();
    });
    expect(queryByText(MCP_SETTINGS_COPY.toolsEmpty)).toBeNull();
  });

  it("replaces discovered tools when a changed connection fails and recovers", async () => {
    const provider: McpProvider = {
      id: "fetch", enabled: true, class: "local", tool_loading: "auto",
      transport: "http", url: "http://127.0.0.1:52353/mcp", status: "ready",
    };
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([provider]),
      updateMcpProvider: vi.fn().mockImplementation(async (_id, body) => ({
        ...provider, ...body, status: body.url === provider.url ? "ready" : "error",
      })),
      listMcpProviderTools: vi.fn()
        .mockResolvedValueOnce([{ name: "old_tool" }])
        .mockRejectedValueOnce(new Error("connection refused"))
        .mockResolvedValueOnce([{ name: "recovered_tool" }]),
    });
    render(() => <McpSettingsPanel client={client as never} />);
    fireEvent.click(await screen.findByTestId("mcp-provider-fetch"));
    fireEvent.click(screen.getByTestId("mcp-tools-fetch"));
    await screen.findByTestId("mcp-tool-fetch-old_tool");
    fireEvent.input(screen.getByTestId("mcp-editor-url"), {
      target: { value: "http://127.0.0.1:55616/mcp" },
    });
    fireEvent.click(screen.getByTestId("mcp-save-connection-fetch"));
    await screen.findByTestId("mcp-tools-unavailable-fetch");
    expect(screen.queryByTestId("mcp-tool-fetch-old_tool")).toBeNull();
    expect(screen.queryByText(MCP_SETTINGS_COPY.toolsEmpty)).toBeNull();
    fireEvent.input(screen.getByTestId("mcp-editor-url"), {
      target: { value: provider.url },
    });
    fireEvent.click(screen.getByTestId("mcp-save-connection-fetch"));
    await screen.findByTestId("mcp-tool-fetch-recovered_tool");
    expect(screen.queryByTestId("mcp-tools-unavailable-fetch")).toBeNull();
    expect(client.listMcpProviderTools).toHaveBeenCalledTimes(3);
  });

  it.each(["loaded", "late success", "late failure"])("disabling discovery handles %s without reporting a connection failure", async (outcome) => {
    let resolveOld!: (value: { name: string }[]) => void;
    let rejectOld!: (error: Error) => void;
    const oldListing = new Promise<{ name: string }[]>((resolve, reject) => {
      resolveOld = resolve;
      rejectOld = reject;
    });
    const provider: McpProvider = {
      id: "fetch", enabled: true, class: "local", tool_loading: "auto", status: "ready",
    };
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([provider]),
      updateMcpProvider: vi.fn().mockImplementation(async (_id, body) => ({
        ...provider, ...body, status: body.enabled ? "ready" : "disabled",
      })),
      listMcpProviderTools: vi.fn().mockReturnValueOnce(oldListing)
        .mockResolvedValueOnce([{ name: "current_tool" }]),
    });
    render(() => <McpSettingsPanel client={client as never} />);
    fireEvent.click(await screen.findByTestId("mcp-provider-fetch"));
    fireEvent.click(screen.getByTestId("mcp-tools-fetch"));
    await waitFor(() => expect(client.listMcpProviderTools).toHaveBeenCalledTimes(1));
    if (outcome === "loaded") {
      resolveOld([{ name: "old_tool" }]);
      await screen.findByTestId("mcp-tool-fetch-old_tool");
    }
    fireEvent.click(screen.getByTestId("mcp-enable-fetch"));
    await screen.findByTestId("mcp-tools-disabled-fetch");
    if (outcome === "late success") resolveOld([{ name: "old_tool" }]);
    if (outcome === "late failure") rejectOld(new Error("old connection failed"));
    await oldListing.catch(() => undefined);
    expect(client.listMcpProviderTools).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("mcp-tool-fetch-old_tool")).toBeNull();
    expect(screen.queryByTestId("mcp-tools-unavailable-fetch")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.click(screen.getByTestId("mcp-enable-fetch"));
    await screen.findByTestId("mcp-tool-fetch-current_tool");
    expect(screen.queryByTestId("mcp-tools-disabled-fetch")).toBeNull();
    expect(client.listMcpProviderTools).toHaveBeenCalledTimes(2);
  });

  it.each(["success", "failure"])("ignores a superseded connection's late %s", async (outcome) => {
    let resolveOld!: (value: { name: string }[]) => void;
    let rejectOld!: (error: Error) => void;
    const oldListing = new Promise<{ name: string }[]>((resolve, reject) => {
      resolveOld = resolve;
      rejectOld = reject;
    });
    const provider: McpProvider = {
      id: "fetch", enabled: true, class: "local", tool_loading: "auto",
      transport: "http", url: "http://127.0.0.1:52353/mcp", status: "ready",
    };
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([provider]),
      updateMcpProvider: vi.fn().mockImplementation(async (_id, body) => ({ ...provider, ...body })),
      listMcpProviderTools: vi.fn().mockReturnValueOnce(oldListing)
        .mockResolvedValueOnce([{ name: "current_tool" }]),
    });
    render(() => <McpSettingsPanel client={client as never} />);
    fireEvent.click(await screen.findByTestId("mcp-provider-fetch"));
    fireEvent.click(screen.getByTestId("mcp-tools-fetch"));
    await waitFor(() => expect(client.listMcpProviderTools).toHaveBeenCalledTimes(1));
    fireEvent.input(screen.getByTestId("mcp-editor-url"), {
      target: { value: "http://127.0.0.1:52354/mcp" },
    });
    fireEvent.click(screen.getByTestId("mcp-save-connection-fetch"));
    await screen.findByTestId("mcp-tool-fetch-current_tool");
    if (outcome === "success") resolveOld([{ name: "stale_tool" }]);
    else rejectOld(new Error("old connection failed"));
    await oldListing.catch(() => undefined);
    expect(screen.getByTestId("mcp-tool-fetch-current_tool")).toBeTruthy();
    expect(screen.queryByTestId("mcp-tool-fetch-stale_tool")).toBeNull();
    expect(screen.queryByTestId("mcp-tools-unavailable-fetch")).toBeNull();
  });

  it("a listing that ran and found nothing still says so", async () => {
    const client = mockClient({
      listMcpProviderTools: vi.fn().mockResolvedValue([]),
    });

    const { getByTestId, getByText } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-fetch")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-provider-fetch"));
    fireEvent.click(getByTestId("mcp-tools-fetch"));

    await waitFor(() => {
      expect(getByText(MCP_SETTINGS_COPY.toolsEmpty)).toBeTruthy();
    });
  });

  it("project scope uses a main override and hides web providers while following Settings", async () => {
    const projectServers: McpProvider[] = [
      {
        id: "opengrep",
        enabled: false,
        class: "local",
        tool_loading: "auto",
        source: "default",
      },
      {
        id: "fetch",
        enabled: true,
        class: "web",
        tool_loading: "auto",
        source: "default",
      },
    ];
    let listed = projectServers;
    const client = mockClient({
      listMcpProviders: vi.fn().mockImplementation(async () => listed),
      updateMcpProvider: vi.fn().mockImplementation(async (id: string, body) => {
        const next = listed.map((s) =>
          s.id === id
            ? {
                ...s,
                enabled: body.inherit ? false : Boolean(body.enabled),
                source: body.inherit
                  ? ("default" as const)
                  : ("override" as const),
              }
            : s,
        );
        listed = next;
        return next.find((s) => s.id === id)!;
      }),
    });

    const { getByTestId, queryByTestId } = render(() => (
      <McpSettingsPanel
        client={client as never}
        alwaysProjectScope
        projectId="proj-1"
      />
    ));

    await waitFor(() => {
      expect(getByTestId("project-settings-override")).toBeTruthy();
    });

    expect(getByTestId("settings-scope-badge").textContent).toBe("Project");
    expect(getByTestId("settings-scope-lede").getAttribute("data-scope")).toBe(
      "project",
    );
    expect(getByTestId("mcp-threat-banner")).toBeTruthy();
    expect(getByTestId("mcp-threat-local")).toBeTruthy();
    expect(queryByTestId("mcp-threat-web")).toBeNull();
    expect(queryByTestId("mcp-test-connection")).toBeNull();
    expect(queryByTestId("mcp-provider-list")).toBeNull();
    expect(getByTestId("project-override-summary").textContent).toMatch(
      /0 of 1 enabled/,
    );

    fireEvent.click(getByTestId("project-override-toggle"));

    await waitFor(() => {
      expect(client.updateMcpProvider).toHaveBeenCalledWith(
        "opengrep",
        { enabled: false },
        "proj-1",
      );
      expect(client.updateMcpProvider).not.toHaveBeenCalledWith(
        "fetch",
        expect.anything(),
        "proj-1",
      );
      expect(
        getByTestId("project-settings-override").getAttribute("data-enabled"),
      ).toBe("true");
      expect(getByTestId("mcp-provider-list")).toBeTruthy();
    });
  });

  it("opens an empty project override so a local provider can be added", async () => {
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([]),
      updateMcpProvider: vi.fn(),
    });

    const { getByTestId, queryByTestId } = render(() => (
      <McpSettingsPanel
        client={client as never}
        alwaysProjectScope
        projectId="proj-1"
      />
    ));

    await waitFor(() => {
      expect(getByTestId("project-override-toggle")).toBeTruthy();
    });
    expect(queryByTestId("mcp-add-provider")).toBeNull();

    fireEvent.click(getByTestId("project-override-toggle"));

    await waitFor(() => {
      expect(getByTestId("mcp-add-provider")).toBeTruthy();
      expect(
        getByTestId("project-settings-override").getAttribute("data-enabled"),
      ).toBe("true");
    });
    expect(client.updateMcpProvider).not.toHaveBeenCalled();
  });

  it("keeps device-managed stdio providers read-only in project Settings", async () => {
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([
        {
          id: "playwright",
          enabled: false,
          class: "local",
          transport: "stdio",
          command: "npx",
          source: "override",
        },
      ]),
    });

    const { getByTestId, queryByTestId } = render(() => (
      <McpSettingsPanel
        client={client as never}
        alwaysProjectScope
        projectId="proj-1"
      />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-playwright")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-provider-playwright"));

    expect(
      (getByTestId("mcp-enable-playwright") as HTMLInputElement).disabled,
    ).toBe(true);
    expect(getByTestId("mcp-project-device-managed-playwright")).toBeTruthy();
    expect(queryByTestId("mcp-editor-form")).toBeNull();
  });

  it.each([false, true])("keeps project loopback authentication device-managed (signed in: %s)", async (signedIn) => {
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([{
        id: "loopback", enabled: false, class: "local", transport: "http",
        tool_loading: "auto", url: "http://127.0.0.1:8765/mcp",
        source: "override", signed_in: signedIn,
      }]),
    });
    const view = render(() => (
      <McpSettingsPanel client={client as never} alwaysProjectScope projectId="proj-1" />
    ));
    await waitFor(() => expect(view.getByTestId("mcp-provider-loopback")).toBeTruthy());
    fireEvent.click(view.getByTestId("mcp-provider-loopback"));
    expect(view.getByTestId("mcp-tools-loopback")).toBeTruthy();
    expect(view.queryByTestId("mcp-oauth-signin-loopback")).toBeNull();
    expect(view.queryByTestId("mcp-oauth-signout-loopback")).toBeNull();
    expect(client.startMcpOAuth).not.toHaveBeenCalled();
    expect(client.revokeMcpOAuth).not.toHaveBeenCalled();
  });

  it("shows OAuth sign-in in detail when an HTTP provider needs_auth", async () => {
    const list = vi
      .fn()
      .mockResolvedValueOnce([
        {
          id: "remote",
          enabled: true,
          class: "web",
          transport: "http",
          url: "https://example.test/mcp",
          status: "needs_auth",
          signed_in: false,
        },
      ])
      .mockResolvedValue([
        {
          id: "remote",
          enabled: true,
          class: "web",
          transport: "http",
          url: "https://example.test/mcp",
          status: "ready",
          signed_in: true,
        },
      ]);
    const startMcpOAuth = vi.fn().mockResolvedValue({
      authorize_url: "https://auth.example/authorize",
      state: "st-1",
    });
    const completeMcpOAuth = vi.fn().mockResolvedValue({
      id: "remote",
      enabled: true,
      class: "web",
      transport: "http",
      status: "ready",
      signed_in: true,
    });
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
    const client = mockClient({
      listMcpProviders: list,
      startMcpOAuth,
      completeMcpOAuth,
    });

    const { getByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(getByTestId("mcp-provider-remote")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-provider-remote"));
    await waitFor(() => {
      expect(getByTestId("mcp-oauth-signin-remote")).toBeTruthy();
    });
    expect(getByTestId("mcp-auth-flags-remote").textContent).toContain(
      MCP_SETTINGS_COPY.needsAuth,
    );

    fireEvent.click(getByTestId("mcp-oauth-signin-remote"));

    await waitFor(() => {
      expect(startMcpOAuth).toHaveBeenCalledWith("remote");
      expect(screen.getByTestId("mcp-oauth-complete-dialog")).toBeTruthy();
    });
    expect(openSpy).toHaveBeenCalled();

    // Loopback exchange: wait, no code field.
    expect(screen.getByTestId("mcp-oauth-waiting")).toBeTruthy();
    expect(screen.queryByTestId("mcp-oauth-code")).toBeNull();

    // Manual code entry stays available.
    fireEvent.click(screen.getByTestId("mcp-oauth-manual"));
    await waitFor(() => {
      expect(screen.getByTestId("mcp-oauth-code")).toBeTruthy();
    });
    fireEvent.input(screen.getByTestId("mcp-oauth-code"), {
      target: { value: "auth-code" },
    });
    fireEvent.click(screen.getByTestId("mcp-oauth-complete-remote"));

    await waitFor(() => {
      expect(completeMcpOAuth).toHaveBeenCalledWith("remote", {
        code: "auth-code",
        state: "st-1",
      });
      expect(getByTestId("mcp-oauth-signout-remote")).toBeTruthy();
    });
    openSpy.mockRestore();
  });

  it.each(["button", "escape", "backdrop"])("retires the exact sign-in on %s dismissal", async (dismiss) => {
    const cancelMcpOAuth = vi.fn().mockResolvedValue(undefined);
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([
        { id: "remote", enabled: false, class: "web", transport: "http", status: "needs_auth", signed_in: false },
      ]),
      startMcpOAuth: vi.fn().mockResolvedValue({ authorize_url: "", state: "attempt-1" }),
      cancelMcpOAuth,
    });
    render(() => <McpSettingsPanel client={client as never} />);
    fireEvent.click(await screen.findByTestId("mcp-provider-remote"));
    fireEvent.click(await screen.findByTestId("mcp-oauth-signin-remote"));
    const backdrop = await screen.findByTestId("mcp-oauth-complete-dialog");
    if (dismiss === "button") fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    if (dismiss === "escape") fireEvent.keyDown(screen.getByRole("dialog", { name: "Complete sign-in" }), { key: "Escape" });
    if (dismiss === "backdrop") fireEvent.click(backdrop);
    await waitFor(() => expect(cancelMcpOAuth).toHaveBeenCalledWith("remote", { state: "attempt-1" }));
    await waitFor(() => expect(screen.queryByTestId("mcp-oauth-complete-dialog")).toBeNull());
    expect(client.revokeMcpOAuth).not.toHaveBeenCalled();
  });

  it("keeps failed cancellation visible and retries the same attempt", async () => {
    const cancelMcpOAuth = vi.fn().mockRejectedValueOnce(new Error("connection unavailable")).mockResolvedValue(undefined);
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([
        { id: "remote", enabled: false, class: "web", transport: "http", status: "needs_auth", signed_in: false },
      ]),
      startMcpOAuth: vi.fn().mockResolvedValue({ authorize_url: "", state: "attempt-retry" }),
      cancelMcpOAuth,
    });
    render(() => <McpSettingsPanel client={client as never} />);
    fireEvent.click(await screen.findByTestId("mcp-provider-remote"));
    fireEvent.click(await screen.findByTestId("mcp-oauth-signin-remote"));
    await screen.findByTestId("mcp-oauth-complete-dialog");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.getByTestId("mcp-oauth-error").textContent).toContain("Could not complete request"));
    expect(screen.getByTestId("mcp-oauth-complete-dialog")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByTestId("mcp-oauth-complete-dialog")).toBeNull());
    expect(cancelMcpOAuth.mock.calls).toEqual([
      ["remote", { state: "attempt-retry" }], ["remote", { state: "attempt-retry" }],
    ]);
  });

  it("adds a known recipe from the picker", async () => {
    const createMcpProvider = vi.fn().mockResolvedValue({
      id: "github-local",
      enabled: false,
      class: "local",
      transport: "stdio",
      recipe: "github-local",
      auth: "none",
    });
    const client = mockClient({
      listMcpRecipes: vi.fn().mockResolvedValue({
        recipes: [
          {
            id: "github-local",
            label: "GitHub (local)",
            hint: "Run the official container locally",
            docs_url: "https://github.com/github/github-mcp-server",
            auth: "none",
            class: "local",
            added: false,
            project_ok: false,
            env_keys: [
              { key: "GITHUB_HOST", label: "GitHub Enterprise host" },
            ],
          },
        ],
      }),
      createMcpProvider,
      listMcpProviders: vi
        .fn()
        .mockResolvedValueOnce(servers)
        .mockResolvedValue([
          {
            id: "github-local",
            enabled: false,
            class: "local",
            transport: "stdio",
            recipe: "github-local",
            auth: "none",
          },
        ]),
    });

    render(() => <McpSettingsPanel client={client as never} />);
    await waitFor(() => {
      expect(screen.getByTestId("mcp-add-provider")).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId("mcp-add-provider"));
    await waitFor(() => {
      expect(screen.getByTestId("mcp-recipe-github-local")).toBeTruthy();
    });
    expect(screen.getByTestId("mcp-recipe-env-github-local").textContent).toContain(
      "GitHub Enterprise host",
    );
    fireEvent.click(screen.getByTestId("mcp-recipe-docs-github-local"));
    expect(confirmAndOpenExternalLink).toHaveBeenCalledWith(
      "https://github.com/github/github-mcp-server",
    );
    fireEvent.click(screen.getByTestId("mcp-recipe-github-local"));
    await waitFor(() => {
      expect(createMcpProvider).toHaveBeenCalledWith({ source: "recipe", recipe_id: "github-local" }, undefined);
      expect(screen.getByTestId("mcp-provider-github-local")).toBeTruthy();
    });
  });

  it("hides Sign in for a static-token recipe", async () => {
    const client = mockClient({
      listMcpProviders: vi.fn().mockResolvedValue([
        {
          id: "github",
          enabled: true,
          class: "web",
          transport: "http",
          url: "https://api.githubcopilot.com/mcp",
          recipe: "github",
          auth: "static_token",
          credential_label: "Personal access token",
          status: "needs_auth",
          signed_in: false,
        },
      ]),
    });

    const { getByTestId, queryByTestId } = render(() => (
      <McpSettingsPanel client={client as never} />
    ));
    await waitFor(() => {
      expect(getByTestId("mcp-provider-github")).toBeTruthy();
    });
    fireEvent.click(getByTestId("mcp-provider-github"));
    await waitFor(() => {
      expect(getByTestId("mcp-save-connection-github")).toBeTruthy();
    });
    expect(queryByTestId("mcp-oauth-signin-github")).toBeNull();
    expect(getByTestId("mcp-editor-token")).toBeTruthy();
    expect(screen.getByText("Personal access token")).toBeTruthy();
    expect(queryByTestId("mcp-editor-credential-wire")).toBeNull();
  });

  it("lets Custom choose Token token= placement", async () => {
    const client = mockClient({
      createMcpProvider: vi.fn().mockResolvedValue({
        id: "pd-eu",
        enabled: false,
        transport: "http",
      }),
    });

    render(() => <McpSettingsPanel client={client as never} />);
    await waitFor(() => {
      expect(screen.getByTestId("mcp-add-provider")).toBeTruthy();
    });
    await openCustomAdd("web");

    fireEvent.input(screen.getByTestId("mcp-editor-id"), {
      target: { value: "pd-eu" },
    });
    fireEvent.input(screen.getByTestId("mcp-editor-url"), {
      target: { value: "https://mcp.eu.pagerduty.com/mcp" },
    });
    fireEvent.click(screen.getByTestId("mcp-editor-credential-wire"));
    const tokenWire = screen
      .getAllByRole("option")
      .find((option) => option.getAttribute("data-value") === "token_token");
    if (!tokenWire) throw new Error("Missing token wire option");
    fireEvent.click(tokenWire);
    fireEvent.input(screen.getByTestId("mcp-editor-token"), {
      target: { value: "u+secret" },
    });
    fireEvent.click(screen.getByTestId("mcp-editor-save"));

    await waitFor(() => {
      expect(client.createMcpProvider).toHaveBeenCalledWith(
        {
          source: "custom",
          id: "pd-eu",
          enabled: false,
          url: "https://mcp.eu.pagerduty.com/mcp",
          token: "u+secret",
          credential_wire: "token_token",
        },
        undefined,
      );
    });
  });
});
