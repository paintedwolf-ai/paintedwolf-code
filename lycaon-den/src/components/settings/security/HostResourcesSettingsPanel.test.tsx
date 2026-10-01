import { stubClient } from "../../../test/client-fixture.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import type { LycaonClient } from "../../../api/client.ts";
import type { HostResourcesResponse } from "../../../api/types.ts";
import { HostResourcesSettingsPanel } from "./HostResourcesSettingsPanel.tsx";

const confirmAndOpenExternalLink = vi.hoisted(() => vi.fn().mockResolvedValue(true));
vi.mock("../../../platform/desktop/external-link.ts", () => ({ confirmAndOpenExternalLink }));

const initial: HostResourcesResponse = {
  version: 1,
  checked_at: "2026-08-04T12:00:00Z",
  diagnostics: [],
  fingerprint: "initial",
  user_catalog_path: "~/.config/paintedwolf/host-resources.yaml",
  resources: [
    {
      id: "docker",
      family: "containers.local",
      label: "Docker",
      category: "Containers",
      description: "Docker CLI with a reachable local daemon socket.",
      docs_url: "https://docs.docker.com/reference/cli/docker/",
      origin: "built-in",
      status: "available",
      host_support: "supported",
      access: "allow",
      access_setting: "inherit",
      prompt: "omit",
      surfaces: ["process_exec"],
      checked_at: "2026-08-04T12:00:00Z",
      connections: [
        { mode: "local_service", transport: "unix_socket", target: "/var/run/docker.sock" },
      ],
    },
    {
      id: "aws-cli",
      family: "cloud",
      label: "AWS CLI",
      category: "Cloud",
      description: "AWS command-line client.",
      origin: "built-in",
      status: "unavailable",
      host_support: "supported",
      access: "deny",
      access_setting: "ask",
      prompt: "avoid",
      reason: "executable_not_found",
      surfaces: ["process_exec"],
      checked_at: "2026-08-04T12:00:00Z",
      connections: [],
    },
  ],
};

function client(refresh = initial): LycaonClient {
  return stubClient({
    getHostResources: vi.fn().mockResolvedValue(initial),
    refreshHostResources: vi.fn().mockResolvedValue(refresh),
    updateHostResource: vi.fn().mockImplementation(async (_id, access) => ({
      ...initial,
      resources: initial.resources.map((item) =>
        item.id === "docker"
          ? {
              ...item,
              access: access === "inherit" ? "allow" : access,
              access_setting: access,
            }
          : item,
      ),
    })),
  });
}

describe("HostResourcesSettingsPanel", () => {
  it("groups live host resources and exposes their connection details", async () => {
    render(() => <HostResourcesSettingsPanel client={client()} />);

    expect(await screen.findByText("1 of 2 available")).toBeTruthy();
    expect(screen.getByText("Containers")).toBeTruthy();
    expect(screen.getByText("Cloud")).toBeTruthy();
    expect(screen.getByText("~/.config/paintedwolf/host-resources.yaml")).toBeTruthy();

    fireEvent.click(screen.getByTestId("resource-row-docker"));
    expect(await screen.findByTestId("host-resources-detail")).toBeTruthy();
    expect(screen.getAllByText("Unix socket")).toHaveLength(3);
    expect(screen.getByText("/var/run/docker.sock")).toBeTruthy();
    expect(screen.getByText("docker")).toBeTruthy();
    expect(screen.getByText("containers.local")).toBeTruthy();
    expect(screen.getByText("Write agents when present")).toBeTruthy();
    fireEvent.click(screen.getByText("Open documentation"));
    expect(confirmAndOpenExternalLink).toHaveBeenCalledWith(
      "https://docs.docker.com/reference/cli/docker/",
    );
  });

  it("changes access through the lossless resource policy endpoint", async () => {
    const mock = client();
    render(() => <HostResourcesSettingsPanel client={mock} />);
    await screen.findByText("1 of 2 available");
    fireEvent.click(screen.getByTestId("resource-row-docker"));
    const select = await screen.findByTestId("resource-access-docker");
    fireEvent.click(select);
    fireEvent.click(screen.getByRole("option", { name: "Ask before use" }));
    await waitFor(() =>
      expect(mock.updateHostResource).toHaveBeenCalledWith("docker", { access: "ask" }),
    );
  });

  it("shows an exact ask even when broader effective policy blocks the resource", async () => {
    render(() => <HostResourcesSettingsPanel client={client()} />);
    await screen.findByText("1 of 2 available");
    fireEvent.click(screen.getByTestId("resource-row-aws-cli"));

    const select = (await screen.findByTestId(
      "resource-access-aws-cli",
    )) as HTMLButtonElement;
    expect(select.textContent).toContain("Ask before use");
  });

  it("refreshes discovery without remounting the panel", async () => {
    const refreshed: HostResourcesResponse = {
      ...initial,
      checked_at: "2026-08-04T12:01:00Z",
      resources: initial.resources.map((item) => ({
        ...item,
        status: "available" as const,
      })),
    };
    const mock = client(refreshed);
    render(() => <HostResourcesSettingsPanel client={mock} />);

    expect(await screen.findByText("1 of 2 available")).toBeTruthy();
    fireEvent.click(screen.getByTestId("host-resources-refresh"));
    await waitFor(() => expect(screen.getByText("2 of 2 available")).toBeTruthy());
    expect(mock.refreshHostResources).toHaveBeenCalledOnce();
  });
});
