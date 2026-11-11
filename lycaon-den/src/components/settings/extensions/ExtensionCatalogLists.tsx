import { For, Show } from "solid-js";
import type {
  ExtensionDesiredState,
  ExtensionDiagnostic,
  ExtensionMetaPackSummary,
  ExtensionPackSummary,
  ExtensionPackUpdateStatus,
  ExtensionUnitDetail,
  ExtensionUnitSummary,
} from "../../../api/types.ts";
import {
  packRowChips,
  unitStatusChip,
  unitViaLabel,
} from "../../../settings/extensions/extensions-chips.ts";
import {
  metaPackIdSafe,
  metaPackStatusChip,
  metaPackStatusChips,
  type MetaPackGrouping,
} from "../../../settings/extensions/extensions-meta.ts";
import { EXTENSIONS_SETTINGS_COPY as C } from "../../../settings/extensions/extensions-settings-copy.ts";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { SettingsListBackChrome } from "../SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { SettingsListGroup } from "../SettingsListGroup.tsx";
import { ListSurfaceInbox } from "../../list/ListSurfaceInbox.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { SettingsListRow } from "../SettingsListRow.tsx";
import { TablePager } from "../../list/TablePager.tsx";
import {
  ExtensionChipBadge,
  ExtensionPackDetailCard,
  ExtensionUnitDetailCard,
  KIND_FILTERS,
  countLabel,
  extTestId,
  packSourceLabel,
  unitKindLabel,
} from "./ExtensionDetailCards.tsx";

export type ExtensionPacksSectionProps = {
  catalogLoaded: boolean;
  packs: ExtensionPackSummary[];
  packGrouping: MetaPackGrouping;
  selectedPack: ExtensionPackSummary | undefined;
  packProvides: ExtensionUnitSummary[];
  profilePacksCount: number;
  actionsDisabled: boolean;
  contributionRuntimeRows: readonly { code: string; message: string }[];
  updatePlan: Record<string, ExtensionPackUpdateStatus | undefined>;
  diagnosticsForPack: (packId: string) => ExtensionDiagnostic[];
  onOpenScanners?: () => void;
  onSelectPack: (packId: string) => void;
  onClosePackDetail: () => void;
  onTogglePack: (
    pack: ExtensionPackSummary,
    enabled: boolean,
    checkbox: HTMLInputElement,
  ) => void;
  onReloadPack: (pack: ExtensionPackSummary) => void;
  onBeginUpdate: (pack: ExtensionPackSummary) => void;
  onConfirmUpdate: (pack: ExtensionPackSummary) => void;
  onRemovePack: (pack: ExtensionPackSummary) => void;
  onEnableSuite: (suiteId: string) => void;
  onDisableSuite: (suiteId: string) => void;
  onRemoveSuite: (meta: ExtensionMetaPackSummary) => void;
  onOpenSuiteInstallDialog: () => void;
  onOpenProfileDialog: () => void;
  onInstallFromFolder: () => void;
  onOpenAddDialog: () => void;
};

