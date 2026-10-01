import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { ScannersSettingsPanel } from "./ScannersSettingsPanel.tsx";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../../settings/security/project-settings-overlay-copy.ts";
import { SCANNERS_SETTINGS_COPY } from "../../../settings/extensions/scanners-settings-copy.ts";
import { SECURITY_SCANNERS_SECTION_LABEL } from "../../../settings/settings-nav-model.ts";

const product = {
  enabled: true,
  merged_from: ["bundled"],
  landed_change_scope: "path_scoped" as const,
  source_verify: "stat" as const,
};

const catalog = {
  scanners: [
    {
      id: "lycaon-sast",
      driver: "bundled",
      categories: ["sast", "security"],
      enabled: true,
      check_ok: true,
      catalog_source: "host" as const,
      rejected: false,
      label: "OpenGrep — bundled rules",
      description: "The OpenGrep engine, shipped as a bundled binary.",
      runtime: { soft_limit_ms: 900000, hard_limit_ms: 7200000, cpu_units: 2, parallelism: 2 },
    },
    {
      id: "lycaon-sca",
      driver: "library",
      categories: ["sca", "security"],
      enabled: true,
      check_ok: true,
      catalog_source: "host" as const,
      rejected: false,
      label: "OSV-Scalibr",
      description: "Google's OSV-Scalibr, linked in as a library.",
    },
    {
      id: "trivy_sca",
      driver: "external",
      categories: ["sca", "security"],
      enabled: false,
      check_ok: false,
      catalog_source: "user" as const,
      rejected: false,
      label: "Trivy — dependencies",
      command_summary: "trivy fs --format sarif {{project_dir}}",
    },
    {
      id: "gitleaks",
      driver: "external",
      categories: ["secret", "security"],
      enabled: false,
      check_ok: false,
      catalog_source: "user" as const,
      rejected: false,
      label: "Gitleaks",
      issues: ["binary_missing"],
      requires_binary: "gitleaks",
    },
  ],
  rejected: [],
  user_scanners_path: "/tmp/scanners.yaml",
};

const knownCatalog = {
  scanners: [
    {
      id: "trivy_sca",
      label: "Trivy — dependencies",
      binary: "trivy",
      categories: ["sca"],
      install: "brew install trivy",
      docs_url: "https://trivy.dev/docs/",
      added: true,
      binary_found: false,
    },
    {
      id: "gitleaks",
      label: "Gitleaks",
      binary: "gitleaks",
      categories: ["secret"],
      install: "brew install gitleaks",
      docs_url: "https://github.com/gitleaks/gitleaks",
      added: true,
      binary_found: false,
    },
    {
      id: "semgrep_sast",
      label: "Semgrep — code",
      binary: "semgrep",
      categories: ["sast"],
      hint: "Reads your rules from .semgrep/.",
      install: "brew install semgrep",
      docs_url: "https://semgrep.dev/docs/cli-reference",
      added: false,
      binary_found: true,
    },
  ],
};

function mockClient(overrides: Record<string, unknown> = {}) {
  return {
    getSecurityScannersSettings: vi.fn().mockResolvedValue(product),
    updateSecurityScannersSettings: vi.fn().mockImplementation(async (body) => body),
    listScanners: vi.fn().mockResolvedValue(catalog),
    listProjectScanners: vi.fn().mockResolvedValue(catalog),
    listScannerCatalog: vi.fn().mockResolvedValue(knownCatalog),
    checkScanners: vi.fn().mockResolvedValue({
      rows: [
        { scanner_id: "trivy_sca", ok: false, binary_found: false, detail: "trivy is not on PATH" },
      ],
    }),
    createScanner: vi.fn().mockResolvedValue(catalog.scanners[1]),
    deleteScanner: vi.fn().mockResolvedValue(undefined),
    replaceScannerSlot: vi.fn().mockResolvedValue(catalog),
    updateProjectScanner: vi.fn().mockResolvedValue(catalog.scanners[0]),
    ...overrides,
  };
}

function chooseValue(group: HTMLElement, value: string): void {
  const option = [...group.querySelectorAll("[data-value]")].find(
    (candidate) => candidate.getAttribute("data-value") === value,
  );
  if (!option) throw new Error(`Missing option: ${value}`);
  fireEvent.click(option);
}

