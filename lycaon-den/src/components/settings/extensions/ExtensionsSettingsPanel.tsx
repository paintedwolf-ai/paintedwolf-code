import { createExtensionActions } from "./extension-actions.ts";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { SurfaceDeck } from "../../primitives/SurfaceDeck.tsx";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { ThemeIcon } from "../../primitives/ThemeIcon.tsx";
import { For, Show, Switch, Match, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ExtensionsWriteTarget } from "../../../api/http-capabilities/extensions.ts";
import type {
  ExtensionDiagnostic,
  ExtensionMetaPackSummary,
  ExtensionPackSummary,
  ExtensionUnitDetail,
  ExtensionUnitSummary,
  ExtensionsDesiredScope,
} from "../../../api/types.ts";
import { packRowChips, unitStatusChip, unitViaLabel, type ExtensionChip } from "../../../settings/extensions/extensions-chips.ts";
import {
  groupPacksByMeta,
  metaPackStatusChips,
  metaPackIdSafe,
  metaPackStatusChip,
} from "../../../settings/extensions/extensions-meta.ts";
import { isProjectExtensionRefusal, projectExtensionRefusalDiagnostics } from "../../../settings/security/trust-copy.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { EXTENSIONS_SETTINGS_COPY as C } from "../../../settings/extensions/extensions-settings-copy.ts";
import { formatSentenceCase } from "../../../format/format-sentence-case.ts";
import {
  rememberExtensionsTab,
  rememberedExtensionsTab,
  type ExtensionsSurface,
  type ExtensionsTab,
} from "../../../settings/extensions/extensions-tab.ts";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { SettingsEditorTitle } from "../SettingsEditorTitle.tsx";
import { SettingsListBackChrome } from "../SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { SettingsListGroup } from "../SettingsListGroup.tsx";
import { ListSurfaceInbox } from "../../list/ListSurfaceInbox.tsx";
import { ExtensionsConfigurationPanel } from "./ExtensionsConfigurationPanel.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { UnderlineTabs } from "../UnderlineTabs.tsx";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import { contributionFrame } from "../../../contributions/contribution-store.ts";
import { paginateSlice } from "../../../list/pagination.ts";
import { TablePager } from "../../list/TablePager.tsx";

type Props = {
  client: LycaonClient;
  surface: ExtensionsSurface;
  projectId?: string;
  /** Bumped by the extensions settings facet; triggers a reload. */
  extensionsRevision?: () => number;
  onOpenScanners?: () => void;
  /** Reports initial catalog readiness. */
  onReadyChange?: (ready: boolean) => void;
};

function extTestId(prefix: string, id: string): string {
  return `${prefix}-${encodeURIComponent(id)}`;
}

export const EXTENSION_UNITS_PAGE_SIZE = 50;

function ExtensionsIcon() {
  return (
    <ThemeIcon slot="stage-extensions" size={18} />
  );
}

const CHIP_CLASS: Record<ExtensionChip["className"], string> = {
  stock: "den-ext-chip-stock",
  add: "den-ext-chip-add",
  selected: "den-ext-chip-selected",
  disabled: "den-ext-chip-disabled",
  conflict: "den-ext-chip-conflict",
  profile: "den-ext-chip-profile",
  feature: "den-ext-chip-feature",
  platform: "den-ext-chip-platform",
  security: "den-ext-chip-security",
  scanner: "den-ext-chip-scanner",
  invalid: "den-ext-chip-invalid",
  integrity: "den-ext-chip-integrity",
  reload: "den-ext-chip-reload",
  warning: "den-ext-chip-warning",
};

function Chip(props: {
  chip: ExtensionChip;
  onOpenScanners?: () => void;
  onReloadFromDisk?: () => void;
}) {
  const clickable =
    (props.chip.className === "scanner" && props.onOpenScanners) ||
    (props.chip.className === "reload" && props.onReloadFromDisk);
  if (clickable) {
    return (
      <button
        type="button"
        class={`den-ext-chip ${CHIP_CLASS[props.chip.className]}`}
        data-testid={`ext-chip-${props.chip.className}`}
        onClick={() => {
          if (props.chip.className === "reload") {
            props.onReloadFromDisk?.();
            return;
          }
          props.onOpenScanners?.();
        }}
      >
        {props.chip.label}
      </button>
    );
  }
  return (
    <span
      class={`den-ext-chip ${CHIP_CLASS[props.chip.className]}`}
      data-testid={`ext-chip-${props.chip.className}`}
    >
      {props.chip.label}
    </span>
  );
}

function packSourceLabel(pack: ExtensionPackSummary): string {
  const base = pack.source || pack.id;
  const selector = pack.ref || pack.version_constraint;
  let line = `${base}${selector ? `@${selector}` : ""} · v${pack.version}`;
  if (pack.installed_from) {
    line += ` · installed from ${pack.installed_from}`;
  }
  if ((pack.referenced_by ?? 0) > 1) {
    line += ` · still referenced by ${pack.referenced_by} projects`;
  }
  return line;
}

function countLabel(n: number, singular: string, plural: string): string {
  return n === 1 ? `1 ${singular}` : `${n} ${plural}`;
}

const KIND_FILTERS: { id: string; label: string }[] = [
  { id: "all", label: C.kindAll },
  { id: "guidance", label: C.kindGuidance },
  { id: "policy", label: C.kindPolicy },
  { id: "workflows", label: C.kindWorkflows },
  { id: "agents", label: C.kindAgents },
  { id: "skills", label: C.kindSkill },
];

const KIND_LABELS: Record<string, string> = {
  guidance: C.kindGuidance,
  policy: C.kindPolicy,
  workflows: C.kindWorkflows,
  agents: C.kindAgents,
  skills: C.kindSkill,
};

function unitKindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind;
}

