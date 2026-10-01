import {
  settingAnchor,
  settingLabel,
  type SettingId,
} from "../../../settings/settings-registry.ts";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { For, Show, createEffect, createMemo, createSignal } from "solid-js";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  CreateCustomScannerRequest,
  UpdateSecurityScannersSettingsRequest,
  ScannerCheckRow,
} from "../../../api/types.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { noticeFromCaught, noticeFromInput, type AppNotice } from "../../../notices/notice-model.ts";
import { APP_SCOPE } from "../../../notices/notice-scope.ts";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import {
  SCANNER_SLOTS,
  scannerMissingBinary,
  slotCandidates,
  type ScannerSlot,
  type SlotCandidate,
} from "../../../settings/extensions/scanners-catalog-model.ts";
import {
  SCANNERS_SETTINGS_COPY as C,
  scannerSlotLabel,
} from "../../../settings/extensions/scanners-settings-copy.ts";
import { BrowseSegmented } from "../../browse/BrowseSegmented.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenNumberInput } from "../../primitives/DenNumberInput.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { SecurityScannersIcon, SettingsEditorTitle } from "../SettingsEditorTitle.tsx";
import { SettingsListChrome } from "../SettingsListChrome.tsx";
import { SettingsListPanel } from "../SettingsListPanel.tsx";
import { SettingsScopeLede } from "../SettingsScopeLede.tsx";
import { SettingsGovernedGroup } from "../SettingsGovernedGroup.tsx";
import { ProjectSettingsOverrideControl } from "../ProjectSettingsOverrideControl.tsx";
import { PROJECT_SETTINGS_OVERLAY_COPY } from "../../../settings/security/project-settings-overlay-copy.ts";

type Props = {
  client: LycaonClient;
  projectId?: string;
  alwaysProjectScope?: boolean;
  counterpartLabel?: string;
  onOpenCounterpart?: () => void;
  onEnabledChange?: (enabled: boolean) => void;
};

type CustomScannerForm = {
  id: string;
  engine: string;
  scope_kind: CreateCustomScannerRequest["scope_kind"];
  command: string[];
  output_parser: CreateCustomScannerRequest["output_parser"];
  categories: CreateCustomScannerRequest["categories"];
  label: string;
  runtime: {
    soft_limit_sec: number;
    hard_limit_sec: number;
    cpu_units: number;
    parallelism: number;
  };
};

const DEFAULT_SCAN_RUNTIME_FORM = {
  soft_limit_sec: 900,
  hard_limit_sec: 0,
  cpu_units: 1,
  parallelism: 1,
};

const SCANNER_SLOT_SETTINGS: Record<ScannerSlot, SettingId> = {
  sast: "scanner-sast",
  sca: "scanner-sca",
  secret: "scanner-secret",
};

function emptyCustomDraft(): CustomScannerForm {
  return {
    id: "",
    engine: "",
    scope_kind: "source_driver",
    command: [],
    output_parser: "sarif",
    categories: ["sast"],
    label: "",
    runtime: { ...DEFAULT_SCAN_RUNTIME_FORM },
  };
}

function scannerScopeKind(slot: ScannerSlot): CreateCustomScannerRequest["scope_kind"] {
  switch (slot) {
    case "sast":
      return "source_driver";
    case "sca":
      return "dependencies";
    case "secret":
      return "secrets";
    default:
      return "custom";
  }
}

