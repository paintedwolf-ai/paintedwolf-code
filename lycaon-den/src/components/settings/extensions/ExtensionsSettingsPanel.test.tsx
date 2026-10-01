import { beforeEach, describe, expect, it, vi } from "vitest";
import { Show, createSignal } from "solid-js";
import { at } from "../../../test/at.ts";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import {
  EXTENSION_UNITS_PAGE_SIZE,
  ExtensionsSettingsPanel,
} from "./ExtensionsSettingsPanel.tsx";
import { resetExtensionsTabMemoryForTest } from "../../../settings/extensions/extensions-tab.ts";
import { LycaonApiError } from "../../../api/http.ts";
import type {
  ExtensionDesiredState,
  ExtensionMetaPackSummary,
  ExtensionPackSummary,
  ExtensionUnitSummary,
} from "../../../api/types.ts";
import { EXTENSIONS_SETTINGS_COPY } from "../../../settings/extensions/extensions-settings-copy.ts";

const packs = [
  {
    id: "painted-wolf/platform",
    name: "Platform",
    version: "1.0.0",
    extension_api: "^1.0.0",
    installation_state: "stock",
    installation_scope: "stock",
    dependencies: {},
    kind: "stock",
    enabled: true,
    removable: false,
    unit_count: 2,
    has_profile: false,
    contributing: true,
    feature: "platform",
    source: "embed://painted-wolf/platform",
    ref: "app@1.0.0",
    meta_pack_ids: ["painted-wolf/stock"],
  },
  {
    id: "painted-wolf/plan",
    name: "Plan",
    version: "1.0.0",
    extension_api: "^1.0.0",
    installation_state: "stock",
    installation_scope: "stock",
    dependencies: {},
    kind: "stock",
    enabled: true,
    removable: false,
    unit_count: 1,
    has_profile: false,
    contributing: true,
    feature: "plan",
    source: "embed://painted-wolf/plan",
    ref: "app@1.0.0",
    meta_pack_ids: ["painted-wolf/stock"],
  },
  {
    id: "acme/extras",
    name: "ACME extras",
    version: "1.0.0",
    extension_api: "^1.0.0",
    installation_state: "release",
    installation_scope: "device",
    version_constraint: "^1.0.0",
    dependencies: {},
    kind: "git",
    enabled: true,
    removable: true,
    unit_count: 1,
    has_profile: true,
    contributing: true,
    source: "https://github.com/acme/extras",
    meta_pack_ids: [],
  },
] satisfies ExtensionPackSummary[];

const stockMeta: ExtensionMetaPackSummary = {
  id: "painted-wolf/stock",
  name: "Painted Wolf stock",
  version: "1.0.0",
  kind: "stock",
  status: "complete",
  members: ["painted-wolf/platform", "painted-wolf/plan"],
  conflicts_with: [],
  extends: [],
  diagnostics: [],
  removable: false,
};

const units: ExtensionUnitSummary[] = [
  {
    id: "workflows/plan",
    project_disable_allowed: true,
    kind: "workflows",
    title: "Plan recipe",
    status: "conflict",
    contributions: [
      { pack_id: "painted-wolf/plan", path: "workflows/plan.yaml" },
      { pack_id: "acme/extras", path: "workflows/plan.yaml" },
    ],
  },
  {
    id: "guidance/gate-blocked",
    project_disable_allowed: true,
    kind: "guidance",
    title: "Gate blocked",
    status: "loaded",
    winner_pack_id: "painted-wolf/plan",
    contributions: [{ pack_id: "painted-wolf/plan", path: "guidance/gate-blocked.md" }],
  },
];

const desired: ExtensionDesiredState = {
  format: 1,
  packs: [],
  disabled: [],
  own: {},
  yaml: "format: 1\npacks: []\n",
};

function catalogView(over: Record<string, unknown> = {}) {
  return {
    revision: (over.revision as string) ?? "rev-1",
    packs: (over.packs as unknown[]) ?? packs,
    meta_packs: (over.metaPacks as unknown[]) ?? [stockMeta],
    diagnostics: [],
    units: (over.units as unknown[]) ?? units,
    desired,
    desired_path: "/tmp/extensions.yaml",
    ok: over.ok ?? true,
    conflicts: over.conflicts ?? 0,
  };
}