export function ExtensionPacksSection(props: ExtensionPacksSectionProps) {
  const packDetailOpen = () => props.selectedPack != null;

  const renderPackRow = (pack: ExtensionPackSummary) => {
    const chips = () => packRowChips(pack, props.diagnosticsForPack(pack.id));
    return (
      <SettingsListRow
        testId={extTestId("extensions-pack", pack.id)}
        leading={
          <DenCheckbox
            checked={pack.enabled}
            disabled={props.actionsDisabled}
            data-testid={extTestId("extensions-pack-enable", pack.id)}
            aria-label={`Enable ${pack.name}`}
            onChange={(e) =>
              props.onTogglePack(pack, e.currentTarget.checked, e.currentTarget)
            }
          />
        }
        primary={pack.name}
        secondary={packSourceLabel(pack)}
        status={
          <span class="den-ext-row-status">
            <For each={chips()}>
              {(chip) => (
                <ExtensionChipBadge
                  chip={chip}
                  onOpenScanners={props.onOpenScanners}
                  onReloadFromDisk={
                    pack.kind === "path" && pack.needs_reload
                      ? () => props.onReloadPack(pack)
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
        onSelect={() => props.onSelectPack(pack.id)}
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
            <ExtensionChipBadge chip={statusChip} />
            <For each={statusChips}>
              {(chip) => <ExtensionChipBadge chip={chip} />}
            </For>
            <DenButton
              variant="ghost"
              compact
              data-testid={`extensions-meta-enable-${safe}`}
              disabled={props.actionsDisabled}
              onClick={() => props.onEnableSuite(meta.id)}
            >
              {C.suiteEnable}
            </DenButton>
            <DenButton
              variant="ghost"
              compact
              data-testid={`extensions-meta-disable-${safe}`}
              disabled={props.actionsDisabled || !meta.removable}
              data-tip={!meta.removable ? C.suiteDisableStock : undefined}
              onClick={() => props.onDisableSuite(meta.id)}
            >
              {C.suiteDisable}
            </DenButton>
            <Show when={meta.removable}>
              <DenButton
                variant="ghost"
                compact
                data-testid={`extensions-meta-remove-${safe}`}
                disabled={props.actionsDisabled}
                onClick={() => props.onRemoveSuite(meta)}
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

  return (
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
              onBack={props.onClosePackDetail}
            />
          ) : (
            <SettingsListChrome
              testId="extensions-pack-chrome"
              count={
                <Show when={props.catalogLoaded}>
                  <span data-testid="extensions-pack-count">
                    {countLabel(props.packs.length, "pack", "packs")}
                  </span>
                </Show>
              }
              secondaryAction={
                <>
                  <DenButton
                    variant="ghost"
                    compact
                    data-testid="extensions-install-suite"
                    disabled={props.actionsDisabled}
                    onClick={props.onOpenSuiteInstallDialog}
                  >
                    {C.suiteInstall}
                  </DenButton>
                  <DenButton
                    variant="secondary"
                    compact
                    data-testid="extensions-apply-profile"
                    disabled={
                      props.actionsDisabled || props.profilePacksCount === 0
                    }
                    onClick={props.onOpenProfileDialog}
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
                    disabled={props.actionsDisabled}
                    onClick={props.onInstallFromFolder}
                  >
                    {C.addFromFolder}
                  </DenButton>
                  <DenButton
                    variant="primary"
                    compact
                    data-testid="extensions-add-git"
                    disabled={props.actionsDisabled}
                    onClick={props.onOpenAddDialog}
                  >
                    {C.addFromGit}
                  </DenButton>
                </>
              }
            />
          )
        }
      >
        <Show
          when={!packDetailOpen() && props.contributionRuntimeRows.length > 0}
        >
          <ul
            class="den-ext-diags"
            data-testid="extensions-contribution-runtime"
          >
            <For each={props.contributionRuntimeRows}>
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
          isEmpty={props.catalogLoaded && props.packs.length === 0}
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
          list={
            <div class="den-settings-list-stack">
              <For each={props.packGrouping.sections}>
                {(section) => renderMetaSection(section.meta, section.packs)}
              </For>
              <Show when={props.packGrouping.other.length > 0}>
                <SettingsListGroup
                  testId="extensions-meta-section-other-packs"
                  label={C.suiteOtherPacks}
                >
                  <For each={props.packGrouping.other}>
                    {(pack) => renderPackRow(pack)}
                  </For>
                </SettingsListGroup>
              </Show>
            </div>
          }
          detail={
            <Show when={props.selectedPack} keyed>
              {(pack) => (
                <ExtensionPackDetailCard
                  pack={pack}
                  diagnostics={props.diagnosticsForPack(pack.id)}
                  packProvides={props.packProvides}
                  updatePlanStatus={props.updatePlan[pack.id]}
                  actionsDisabled={props.actionsDisabled}
                  onOpenScanners={props.onOpenScanners}
                  onReloadPack={props.onReloadPack}
                  onBeginUpdate={props.onBeginUpdate}
                  onConfirmUpdate={props.onConfirmUpdate}
                  onRemovePack={props.onRemovePack}
                />
              )}
            </Show>
          }
        />
      </SettingsListPanel>
    </div>
  );
}

export type ExtensionUnitsSectionProps = {
  catalogLoaded: boolean;
  isProject: boolean;
  actionsDisabled: boolean;
  kindFilter: string;
  onSelectKindFilter: (kind: string) => void;
  unitQuery: string;
  onUpdateUnitQuery: (query: string) => void;
  filteredUnitsCount: number;
  pagedUnitsSlice: ExtensionUnitSummary[];
  pagedUnitsTotal: number;
  unitPage: number;
  unitPageSize: number;
  onUnitPageChange: (page: number) => void;
  unitDetailOpen: boolean;
  unitDetail: ExtensionUnitDetail | undefined;
  packKinds: Record<string, ExtensionPackSummary["kind"]>;
  packNames: Record<string, string>;
  onSelectUnit: (unitId: string) => void;
  onCloseUnitDetail: () => void;
  onSetUnitDisabled: (unitId: string, disabled: boolean) => void;
  onSetUnitOwn: (unitId: string, packId: string | null) => void;
};

export function ExtensionUnitsSection(props: ExtensionUnitsSectionProps) {
  const renderUnitRow = (unit: ExtensionUnitSummary) => {
    const statusChip = unitStatusChip({
      status: unit.status,
      contributions: unit.contributions,
      packKinds: props.packKinds,
    });
    const via = unitViaLabel({
      status: unit.status,
      contributions: unit.contributions,
      winner_pack_id: unit.winner_pack_id,
      packNames: props.packNames,
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
            <ExtensionChipBadge chip={statusChip} />
            <span class="den-ext-row-via" data-tip={via} data-tip-when-clipped>
              {via}
            </span>
          </span>
        }
        onSelect={() => props.onSelectUnit(unit.id)}
      />
    );
  };

  return (
    <div
      id="ext-panel-units"
      role="tabpanel"
      aria-labelledby="ext-tab-units"
      class="den-settings-section"
      data-testid="extensions-panel-units"
    >
      <Show when={!props.unitDetailOpen}>
        <div
          class="den-settings-prefs-band"
          data-testid="extensions-unit-filters"
        >
          <BrowseSegmented
            ariaLabel="Unit kind filter"
            value={props.kindFilter}
            onChange={props.onSelectKindFilter}
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
          props.unitDetailOpen ? (
            <SettingsListBackChrome
              testId="extensions-unit-back"
              label={C.unitsBack}
              onBack={props.onCloseUnitDetail}
            />
          ) : (
            <SettingsListChrome
              testId="extensions-unit-chrome"
              count={
                <Show when={props.catalogLoaded}>
                  <span data-testid="extensions-unit-count">
                    {countLabel(props.filteredUnitsCount, "unit", "units")}
                  </span>
                </Show>
              }
              action={
                <DenInput
                  class="den-ext-search"
                  type="search"
                  placeholder={C.searchPlaceholder}
                  value={props.unitQuery}
                  data-testid="extensions-unit-search"
                  onInput={(e) => props.onUpdateUnitQuery(e.currentTarget.value)}
                />
              }
            />
          )
        }
      >
        <ListSurfaceInbox
          detailOpen={props.unitDetailOpen}
          isEmpty={props.catalogLoaded && props.filteredUnitsCount === 0}
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
              <For each={props.pagedUnitsSlice}>
                {(unit) => renderUnitRow(unit)}
              </For>
              <TablePager
                page={props.unitPage}
                pageSize={props.unitPageSize}
                total={props.pagedUnitsTotal}
                onPageChange={props.onUnitPageChange}
                testId="extensions-unit-pager"
                ariaLabel="Extension units pagination"
                compact
              />
            </>
          }
          detail={
            <Show when={props.unitDetail} keyed>
              {(detail) => (
                <ExtensionUnitDetailCard
                  detail={detail}
                  isProject={props.isProject}
                  actionsDisabled={props.actionsDisabled}
                  packKinds={props.packKinds}
                  packNames={props.packNames}
                  onSetUnitDisabled={props.onSetUnitDisabled}
                  onSetUnitOwn={props.onSetUnitOwn}
                />
              )}
            </Show>
          }
        />
      </SettingsListPanel>
    </div>
  );
}

export function ExtensionDesiredSection(props: {
  isProject: boolean;
  desired: ExtensionDesiredState | undefined;
}) {
  return (
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
              {props.isProject ? C.projectDesiredPath : C.deviceDesiredPath}
            </p>
          </div>
          <span class="den-ext-chip den-ext-chip-stock">{C.sourceOfTruth}</span>
        </div>
        <pre class="den-ext-desired-yaml" data-testid="extensions-desired-yaml">
          {props.desired?.yaml || "format: 1\npacks: []\ndisabled: []\nown: {}\n"}
        </pre>
      </div>
      <p class="den-settings-hint">
        {props.isProject ? C.projectDesiredHint : C.desiredHint}
      </p>
      <Show when={props.desired?.scope_note}>
        <p class="den-settings-hint" data-testid="extensions-desired-scope">
          {props.desired?.scope_note}
        </p>
      </Show>
    </div>
  );
}

export function ExtensionModelSection() {
  return (
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
  );
}
