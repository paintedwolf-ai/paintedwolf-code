import { describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { fireEvent, render } from "@solidjs/testing-library";
import type { ExternalAccess } from "../../api/types.ts";
import { NetworkChicklet } from "./NetworkChicklet.tsx";
import { NetworkActionContext } from "../../chat/network-action-context.ts";
import {
  buildExternalAccessView,
  EXTERNAL_ACCESS_LABEL,
} from "../../chat/tool/external-access-presentation.ts";

describe("NetworkChicklet / External access", () => {
  const endpointAccess = (host: string, decision: "allow" | "deny"): ExternalAccess => ({
    modes: ["mediated_http"],
    visibility_summary: "observed",
    endpoints: [{
      host,
      port: 443,
      transport: "http_connect",
      visibility: "observed",
      decision,
      attempt_count: 1,
    }],
  });

  it("offers Block on allowed hosts, but not on already-blocked ones", () => {
    const { getByTestId, queryByTestId } = render(() => (
      <NetworkActionContext.Provider
        value={{ blockHost: vi.fn(), isBlocked: () => false }}
      >
        <NetworkChicklet
          externalAccess={{
            ...endpointAccess("github.com", "allow"),
            endpoints: [
              ...(endpointAccess("github.com", "allow").endpoints ?? []),
              ...(endpointAccess("evil.test", "deny").endpoints ?? []),
            ],
          }}
        />
      </NetworkActionContext.Provider>
    ));
    expect(getByTestId("network-chicklet-block-github.com")).toBeTruthy();
    expect(queryByTestId("network-chicklet-block-evil.test")).toBeNull();
  });

  it("blocks a host through the context and reflects it optimistically", async () => {
    const [blocked, setBlocked] = createSignal<ReadonlySet<string>>(new Set());
    const blockHost = vi.fn(async (host: string) => {
      setBlocked((prev) => new Set(prev).add(host));
    });
    const { getByTestId, queryByTestId } = render(() => (
      <NetworkActionContext.Provider
        value={{ blockHost, isBlocked: (h) => blocked().has(h) }}
      >
        <NetworkChicklet externalAccess={endpointAccess("github.com", "allow")} />
      </NetworkActionContext.Provider>
    ));
    fireEvent.click(getByTestId("network-chicklet-block-github.com"));
    await Promise.resolve();
    expect(blockHost).toHaveBeenCalledWith("github.com");
    expect(queryByTestId("network-chicklet-block-github.com")).toBeNull();
  });

  it("renders no Block button when no network actions are available", () => {
    const { queryByTestId } = render(() => (
      <NetworkChicklet externalAccess={endpointAccess("github.com", "allow")} />
    ));
    expect(queryByTestId("network-chicklet-block-github.com")).toBeNull();
  });

  it("shows External access label and a11y visibility text", () => {
    const { getByTestId, getByText } = render(() => (
      <NetworkChicklet
        externalAccess={{
          modes: ["local_socket"],
          visibility_summary: "unobserved",
          sockets: [
            {
              approved_path: "/var/run/docker.sock",
              resolved_path: "/var/run/docker.sock",
              scope: "chat",
              capability_visibility: "observed",
              connection_visibility: "unobserved",
              inner_effect_visibility: "unobserved",
              authority: "outside_sandbox_daemon",
            },
          ],
        }}
      />
    ));
    expect(getByText(EXTERNAL_ACCESS_LABEL)).toBeTruthy();
    expect(getByTestId("external-access-sockets")).toBeTruthy();
    expect(getByTestId("external-access-visibility").textContent).toMatch(
      /unobserved/i,
    );
    const summary = getByTestId("network-chicklet").querySelector("summary");
    expect(summary?.getAttribute("aria-label")).toMatch(/External access/);
    expect(summary?.getAttribute("aria-label")).toMatch(/unobserved/i);
  });

  it("renders empty observed + socket without disappearing or claiming no calls", () => {
    const ea: ExternalAccess = {
      modes: ["local_socket"],
      visibility_summary: "unobserved",
      sockets: [
        {
          approved_path: "/tmp/very/long/path/to/redacted.sock",
          resolved_path: "/tmp/very/long/path/to/redacted.sock",
          scope: "current_action",
          capability_visibility: "observed",
          connection_visibility: "unobserved",
          inner_effect_visibility: "unobserved",
          authority: "outside_sandbox_daemon",
        },
      ],
    };
    const view = buildExternalAccessView({ externalAccess: ea });
    expect(view?.mustShow).toBe(true);
    expect(view?.summary ?? "").not.toMatch(/no network calls|made no network|no outbound/i);
    expect(view?.summary.toLowerCase()).not.toMatch(/no network/);

    const { getByTestId, queryByTestId } = render(() => (
      <NetworkChicklet externalAccess={ea} />
    ));
    expect(getByTestId("network-chicklet")).toBeTruthy();
    expect(getByTestId("external-access-sockets")).toBeTruthy();
    expect(queryByTestId("external-access-endpoints")).toBeNull();
  });

  it("renders mixed mediated + socket/direct", () => {
    const ea: ExternalAccess = {
      modes: ["mediated_socks", "local_socket", "direct_ip"],
      visibility_summary: "mixed",
      endpoints: [
        {
          host: "db.example.com",
          port: 5432,
          transport: "socks_tcp",
          visibility: "observed",
          decision: "allow",
          attempt_count: 2,
        },
      ],
      sockets: [
        {
          approved_path: "/run/podman.sock",
          resolved_path: "/run/podman.sock",
          scope: "chat",
          capability_visibility: "observed",
          connection_visibility: "unobserved",
          inner_effect_visibility: "unobserved",
          authority: "outside_sandbox_daemon",
        },
      ],
      direct: {
        scope: "current_action",
        actual_destination_visibility: "unobserved",
      },
      declared_destinations: ["db.example.com:5432"],
    };
    const { getByTestId, getByText } = render(() => (
      <NetworkChicklet externalAccess={ea} />
    ));
    expect(getByTestId("external-access-endpoints")).toBeTruthy();
    expect(getByTestId("external-access-sockets")).toBeTruthy();
    expect(getByTestId("external-access-direct")).toBeTruthy();
    expect(getByTestId("external-access-declared")).toBeTruthy();
    expect(getByText("Declared")).toBeTruthy();
    expect(getByText("Chat")).toBeTruthy();
    expect(getByTestId("network-chicklet").getAttribute("data-visibility")).toBe(
      "mixed",
    );
  });

  it("never fabricates a no-network-calls claim for unobserved/unknown", () => {
    for (const summary of ["unobserved", "unknown", "mixed"] as const) {
      const view = buildExternalAccessView({
        externalAccess: {
          modes: summary === "mixed" ? ["mediated_http", "direct_ip"] : ["direct_ip"],
          visibility_summary: summary,
          direct: {
            scope: "current_action",
            actual_destination_visibility: "unobserved",
          },
        },
      });
      expect(view?.mustShow).toBe(true);
      expect(view?.summary ?? "").not.toMatch(/no network calls|made no network|no outbound/i);
      expect(view?.visibilityAnnouncement ?? "").not.toMatch(/no network calls|made no network|no outbound/i);
    }
  });

  it("renders IPv6 and transport intelligibly", () => {
    const view = buildExternalAccessView({
      externalAccess: {
        modes: ["mediated_socks"],
        visibility_summary: "observed",
        endpoints: [
          {
            host: "2001:db8::1",
            port: 443,
            transport: "socks_tcp",
            visibility: "observed",
            decision: "allow",
            attempt_count: 1,
          },
        ],
      },
    });
    expect(view?.endpoints[0]?.label).toContain("[2001:db8::1]:443");
    expect(view?.endpoints[0]?.label).toContain("SOCKS TCP");
  });
});