function mockClient(overrides: Record<string, unknown> = {}) {
  return {
    getExtensionsCatalog: vi.fn().mockResolvedValue(
      catalogView({ ok: false, conflicts: 1 }),
    ),
    getExtensionUnit: vi.fn().mockImplementation((id: string) => {
      const u = units.find((x) => x.id === id)!;
      return Promise.resolve({
        ...u,
        content: u.status === "conflict" ? "" : "body",
        contributions: u.contributions.map((p) => ({
          ...p,
          content: `from ${p.pack_id}`,
        })),
      });
    }),
    installExtensionPack: vi
      .fn()
      .mockResolvedValue({ view: catalogView({}), pack_id: "x", package_root: "/tmp" }),
    deleteExtensionPack: vi.fn().mockResolvedValue({ view: catalogView({}) }),
    updateExtensionUnit: vi.fn().mockResolvedValue({ view: catalogView({}) }),
    updateExtensionMetaPack: vi.fn().mockResolvedValue({ view: catalogView({}) }),
    applyExtensionPackProfile: vi.fn().mockResolvedValue({ view: catalogView({}) }),
    applyExtensionMetaPack: vi.fn().mockResolvedValue({ view: catalogView({}) }),
    installExtensionMetaPack: vi.fn().mockResolvedValue({ view: catalogView({}) }),
    deleteExtensionMetaPack: vi.fn().mockResolvedValue({ view: catalogView({}) }),
    updateExtensionPack: vi.fn().mockResolvedValue({ view: catalogView({}), pack_id: "acme/extras", resolved_revision: "abc", message: "updated" }),
    reloadExtensionPack: vi.fn().mockResolvedValue({ view: catalogView({}), pack_id: "acme/linked", message: "reloaded" }),
    getExtensionPackUpdate: vi.fn().mockResolvedValue({
      pack_id: "acme/extras",
      available: false,
      changes: [],
      message: "ok",
    }),
    ...overrides,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

describe("ExtensionsSettingsPanel", () => {
  const packTid = (id: string) => `extensions-pack-${encodeURIComponent(id)}`;
  const unitTid = (id: string) =>
    `extensions-unit-row-${encodeURIComponent(id)}`;

  beforeEach(() => {
    resetExtensionsTabMemoryForTest();
  });

  it.each([
    { surface: "project" as const, allowed: false, status: "loaded" as const, control: null },
    { surface: "project" as const, allowed: false, status: "disabled" as const, control: null },
    { surface: "project" as const, allowed: true, status: "loaded" as const, control: "disable" },
    { surface: "project" as const, allowed: true, status: "disabled" as const, control: "reenable" },
    { surface: "settings" as const, allowed: false, status: "loaded" as const, control: "disable" },
  ])("uses the host capability for $surface $status units (allowed=$allowed)", async ({ surface, allowed, status, control }) => {
    const unit = { ...units[0], id: "future/fixture", kind: "future_kind", status, project_disable_allowed: allowed };
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue(catalogView({ units: [unit] })),
      getExtensionUnit: vi.fn().mockResolvedValue({ ...unit, content: "fixture" }),
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface={surface} projectId="proj-1" />
    ));
    if (surface === "settings") fireEvent.click(getByTestId("extensions-tab-units"));
    await waitFor(() => getByTestId(unitTid(unit.id)));
    fireEvent.click(getByTestId(unitTid(unit.id)));
    await waitFor(() => getByTestId("extensions-unit-detail"));
    expect(Boolean(queryByTestId("extensions-unit-disable"))).toBe(control === "disable");
    expect(Boolean(queryByTestId("extensions-unit-reenable"))).toBe(control === "reenable");
    if (control === null) expect(getByTestId("extensions-unit-detail").textContent).toContain(EXTENSIONS_SETTINGS_COPY.deviceControlledUnit);
  });

  it("reports first catalog readiness as one settled boundary", async () => {
    const load = deferred<ReturnType<typeof catalogView>>();
    const onReadyChange = vi.fn();
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockReturnValue(load.promise),
    });
    render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="first"
        onReadyChange={onReadyChange}
      />
    ));

    await waitFor(() => expect(onReadyChange).toHaveBeenLastCalledWith(false));
    load.resolve(catalogView());
    await waitFor(() => expect(onReadyChange).toHaveBeenLastCalledWith(true));
  });

  it("ignores a stale catalog response after a project switch", async () => {
    const first = deferred<ReturnType<typeof catalogView>>();
    const currentUnit = { ...at(units, 0), id: "workflows/current", title: "Current" };
    const staleUnit = { ...at(units, 0), id: "workflows/stale", title: "Stale" };
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockImplementation((projectId?: string) =>
        projectId === "first"
          ? first.promise
          : Promise.resolve(catalogView({ packs: [], metaPacks: [], units: [currentUnit] })),
      ),
    });
    const [projectId, setProjectId] = createSignal("first");
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId={projectId()}
      />
    ));

    await waitFor(() => expect(client.getExtensionsCatalog).toHaveBeenCalledWith("first"));
    setProjectId("current");
    await waitFor(() => getByTestId(unitTid(currentUnit.id)));
    first.resolve(catalogView({ packs: [], metaPacks: [], units: [staleUnit] }));
    await waitFor(() => {
      expect(getByTestId(unitTid(currentUnit.id))).toBeTruthy();
      expect(queryByTestId(unitTid(staleUnit.id))).toBeNull();
    });
  });

  it("does not fall back to device extensions without a project id", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="project" />
    ));

    await waitFor(() => {
      expect(getByTestId("extensions-load-error").textContent).toContain(
        EXTENSIONS_SETTINGS_COPY.projectRequired,
      );
    });
    expect(client.getExtensionsCatalog).not.toHaveBeenCalled();
  });

  it("settings surface defaults to How it works", async () => {
    const client = mockClient();
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    await waitFor(() => getByTestId("extensions-panel-model"));
    expect(getByTestId("extensions-panel-model").hidden).toBe(false);
    expect(getByTestId("extensions-tab-model")).toBeTruthy();
    expect(queryByTestId("extensions-tab-units")).toBeTruthy();
    expect(getByTestId("extensions-panel-model").textContent).toContain(
      EXTENSIONS_SETTINGS_COPY.modelIntroTitle,
    );
    expect(getByTestId("extensions-panel-model").textContent).toContain(
      EXTENSIONS_SETTINGS_COPY.modelWhereTitle,
    );
  });

  it("stays on Installed after a remount", async () => {
    const client = mockClient();
    const [mounted, setMounted] = createSignal(true);
    const { getByTestId } = render(() => (
      <Show when={mounted()}>
        <ExtensionsSettingsPanel client={client as never} surface="settings" />
      </Show>
    ));

    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => {
      expect(getByTestId("extensions-panel-packs").hidden).toBe(false);
    });

    setMounted(false);
    setMounted(true);
    await waitFor(() => getByTestId("extensions-panel-packs"));
    expect(getByTestId("extensions-panel-packs").hidden).toBe(false);
    await waitFor(() => expect(screen.queryByTestId("extensions-panel-model")?.closest("[data-resident]")?.getAttribute("data-resident")).not.toBe("active"));
  });

  it("settings surface loads stock packs and shows device scope only", async () => {
    const client = mockClient();
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => {
      expect(getByTestId(packTid("painted-wolf/plan")).textContent).toContain(
        "Stock",
      );
    });
    expect(getByTestId("extensions-device-banner").textContent).toBe(
      EXTENSIONS_SETTINGS_COPY.settingsBanner,
    );
    expect(queryByTestId("extensions-tab-units")).toBeTruthy();
    expect(queryByTestId("extensions-tab-desired")).toBeNull();
    expect(client.getExtensionsCatalog).toHaveBeenCalledWith(undefined);
  });

  it("settings ignores projectId and never shows This project", async () => {
    const client = mockClient();
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="settings"
        projectId="proj-1"
      />
    ));

    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId("extensions-pack-list"));
    expect(queryByTestId("extensions-scope-project")).toBeNull();
    expect(getByTestId("extensions-device-banner")).toBeTruthy();
    expect(client.getExtensionsCatalog).toHaveBeenCalledWith(undefined);
  });

  it("validates empty URL in Add from Git dialog", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => {
      expect((getByTestId("extensions-add-git") as HTMLButtonElement).disabled).toBe(false);
    });
    fireEvent.click(getByTestId("extensions-add-git"));
    fireEvent.click(getByTestId("extensions-add-install"));
    await waitFor(() => {
      expect(getByTestId("extensions-add-error").textContent).toContain(
        EXTENSIONS_SETTINGS_COPY.addUrlRequired,
      );
    });
    expect(client.installExtensionPack).not.toHaveBeenCalled();
  });

  it("installs a SemVer range and refuses an ambiguous range plus ref", async () => {
    const client = mockClient();
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));
    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => expect((getByTestId("extensions-add-git") as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(getByTestId("extensions-add-git"));
    fireEvent.input(getByTestId("extensions-add-url"), { target: { value: "https://example.com/acme.git" } });
    fireEvent.input(getByTestId("extensions-add-version"), { target: { value: "^2.0.0" } });
    fireEvent.click(getByTestId("extensions-add-install"));
    await waitFor(() => {
      expect(client.installExtensionPack).toHaveBeenCalledWith(
        {
          source: "https://example.com/acme.git",
          version: "^2.0.0",
          ref: undefined,
          expected_revision: "rev-1",
        },
      );
    });
    await waitFor(() => expect(queryByTestId("extensions-add-dialog")).toBeNull());

    await waitFor(() => {
      expect((getByTestId("extensions-add-git") as HTMLButtonElement).disabled).toBe(false);
    });
    fireEvent.click(getByTestId("extensions-add-git"));
    fireEvent.input(getByTestId("extensions-add-url"), { target: { value: "https://example.com/acme.git" } });
    fireEvent.input(getByTestId("extensions-add-version"), { target: { value: "^2.0.0" } });
    fireEvent.input(getByTestId("extensions-add-ref"), { target: { value: "main" } });
    fireEvent.click(getByTestId("extensions-add-install"));
    await waitFor(() => {
      expect(getByTestId("extensions-add-error").textContent).toContain(
        EXTENSIONS_SETTINGS_COPY.versionOrRef,
      );
    });
  });

  it("offers a reload action on a stale-revision conflict and refetches the catalog", async () => {
    const client = mockClient({
      installExtensionPack: vi
        .fn()
        .mockRejectedValueOnce(
          new LycaonApiError(
            "extension state changed since the expected revision; reload and retry",
            409,
            "extension_state_changed",
          ),
        ),
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));
    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => expect((getByTestId("extensions-add-git") as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(getByTestId("extensions-add-git"));
    fireEvent.input(getByTestId("extensions-add-url"), {
      target: { value: "https://example.com/acme.git" },
    });
    fireEvent.click(getByTestId("extensions-add-install"));

    await waitFor(() => getByTestId("extensions-action-error-reload"));
    expect(getByTestId("extensions-action-error").getAttribute("data-code")).toBe(
      "extension_state_changed",
    );
    expect(client.getExtensionsCatalog).toHaveBeenCalledTimes(1);

    fireEvent.click(getByTestId("extensions-action-error-reload"));
    await waitFor(() => expect(client.getExtensionsCatalog).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(queryByTestId("extensions-action-error")).toBeNull());
  });

  it("project surface defaults to Active and shows Conflict chip", async () => {
    const client = mockClient();
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));

    await waitFor(() => getByTestId(unitTid("workflows/plan")));
    expect(getByTestId("extensions-panel-units").hidden).toBe(false);
    expect(
      getByTestId("extensions-tab-units").getAttribute("data-first-time-tip-anchor"),
    ).toBe("project-extensions");
    expect(getByTestId(unitTid("workflows/plan")).textContent).toContain(
      "Conflict",
    );
    expect(queryByTestId("extensions-tab-model")).toBeNull();
    expect(queryByTestId("extensions-tab-settings")).toBeNull();
    expect(queryByTestId("extensions-device-banner")).toBeNull();
    expect(queryByTestId("extensions-add-git")).toBeNull();
    expect(client.getExtensionsCatalog).toHaveBeenCalledWith("proj-1");
  });

  it("bounds the unit DOM and resets pagination when search changes", async () => {
    const manyUnits = Array.from({ length: EXTENSION_UNITS_PAGE_SIZE * 2 + 1 }, (_, i) => ({
      id: `guidance/unit-${String(i).padStart(3, "0")}`,
      kind: "guidance" as const,
      title: `Unit ${String(i).padStart(3, "0")}`,
      status: "loaded" as const,
      winner_pack_id: "painted-wolf/plan",
      contributions: [
        { pack_id: "painted-wolf/plan", path: `guidance/unit-${i}.md` },
      ],
    }));
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue(
        catalogView({ units: manyUnits }),
      ),
    });
    const { container, getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));

    await waitFor(() => {
      expect(
        container.querySelectorAll('[data-testid^="extensions-unit-row-"]'),
      ).toHaveLength(EXTENSION_UNITS_PAGE_SIZE);
    });
    expect(getByTestId("extensions-unit-pager").textContent).toContain(
      `1–${EXTENSION_UNITS_PAGE_SIZE} of ${manyUnits.length}`,
    );

    fireEvent.click(getByTestId("extensions-unit-pager-next"));
    await waitFor(() => getByTestId(unitTid("guidance/unit-050")));
    expect(queryByTestId(unitTid("guidance/unit-000"))).toBeNull();

    fireEvent.input(getByTestId("extensions-unit-search"), {
      target: { value: "Unit 000" },
    });
    await waitFor(() => getByTestId(unitTid("guidance/unit-000")));
    expect(queryByTestId("extensions-unit-pager")).toBeNull();
  });

  it("disables stock remove in pack detail on settings", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId(packTid("painted-wolf/plan")));
    fireEvent.click(getByTestId(packTid("painted-wolf/plan")));
    await waitFor(() => getByTestId("extensions-pack-back"));
    await waitFor(() => getByTestId("extensions-remove-pack"));
    expect(
      (getByTestId("extensions-remove-pack") as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("navigates pack list into detail with shared list chrome", async () => {
    const client = mockClient();
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId("extensions-pack-chrome"));
    await waitFor(() => expect(getByTestId("extensions-pack-count").textContent).toContain("3 packs"));
    expect(getByTestId("extensions-add-git")).toBeTruthy();
    fireEvent.click(getByTestId(packTid("acme/extras")));
    await waitFor(() => getByTestId("extensions-pack-detail"));
    expect(queryByTestId("extensions-pack-chrome")).toBeNull();
    expect(getByTestId("extensions-pack-back")).toBeTruthy();
    fireEvent.click(getByTestId("extensions-pack-back"));
    await waitFor(() => getByTestId("extensions-pack-chrome"));
    expect(queryByTestId("extensions-pack-detail")).toBeNull();
  });

  it("applies a device profile", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    await waitFor(() => getByTestId("extensions-tab-packs"));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => {
      expect(
        (getByTestId("extensions-apply-profile") as HTMLButtonElement).disabled,
      ).toBe(false);
    });
    fireEvent.click(getByTestId("extensions-apply-profile"));
    await waitFor(() => getByTestId("extensions-profile-name"));
    fireEvent.input(getByTestId("extensions-profile-name"), {
      target: { value: "quieter" },
    });
    fireEvent.click(getByTestId("extensions-profile-apply"));

    await waitFor(() => {
      expect(client.applyExtensionPackProfile).toHaveBeenCalledWith(
        "acme/extras",
        "quieter",
        { expected_revision: "rev-1" },
      );
    });
    expect(getByTestId("extensions-action-status").textContent).toContain(
      EXTENSIONS_SETTINGS_COPY.deviceBanner,
    );
  });

  it("refetches the open unit detail after Own resolves a conflict", async () => {
    const client = mockClient();
    // Selection resolves the unit to the chosen provider.
    client.updateExtensionUnit = vi.fn().mockImplementation(() => {
      client.getExtensionUnit = vi.fn().mockResolvedValue({
        id: "workflows/plan",
        project_disable_allowed: true,
    kind: "workflows",
        title: "Plan recipe",
        status: "owned",
        winner_pack_id: "acme/extras",
        content: "from acme/extras",
        contributions: [
          { pack_id: "painted-wolf/plan", path: "p", content: "from painted-wolf/plan" },
          { pack_id: "acme/extras", path: "p", content: "from acme/extras" },
        ],
      });
      return Promise.resolve({
        view: catalogView({
          units: units.map((u) =>
            u.id === "workflows/plan"
              ? { ...u, status: "owned", winner_pack_id: "acme/extras" }
              : u,
          ),
        }),
      });
    });

    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    fireEvent.click(getByTestId("extensions-tab-units"));
    await waitFor(() => getByTestId(unitTid("workflows/plan")));
    fireEvent.click(getByTestId(unitTid("workflows/plan")));
    await waitFor(() => getByTestId("extensions-unit-detail"));

    fireEvent.click(
      getByTestId(`extensions-unit-own-${encodeURIComponent("acme/extras")}`),
    );

    await waitFor(() => {
      expect(getByTestId("extensions-unit-effective-body").textContent).toContain(
        "from acme/extras",
      );
    });
  });

  it("restores the pack checkbox when a platform disable is cancelled", async () => {
    // Component tests use the registered confirmation fallback.
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    try {
      const platformPack: ExtensionPackSummary = {
        ...at(packs, 0),
        id: "painted-wolf/platform",
        name: "Platform",
        feature: "platform",
      };
      const client = mockClient({
        getExtensionsCatalog: vi.fn().mockResolvedValue({
          packs: [platformPack],
          meta_packs: [],
          diagnostics: [],
          ok: true,
          conflicts: 0,
        }),
      });
      const { getByTestId } = render(() => (
        <ExtensionsSettingsPanel client={client as never} surface="settings" />
      ));

      fireEvent.click(getByTestId("extensions-tab-packs"));
      await waitFor(() => getByTestId(packTid("painted-wolf/platform")));

      const box = getByTestId(
        `extensions-pack-enable-${encodeURIComponent("painted-wolf/platform")}`,
      ) as HTMLInputElement;
      fireEvent.click(box);

      await waitFor(() => expect(confirmSpy).toHaveBeenCalled());
      await waitFor(() => expect(box.checked).toBe(true));
      expect(client.updateExtensionPack).not.toHaveBeenCalled();
      expect(confirmSpy.mock.calls[0]?.[0]).toContain("Disabling Platform");
    } finally {
      confirmSpy.mockRestore();
    }
  });

  // Mutation responses carry the committed catalog.
  it("applies the generation a mutation returns without re-reading the catalog", async () => {
    const client = mockClient({
      updateExtensionPack: vi.fn().mockResolvedValue({
        view: catalogView({
          revision: "rev-2",
          packs: packs.map((p) =>
            p.id === "acme/extras" ? { ...p, enabled: false } : p,
          ),
        }),
      }),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId(packTid("acme/extras")));
    expect(client.getExtensionsCatalog).toHaveBeenCalledTimes(1);

    const box = getByTestId(
      `extensions-pack-enable-${encodeURIComponent("acme/extras")}`,
    ) as HTMLInputElement;
    fireEvent.click(box);

    await waitFor(() => expect(client.updateExtensionPack).toHaveBeenCalled());
    await waitFor(() => expect(box.checked).toBe(false));
    expect(client.getExtensionsCatalog).toHaveBeenCalledTimes(1);
    expect(getByTestId("extensions-panel-packs").hidden).toBe(false);
    await waitFor(() => expect(screen.queryByTestId("extensions-panel-model")?.closest("[data-resident]")?.getAttribute("data-resident")).not.toBe("active"));
  });

  it("groups stock packs under Painted Wolf stock and orphans under Other packs", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId("extensions-meta-section-painted-wolf-stock"));
    const stockSection = getByTestId("extensions-meta-section-painted-wolf-stock");
    expect(stockSection.textContent).toContain("Painted Wolf stock");
    expect(stockSection.textContent).toContain("Platform");
    expect(stockSection.textContent).toContain("Complete");
    expect(getByTestId("extensions-meta-section-other-packs").textContent).toContain(
      "ACME extras",
    );
    expect(getByTestId("extensions-install-suite")).toBeTruthy();
    const disableStock = getByTestId(
      "extensions-meta-disable-painted-wolf-stock",
    ) as HTMLButtonElement;
    expect(disableStock.disabled).toBe(true);
    expect(disableStock.dataset.tip).toContain("platform");
  });

  it("Enable suite calls applyExtensionMetaPack with the stock id", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() =>
      getByTestId("extensions-meta-enable-painted-wolf-stock"),
    );
    fireEvent.click(getByTestId("extensions-meta-enable-painted-wolf-stock"));
    await waitFor(() => {
      expect(client.applyExtensionMetaPack).toHaveBeenCalledWith(
        "painted-wolf/stock",
        { expected_revision: "rev-1" },
      );
    });
  });

  it("Remove suite asks for confirmation and only removes it when confirmed", async () => {
    // Component tests use the registered confirmation fallback.
    const removableMeta: ExtensionMetaPackSummary = {
      id: "acme/toolkit",
      name: "Acme Toolkit",
      version: "1.0.0",
      kind: "git",
      status: "complete",
      members: ["acme/extras"],
      conflicts_with: [],
      extends: [],
      diagnostics: [],
      removable: true,
    };
    const client = mockClient({
      getExtensionsCatalog: vi
        .fn()
        .mockResolvedValue(catalogView({ metaPacks: [stockMeta, removableMeta] })),
    });
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    try {
      const { getByTestId } = render(() => (
        <ExtensionsSettingsPanel client={client as never} surface="settings" />
      ));

      fireEvent.click(getByTestId("extensions-tab-packs"));
      await waitFor(() => getByTestId("extensions-meta-remove-acme-toolkit"));
      fireEvent.click(getByTestId("extensions-meta-remove-acme-toolkit"));

      await waitFor(() => expect(confirmSpy).toHaveBeenCalled());
      expect(client.deleteExtensionMetaPack).not.toHaveBeenCalled();

      confirmSpy.mockReturnValue(true);
      fireEvent.click(getByTestId("extensions-meta-remove-acme-toolkit"));
      await waitFor(() => {
        expect(client.deleteExtensionMetaPack).toHaveBeenCalledWith(
          "acme/toolkit",
          "rev-1",
        );
      });
    } finally {
      confirmSpy.mockRestore();
    }
  });

  it("shows Suite conflict chip from suite diagnostics", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue({
        packs,
        meta_packs: [
          {
            ...stockMeta,
            status: "partial",
            diagnostics: [
              {
                code: "suite_conflict",
                message: "suite conflict",
                pack_id: "acme/alt",
              },
              {
                code: "member_missing",
                message: "missing",
                pack_id: "painted-wolf/ghost",
              },
            ],
          },
        ],
        diagnostics: [],
        ok: true,
        conflicts: 0,
      }),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId("extensions-meta-section-painted-wolf-stock"));
    const section = getByTestId("extensions-meta-section-painted-wolf-stock");
    expect(section.textContent).toContain(
      EXTENSIONS_SETTINGS_COPY.chipSuiteConflict,
    );
    expect(section.textContent).toContain(
      EXTENSIONS_SETTINGS_COPY.chipPartialSuite,
    );
    expect(section.textContent).toContain(
      EXTENSIONS_SETTINGS_COPY.chipMissingMember,
    );
  });

  it("How it works mentions suites", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));
    await waitFor(() => getByTestId("extensions-model-meta-packs"));
    expect(getByTestId("extensions-model-meta-packs").textContent).toContain(
      "provide(enabled)",
    );
  });

  it("settings Installed offers Install from folder and author loop copy", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));
    await waitFor(() => getByTestId("extensions-author-loop"));
    expect(getByTestId("extensions-author-loop").textContent).toContain(
      "no special app build",
    );
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId("extensions-add-folder"));
    expect(getByTestId("extensions-add-git")).toBeTruthy();
  });

  it("keeps mutations disabled until the concurrency revision is loaded", async () => {
    let resolveCatalog!: (value: unknown) => void;
    const [revision, setRevision] = createSignal(0);
    const client = mockClient({
      getExtensionsCatalog: vi
        .fn()
        .mockResolvedValueOnce(catalogView({}))
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              resolveCatalog = resolve;
            }),
        ),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="settings"
        extensionsRevision={revision}
      />
    ));

    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId("extensions-add-folder"));
    const addFolder = getByTestId(
      "extensions-add-folder",
    ) as HTMLButtonElement;
    await waitFor(() => expect(addFolder.disabled).toBe(false));

    setRevision(1);
    await waitFor(() => expect(client.getExtensionsCatalog).toHaveBeenCalledTimes(2));
    expect(addFolder.disabled).toBe(true);

    resolveCatalog(catalogView({ revision: "rev-2" }));
    await waitFor(() => expect(addFolder.disabled).toBe(false));
  });

  it("a mutation that outraces an in-flight background reload does not leave the panel stuck disabled", async () => {
    // The mutation completes while a background catalog read is pending.
    let resolveMutation!: (value: unknown) => void;
    let resolveReload!: (value: unknown) => void;
    const [revision, setRevision] = createSignal(0);
    const client = mockClient({
      getExtensionsCatalog: vi
        .fn()
        .mockResolvedValueOnce(catalogView({}))
        .mockImplementationOnce(
          () => new Promise((resolve) => (resolveReload = resolve)),
        ),
      updateExtensionPack: vi.fn().mockImplementation(
        () => new Promise((resolve) => (resolveMutation = resolve)),
      ),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="settings"
        extensionsRevision={revision}
      />
    ));

    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId(packTid("acme/extras")));

    const box = getByTestId(
      `extensions-pack-enable-${encodeURIComponent("acme/extras")}`,
    ) as HTMLInputElement;
    fireEvent.click(box);
    await waitFor(() => expect(client.updateExtensionPack).toHaveBeenCalled());

    // A background reload starts while the mutation above is still pending.
    setRevision(1);
    await waitFor(() => expect(client.getExtensionsCatalog).toHaveBeenCalledTimes(2));

    const addFolder = getByTestId(
      "extensions-add-folder",
    ) as HTMLButtonElement;
    expect(addFolder.disabled).toBe(true);

    // The committed mutation invalidates the pending reload.
    resolveMutation({ view: catalogView({ revision: "rev-2" }) });
    await waitFor(() => expect(box.checked).toBe(false));

    // A superseded read settles without replacing the mutation result.
    resolveReload(catalogView({ revision: "rev-3" }));
    await waitFor(() => expect(addFolder.disabled).toBe(false));
  });

  it("git Update fetches a plan then confirms", async () => {
    const client = mockClient();
    client.getExtensionPackUpdate = vi.fn().mockResolvedValue({
      pack_id: "acme/extras",
      available: true,
      changes: [{
        pack_id: "acme/dependency",
        current_version: "1.0.0",
        candidate_version: "1.1.0",
        kind: "upgraded",
      }],
      message: "update available",
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId(packTid("acme/extras")));
    fireEvent.click(getByTestId(packTid("acme/extras")));
    await waitFor(() => getByTestId("extensions-update-pack"));
    expect((getByTestId("extensions-update-pack") as HTMLButtonElement).disabled).toBe(
      false,
    );
    fireEvent.click(getByTestId("extensions-update-pack"));
    await waitFor(() => getByTestId("extensions-update-plan"));
    expect(getByTestId("extensions-update-plan").textContent).toContain("acme/dependency");
    fireEvent.click(getByTestId("extensions-update-confirm"));
    await waitFor(() => {
      expect(client.updateExtensionPack).toHaveBeenCalledWith(
        "acme/extras",
        { expected_revision: "rev-1" },
      );
    });
  });

  it("renders skills units with the Skill label and standard status chrome", async () => {
    const skillUnits: ExtensionUnitSummary[] = [
      {
        id: "skills/house-style",
        project_disable_allowed: false,
        kind: "skills",
        title: "House style",
        status: "loaded",
        winner_pack_id: "acme/extras",
        contributions: [{ pack_id: "acme/extras", path: "skills/house-style/SKILL.md" }],
      },
      {
        id: "skills/conflicted",
        project_disable_allowed: false,
        kind: "skills",
        title: "Conflicted skill",
        status: "conflict",
        contributions: [
          { pack_id: "painted-wolf/plan", path: "skills/conflicted/SKILL.md" },
          { pack_id: "acme/extras", path: "skills/conflicted/SKILL.md" },
        ],
      },
      {
        id: "skills/selected",
        project_disable_allowed: false,
        kind: "skills",
        title: "Selected skill",
        status: "owned",
        winner_pack_id: "acme/extras",
        contributions: [
          { pack_id: "painted-wolf/plan", path: "skills/selected/SKILL.md" },
          { pack_id: "acme/extras", path: "skills/selected/SKILL.md" },
        ],
      },
      {
        id: "skills/disabled",
        project_disable_allowed: false,
        kind: "skills",
        title: "Disabled skill",
        status: "disabled",
        contributions: [{ pack_id: "acme/extras", path: "skills/disabled/SKILL.md" }],
      },
    ];
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue(catalogView({ units: skillUnits })),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));

    await waitFor(() => getByTestId(unitTid("skills/house-style")));
    expect(getByTestId(unitTid("skills/house-style")).textContent).toContain("Skill");
    expect(getByTestId(unitTid("skills/house-style")).textContent).toContain("Provided");
    expect(getByTestId(unitTid("skills/conflicted")).textContent).toContain("Conflict");
    expect(getByTestId(unitTid("skills/selected")).textContent).toContain("Selected");
    expect(getByTestId(unitTid("skills/disabled")).textContent).toContain("Disabled");
    expect(getByTestId("extensions-kind-skills").textContent).toBe(
      EXTENSIONS_SETTINGS_COPY.kindSkill,
    );
  });

  it("renders an unknown unit kind without crashing", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue(
        catalogView({
          units: [
          {
            id: "future/thing",
            kind: "future_kind",
            title: "Future unit",
            status: "loaded",
            winner_pack_id: "acme/extras",
            contributions: [{ pack_id: "acme/extras", path: "future/thing.yaml" }],
          },
        ],
        }),
      ),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));
    await waitFor(() => getByTestId(unitTid("future/thing")));
    expect(getByTestId(unitTid("future/thing")).textContent).toContain("future_kind");
  });

  it("renders all skill-load diagnostics including unit-less skill_catalog_full", async () => {
    const skillCodes = [
      "skill_name_invalid",
      "skill_frontmatter_invalid",
      "skill_field_missing",
      "skill_field_invalid",
      "skill_compatibility_invalid",
      "skill_name_mismatch",
      "skill_too_large",
      "skill_catalog_full",
      "skill_shadowed",
    ] as const;
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue({
        packs,
        meta_packs: [stockMeta],
        diagnostics: skillCodes.map((code) => ({
          code,
          message: `${code} message`,
          ...(code === "skill_catalog_full"
            ? {}
            : { unit_id: "skills/example" }),
        })),
        ok: false,
        conflicts: 0,
      }),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));
    await waitFor(() => getByTestId("extensions-diagnostics"));
    const text = getByTestId("extensions-diagnostics").textContent ?? "";
    for (const code of skillCodes) {
      expect(text).toContain(code);
    }
    expect(text).toContain("skill_catalog_full message");
  });

  it("attributes a pack-scoped diagnostic to that pack's row and detail, not the global banner", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue({
        packs,
        meta_packs: [stockMeta],
        diagnostics: [
          {
            code: "skill_name_mismatch",
            message: "The name in SKILL.md must match its folder.",
            severity: "error",
            unit_id: "skills/tidy-notes",
            pack_id: "acme/extras",
          },
        ],
        ok: false,
        conflicts: 0,
      }),
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));

    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId(packTid("acme/extras")));

    // Pack diagnostics stay off the global banner.
    expect(queryByTestId("extensions-diagnostics")).toBeNull();

    // The pack row carries its diagnostic state.
    expect(getByTestId(packTid("acme/extras")).textContent).toContain("Has an error");

    // The detail view carries the full diagnostic.
    fireEvent.click(getByTestId(packTid("acme/extras")));
    await waitFor(() => getByTestId("extensions-pack-diagnostics"));
    const diagText = getByTestId("extensions-pack-diagnostics").textContent ?? "";
    expect(diagText).toContain("Error");
    expect(diagText).toContain("The name in SKILL.md must match its folder.");
    expect(diagText).toContain("skills/tidy-notes");
  });

  it("does not render skill body in unit detail", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue(
        catalogView({
          units: [
          {
            id: "skills/house-style",
            project_disable_allowed: false,
        kind: "skills",
            title: "House style",
            status: "loaded",
            winner_pack_id: "acme/extras",
            contributions: [
              { pack_id: "acme/extras", path: "skills/house-style/SKILL.md" },
            ],
          },
        ],
        }),
      ),
      getExtensionUnit: vi.fn().mockResolvedValue({
        id: "skills/house-style",
        project_disable_allowed: false,
        kind: "skills",
        title: "House style",
        status: "loaded",
        winner_pack_id: "acme/extras",
        content: "SECRET SKILL BODY",
        contributions: [
          {
            pack_id: "acme/extras",
            path: "skills/house-style/SKILL.md",
            content: "SECRET SKILL BODY",
          },
        ],
      }),
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));
    await waitFor(() => getByTestId(unitTid("skills/house-style")));
    fireEvent.click(getByTestId(unitTid("skills/house-style")));
    await waitFor(() => getByTestId("extensions-unit-detail"));
    expect(queryByTestId("extensions-unit-effective-body")).toBeNull();
    expect(queryByTestId("extensions-unit-contributions-body")).toBeNull();
    expect(getByTestId("extensions-unit-detail").textContent).not.toContain(
      "SECRET SKILL BODY",
    );
    expect(getByTestId("extensions-unit-detail").textContent).toContain("Skill");
  });

  it("linked pack shows Reload from disk", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue(
        catalogView({
          packs: [
            {
              ...packs[2],
              id: "acme/linked",
              name: "Linked",
              kind: "path",
              source: "/tmp/linked",
              meta_pack_ids: [],
            },
          ],
          metaPacks: [],
        }),
      ),
    });
    const { getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId(packTid("acme/linked")));
    fireEvent.click(getByTestId(packTid("acme/linked")));
    await waitFor(() => getByTestId("extensions-reload-pack"));
    fireEvent.click(getByTestId("extensions-reload-pack"));
    await waitFor(() => {
      expect(client.reloadExtensionPack).toHaveBeenCalledWith(
        "acme/linked",
        { expected_revision: "rev-1" },
      );
    });
  });

  it("project surface lists floor refusals with unit id and host reason", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue({
        packs,
        meta_packs: [stockMeta],
        diagnostics: [
          {
            code: "project_scope_refused",
            message: "project cannot disable this unit",
            unit_id: "policy/WRITE_SCOPE_DENIED",
          },
          {
            code: "skill_too_large",
            message: "skill too large",
            unit_id: "skills/example",
          },
        ],
        ok: false,
        conflicts: 0,
      }),
    });
    const { getByTestId, queryByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));
    await waitFor(() => getByTestId("extensions-project-refusals"));
    const group = getByTestId("extensions-project-refusals");
    expect(group.textContent).toContain(
      EXTENSIONS_SETTINGS_COPY.notAppliedFromProject,
    );
    expect(group.textContent).toContain("policy/WRITE_SCOPE_DENIED");
    expect(group.textContent).toContain("project cannot disable this unit");
    expect(group.getAttribute("aria-labelledby")).toBe(
      "extensions-project-refusals-heading",
    );

    await waitFor(() => getByTestId("extensions-diagnostics"));
    const diags = getByTestId("extensions-diagnostics").textContent ?? "";
    expect(diags).toContain("skill_too_large");
    expect(diags).not.toContain("project_scope_refused");
    expect(queryByTestId("extensions-project-refusals")).toBeTruthy();
  });

  it("omits the refusal group when there are no floor refusals", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue({
        packs,
        meta_packs: [stockMeta],
        diagnostics: [
          { code: "skill_too_large", message: "big", unit_id: "skills/a" },
        ],
        ok: true,
        conflicts: 0,
      }),
    });
    const { queryByTestId, getByTestId } = render(() => (
      <ExtensionsSettingsPanel
        client={client as never}
        surface="project"
        projectId="proj-1"
      />
    ));
    await waitFor(() => getByTestId("project-extensions-panel"));
    expect(queryByTestId("extensions-project-refusals")).toBeNull();
  });

  it("never renders the project refusal group on the device Settings panel", async () => {
    const client = mockClient({
      getExtensionsCatalog: vi.fn().mockResolvedValue({
        packs,
        meta_packs: [stockMeta],
        diagnostics: [
          {
            code: "project_scope_refused",
            message: "should not appear as a group here",
            unit_id: "policy/WRITE_SCOPE_DENIED",
          },
        ],
        ok: false,
        conflicts: 0,
      }),
    });
    const { queryByTestId, getByTestId } = render(() => (
      <ExtensionsSettingsPanel client={client as never} surface="settings" />
    ));
    fireEvent.click(getByTestId("extensions-tab-packs"));
    await waitFor(() => getByTestId("extensions-diagnostics"));
    expect(queryByTestId("extensions-project-refusals")).toBeNull();
    expect(getByTestId("extensions-diagnostics").textContent).toContain(
      "project_scope_refused",
    );
  });
});
