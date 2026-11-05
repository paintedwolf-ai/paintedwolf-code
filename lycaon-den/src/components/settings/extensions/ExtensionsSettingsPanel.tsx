import { createExtensionActions } from "./extension-actions.ts";
import { SurfaceDeck } from "../../primitives/SurfaceDeck.tsx";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { ThemeIcon } from "../../primitives/ThemeIcon.tsx";
import { For, Show, Switch, Match, createEffect, createMemo, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ExtensionsWriteTarget } from "../../../api/http-capabilities/extensions.ts";
import type {
  ExtensionDiagnostic,
  ExtensionPackSummary,
  ExtensionUnitDetail,
  ExtensionUnitSummary,
  ExtensionsDesiredScope,
} from "../../../api/types.ts";
import { groupPacksByMeta } from "../../../settings/extensions/extensions-meta.ts";
import {
  isProjectExtensionRefusal,
  projectExtensionRefusalDiagnostics,
} from "../../../settings/security/trust-copy.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { EXTENSIONS_SETTINGS_COPY as C } from "../../../settings/extensions/extensions-settings-copy.ts";
import {
  rememberExtensionsTab,
  rememberedExtensionsTab,
  type ExtensionsSurface,
  type ExtensionsTab,
} from "../../../settings/extensions/extensions-tab.ts";
import { SettingsEditorTitle } from "../SettingsEditorTitle.tsx";
import { ExtensionsConfigurationPanel } from "./ExtensionsConfigurationPanel.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { UnderlineTabs } from "../UnderlineTabs.tsx";
import { contributionFrame } from "../../../contributions/contribution-store.ts";
import { paginateSlice } from "../../../list/pagination.ts";
import { ExtensionInstallModals } from "./ExtensionInstallModals.tsx";
import {
  ExtensionDesiredSection,
  ExtensionModelSection,
  ExtensionPacksSection,
  ExtensionUnitsSection,
} from "./ExtensionCatalogLists.tsx";

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

export const EXTENSION_UNITS_PAGE_SIZE = 50;