export function ExtensionsSettingsPanel(props: Props) {
  const surface = (): ExtensionsSurface => props.surface;
  const isProject = () => surface() === "project";
  const [tab, setTabState] = createSignal<ExtensionsTab>(
    rememberedExtensionsTab(surface()),
  );
  const setTab = (next: ExtensionsTab) => {
    rememberExtensionsTab(surface(), next);
    setTabState(next);
  };
  const projectId = () => isProject() ? props.projectId?.trim() || undefined : undefined;
  const catalogQuery = createSurfaceQuery({
    name: "extensions-catalog",
    source: () => {
      const pid = projectId();
      return isProject() && !pid ? null : { client: props.client, projectId: pid,
        key: pid ?? "device", revision: props.extensionsRevision?.() };
    },
    scope: (source) => source.projectId ?? "device",
    load: ({ client, projectId }) => client.getExtensionsCatalog(projectId),
  });
  createEffect((prev) => {
    const rev = props.extensionsRevision?.();
    if (prev !== undefined && rev !== prev) {
      void catalogQuery.refresh();
    }
    return rev;
  });
  const packs = () => catalogQuery.value()?.packs ?? [];
  const metaPacks = () => catalogQuery.value()?.meta_packs ?? [];
  const diagnostics = () => catalogQuery.value()?.diagnostics ?? [];
  const units = () => catalogQuery.value()?.units ?? [];
  const desired = () => catalogQuery.value()?.desired;
  const stateRevision = () => catalogQuery.value()?.revision ?? "";
  const {
    actionError,
    setActionError,
    actionStatus,
    setActionStatus,
    busy,
    addOpen,
    setAddOpen,
    addUrl,
    setAddUrl,
    addRef,
    setAddRef,
    addVersion,
    setAddVersion,
    addError,
    updatePlan,
    suiteInstallOpen,
    setSuiteInstallOpen,
    suiteInstallUrl,
    setSuiteInstallUrl,
    suiteInstallRef,
    setSuiteInstallRef,
    suiteInstallVersion,
    setSuiteInstallVersion,
    suiteInstallError,
    profileOpen,
    setProfileOpen,
    profilePackId,
    setProfilePackId,
    profileName,
    setProfileName,
    profileError,
    catchNotice,
    copyNotice,
    togglePack,
    removePack,
    beginUpdate,
    confirmUpdate,
    reloadPack,
    installFromGit,
    installFromFolder,
    openSuiteInstallDialog,
    installSuite,
    enableSuite,
    disableSuite,
    removeSuite,
    applyProfile,
    openAddDialog,
    openProfileDialog,
    setUnitDisabled,
    setUnitOwn,
  } = createExtensionActions({
    client: () => props.client,
    captureCatalog: () => catalogQuery.capture(),
    stateRevision,
    selectedPackId: () => selectedPackId(),
    setSelectedPackId: (value) => setSelectedPackId(value),
    setDetailRevision: (update) => setDetailRevision(update),
    profilePacks: () => profilePacks(),
    isProject,
    unitWriteTarget: () => unitWriteTarget(),
  });
  const [selectedPackId, setSelectedPackId] = createSignal<string | undefined>();
  const [selectedUnitId, setSelectedUnitId] = createSignal<string | undefined>();
  const [unitDetail, setUnitDetail] = createSignal<ExtensionUnitDetail | undefined>();
  const [detailRevision, setDetailRevision] = createSignal(0);
  const [kindFilter, setKindFilter] = createSignal<string>("all");
  const [unitQuery, setUnitQuery] = createSignal("");
  const [unitPage, setUnitPage] = createSignal(0);
  const actionsDisabled = () => busy() || loading() || !stateRevision();
  let addDialogRef: HTMLDivElement | undefined;
  let suiteDialogRef: HTMLDivElement | undefined;
  let profileDialogRef: HTMLDivElement | undefined;
  let unitDetailRevision = 0;

  createModalFocusTrap(addOpen, () => addDialogRef, {
    onEscape: () => {
      if (!busy()) setAddOpen(false);
    },
  });
  createModalFocusTrap(suiteInstallOpen, () => suiteDialogRef, {
    onEscape: () => {
      if (!busy()) setSuiteInstallOpen(false);
    },
  });
  createModalFocusTrap(profileOpen, () => profileDialogRef, {
    onEscape: () => {
      if (!busy()) setProfileOpen(false);
    },
  });

  const scope = (): ExtensionsDesiredScope =>
    isProject() ? "project" : "device";
  const unitWriteTarget = (): ExtensionsWriteTarget => ({
	  scope: scope(),
	  projectId: projectId(),
  });

  const packKinds = createMemo(() => {
    const map: Record<string, ExtensionPackSummary["kind"]> = {};
    for (const p of packs()) map[p.id] = p.kind;
    return map;
  });

  const packNames = createMemo(() => {
    const map: Record<string, string> = {};
    for (const p of packs()) map[p.id] = p.name;
    return map;
  });

  const selectedPack = createMemo(() =>
    packs().find((p) => p.id === selectedPackId()),
  );

  const profilePacks = createMemo(() => packs().filter((p) => p.has_profile));

  const packGrouping = createMemo(() =>
    groupPacksByMeta(packs(), metaPacks()),
  );

  const packProvides = createMemo(() => {
    const packId = selectedPackId();
    if (!packId) return [] as ExtensionUnitSummary[];
    return units()
      .filter((u) => u.contributions.some((p) => p.pack_id === packId))
      .slice()
      .sort((a, b) => a.id.localeCompare(b.id));
  });

  const projectRefusals = createMemo(() =>
    isProject() ? projectExtensionRefusalDiagnostics(diagnostics()) : [],
  );

  // The global banner contains only diagnostics without a pack.
  const panelDiagnostics = createMemo(() => {
    const rows = isProject()
      ? diagnostics().filter((d) => !isProjectExtensionRefusal(d.code))
      : diagnostics();
    return rows.filter((d) => !d.pack_id);
  });

  const diagnosticsForPack = (packId: string): ExtensionDiagnostic[] =>
    diagnostics().filter((d) => d.pack_id === packId);

  const contributionRuntimeRows = createMemo(() => {
    const frame = contributionFrame();
    if (!frame) return [];
    const rows: Array<{ code: string; message: string }> = [];
    for (const note of frame.notes) {
      rows.push({ code: note.code, message: note.message });
    }
    for (const requirement of frame.requirements) {
      // Requirement ids identify catalog positions, not external identities.
      const server = `the server installed as ${requirement.provider_id}`;
      if (requirement.ready) {
        rows.push({
          code: "requirement_ready",
          message: `${requirement.id} may send data to ${server}`,
        });
        continue;
      }
      rows.push({
        code: requirement.reason ?? "requirement_not_ready",
        message: `${requirement.id} (${server}) is not ready`,
      });
    }
    return rows;
  });

  const loading = catalogQuery.loading;
  const loadError = () => isProject() && !projectId() ? copyNotice(C.projectRequired)
    : catalogQuery.error() ? catchNotice(catalogQuery.cause(), C.loadError) : undefined;
  const reload = catalogQuery.refresh;
  createEffect(() => {
    const view = catalogQuery.value();
    if (!view) return;
    if (selectedPackId() && !(view.packs ?? []).some((pack) => pack.id === selectedPackId())) setSelectedPackId(undefined);
    if (selectedUnitId() && !(view.units ?? []).some((unit) => unit.id === selectedUnitId())) setSelectedUnitId(undefined);
  });
  createEffect(() => props.onReadyChange?.(catalogQuery.ready()));

  const filteredUnits = createMemo(() => {
    let list = units();
    const kind = kindFilter();
    if (kind !== "all") {
      list = list.filter((u) => u.kind === kind);
    }
    const q = unitQuery().trim().toLowerCase();
    if (q) {
      list = list.filter((u) =>
        `${u.id} ${u.title ?? ""}`.toLowerCase().includes(q),
      );
    }
    return list;
  });

  const pagedUnits = createMemo(() =>
    paginateSlice(filteredUnits(), unitPage(), EXTENSION_UNITS_PAGE_SIZE),
  );

  createEffect(() => {
    const page = pagedUnits().page;
    if (page !== unitPage()) setUnitPage(page);
  });

  const selectKindFilter = (kind: string) => {
    setKindFilter(kind);
    setUnitPage(0);
  };

  const updateUnitQuery = (query: string) => {
    setUnitQuery(query);
    setUnitPage(0);
  };

  createEffect(() => {
    const id = selectedUnitId();
    const pid = projectId();
    props.client;
    detailRevision();
    const revision = ++unitDetailRevision;
    if (!id) {
      setUnitDetail(undefined);
      return;
    }
    void props.client
      .getExtensionUnit(id, pid)
      .then((detail) => {
        if (revision === unitDetailRevision) setUnitDetail(detail);
      })
      .catch((err) => {
        if (revision === unitDetailRevision) {
          setActionError(catchNotice(err, C.actionError));
          setUnitDetail(undefined);
        }
      });
  });

  const selectPack = (packId: string) => {
    setSelectedPackId(packId);
  };

  const closePackDetail = () => {
    setSelectedPackId(undefined);
  };

  const selectUnit = (unitId: string) => {
    setSelectedUnitId(unitId);
  };

  const closeUnitDetail = () => {
    setSelectedUnitId(undefined);
  };

  const tabs = createMemo((): { id: ExtensionsTab; label: string }[] =>
    isProject()
      ? [
          { id: "units", label: C.tabUnits },
          { id: "desired", label: C.tabDesired },
        ]
      : [
          { id: "model", label: C.tabModel },
          { id: "packs", label: C.tabPacks },
          { id: "units", label: C.tabUnits },
          { id: "settings", label: C.tabSettings },
        ],
  );

  createEffect(() => {
    const allowed = new Set(tabs().map((t) => t.id));
    if (!allowed.has(tab())) {
      setTab(isProject() ? "units" : "model");
    }
  });

  const packDetailOpen = () => selectedPack() != null;
  const unitDetailOpen = () => selectedUnitId() != null && unitDetail() != null;

  const renderPackRow = (pack: ExtensionPackSummary) => {
    const chips = () => packRowChips(pack, diagnosticsForPack(pack.id));
    return (
      <SettingsListRow
        testId={extTestId("extensions-pack", pack.id)}
        leading={
          <DenCheckbox
            checked={pack.enabled}
            disabled={actionsDisabled()}
            data-testid={extTestId("extensions-pack-enable", pack.id)}
            aria-label={`Enable ${pack.name}`}
            onChange={(e) =>
              togglePack(pack, e.currentTarget.checked, e.currentTarget)
            }
          />
        }
        primary={pack.name}
        secondary={packSourceLabel(pack)}
        status={
          <span class="den-ext-row-status">
            <For each={chips()}>
              {(chip) => (
                <Chip
                  chip={chip}
                  onOpenScanners={props.onOpenScanners}
                  onReloadFromDisk={
                    pack.kind === "path" && pack.needs_reload
                      ? () => reloadPack(pack)
                      : undefined
                  }
                />
              )}
            </For>
            <span class="den-ext-row-units">
              {countLabel(pack.unit_count, "item", "items")}
            </span>
          </span>
        }
        onSelect={() => selectPack(pack.id)}
      />
    );
  };

  const renderMetaSection = (
    meta: ExtensionMetaPackSummary,
    sectionPacks: ExtensionPackSummary[],
  ) => {
    const safe = metaPackIdSafe(meta.id);
    const statusChip = metaPackStatusChip(meta.status);
    const statusChips = metaPackStatusChips(meta);
    return (
      <SettingsListGroup
        testId={`extensions-meta-section-${safe}`}
        label={meta.name}
        meta={`v${meta.version}`}
        action={
          <>
            <Chip chip={statusChip} />
            <For each={statusChips}>{(chip) => <Chip chip={chip} />}</For>
            <DenButton
              variant="ghost"
              compact
              data-testid={`extensions-meta-enable-${safe}`}
              disabled={actionsDisabled()}
              onClick={() => enableSuite(meta.id)}
            >
              {C.suiteEnable}
            </DenButton>
            <DenButton
              variant="ghost"
              compact
              data-testid={`extensions-meta-disable-${safe}`}
              disabled={actionsDisabled() || !meta.removable}
              data-tip={!meta.removable ? C.suiteDisableStock : undefined}
              onClick={() => disableSuite(meta.id)}
            >
              {C.suiteDisable}
            </DenButton>
            <Show when={meta.removable}>
              <DenButton
                variant="ghost"
                compact
                data-testid={`extensions-meta-remove-${safe}`}
                disabled={actionsDisabled()}
                onClick={() => removeSuite(meta)}
              >
                {C.suiteRemove}
              </DenButton>
            </Show>
          </>
        }
      >
        <For each={sectionPacks}>{(pack) => renderPackRow(pack)}</For>
      </SettingsListGroup>
    );
  };

  const renderPackList = () => {
    const grouped = packGrouping();
    return (
      <div class="den-settings-list-stack">
        <For each={grouped.sections}>
          {(section) => renderMetaSection(section.meta, section.packs)}
        </For>
        <Show when={grouped.other.length > 0}>
          <SettingsListGroup
            testId="extensions-meta-section-other-packs"
            label={C.suiteOtherPacks}
          >
            <For each={grouped.other}>{(pack) => renderPackRow(pack)}</For>
          </SettingsListGroup>
        </Show>
      </div>
    );
  };

  const renderUnitRow = (unit: ExtensionUnitSummary) => {
    const statusChip = unitStatusChip({
      status: unit.status,
      contributions: unit.contributions,
      packKinds: packKinds(),
    });
    const via = unitViaLabel({
      status: unit.status,
      contributions: unit.contributions,
      winner_pack_id: unit.winner_pack_id,
      packNames: packNames(),
    });
    return (
      <SettingsListRow
        testId={extTestId("extensions-unit-row", unit.id)}
        variant={unit.status === "conflict" ? "warning" : "default"}
        primary={unit.title || unit.id}
        secondary={
          unit.title
            ? `${unit.id} · ${unitKindLabel(unit.kind)}`
            : unitKindLabel(unit.kind)
        }
        status={
          <span class="den-ext-row-status">
            <Chip chip={statusChip} />
            <span class="den-ext-row-via" data-tip={via} data-tip-when-clipped>
              {via}
            </span>
          </span>
        }
        onSelect={() => selectUnit(unit.id)}
      />
    );
  };

  const renderPackDetail = (pack: ExtensionPackSummary) => (
    <article
      class="den-settings-provider-card"
      data-testid="extensions-pack-detail"
    >
      <header class="den-settings-provider-head">
        <strong>{pack.name}</strong>
        <For each={packRowChips(pack, diagnosticsForPack(pack.id))}>
          {(chip) => (
            <Chip
              chip={chip}
              onOpenScanners={props.onOpenScanners}
              onReloadFromDisk={
                pack.kind === "path" && pack.needs_reload
                  ? () => reloadPack(pack)
                  : undefined
              }
            />
          )}
        </For>
        <Show when={pack.kind === "git" && pack.removable}>
          <DenButton
            variant="secondary"
            compact
            disabled={actionsDisabled()}
            data-testid="extensions-update-pack"
            onClick={() => beginUpdate(pack)}
          >
            {C.updatePack}
          </DenButton>
        </Show>
        <Show when={pack.kind === "path" && pack.removable}>
          <DenButton
            variant="secondary"
            compact
            disabled={actionsDisabled()}
            data-testid="extensions-reload-pack"
            onClick={() => reloadPack(pack)}
          >
            {C.reloadFromDisk}
          </DenButton>
        </Show>
        <DenButton
          variant="link"
          class="den-settings-provider-remove"
          disabled={actionsDisabled() || !pack.removable}
          data-tip={!pack.removable ? C.removeDisabledStock : undefined}
          data-testid="extensions-remove-pack"
          onClick={() => removePack(pack)}
        >
          {C.remove}
        </DenButton>
      </header>
      <p class="den-settings-hint">{packSourceLabel(pack)}</p>
      <dl class="den-ext-version-facts" data-testid="extensions-pack-version-facts">
        <div><dt>{C.version}</dt><dd><code>{pack.version}</code></dd></div>
        <div><dt>{C.installationState}</dt><dd>{pack.installation_state} · {pack.installation_scope}</dd></div>
        <div><dt>{C.extensionApi}</dt><dd><code>{pack.extension_api}</code></dd></div>
        <Show when={pack.resolved_revision}>
          <div><dt>{C.revision}</dt><dd><code>{pack.resolved_revision}</code></dd></div>
        </Show>
        <Show when={pack.integrity}>
          <div><dt>{C.integrity}</dt><dd><code>{pack.integrity}</code></dd></div>
        </Show>
      </dl>
      <Show when={diagnosticsForPack(pack.id).length > 0}>
        <h4 class="den-ext-detail-section">{C.diagnosticsHeading}</h4>
        <ul class="den-ext-provides" data-testid="extensions-pack-diagnostics">
          <For each={diagnosticsForPack(pack.id)}>
            {(d) => (
              <li>
                <span class={`den-ext-diagnostic-severity den-ext-diagnostic-severity-${d.severity}`}>
                  {formatSentenceCase(d.severity)}
                </span>{" "}
                {d.message}
                <Show when={d.unit_id}> — <code>{d.unit_id}</code></Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
      <Show when={Object.entries(pack.dependencies ?? {}).length > 0}>
        <h4 class="den-ext-detail-section">{C.dependencies}</h4>
        <ul class="den-ext-provides" data-testid="extensions-pack-dependencies">
          <For each={Object.entries(pack.dependencies ?? {}).sort(([a], [b]) => a.localeCompare(b))}>
            {([id, constraint]) => <li><code>{id}</code> <span>{constraint}</span></li>}
          </For>
        </ul>
      </Show>
      <Show when={updatePlan()[pack.id]} keyed>
        {(status) => (
          <div data-testid="extensions-update-plan">
            <p class="den-settings-hint">{status.message}</p>
            <ul class="den-ext-provides">
              <For each={status.changes}>
                {(change) => (
                  <li><code>{change.pack_id}</code> — {change.kind}{change.candidate_version ? ` → ${change.candidate_version}` : ""}</li>
                )}
              </For>
            </ul>
            <Show when={status.available}>
              <DenButton
                variant="primary"
                compact
                disabled={actionsDisabled()}
                data-testid="extensions-update-confirm"
                onClick={() => confirmUpdate(pack)}
              >
                {C.confirmUpdate}
              </DenButton>
            </Show>
          </div>
        )}
      </Show>
      <Show
        when={
          (pack.unmet_requires_scanners?.length ?? 0) > 0
            ? pack.unmet_requires_scanners
            : null
        }
        keyed
      >
        {(scannerIds) => (
          <div class="den-ext-mcp-callout" data-testid="extensions-needs-scanner">
            <p class="den-ext-mcp-callout__title">{C.needsScannerTitle}</p>
            <p class="den-settings-hint">{C.needsScannerHint}</p>
            <ul>
              <For each={scannerIds}>{(id) => <li><code>{id}</code></li>}</For>
            </ul>
            <Show when={props.onOpenScanners}>
              <DenButton
                variant="secondary"
                compact
                data-testid="extensions-open-scanners"
                onClick={() => props.onOpenScanners?.()}
              >
                {C.scannerSettingsLink}
              </DenButton>
            </Show>
          </div>
        )}
      </Show>
      <Show when={pack.needs_reload}>
        <div class="den-ext-mcp-callout" data-testid="extensions-needs-reload">
          <p class="den-ext-mcp-callout__title">{C.packNeedsReloadTitle}</p>
          <p class="den-settings-hint">{C.packNeedsReloadHint}</p>
        </div>
      </Show>
      <Show when={pack.blocked_reason === "invalid"}>
        <div class="den-ext-mcp-callout" data-testid="extensions-pack-invalid">
          <p class="den-ext-mcp-callout__title">{C.packInvalidTitle}</p>
          <p class="den-settings-hint">{C.packInvalidHint}</p>
        </div>
      </Show>
      <Show when={pack.blocked_reason === "integrity"}>
        <div class="den-ext-mcp-callout" data-testid="extensions-pack-integrity">
          <p class="den-ext-mcp-callout__title">{C.packIntegrityTitle}</p>
          <p class="den-settings-hint">{C.packIntegrityHint}</p>
        </div>
      </Show>
      <div>
        <h4 class="den-ext-detail-section">{C.providesHeading}</h4>
        <Show
          when={packProvides().length > 0}
          fallback={<p class="den-settings-hint">{C.emptyProvides}</p>}
        >
          <ul class="den-ext-provides" data-testid="extensions-pack-provides">
            <For each={packProvides()}>
              {(unit) => {
                const label = unit.title?.trim() || unit.id;
                const showId = Boolean(unit.title?.trim());
                return (
                  <li class="den-ext-provides__item">
                    <div class="den-ext-provides__body">
                      <span class="den-ext-provides__title">{label}</span>
                      <Show when={showId}>
                        <span class="den-ext-provides__id">{unit.id}</span>
                      </Show>
                    </div>
                  </li>
                );
              }}
            </For>
          </ul>
        </Show>
      </div>
    </article>
  );

  const renderUnitDetail = (detail: ExtensionUnitDetail) => (
    <article
      class="den-settings-provider-card"
      data-testid="extensions-unit-detail"
    >
      <header class="den-settings-provider-head">
        <strong>{detail.title || detail.id}</strong>
        <Chip
          chip={unitStatusChip({
            status: detail.status,
            contributions: detail.contributions,
            packKinds: packKinds(),
          })}
        />
        <Show when={detail.status === "disabled" && (!isProject() || detail.project_disable_allowed)}>
          <DenButton
            variant="primary"
            compact
            disabled={actionsDisabled()}
            data-testid="extensions-unit-reenable"
            onClick={() => setUnitDisabled(detail.id, false)}
          >
            {C.reEnable}
          </DenButton>
        </Show>
        <Show when={detail.status !== "disabled" && (!isProject() || detail.project_disable_allowed)}>
          <DenButton
            variant="danger"
            compact
            disabled={actionsDisabled()}
            data-testid="extensions-unit-disable"
            onClick={() => setUnitDisabled(detail.id, true)}
          >
            {C.disable}
          </DenButton>
        </Show>
        <Show when={isProject() && !detail.project_disable_allowed}>
          <span class="den-settings-hint">{C.deviceControlledUnit}</span>
        </Show>
        <Show when={!isProject() && detail.contributions.length > 1}>
          <For each={detail.contributions}>
            {(prov) => (
              <DenButton
                variant={
                  detail.winner_pack_id === prov.pack_id
                    ? "primary"
                    : "secondary"
                }
                compact
                disabled={actionsDisabled()}
                data-testid={extTestId("extensions-unit-own", prov.pack_id)}
                onClick={() => setUnitOwn(detail.id, prov.pack_id)}
              >
                {C.selectionPrefix}{" "}
                {packNames()[prov.pack_id] ?? prov.pack_id}
              </DenButton>
            )}
          </For>
          <Show when={detail.status === "owned" || detail.winner_pack_id}>
            <DenButton
              variant="secondary"
              compact
              disabled={actionsDisabled()}
              data-testid="extensions-unit-clear-own"
              onClick={() => setUnitOwn(detail.id, null)}
            >
              {C.clearSelection}
            </DenButton>
          </Show>
        </Show>
      </header>
      <p class="den-settings-hint">
        {detail.id} · {unitKindLabel(detail.kind)}
      </p>

      <div>
        <h4 class="den-ext-detail-section">{C.resolution}</h4>
        <ol class="den-ext-resolution" data-testid="extensions-unit-resolution">
          <For each={detail.contributions}>
            {(prov, i) => {
              const winner =
                detail.winner_pack_id === prov.pack_id &&
                detail.status !== "conflict" &&
                detail.status !== "disabled";
              return (
                <li>
                  <span
                    class="den-ext-resolution-rank"
                    classList={{ "den-ext-resolution-rank--winner": winner }}
                  >
                    {winner ? C.winnerLabel : C.contributionLabel}
                  </span>
                  <span>{packNames()[prov.pack_id] ?? prov.pack_id}</span>
                  <code>{prov.pack_id}</code>
                  <Chip
                    chip={{
                      label:
                        packKinds()[prov.pack_id] === "stock"
                          ? "Stock"
                          : detail.status === "owned" && winner
                            ? "Selected"
                            : "Provided",
                      className:
                        packKinds()[prov.pack_id] === "stock"
                          ? "stock"
                          : detail.status === "owned" && winner
                            ? "selected"
                            : "add",
                    }}
                  />
                  <Show when={i() === detail.contributions.length - 1}>
                    <Show when={detail.status === "disabled"}>
                      <div class="den-ext-resolution-final">
                        <strong>{C.effectiveDisabled}</strong>{" "}
                        {C.effectiveDisabledDetail}{" "}
                        <Chip chip={{ label: "Disabled", className: "disabled" }} />
                      </div>
                    </Show>
                    <Show when={detail.status === "conflict"}>
                      <div class="den-ext-resolution-final">
                        <strong>{C.effectiveBlocked}</strong>{" "}
                        {C.effectiveConflictDetail}{" "}
                        <Chip chip={{ label: "Conflict", className: "conflict" }} />
                      </div>
                    </Show>
                  </Show>
                </li>
              );
            }}
          </For>
        </ol>
      </div>

      <Show when={detail.kind !== "skills"}>
        <div class="den-ext-body-split">
          <div>
            <h4 class="den-ext-detail-section">{C.contributionsPane}</h4>
            <Scrollport
              class="den-ext-body"
              contentAs="pre"
              contentClass="den-ext-body__content"
              axis="both"
              data-testid="extensions-unit-contributions-body"
            >
              {detail.contributions
                .map(
                  (p) =>
                    `── ${p.pack_id} ──\n${p.content ?? "(no body)"}\n`,
                )
                .join("\n")}
            </Scrollport>
          </div>
          <div>
            <h4 class="den-ext-detail-section">{C.effectivePane}</h4>
            <Scrollport
              class="den-ext-body"
              classList={{
                "den-ext-body-disabled": detail.status === "disabled",
                "den-ext-body-conflict": detail.status === "conflict",
              }}
              contentAs="pre"
              contentClass="den-ext-body__content"
              axis="both"
              data-testid="extensions-unit-effective-body"
            >
              {detail.status === "conflict"
                ? C.conflictEffective
                : detail.status === "disabled"
                  ? C.disabledEffective
                  : detail.content || "(empty)"}
            </Scrollport>
          </div>
        </div>
      </Show>
    </article>
  );

  return (
    <div
      class="den-settings-editor"
      data-testid={
        isProject() ? "project-extensions-panel" : "extensions-settings-panel"
      }
      data-surface={surface()}
    >
      <Show when={!isProject()}>
        <SettingsEditorTitle icon={<ExtensionsIcon />}>{C.title}</SettingsEditorTitle>
        <SettingsScopeLede scope="device">
          <p class="den-settings-hint" data-testid="extensions-device-banner">
            {C.settingsBanner}
          </p>
        </SettingsScopeLede>
      </Show>

      <Show when={isProject()}>
        <SettingsScopeLede scope="project">
          <p class="den-settings-hint">{C.projectBanner}</p>
        </SettingsScopeLede>
      </Show>

      <Show when={isProject() && projectRefusals().length > 0}>
        <section
          class="den-ext-project-refusals"
          data-testid="extensions-project-refusals"
          aria-labelledby="extensions-project-refusals-heading"
        >
          <h3
            id="extensions-project-refusals-heading"
            class="den-ext-project-refusals__title"
          >
            {C.notAppliedFromProject}
          </h3>
          <ul class="den-ext-project-refusals__list">
            <For each={projectRefusals()}>
              {(d) => (
                <li
                  class="den-ext-project-refusals__row"
                  data-testid={
                    d.unit_id
                      ? `extensions-project-refusal-${encodeURIComponent(d.unit_id)}`
                      : "extensions-project-refusal"
                  }
                >
                  <Show when={d.unit_id}>
                    <code class="den-ext-project-refusals__unit">{d.unit_id}</code>
                  </Show>
                  <span class="den-ext-project-refusals__reason">{d.message}</span>
                </li>
              )}
            </For>
          </ul>
        </section>
      </Show>

      <InlineNotice notice={loadError()} testId="extensions-load-error" />
      <InlineNotice
        notice={actionError()}
        testId="extensions-action-error"
        action={
          actionError()?.code === "extension_state_changed"
            ? {
                label: C.actionErrorReload,
                testId: "extensions-action-error-reload",
                onClick: () => {
                  setActionError(undefined);
                  void reload();
                },
              }
            : undefined
        }
      />
      <Show when={actionStatus()}>
        <p class="den-settings-hint" data-testid="extensions-action-status" role="status">
          {actionStatus()}
        </p>
      </Show>

      <Show when={panelDiagnostics().length > 0}>
        <ul class="den-ext-diags" data-testid="extensions-diagnostics">
          <For each={panelDiagnostics()}>
            {(d) => (
              <li class="den-settings-hint">
                <code>{d.code}</code>: {d.message}
              </li>
            )}
          </For>
        </ul>
      </Show>

      <UnderlineTabs aria-label="Extensions sections">
        <For each={tabs()}>
          {(t) => (
            <button
              type="button"
              role="tab"
              id={`ext-tab-${t.id}`}
              aria-selected={tab() === t.id}
              aria-controls={`ext-panel-${t.id}`}
              class="den-underline-tab"
              data-active={tab() === t.id}
              data-testid={`extensions-tab-${t.id}`}
              data-first-time-tip-anchor={
                t.id === "units"
                  ? "project-extensions"
                  : undefined
              }
              onClick={() => setTab(t.id)}
            >
              {t.label}
            </button>
          )}
        </For>
      </UnderlineTabs>

      <Show when={catalogQuery.showLoading()}>
        <p class="den-settings-hint">{C.loading}</p>
      </Show>

      <SurfaceDeck active={tab()}>
        {(section) => <Switch>
      <Match when={section === "packs" && !isProject()}>
      <div
        id="ext-panel-packs"
        role="tabpanel"
        aria-labelledby="ext-tab-packs"
        class="den-settings-section"
        data-testid="extensions-panel-packs"
      >
        <SettingsListPanel
            testId="extensions-pack-list-panel"
            chrome={
              packDetailOpen() ? (
                <SettingsListBackChrome
                  testId="extensions-pack-back"
                  label={C.packsBack}
                  onBack={closePackDetail}
                />
              ) : (
                <SettingsListChrome
                  testId="extensions-pack-chrome"
                  count={
                    <Show when={catalogQuery.value() !== undefined}>
                      <span data-testid="extensions-pack-count">
                        {countLabel(packs().length, "pack", "packs")}
                      </span>
                    </Show>
                  }
                  secondaryAction={
                    <>
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid="extensions-install-suite"
                        disabled={actionsDisabled()}
                        onClick={() => openSuiteInstallDialog()}
                      >
                        {C.suiteInstall}
                      </DenButton>
                      <DenButton
                        variant="secondary"
                        compact
                        data-testid="extensions-apply-profile"
                        disabled={actionsDisabled() || profilePacks().length === 0}
                        onClick={() => openProfileDialog()}
                      >
                        {C.applyProfile}
                      </DenButton>
                    </>
                  }
                  action={
                    <>
                      <DenButton
                        variant="ghost"
                        compact
                        data-testid="extensions-add-folder"
                        disabled={actionsDisabled()}
                        onClick={() => installFromFolder()}
                      >
                        {C.addFromFolder}
                      </DenButton>
                      <DenButton
                        variant="primary"
                        compact
                        data-testid="extensions-add-git"
                        disabled={actionsDisabled()}
                        onClick={() => openAddDialog()}
                      >
                        {C.addFromGit}
                      </DenButton>
                    </>
                  }
                />
              )
            }
          >
            <Show when={!packDetailOpen() && contributionRuntimeRows().length > 0}>
              <ul
                class="den-ext-diags"
                data-testid="extensions-contribution-runtime"
              >
                <For each={contributionRuntimeRows()}>
                  {(row) => (
                    <li class="den-settings-hint">
                      <code>{row.code}</code>: {row.message}
                    </li>
                  )}
                </For>
              </ul>
            </Show>

            <ListSurfaceInbox
              detailOpen={packDetailOpen()}
              isEmpty={catalogQuery.value() !== undefined && packs().length === 0}
              listTestId="extensions-pack-list"
              detailTestId="extensions-pack-detail-pane"
              empty={
                <p
                  class="den-settings-hint den-settings-list-inbox__empty"
                  data-testid="extensions-empty-packs"
                >
                  {C.emptyPacks}
                </p>
              }
              list={renderPackList()}
              detail={
                <Show when={selectedPack()} keyed>
                  {(pack) => renderPackDetail(pack)}
                </Show>
              }
            />
        </SettingsListPanel>
      </div>
      </Match>

      <Match when={section === "units"}>
      <div
        id="ext-panel-units"
        role="tabpanel"
        aria-labelledby="ext-tab-units"
        class="den-settings-section"
        data-testid="extensions-panel-units"
      >
        <Show when={!unitDetailOpen()}>
          <div class="den-settings-prefs-band" data-testid="extensions-unit-filters">
            <BrowseSegmented
              ariaLabel="Unit kind filter"
              value={kindFilter()}
              onChange={selectKindFilter}
              options={KIND_FILTERS.map((k) => ({
                id: k.id,
                label: k.label,
                testId: `extensions-kind-${k.id}`,
              }))}
            />
          </div>
        </Show>

        <SettingsListPanel
          testId="extensions-unit-list-panel"
          chrome={
            unitDetailOpen() ? (
              <SettingsListBackChrome
                testId="extensions-unit-back"
                label={C.unitsBack}
                onBack={closeUnitDetail}
              />
            ) : (
              <SettingsListChrome
                testId="extensions-unit-chrome"
                count={
                  <Show when={catalogQuery.value() !== undefined}>
                    <span data-testid="extensions-unit-count">
                      {countLabel(filteredUnits().length, "unit", "units")}
                    </span>
                  </Show>
                }
                action={
                  <DenInput
                    class="den-ext-search"
                    type="search"
                    placeholder={C.searchPlaceholder}
                    value={unitQuery()}
                    data-testid="extensions-unit-search"
                    onInput={(e) => updateUnitQuery(e.currentTarget.value)}
                  />
                }
              />
            )
          }
        >
          <ListSurfaceInbox
            detailOpen={unitDetailOpen()}
            isEmpty={catalogQuery.value() !== undefined && filteredUnits().length === 0}
            listTestId="extensions-unit-list"
            detailTestId="extensions-unit-detail-pane"
            empty={
              <p
                class="den-settings-hint den-settings-list-inbox__empty"
                data-testid="extensions-empty-units"
              >
                {C.emptyUnits}
              </p>
            }
            list={
              <>
                <For each={pagedUnits().slice}>
                  {(unit) => renderUnitRow(unit)}
                </For>
                <TablePager
                  page={unitPage()}
                  pageSize={EXTENSION_UNITS_PAGE_SIZE}
                  total={pagedUnits().total}
                  onPageChange={setUnitPage}
                  testId="extensions-unit-pager"
                  ariaLabel="Extension units pagination"
                  compact
                />
              </>
            }
            detail={
              <Show when={unitDetail()} keyed>
                {(detail) => renderUnitDetail(detail)}
              </Show>
            }
          />
        </SettingsListPanel>
      </div>
      </Match>

      <Match when={section === "settings" && !isProject()}>
      <div
        id="ext-panel-settings"
        role="tabpanel"
        aria-labelledby="ext-tab-settings"
        class="den-settings-section"
        data-testid="extensions-panel-settings"
      >
        <ExtensionsConfigurationPanel
          client={props.client}
          expectedRevision={stateRevision}
          busy={busy}
          onCommitted={() => {
            setActionStatus(C.configurationSaved);
            void reload();
          }}
          onError={(message) => setActionError(copyNotice(message))}
        />
      </div>
      </Match>

      <Match when={section === "desired"}>
      <div
        id="ext-panel-desired"
        role="tabpanel"
        aria-labelledby="ext-tab-desired"
        class="den-settings-section"
        data-testid="extensions-panel-desired"
      >
        <div class="den-ext-desired">
          <div class="den-ext-desired-head">
            <div>
              <h3>{C.sourceOfTruth}</h3>
              <p class="den-ext-desired-path">
                {isProject() ? C.projectDesiredPath : C.deviceDesiredPath}
              </p>
            </div>
            <span class="den-ext-chip den-ext-chip-stock">{C.sourceOfTruth}</span>
          </div>
          <pre class="den-ext-desired-yaml" data-testid="extensions-desired-yaml">
            {desired()?.yaml || "format: 1\npacks: []\ndisabled: []\nown: {}\n"}
          </pre>
        </div>
        <p class="den-settings-hint">
          {isProject() ? C.projectDesiredHint : C.desiredHint}
        </p>
        <Show when={desired()?.scope_note}>
          <p class="den-settings-hint" data-testid="extensions-desired-scope">
            {desired()?.scope_note}
          </p>
        </Show>
      </div>
      </Match>

      <Match when={section === "model"}>
      <div
        id="ext-panel-model"
        role="tabpanel"
        aria-labelledby="ext-tab-model"
        class="den-settings-section"
        data-testid="extensions-panel-model"
      >
        <section class="den-settings-subsection">
          <h3 class="den-settings-subhead">{C.modelIntroTitle}</h3>
          <p class="den-settings-hint">{C.modelIntro}</p>
          <p class="den-settings-hint" data-testid="extensions-author-loop">
            {C.authorLoopBlurb}
          </p>
        </section>
        <section class="den-settings-subsection">
          <h3 class="den-settings-subhead">{C.modelWhereTitle}</h3>
          <For each={[...C.modelWhereBullets]}>
            {(line) => <p class="den-settings-hint">{line}</p>}
          </For>
        </section>
        <section class="den-settings-subsection">
          <h3 class="den-settings-subhead">{C.modelActiveTitle}</h3>
          <p class="den-settings-hint">{C.modelActive}</p>
        </section>
        <section class="den-settings-subsection">
          <h3 class="den-settings-subhead">{C.modelMetaPacksTitle}</h3>
          <p class="den-settings-hint" data-testid="extensions-model-meta-packs">
            {C.modelMetaPacks}
          </p>
        </section>
      </div>
      </Match>
        </Switch>}
      </SurfaceDeck>

      <Show when={addOpen()}>
        <div
          class="den-dialog-backdrop"
          data-testid="extensions-add-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget && !busy()) setAddOpen(false);
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={addDialogRef}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="ext-add-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="ext-add-title">{C.addDialogTitle}</h2>
            </header>
            <p class="den-dialog__hint">{C.addDialogBody}</p>
            <label class="den-dialog__label" for="ext-add-url">
              {C.addUrlLabel}
            </label>
            <DenInput
              id="ext-add-url"
              class="den-dialog__field"
              data-testid="extensions-add-url"
              value={addUrl()}
              onInput={(e) => setAddUrl(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-add-version">
              {C.addVersionLabel}
            </label>
            <DenInput
              id="ext-add-version"
              class="den-dialog__field"
              data-testid="extensions-add-version"
              value={addVersion()}
              placeholder="^1.0.0"
              onInput={(e) => setAddVersion(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-add-ref">
              {C.addRefLabel}
            </label>
            <DenInput
              id="ext-add-ref"
              class="den-dialog__field"
              data-testid="extensions-add-ref"
              value={addRef()}
              onInput={(e) => setAddRef(e.currentTarget.value)}
            />
            <InlineNotice notice={addError()} testId="extensions-add-error" />
            <footer class="den-dialog__footer">
              <DenButton
                variant="ghost"
                disabled={busy()}
                onClick={() => setAddOpen(false)}
              >
                {C.addCancel}
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="extensions-add-install"
                disabled={actionsDisabled()}
                onClick={() => installFromGit()}
              >
                {C.addInstall}
              </DenButton>
            </footer>
          </div>
        </div>
      </Show>

      <Show when={suiteInstallOpen()}>
        <div
          class="den-dialog-backdrop"
          data-testid="extensions-install-suite-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget && !busy()) setSuiteInstallOpen(false);
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={suiteDialogRef}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="ext-suite-install-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="ext-suite-install-title">{C.suiteInstallTitle}</h2>
            </header>
            <p class="den-dialog__hint">{C.suiteInstallBody}</p>
            <label class="den-dialog__label" for="ext-suite-install-url">
              {C.suiteInstallUrlLabel}
            </label>
            <DenInput
              id="ext-suite-install-url"
              class="den-dialog__field"
              data-testid="extensions-install-suite-url"
              value={suiteInstallUrl()}
              onInput={(e) => setSuiteInstallUrl(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-suite-install-version">
              {C.suiteInstallVersionLabel}
            </label>
            <DenInput
              id="ext-suite-install-version"
              class="den-dialog__field"
              data-testid="extensions-install-suite-version"
              value={suiteInstallVersion()}
              placeholder="^1.0.0"
              onInput={(e) => setSuiteInstallVersion(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-suite-install-ref">
              {C.suiteInstallRefLabel}
            </label>
            <DenInput
              id="ext-suite-install-ref"
              class="den-dialog__field"
              data-testid="extensions-install-suite-ref"
              value={suiteInstallRef()}
              onInput={(e) => setSuiteInstallRef(e.currentTarget.value)}
            />
            <InlineNotice
              notice={suiteInstallError()}
              testId="extensions-install-suite-error"
            />
            <footer class="den-dialog__footer">
              <DenButton
                variant="ghost"
                disabled={busy()}
                onClick={() => setSuiteInstallOpen(false)}
              >
                {C.addCancel}
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="extensions-install-suite-submit"
                disabled={actionsDisabled()}
                onClick={() => installSuite()}
              >
                {C.suiteInstallSubmit}
              </DenButton>
            </footer>
          </div>
        </div>
      </Show>

      <Show when={profileOpen()}>
        <div
          class="den-dialog-backdrop"
          data-testid="extensions-profile-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget && !busy()) setProfileOpen(false);
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={profileDialogRef}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="ext-profile-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="ext-profile-title">{C.profileDialogTitle}</h2>
            </header>
            <p class="den-dialog__hint">{C.profileDialogBody}</p>
            <label class="den-dialog__label" for="ext-profile-pack">
              {C.profilePackLabel}
            </label>
            <DenSelect
              id="ext-profile-pack"
              class="den-dialog__select"
              aria-label={C.profilePackLabel}
              data-testid="extensions-profile-pack"
              value={profilePackId()}
              options={profilePacks().map((pack) => ({
                value: pack.id,
                label: pack.name,
              }))}
              onValueChange={setProfilePackId}
            />
            <label class="den-dialog__label" for="ext-profile-name">
              {C.profileNameLabel}
            </label>
            <DenInput
              id="ext-profile-name"
              class="den-dialog__field"
              data-testid="extensions-profile-name"
              value={profileName()}
              onInput={(e) => setProfileName(e.currentTarget.value)}
            />
            <InlineNotice notice={profileError()} testId="extensions-profile-error" />
            <footer class="den-dialog__footer">
              <DenButton
                variant="ghost"
                disabled={busy()}
                onClick={() => setProfileOpen(false)}
              >
                {C.profileCancel}
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="extensions-profile-apply"
                disabled={actionsDisabled()}
                onClick={() => applyProfile()}
              >
                {C.profileApplyDevice}
              </DenButton>
            </footer>
          </div>
        </div>
      </Show>
    </div>
  );
}