describe("ScannersSettingsPanel", () => {
  it("shows one permanent row per job with the tool doing it", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() =>
      expect(getByTestId("scanners-selected-sast")).toBeTruthy(),
    );
    expect(getByTestId("scanners-threat-banner").textContent).toContain(
      SCANNERS_SETTINGS_COPY.threatBanner.slice(0, 20),
    );
    expect(getByTestId("scanners-main")).toBeTruthy();

    // Every job remains visible.
    for (const slot of ["sast", "sca", "secret"]) {
      expect(getByTestId(`scanners-slot-${slot}`)).toBeTruthy();
      expect(getByTestId(`scanners-change-${slot}`)).toBeTruthy();
    }

    // Rows show display labels.
    expect(getByTestId("scanners-selected-sast").textContent).toBe(
      "OpenGrep — bundled rules",
    );
    expect(getByTestId("scanners-runtime-lycaon-sast").textContent).toContain("120m max");
    expect(getByTestId("scanners-selected-sca").textContent).toBe(
      "OSV-Scalibr",
    );
    expect(getByTestId("scanners-path-hint").textContent).toContain(
      "/tmp/scanners.yaml",
    );
  });

  it("persists the landed change scope", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() =>
      expect(getByTestId("scanners-landed-change-scope")).toBeTruthy(),
    );
    expect(getByTestId("scanners-landed-change-row").textContent).toContain(
      SCANNERS_SETTINGS_COPY.landedChangeLabel,
    );
    expect(getByTestId("scanners-landed-change-row").textContent).toContain(
      SCANNERS_SETTINGS_COPY.landedChangeHint.slice(0, 24),
    );
    const select = getByTestId("scanners-landed-change-scope");
    expect(select.textContent).toContain(SCANNERS_SETTINGS_COPY.landedChangePathScoped);
    chooseValue(select, "full_root");
    await waitFor(() => {
      expect(client.updateSecurityScannersSettings).toHaveBeenCalledWith(
        expect.objectContaining({ landed_change_scope: "full_root" }),
      );
    });
  });

  it("offers the source verify escape hatch and keeps the other settings", async () => {
    const client = mockClient({});
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("scanners-source-verify")).toBeTruthy());
    expect(getByTestId("scanners-source-verify-row").textContent).toContain(
      SCANNERS_SETTINGS_COPY.sourceVerifyLabel,
    );
    const select = getByTestId("scanners-source-verify");
    expect(select.textContent).toContain(SCANNERS_SETTINGS_COPY.sourceVerifyStat);
    chooseValue(select, "content");
    await waitFor(() => {
      expect(client.updateSecurityScannersSettings).toHaveBeenCalledWith(
        expect.objectContaining({
          source_verify: "content",
          landed_change_scope: "path_scoped",
          enabled: true,
        }),
      );
    });
  });

  it("locks the scanner options while Security scanners main is off", async () => {
    const client = mockClient({
      getSecurityScannersSettings: vi.fn().mockResolvedValue({
        ...product,
        enabled: false,
      }),
    });
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));
    await waitFor(() => expect(getByTestId("scanners-main")).toBeTruthy());
    const group = getByTestId("scanners-landed-change-scope").closest("fieldset");
    expect(group?.disabled).toBe(true);
    expect(group?.contains(getByTestId("scanners-source-verify"))).toBe(true);
  });

  it("never renders a raw scanner id as the tool name", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() =>
      expect(getByTestId("scanners-selected-sast")).toBeTruthy(),
    );
    for (const slot of ["sast", "sca"]) {
      expect(getByTestId(`scanners-selected-${slot}`).textContent).not.toMatch(
        /lycaon/i,
      );
    }
  });

  it("adds an external scanner then enables it, scoped to one slot", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("scanners-change-sast")).toBeTruthy());
    fireEvent.click(getByTestId("scanners-change-sast"));

    await waitFor(() =>
      expect(screen.getByTestId("scanners-add-dialog")).toBeTruthy(),
    );
    expect(screen.queryByTestId("scanners-add-option-trivy_sca")).toBeNull();
    expect(
      (screen.getByTestId("scanners-add-option-lycaon-sast") as HTMLButtonElement)
        .disabled,
    ).toBe(true);

    fireEvent.click(screen.getByTestId("scanners-add-option-semgrep_sast"));
    await waitFor(() => {
      // Add missing tools before selecting them.
      expect(client.createScanner).toHaveBeenCalledWith({ source: "catalog", catalog_id: "semgrep_sast" });
      expect(client.replaceScannerSlot).toHaveBeenCalledWith(
        "sast",
        "semgrep_sast",
      );
    });
  });

  it("names the engine behind every option, ours included", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("scanners-change-sast")).toBeTruthy());
    fireEvent.click(getByTestId("scanners-change-sast"));

    await waitFor(() =>
      expect(screen.getByTestId("scanners-add-option-lycaon-sast")).toBeTruthy(),
    );
    expect(screen.getByTestId("scanners-detail-lycaon-sast").textContent).toContain(
      "OpenGrep engine",
    );
    expect(screen.getByTestId("scanners-detail-semgrep_sast").textContent).toContain(
      ".semgrep/",
    );
  });

  it("swaps an already-added tool without adding it twice", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("scanners-change-sca")).toBeTruthy());
    fireEvent.click(getByTestId("scanners-change-sca"));
    await waitFor(() =>
      expect(screen.getByTestId("scanners-add-option-trivy_sca")).toBeTruthy(),
    );

    fireEvent.click(screen.getByTestId("scanners-add-option-trivy_sca"));
    await waitFor(() => {
      expect(client.replaceScannerSlot).toHaveBeenCalledWith("sca", "trivy_sca");
    });
    expect(client.createScanner).not.toHaveBeenCalled();
  });

  it("cannot choose a tool that is not installed, and shows how to get it", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("scanners-change-secret")).toBeTruthy());
    fireEvent.click(getByTestId("scanners-change-secret"));

    await waitFor(() =>
      expect(screen.getByTestId("scanners-add-option-gitleaks")).toBeTruthy(),
    );
    expect(
      (screen.getByTestId("scanners-add-option-gitleaks") as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    expect(screen.getByTestId("scanners-install-gitleaks").textContent).toContain(
      "brew install gitleaks",
    );
  });

  it("checks installs and reports why a scanner does not work", async () => {
    // Slot an external tool for the install check.
    const swapped = {
      ...catalog,
      scanners: catalog.scanners.map((s) =>
        s.id === "trivy_sca"
          ? { ...s, enabled: true }
          : s.id === "lycaon-sca"
            ? { ...s, enabled: false }
            : s,
      ),
    };
    const client = mockClient({
      listScanners: vi.fn().mockResolvedValue(swapped),
    });
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => {
      expect(
        (getByTestId("scanners-check-installs") as HTMLButtonElement).disabled,
      ).toBe(false);
    });
    fireEvent.click(getByTestId("scanners-check-installs"));

    await waitFor(() => {
      expect(client.checkScanners).toHaveBeenCalled();
    });
    await waitFor(() => {
      const chip = getByTestId("scanners-install-status-trivy_sca");
      expect(chip.textContent).toBe(SCANNERS_SETTINGS_COPY.installFailed);
      expect(chip.getAttribute("data-tip")).toContain("trivy is not on PATH");
    });
  });

  it("allows a custom scanner for tools not in the catalog", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("scanners-change-sast")).toBeTruthy());
    fireEvent.click(getByTestId("scanners-change-sast"));
    await waitFor(() =>
      expect(screen.getByTestId("scanners-add-custom")).toBeTruthy(),
    );
    fireEvent.click(screen.getByTestId("scanners-add-custom"));

    await waitFor(() =>
      expect(screen.getByTestId("scanners-custom-dialog")).toBeTruthy(),
    );
    fireEvent.input(screen.getByTestId("scanners-custom-id"), {
      target: { value: "my-ext" },
    });
    fireEvent.input(screen.getByTestId("scanners-custom-command"), {
      target: { value: "acme-scan --sarif" },
    });
    fireEvent.click(screen.getByTestId("scanners-custom-submit"));

    await waitFor(() => {
      expect(client.createScanner).toHaveBeenCalledWith(
        expect.objectContaining({
          id: "my-ext",
          command: ["acme-scan", "--sarif"],
          output_parser: "sarif",
          // New tools inherit the open job.
          categories: ["sast"],
          runtime: { soft_limit_ms: 900000, hard_limit_ms: 0, cpu_units: 1, parallelism: 1 },
        }),
      );
    });
  });

  it("rejects an invalid custom runtime before calling the host", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel client={client as never} />
    ));

    await waitFor(() => expect(getByTestId("scanners-change-sast")).toBeTruthy());
    fireEvent.click(getByTestId("scanners-change-sast"));
    await waitFor(() => expect(screen.getByTestId("scanners-add-custom")).toBeTruthy());
    fireEvent.click(screen.getByTestId("scanners-add-custom"));
    await waitFor(() => expect(screen.getByTestId("scanners-custom-dialog")).toBeTruthy());
    fireEvent.input(screen.getByTestId("scanners-custom-id"), { target: { value: "my-ext" } });
    fireEvent.input(screen.getByTestId("scanners-custom-command"), { target: { value: "acme-scan" } });
    fireEvent.input(screen.getByTestId("scanners-custom-hard-limit"), { target: { value: "10" } });
    fireEvent.click(screen.getByTestId("scanners-custom-submit"));

    expect(client.createScanner).not.toHaveBeenCalled();
    await waitFor(() => {
      expect(screen.getByTestId("scanners-custom-error").textContent).toContain("whole positive numbers");
    });
  });

  it("follows Settings by default on the project surface", async () => {
    const client = mockClient();
    const counterpartLabel = PROJECT_SETTINGS_OVERLAY_COPY.openInSettings(
      SECURITY_SCANNERS_SECTION_LABEL,
    );
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel
        client={client as never}
        projectId="p1"
        alwaysProjectScope
        counterpartLabel={counterpartLabel}
        onOpenCounterpart={() => {}}
      />
    ));

    await waitFor(() =>
      expect(getByTestId("project-override-summary").textContent).toContain(
        "OpenGrep — bundled rules",
      ),
    );
    expect(
      getByTestId("project-settings-override").getAttribute("data-enabled"),
    ).toBe("false");
    expect(getByTestId("settings-scope-counterpart").textContent).toContain(
      counterpartLabel,
    );
    expect(() => getByTestId("scanners-main")).toThrow();
    expect(() => getByTestId("scanners-list")).toThrow();
  });

  it("reveals the jobs list once the project overrides Settings", async () => {
    const overridden = {
      ...catalog,
      scanners: catalog.scanners.map((s) => ({
        ...s,
        catalog_source: "project" as const,
      })),
    };
    const client = mockClient({
      listProjectScanners: vi.fn().mockResolvedValue(overridden),
    });
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel
        client={client as never}
        projectId="p1"
        alwaysProjectScope
      />
    ));

    await waitFor(() => expect(getByTestId("scanners-list")).toBeTruthy());
    expect(getByTestId("scanners-selected-sast")).toBeTruthy();
  });

  it("seeds the project overlay when the override is turned on", async () => {
    const client = mockClient();
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel
        client={client as never}
        projectId="p1"
        alwaysProjectScope
      />
    ));

    await waitFor(() =>
      expect(
        (getByTestId("project-override-toggle") as HTMLInputElement).disabled,
      ).toBe(false),
    );
    fireEvent.click(getByTestId("project-override-toggle"));

    await waitFor(() => {
      // Seed the override from resolved project settings.
      expect(client.updateProjectScanner).toHaveBeenCalledWith(
        "p1",
        "lycaon-sast",
        { enabled: true },
      );
      expect(client.updateProjectScanner).toHaveBeenCalledWith(
        "p1",
        "gitleaks",
        { enabled: false },
      );
    });
  });

  it("clears every project row when the override is turned off", async () => {
    const overridden = {
      ...catalog,
      scanners: catalog.scanners.map((s) => ({
        ...s,
        catalog_source: "project" as const,
      })),
    };
    const client = mockClient({
      listProjectScanners: vi.fn().mockResolvedValue(overridden),
    });
    const { getByTestId } = render(() => (
      <ScannersSettingsPanel
        client={client as never}
        projectId="p1"
        alwaysProjectScope
      />
    ));

    await waitFor(() =>
      expect(
        getByTestId("project-settings-override").getAttribute("data-enabled"),
      ).toBe("true"),
    );
    await waitFor(() =>
      expect(
        (getByTestId("project-override-toggle") as HTMLInputElement).disabled,
      ).toBe(false),
    );
    fireEvent.click(getByTestId("project-override-toggle"));

    await waitFor(() => {
      expect(client.updateProjectScanner).toHaveBeenCalledWith(
        "p1",
        "lycaon-sast",
        { enabled: false },
      );
    });
  });
});