function ExtensionsIcon() {
  return <ThemeIcon slot="stage-extensions" size={18} />;
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
  const projectId = () => (isProject() ? props.projectId?.trim() || undefined : undefined);
  const catalogQuery = createSurfaceQuery({
    name: "extensions-catalog",
    source: () => {
      const pid = projectId();
      return isProject() && !pid
        ? null
        : {
            client: props.client,
            projectId: pid,
            key: pid ?? "device",
            revision: props.extensionsRevision?.(),
          };
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

  const [selectedPackId, setSelectedPackId] = createSignal<string | undefined>();
  const [selectedUnitId, setSelectedUnitId] = createSignal<string | undefined>();
  const [unitDetail, setUnitDetail] = createSignal<ExtensionUnitDetail | undefined>();
  const [detailRevision, setDetailRevision] = createSignal(0);
  const [kindFilter, setKindFilter] = createSignal<string>("all");
  const [unitQuery, setUnitQuery] = createSignal("");
  const [unitPage, setUnitPage] = createSignal(0);
  let unitDetailRevision = 0;

  const scope = (): ExtensionsDesiredScope => (isProject() ? "project" : "device");
  const unitWriteTarget = (): ExtensionsWriteTarget => ({
    scope: scope(),
    projectId: projectId(),
  });

  const profilePacks = createMemo(() => packs().filter((p) => p.has_profile));

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

  const loading = catalogQuery.loading;
  const actionsDisabled = () => busy() || loading() || !stateRevision();

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

  const loadError = () =>
    isProject() && !projectId()
      ? copyNotice(C.projectRequired)
      : catalogQuery.error()
        ? catchNotice(catalogQuery.cause(), C.loadError)
        : undefined;
  const reload = catalogQuery.refresh;

  createEffect(() => {
    const view = catalogQuery.value();
    if (!view) return;
    if (selectedPackId() && !(view.packs ?? []).some((pack) => pack.id === selectedPackId())) {
      setSelectedPackId(undefined);
    }
    if (selectedUnitId() && !(view.units ?? []).some((unit) => unit.id === selectedUnitId())) {
      setSelectedUnitId(undefined);
    }
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

  const unitDetailOpen = () => selectedUnitId() != null && unitDetail() != null;

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
        <p
          class="den-settings-hint"
          data-testid="extensions-action-status"
          role="status"
        >
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
                t.id === "units" ? "project-extensions" : undefined
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
        {(section) => (
          <Switch>
            <Match when={section === "packs" && !isProject()}>
              <ExtensionPacksSection
                catalogLoaded={catalogQuery.value() !== undefined}
                packs={packs()}
                packGrouping={packGrouping()}
                selectedPack={selectedPack()}
                packProvides={packProvides()}
                profilePacksCount={profilePacks().length}
                actionsDisabled={actionsDisabled()}
                contributionRuntimeRows={contributionRuntimeRows()}
                updatePlan={updatePlan()}
                diagnosticsForPack={diagnosticsForPack}
                onOpenScanners={props.onOpenScanners}
                onSelectPack={setSelectedPackId}
                onClosePackDetail={() => setSelectedPackId(undefined)}
                onTogglePack={togglePack}
                onReloadPack={reloadPack}
                onBeginUpdate={beginUpdate}
                onConfirmUpdate={confirmUpdate}
                onRemovePack={removePack}
                onEnableSuite={enableSuite}
                onDisableSuite={disableSuite}
                onRemoveSuite={removeSuite}
                onOpenSuiteInstallDialog={openSuiteInstallDialog}
                onOpenProfileDialog={openProfileDialog}
                onInstallFromFolder={installFromFolder}
                onOpenAddDialog={openAddDialog}
              />
            </Match>

            <Match when={section === "units"}>
              <ExtensionUnitsSection
                catalogLoaded={catalogQuery.value() !== undefined}
                isProject={isProject()}
                actionsDisabled={actionsDisabled()}
                kindFilter={kindFilter()}
                onSelectKindFilter={selectKindFilter}
                unitQuery={unitQuery()}
                onUpdateUnitQuery={updateUnitQuery}
                filteredUnitsCount={filteredUnits().length}
                pagedUnitsSlice={pagedUnits().slice}
                pagedUnitsTotal={pagedUnits().total}
                unitPage={unitPage()}
                unitPageSize={EXTENSION_UNITS_PAGE_SIZE}
                onUnitPageChange={setUnitPage}
                unitDetailOpen={unitDetailOpen()}
                unitDetail={unitDetail()}
                packKinds={packKinds()}
                packNames={packNames()}
                onSelectUnit={setSelectedUnitId}
                onCloseUnitDetail={() => setSelectedUnitId(undefined)}
                onSetUnitDisabled={setUnitDisabled}
                onSetUnitOwn={setUnitOwn}
              />
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
              <ExtensionDesiredSection isProject={isProject()} desired={desired()} />
            </Match>

            <Match when={section === "model"}>
              <ExtensionModelSection />
            </Match>
          </Switch>
        )}
      </SurfaceDeck>

      <ExtensionInstallModals
        busy={busy}
        actionsDisabled={actionsDisabled}
        addOpen={addOpen}
        setAddOpen={setAddOpen}
        addUrl={addUrl}
        setAddUrl={setAddUrl}
        addVersion={addVersion}
        setAddVersion={setAddVersion}
        addRef={addRef}
        setAddRef={setAddRef}
        addError={addError}
        onInstallFromGit={installFromGit}
        suiteInstallOpen={suiteInstallOpen}
        setSuiteInstallOpen={setSuiteInstallOpen}
        suiteInstallUrl={suiteInstallUrl}
        setSuiteInstallUrl={setSuiteInstallUrl}
        suiteInstallVersion={suiteInstallVersion}
        setSuiteInstallVersion={setSuiteInstallVersion}
        suiteInstallRef={suiteInstallRef}
        setSuiteInstallRef={setSuiteInstallRef}
        suiteInstallError={suiteInstallError}
        onInstallSuite={installSuite}
        profileOpen={profileOpen}
        setProfileOpen={setProfileOpen}
        profilePacks={profilePacks}
        profilePackId={profilePackId}
        setProfilePackId={setProfilePackId}
        profileName={profileName}
        setProfileName={setProfileName}
        profileError={profileError}
        onApplyProfile={applyProfile}
      />
    </div>
  );
}