export function ScannersSettingsPanel(props: Props) {
  const scannerQuery = createSurfaceQuery({
    name: "scanner-settings",
    source: () => ({ client: props.client, key: props.alwaysProjectScope ? props.projectId ?? "global" : "global", projectId: props.alwaysProjectScope ? props.projectId : undefined }),
    load: async ({ client, projectId }) => {
      const [product, catalog, known] = await Promise.all([
        client.getSecurityScannersSettings(),
        projectId ? client.listProjectScanners(projectId) : client.listScanners(),
        client.listScannerCatalog(),
      ]);
      return { product, catalog, known: known.scanners };
    },
  });
  const product = () => scannerQuery.value()?.product;
  const catalog = () => scannerQuery.value()?.catalog;
  const known = () => scannerQuery.value()?.known ?? [];
  const loading = scannerQuery.loading;
  const loadError = () => scannerQuery.error() ? catchNotice(scannerQuery.cause(), C.loadError) : undefined;
  const [saving, setSaving] = createSignal(false);
  const [saveError, setSaveError] = createSignal<AppNotice | undefined>();
  const [overrideBusy, setOverrideBusy] = createSignal(false);

  const [pickerSlot, setPickerSlot] = createSignal<ScannerSlot | undefined>();
  const [customOpen, setCustomOpen] = createSignal(false);
  const [customDraft, setCustomDraft] = createSignal(emptyCustomDraft());
  const [commandText, setCommandText] = createSignal("");
  const [addError, setAddError] = createSignal<AppNotice | undefined>();
  let pickerDialogRef: HTMLDivElement | undefined;
  let customDialogRef: HTMLDivElement | undefined;

  createModalFocusTrap(
    () => pickerSlot() !== undefined,
    () => pickerDialogRef,
    {
      onEscape: () => {
        if (!saving()) setPickerSlot(undefined);
      },
    },
  );
  createModalFocusTrap(customOpen, () => customDialogRef, {
    onEscape: () => {
      if (!saving()) setCustomOpen(false);
    },
  });

  const [installCheckRows, setInstallCheckRows] = createSignal<
    ScannerCheckRow[] | undefined
  >();
  const [installChecking, setInstallChecking] = createSignal(false);
  const [installCheckError, setInstallCheckError] = createSignal<AppNotice | undefined>();
  const catchNotice = (err: unknown, fallback: string) =>
    noticeFromCaught(err, APP_SCOPE, { title: fallback, message: fallback });
  const copyNotice = (message: string) =>
    noticeFromInput({ title: message, message }, APP_SCOPE);

  const projectLocal = () => props.alwaysProjectScope === true;
  const scopedProjectId = () => (projectLocal() ? props.projectId : undefined);

  const reload = scannerQuery.refresh;
  createEffect(() => {
    const current = product();
    if (current) props.onEnabledChange?.(current.enabled);
  });

  const engines = () => catalog()?.scanners ?? [];
  const featureEnabled = () => product()?.enabled ?? true;
  const pathHint = () =>
    catalog()?.user_scanners_path?.trim() || C.pathHintFallback;
  const customRuntime = () => customDraft().runtime ?? DEFAULT_SCAN_RUNTIME_FORM;

  const installCheckById = createMemo(() => {
    const map = new Map<string, ScannerCheckRow>();
    for (const row of installCheckRows() ?? []) map.set(row.scanner_id, row);
    return map;
  });

  const candidatesFor = (slot: ScannerSlot) =>
    slotCandidates(slot, engines(), known());

  const selectedFor = (slot: ScannerSlot): SlotCandidate | undefined =>
    candidatesFor(slot).find((c) => c.current);

  const projectOverrideEnabled = () =>
    engines().some((s) => s.catalog_source === "project");

  const followingSummary = () => {
    const names = SCANNER_SLOTS.map((slot) => selectedFor(slot)?.label).filter(
      Boolean,
    );
    return names.length ? names.join(" · ") : C.slotEmpty;
  };

  const applyProjectOverride = async (enabled: boolean) => {
    const projectId = scopedProjectId();
    if (!projectId || enabled === projectOverrideEnabled()) return;
    setOverrideBusy(true);
    setSaveError(undefined);
    try {
      if (enabled) {
        for (const scanner of engines()) {
          await props.client.updateProjectScanner(projectId, scanner.id, {
            enabled: scanner.enabled,
          });
        }
      } else {
        for (const scanner of engines()) {
          await props.client.updateProjectScanner(projectId, scanner.id, {
            enabled: false,
          });
        }
      }
      await reload();
    } catch (err) {
      setSaveError(catchNotice(err, C.saveError));
    } finally {
      setOverrideBusy(false);
    }
  };

  const listVisible = () => !projectLocal() || projectOverrideEnabled();

  const persistProduct = async (next: UpdateSecurityScannersSettingsRequest) => {
    const target = scannerQuery.capture();
    setSaving(true);
    setSaveError(undefined);
    try {
      const updated = await props.client.updateSecurityScannersSettings(next);
      const current = scannerQuery.value();
      if (current) target.publish({ ...current, product: updated });
      props.onEnabledChange?.(updated.enabled);
      await reload();
    } catch (err) {
      setSaveError(catchNotice(err, C.saveError));
    } finally {
      setSaving(false);
    }
  };

  const chooseCandidate = async (slot: ScannerSlot, candidate: SlotCandidate) => {
    setSaving(true);
    setSaveError(undefined);
    try {
      if (!candidate.scanner) {
        await props.client.createScanner({ source: "catalog", catalog_id: candidate.id });
      }
      const projectId = scopedProjectId();
      if (projectId) {
        await props.client.updateProjectScanner(projectId, candidate.id, {
          enabled: true,
        });
      } else {
        await props.client.replaceScannerSlot(slot, candidate.id);
      }
      setPickerSlot(undefined);
      await reload();
    } catch (err) {
      setSaveError(catchNotice(err, C.saveError));
    } finally {
      setSaving(false);
    }
  };

  const removeScanner = async (id: string) => {
    setSaving(true);
    setSaveError(undefined);
    try {
      await props.client.deleteScanner(id);
      await reload();
    } catch (err) {
      setSaveError(catchNotice(err, C.deleteError));
    } finally {
      setSaving(false);
    }
  };

  const checkInstalls = async () => {
    setInstallChecking(true);
    setInstallCheckError(undefined);
    try {
      const res = await props.client.checkScanners();
      setInstallCheckRows(res.rows);
    } catch (err) {
      setInstallCheckError(catchNotice(err, C.checkInstallsError));
    } finally {
      setInstallChecking(false);
    }
  };

  const submitCustom = async () => {
    const draft = customDraft();
    const command = commandText().trim().split(/\s+/).filter(Boolean);
    if (!draft.id.trim() || command.length === 0) {
      setAddError(copyNotice(C.createError));
      return;
    }
    const runtime = customRuntime();
    if (
      ![runtime.soft_limit_sec, runtime.cpu_units, runtime.parallelism].every(
        (value) => Number.isInteger(value) && value > 0,
      ) ||
      !Number.isInteger(runtime.hard_limit_sec) || runtime.hard_limit_sec < 0 ||
      (runtime.hard_limit_sec > 0 && runtime.hard_limit_sec < runtime.soft_limit_sec) ||
      runtime.cpu_units > 8 ||
      runtime.parallelism > 16
    ) {
      setAddError(copyNotice(C.runtimeInvalid));
      return;
    }
    setSaving(true);
    setAddError(undefined);
    try {
      await props.client.createScanner({
        source: "custom",
        id: draft.id.trim(),
        engine: draft.id.trim(),
        scope_kind: scannerScopeKind(pickerSlot() ?? "sast"),
        command,
        output_parser: draft.output_parser,
        categories: [pickerSlot() ?? "sast"],
        label: draft.label?.trim() || undefined,
        runtime: {
          soft_limit_ms: runtime.soft_limit_sec * 1000,
          hard_limit_ms: runtime.hard_limit_sec * 1000,
          cpu_units: runtime.cpu_units,
          parallelism: runtime.parallelism,
        },
      });
      setCustomOpen(false);
      setCustomDraft(emptyCustomDraft());
      setCommandText("");
      await reload();
    } catch (err) {
      setAddError(catchNotice(err, C.createError));
    } finally {
      setSaving(false);
    }
  };

  const openCustom = () => {
    setCustomDraft(emptyCustomDraft());
    setCommandText("");
    setAddError(undefined);
    setCustomOpen(true);
  };

  const renderSlotRow = (slot: ScannerSlot) => {
    const selected = () => selectedFor(slot);
    const installCheck = () => {
      const id = selected()?.id;
      return id ? installCheckById().get(id) : undefined;
    };
    const missing = () => {
      const scanner = selected()?.scanner;
      return scanner ? scannerMissingBinary(scanner) : false;
    };
    return (
      <div
        class="den-settings-slot-row"
        data-testid={`scanners-slot-${slot}`}
        {...settingAnchor(SCANNER_SLOT_SETTINGS[slot])}
      >
        <div class="den-settings-slot-row__copy">
          <span class="den-settings-pref-label">{settingLabel(SCANNER_SLOT_SETTINGS[slot])}</span>
          <p class="den-settings-hint">{C.slotHint[slot]}</p>
        </div>
        <div class="den-settings-slot-row__value">
          <Show
            when={selected()}
            fallback={
              <span class="den-settings-hint" data-testid={`scanners-empty-${slot}`}>
                {C.slotEmpty}
              </span>
            }
          >
            {(candidate) => (
              <>
                <span
                  class="den-settings-slot-row__name"
                  data-testid={`scanners-selected-${slot}`}
                >
                  {candidate().label}
                </span>
                <Show when={candidate().scanner?.runtime} keyed>
                  {(runtime) => (
                    <span class="den-settings-hint" data-testid={`scanners-runtime-${candidate().id}`}>
                      {C.runtimeSummary(
                        Math.round(runtime.soft_limit_ms / 1000),
                        Math.round(runtime.hard_limit_ms / 1000),
                        runtime.cpu_units,
                        runtime.parallelism,
                      )}
                    </span>
                  )}
                </Show>
                <Show when={missing()}>
                  <span
                    class="den-settings-provider-status"
                    data-configured={false}
                    data-testid={`scanners-needs-binary-${candidate().id}`}
                  >
                    {C.needsBinary}
                  </span>
                </Show>
                <Show when={installCheck()} keyed>
                  {(row) => (
                    <span
                      class="den-settings-provider-status"
                      data-configured={row.ok}
                      data-tip={row.detail}
                      data-testid={`scanners-install-status-${row.scanner_id}`}
                    >
                      {row.ok ? C.installOK : C.installFailed}
                    </span>
                  )}
                </Show>
              </>
            )}
          </Show>
          <DenButton
            variant="secondary"
            compact
            disabled={saving() || !featureEnabled()}
            data-testid={`scanners-change-${slot}`}
            onClick={() => setPickerSlot(slot)}
          >
            {C.changeButton}
          </DenButton>
        </div>
      </div>
    );
  };

  const renderPicker = () => (
    <Show when={pickerSlot()} keyed>
      {(slot) => (
        <ResidentPortal mount={document.body}>
          <div
            class="den-dialog-backdrop den-dialog-backdrop--viewport"
            data-testid="scanners-add-dialog"
            onClick={(e) => {
              if (e.target === e.currentTarget && !saving()) {
                setPickerSlot(undefined);
              }
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={pickerDialogRef}
              class="den-dialog den-dialog--sheet"
              role="dialog"
              aria-modal="true"
              aria-labelledby="scanners-picker-title"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="scanners-picker-title">
                  {C.chooseFor(scannerSlotLabel(slot))}
                </h2>
              </header>
              <p class="den-dialog__hint">{C.slotPickerIntro}</p>
              <Scrollport
                class="den-dialog__list"
                contentAs="ul"
                contentClass="den-dialog__list-content"
                eager
                data-testid="scanners-add-list"
              >
                <For each={candidatesFor(slot)}>
                  {(candidate) => (
                    <li>
                      <button
                        type="button"
                        class="den-dialog__row"
                        data-testid={`scanners-add-option-${candidate.id}`}
                        aria-current={candidate.current}
                        disabled={
                          saving() || !candidate.installed || candidate.current
                        }
                        onClick={() => void chooseCandidate(slot, candidate)}
                      >
                        <span class="den-settings-slot-option__head">
                          <span class="den-dialog__row-title">
                            {candidate.label}
                          </span>
                          <Show when={candidate.current}>
                            <span class="den-ext-chip den-ext-chip-selected">
                              {C.currentBadge}
                            </span>
                          </Show>
                          <Show when={candidate.builtIn && !candidate.current}>
                            <span class="den-ext-chip den-ext-chip-stock">
                              {C.recommendedBadge}
                            </span>
                          </Show>
                          <Show when={!candidate.installed}>
                            <span class="den-ext-chip den-ext-chip-disabled">
                              {C.installFirst}
                            </span>
                          </Show>
                        </span>
                        <Show when={candidate.description}>
                          <span
                            class="den-settings-slot-option__detail"
                            data-testid={`scanners-detail-${candidate.id}`}
                          >
                            {candidate.description}
                          </span>
                        </Show>
                        <Show
                          when={!candidate.installed && candidate.known?.install}
                        >
                          <span
                            class="den-settings-slot-install"
                            data-testid={`scanners-install-${candidate.id}`}
                          >
                            <code>{candidate.known?.install}</code>
                          </span>
                        </Show>
                      </button>
                      <Show
                        when={
                          candidate.scanner &&
                          !candidate.builtIn &&
                          !candidate.current &&
                          !projectLocal()
                        }
                      >
                        <DenButton
                          variant="link"
                          class="den-settings-provider-remove"
                          disabled={saving()}
                          data-testid={`scanners-delete-${candidate.id}`}
                          onClick={() => void removeScanner(candidate.id)}
                        >
                          {C.deleteButton}
                        </DenButton>
                      </Show>
                    </li>
                  )}
                </For>
                <li class="den-settings-add-dialog__group">{C.customGroup}</li>
                <li>
                  <button
                    type="button"
                    class="den-dialog__row"
                    data-testid="scanners-add-custom"
                    disabled={saving()}
                    onClick={() => openCustom()}
                  >
                    <span class="den-dialog__row-title">{C.customOption}</span>
                  </button>
                </li>
              </Scrollport>
              <footer class="den-settings-add-dialog__footer">
                <DenButton
                  variant="secondary"
                  compact
                  disabled={saving()}
                  data-testid="scanners-add-cancel"
                  onClick={() => setPickerSlot(undefined)}
                >
                  {C.addCancel}
                </DenButton>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      )}
    </Show>
  );

  const renderCustomDialog = () => (
    <Show when={customOpen()}>
      <ResidentPortal mount={document.body}>
        <div
          class="den-dialog-backdrop den-dialog-backdrop--viewport"
          data-testid="scanners-custom-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget && !saving()) setCustomOpen(false);
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={customDialogRef}
            class="den-dialog den-dialog--sheet"
            role="dialog"
            aria-modal="true"
            aria-labelledby="scanners-custom-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="scanners-custom-title">{C.customTitle}</h2>
            </header>
            <Scrollport
              class="den-settings-add-dialog__body"
              contentClass="den-settings-add-dialog__body-content"
              eager
            >
              <p class="den-dialog__hint">{C.customIntro}</p>
              <DenField label={C.fieldId}>
                <DenInput
                  data-testid="scanners-custom-id"
                  autocomplete="off"
                  value={customDraft().id}
                  onInput={(e) =>
                    setCustomDraft({
                      ...customDraft(),
                      id: e.currentTarget.value,
                    })
                  }
                />
              </DenField>
              <p class="den-dialog__hint">{C.fieldIdHint}</p>
              <DenField label={C.fieldCommand}>
                <DenInput
                  data-testid="scanners-custom-command"
                  autocomplete="off"
                  value={commandText()}
                  onInput={(e) => setCommandText(e.currentTarget.value)}
                />
              </DenField>
              <p class="den-dialog__hint">{C.fieldCommandHint}</p>
              <DenField label={C.fieldLabel}>
                <DenInput
                  data-testid="scanners-custom-label"
                  autocomplete="off"
                  value={customDraft().label ?? ""}
                  onInput={(e) =>
                    setCustomDraft({
                      ...customDraft(),
                      label: e.currentTarget.value,
                    })
                  }
                />
              </DenField>
              <DenField label={C.fieldSoftLimit}>
                <DenNumberInput
                  data-testid="scanners-custom-soft-limit"
                  min="1"
                  value={customRuntime().soft_limit_sec}
                  onInput={(e) => setCustomDraft({
                    ...customDraft(),
                    runtime: { ...customRuntime(), soft_limit_sec: Number(e.currentTarget.value) },
                  })}
                />
              </DenField>
              <p class="den-dialog__hint">{C.fieldSoftLimitHint}</p>
              <DenField label={C.fieldHardLimit}>
                <DenNumberInput
                  data-testid="scanners-custom-hard-limit"
                  min="0"
                  value={customRuntime().hard_limit_sec}
                  onInput={(e) => setCustomDraft({
                    ...customDraft(),
                    runtime: { ...customRuntime(), hard_limit_sec: Number(e.currentTarget.value) },
                  })}
                />
              </DenField>
              <p class="den-dialog__hint">{C.fieldHardLimitHint}</p>
              <DenField label={C.fieldCPUUnits}>
                <DenNumberInput
                  data-testid="scanners-custom-cpu-units"
                  min="1"
                  max="8"
                  value={customRuntime().cpu_units}
                  onInput={(e) => setCustomDraft({
                    ...customDraft(),
                    runtime: { ...customRuntime(), cpu_units: Number(e.currentTarget.value) },
                  })}
                />
              </DenField>
              <DenField label={C.fieldParallelism}>
                <DenNumberInput
                  data-testid="scanners-custom-parallelism"
                  min="1"
                  max="16"
                  value={customRuntime().parallelism}
                  onInput={(e) => setCustomDraft({
                    ...customDraft(),
                    runtime: { ...customRuntime(), parallelism: Number(e.currentTarget.value) },
                  })}
                />
              </DenField>
              <InlineNotice notice={addError()} testId="scanners-custom-error" />
            </Scrollport>
            <footer class="den-settings-add-dialog__footer">
              <DenButton
                variant="secondary"
                compact
                disabled={saving()}
                data-testid="scanners-custom-cancel"
                onClick={() => setCustomOpen(false)}
              >
                {C.addCancel}
              </DenButton>
              <DenButton
                variant="primary"
                compact
                disabled={saving()}
                data-testid="scanners-custom-submit"
                onClick={() => void submitCustom()}
              >
                {C.customSubmit}
              </DenButton>
            </footer>
          </div>
        </div>
      </ResidentPortal>
    </Show>
  );

  return (
    <div
      class="den-settings-editor"
      classList={{ "den-settings-editor--project": projectLocal() }}
      data-testid="scanners-settings-panel"
      data-scope={projectLocal() ? "project" : "global"}
    >
      <SettingsEditorTitle
        icon={<SecurityScannersIcon />}
        scopeBadge={
          projectLocal() ? PROJECT_SETTINGS_OVERLAY_COPY.badge : undefined
        }
      >
        {C.title}
      </SettingsEditorTitle>

      <SettingsScopeLede
        scope={projectLocal() ? "project" : "device"}
        counterpartLabel={props.counterpartLabel}
        onOpenCounterpart={props.onOpenCounterpart}
      >
        <p class="den-settings-hint" data-testid="scanners-product-intro">
          {C.productIntro}
        </p>
      </SettingsScopeLede>

      <div class="den-settings-prefs-band">
        <p
          class="den-settings-hint den-settings-prefs-band__warn"
          data-testid="scanners-threat-banner"
          role="note"
        >
          {C.threatBanner}
        </p>

        <Show when={projectLocal() && scannerQuery.ready()}>
          <div class="den-settings-prefs-band__warn">
            <ProjectSettingsOverrideControl
              settingsPath={PROJECT_SETTINGS_OVERLAY_COPY.settingsPath.scanners}
              enabled={projectOverrideEnabled()}
              disabled={overrideBusy() || loading()}
              followingSummary={followingSummary()}
              onChange={(enabled) => void applyProjectOverride(enabled)}
            />
          </div>
        </Show>

        <ShowLatest when={!projectLocal() && product()}>
          {(cfg) => (
            <>
              <div
                class="den-settings-pref-row"
                data-testid="scanners-main-row"
                {...settingAnchor("security-scanners")}
              >
                <div class="den-settings-pref-copy">
                  <span class="den-settings-pref-label">{settingLabel("security-scanners")}</span>
                  <p class="den-settings-hint">{C.mainHint}</p>
                </div>
                <DenCheckbox
                  checked={cfg().enabled}
                  disabled={saving()}
                  data-testid="scanners-main"
                  onChange={(e) => {
                    void persistProduct({
                      enabled: e.currentTarget.checked,
                      landed_change_scope: cfg().landed_change_scope,
                      source_verify: cfg().source_verify,
                    });
                  }}
                >
                  <span class="sr-only">{settingLabel("security-scanners")}</span>
                </DenCheckbox>
              </div>
              <SettingsGovernedGroup
                label="Scanner options"
                active={cfg().enabled}
              >
                <div
                  class="den-settings-pref-row"
                  data-testid="scanners-landed-change-row"
                  {...settingAnchor("scan-after-changes")}
                >
                  <div class="den-settings-pref-copy">
                    <span class="den-settings-pref-label">
                      {settingLabel("scan-after-changes")}
                    </span>
                    <p class="den-settings-hint">{C.landedChangeHint}</p>
                  </div>
                  <BrowseSegmented
                    class="den-settings-segmented"
                    ariaLabel={settingLabel("scan-after-changes")}
                    testId="scanners-landed-change-scope"
                    value={cfg().landed_change_scope}
                    disabled={saving()}
                    onChange={(id) => {
                      if (id === "path_scoped" || id === "full_root") {
                        void persistProduct({
                          enabled: cfg().enabled,
                          landed_change_scope: id,
                          source_verify: cfg().source_verify,
                        });
                      }
                    }}
                    options={[
                      { id: "path_scoped", label: C.landedChangePathScoped },
                      { id: "full_root", label: C.landedChangeFullRoot },
                    ]}
                  />
                </div>
                <div
                  class="den-settings-pref-row"
                  data-testid="scanners-source-verify-row"
                  {...settingAnchor("scan-source-reading")}
                >
                  <div class="den-settings-pref-copy">
                    <span class="den-settings-pref-label">
                      {settingLabel("scan-source-reading")}
                    </span>
                    <p class="den-settings-hint">{C.sourceVerifyHint}</p>
                  </div>
                  <BrowseSegmented
                    class="den-settings-segmented"
                    ariaLabel={settingLabel("scan-source-reading")}
                    testId="scanners-source-verify"
                    value={cfg().source_verify}
                    disabled={saving()}
                    onChange={(id) => {
                      if (id === "stat" || id === "content") {
                        void persistProduct({
                          enabled: cfg().enabled,
                          landed_change_scope: cfg().landed_change_scope,
                          source_verify: id,
                        });
                      }
                    }}
                    options={[
                      { id: "stat", label: C.sourceVerifyStat },
                      { id: "content", label: C.sourceVerifyContent },
                    ]}
                  />
                </div>
              </SettingsGovernedGroup>
            </>
          )}
        </ShowLatest>

        <p
          class="den-settings-hint den-settings-prefs-band__warn"
          data-testid="scanners-path-hint"
        >
          {C.pathHint}: <code>{pathHint()}</code>
        </p>
      </div>

      <InlineNotice notice={loadError()} testId="scanners-load-error" />
      <InlineNotice notice={saveError()} testId="scanners-save-error" />
      <Show when={saving()}>
        <span class="sr-only" role="status">
          {C.saving}
        </span>
      </Show>

      <Show when={listVisible() && scannerQuery.value() !== undefined}>
      <SettingsListPanel
        testId="scanners-list-panel"
        chrome={
          <SettingsListChrome
            testId="scanners-list-chrome"
            count={
              <span class="den-settings-pref-label" data-testid="scanners-count">
                {C.jobsHeading}
              </span>
            }
            action={
              <DenButton
                variant="ghost"
                compact
                data-testid="scanners-check-installs"
                disabled={installChecking() || loading()}
                onClick={() => void checkInstalls()}
              >
                {installChecking() ? C.checkingInstalls : C.checkInstalls}
              </DenButton>
            }
          />
        }
      >
        <Show when={scannerQuery.showLoading()}>
          <p class="den-settings-hint">Loading…</p>
        </Show>

        <InlineNotice notice={installCheckError()} testId="scanners-check-installs-error" />

        <div class="den-settings-slot-list" data-testid="scanners-list">
          <For each={SCANNER_SLOTS}>{(slot) => renderSlotRow(slot)}</For>
        </div>
      </SettingsListPanel>
      </Show>

      {renderPicker()}
      {renderCustomDialog()}
    </div>
  );
}
