import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { For, Show, createSignal } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  DetectionPack,
  DetectionRuleSummary,
} from "../../../api/types.ts";
import { confirmDestructive } from "../../../platform/interaction/confirm-dialog.ts";
import { pickProjectFolder } from "../../../platform/files/folder.ts";
import { DETECTIONS_COPY } from "../../../settings/security/approvals-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DetectionPackImportDialog } from "./DetectionPackImportDialog.tsx";

type Props = {
  client: LycaonClient;
};

/** Bundled egress pack — holds CONNECT, not command asks. */
const EGRESS_PACK_ID = "egress-providers";

function sourceBadge(source: DetectionPack["source"]): string {
  switch (source) {
    case "generated":
      return DETECTIONS_COPY.generatedBadge;
    case "extension":
      return DETECTIONS_COPY.extensionBadge;
    case "device":
      return DETECTIONS_COPY.deviceBadge;
    default:
      return DETECTIONS_COPY.bundledBadge;
  }
}

/** Why a pack has no Remove button, in terms of who to ask instead. */
function removeHint(pack: DetectionPack): string | undefined {
  if (pack.removable) return undefined;
  if (pack.source === "extension" && pack.provider_pack_id) {
    return DETECTIONS_COPY.removeExtensionHint(pack.provider_pack_id);
  }
  return DETECTIONS_COPY.removeBundledHint;
}

function packHoldsConnections(pack: DetectionPack): boolean {
  return pack.id === EGRESS_PACK_ID;
}

function sortedRules(rules: DetectionRuleSummary[]): DetectionRuleSummary[] {
  if (!rules.some((rule) => (rule.recent_asks ?? 0) > 0)) {
    return rules;
  }
  return [...rules].sort((a, b) => {
    const left = a.recent_asks ?? 0;
    const right = b.recent_asks ?? 0;
    if (left !== right) {
      return right - left;
    }
    return a.title.localeCompare(b.title);
  });
}

function confirmRemove(label: string): Promise<boolean> {
  return confirmDestructive({
    message: DETECTIONS_COPY.removeConfirm(label),
    title: DETECTIONS_COPY.remove,
    okLabel: DETECTIONS_COPY.remove,
    cancelLabel: DETECTIONS_COPY.addCancel,
  });
}

function RuleRow(props: { rule: DetectionRuleSummary }) {
  return (
    <li class="den-detections-rule" data-testid={`detection-rule-${props.rule.id}`}>
      <div class="den-detections-rule__head">
        <span class="den-detections-rule__title">{props.rule.title}</span>
        <span class="den-settings-badge" data-level={props.rule.level}>
          {props.rule.level}
        </span>
        <Show when={(props.rule.recent_asks ?? 0) > 0}>
          <span class="den-settings-badge" data-testid={`detection-rule-${props.rule.id}-recent-asks`}>
            {DETECTIONS_COPY.recentAsksBadge(props.rule.recent_asks ?? 0)}
          </span>
        </Show>
        <Show when={!props.rule.supported}>
          <span class="den-settings-badge" data-testid="detection-rule-unsupported">
            {DETECTIONS_COPY.unsupportedBadge}
          </span>
        </Show>
      </div>
      <Show when={!props.rule.supported && props.rule.unsupported_reason}>
        <p class="den-settings-hint">{props.rule.unsupported_reason}</p>
      </Show>
    </li>
  );
}

/** Device detection packs load independently from approval settings. */
export function DetectionsPanel(props: Props) {
  const query = createSurfaceQuery({
    name: "detection-packs",
    source: () => ({ client: props.client, key: "device" }),
    load: ({ client }) => client.listDetectionPacks(),
  });
  const packs = () => query.value()?.packs ?? [];
  const rejected = () => {
    const r = query.value()?.rejected;
    if (!r) return [];
    return Array.isArray(r) ? r : Object.values(r);
  };
  const loading = query.loading;
  const loadError = () => query.error() !== undefined;
  const setPacks = (change: (previous: DetectionPack[]) => DetectionPack[]) =>
    query.update((previous) => ({ packs: change(previous?.packs ?? []), rejected: previous?.rejected ?? {} }));
  const [busyId, setBusyId] = createSignal<string | undefined>();
  const [saveError, setSaveError] = createSignal<string | undefined>();
  const [removeError, setRemoveError] = createSignal<string | undefined>();
  const [pickerError, setPickerError] = createSignal<string | undefined>();
  const [importPath, setImportPath] = createSignal<string | null>(null);


  const onToggle = async (pack: DetectionPack, next: boolean) => {
    setBusyId(pack.id);
    setSaveError(undefined);
    setPacks((prev) =>
      prev.map((p) => (p.id === pack.id ? { ...p, enabled: next } : p)),
    );
    try {
      const updated = await props.client.updateDetectionPack(pack.id, { enabled: next });
      setPacks((prev) => prev.map((p) => (p.id === pack.id ? updated : p)));
    } catch {
      setPacks((prev) => prev.map((p) => (p.id === pack.id ? pack : p)));
      setSaveError(DETECTIONS_COPY.saveError);
    } finally {
      setBusyId(undefined);
    }
  };

  const onRemove = async (pack: DetectionPack) => {
    if (!(await confirmRemove(pack.label))) return;
    setBusyId(pack.id);
    setRemoveError(undefined);
    try {
      await props.client.deleteDetectionPack(pack.id);
      setPacks((prev) => prev.filter((p) => p.id !== pack.id));
    } catch {
      setRemoveError(DETECTIONS_COPY.removeError);
    } finally {
      setBusyId(undefined);
    }
  };

  const onAddPack = async () => {
    setPickerError(undefined);
    try {
      const folder = await pickProjectFolder();
      if (!folder) return;
      setImportPath(folder);
    } catch {
      setPickerError(DETECTIONS_COPY.pickerError);
    }
  };

  const onImported = (pack: DetectionPack) => {
    setPacks((prev) => {
      const rest = prev.filter((p) => p.id !== pack.id);
      return [pack, ...rest];
    });
    setImportPath(null);
  };

  return (
    <div class="den-detections-panel" data-testid="detections-panel">
      <p class="den-settings-hint">{DETECTIONS_COPY.intro}</p>
      <p class="den-settings-hint">{DETECTIONS_COPY.limits}</p>
      <p class="den-settings-hint">{DETECTIONS_COPY.levelNote}</p>
      <p class="den-settings-hint" data-testid="detection-egress-note">
        {DETECTIONS_COPY.egressNote}
      </p>

      <Show when={loadError()}>
        <p class="den-settings-warn" data-testid="detection-load-error" role="alert">
          {DETECTIONS_COPY.loadError}
        </p>
      </Show>
      <Show when={saveError()}>
        <p class="den-settings-warn" data-testid="detection-save-error" role="alert">
          {saveError()}
        </p>
      </Show>
      <Show when={removeError()}>
        <p class="den-settings-warn" data-testid="detection-remove-error" role="alert">
          {removeError()}
        </p>
      </Show>
      <Show when={pickerError()}>
        <p class="den-settings-warn" data-testid="detection-picker-error" role="alert">
          {pickerError()}
        </p>
      </Show>

      {/* Show overlay diagnostics before the installed pack list. */}
      <Show when={!loadError() && rejected().length > 0}>
        <section class="den-approvals-block" data-testid="detection-rejected">
          <h3 class="den-settings-overline">
            {DETECTIONS_COPY.rejectedHeading}
          </h3>
          <p class="den-settings-hint">{DETECTIONS_COPY.rejectedHint}</p>
          <ul class="den-detections-packs">
            <For each={rejected()}>
              {(row, index) => (
                <li
                  class="den-detections-pack"
                  data-testid={`detection-rejected-row-${index()}`}
                  data-code={row.code}
                >
                  <div class="den-detections-pack__copy">
                    <strong>
                      {DETECTIONS_COPY.rejectedRowLabel(row.id ?? "")}
                    </strong>
                    <span class="den-settings-hint">
                      {DETECTIONS_COPY.rejectedReason(row.code)}
                    </span>
                    <Show when={row.detail}>
                      <span class="den-settings-hint">{row.detail}</span>
                    </Show>
                  </div>
                </li>
              )}
            </For>
          </ul>
        </section>
      </Show>

      <Show when={!loadError() && !loading() && packs().length === 0}>
        <p class="den-settings-hint" data-testid="detection-empty">
          {DETECTIONS_COPY.emptyState}
        </p>
        <DenButton
          variant="secondary"
          data-testid="detection-add-pack"
          onClick={() => void onAddPack()}
          data-tip={DETECTIONS_COPY.addPackHint}
        >
          {DETECTIONS_COPY.addPack}
        </DenButton>
      </Show>

      <Show when={!loadError() && packs().length > 0}>
        <section class="den-approvals-block">
          <div class="den-detections-packs-head">
            <h3 class="den-settings-overline">{DETECTIONS_COPY.packsHeading}</h3>
            <DenButton
              variant="secondary"
              data-testid="detection-add-pack"
              disabled={busyId() != null}
              onClick={() => void onAddPack()}
              data-tip={DETECTIONS_COPY.addPackHint}
            >
              {DETECTIONS_COPY.addPack}
            </DenButton>
          </div>

          <ul class="den-detections-packs">
            <For each={packs()}>
              {(pack) => (
                <li
                  class="den-detections-pack"
                  data-testid={`detection-pack-${pack.id}`}
                  data-enabled={pack.enabled}
                  data-source={pack.source}
                  data-tip={removeHint(pack)}
                >
                  <div class="den-detections-pack__row">
                    <DenCheckbox
                      checked={pack.enabled}
                      disabled={busyId() === pack.id}
                      data-testid={`detection-pack-${pack.id}-toggle`}
                      onChange={(e) =>
                        void onToggle(pack, e.currentTarget.checked)
                      }
                    >
                      <span class="sr-only">{pack.label}</span>
                    </DenCheckbox>
                    <div class="den-detections-pack__copy">
                      <div class="den-detections-pack__label-row">
                        <strong>{pack.label}</strong>
                        <span class="den-settings-badge">
                          {sourceBadge(pack.source)}
                        </span>
                        <Show when={packHoldsConnections(pack)}>
                          <span
                            class="den-settings-badge"
                            data-testid={`detection-pack-${pack.id}-egress`}
                          >
                            {DETECTIONS_COPY.egressBadge}
                          </span>
                        </Show>
                        <span class="den-settings-hint">
                          {DETECTIONS_COPY.rulesLabel(pack.rules.length)}
                        </span>
                      </div>
                      <p class="den-settings-hint">{pack.description}</p>
                      <Show when={pack.provider_pack_id}>
                        {(providerID) => (
                          <p
                            class="den-settings-hint"
                            data-testid={`detection-pack-${pack.id}-provider`}
                          >
                            {DETECTIONS_COPY.providerHint(providerID())}
                          </p>
                        )}
                      </Show>
                      <Show when={(pack.equivalent_binaries ?? []).length > 0}>
                        <p
                          class="den-settings-hint"
                          data-testid={`detection-pack-${pack.id}-also-covers`}
                        >
                          {DETECTIONS_COPY.alsoCovers(
                            pack.equivalent_binaries ?? [],
                          )}
                        </p>
                      </Show>
                    </div>
                    <Show when={pack.removable}>
                      <DenButton
                        variant="danger"
                        data-testid={`detection-pack-${pack.id}-remove`}
                        disabled={busyId() === pack.id}
                        onClick={() => void onRemove(pack)}
                      >
                        {DETECTIONS_COPY.remove}
                      </DenButton>
                    </Show>
                  </div>

                  <Show when={(pack.load_warnings ?? []).length > 0}>
                    <div
                      class="den-detections-pack__warnings"
                      data-testid={`detection-pack-${pack.id}-warnings`}
                    >
                      <p class="den-settings-hint">
                        {DETECTIONS_COPY.loadWarningsHint}
                      </p>
                      <ul>
                        <For each={pack.load_warnings ?? []}>
                          {(warning) => (
                            <li class="den-settings-hint">{warning}</li>
                          )}
                        </For>
                      </ul>
                    </div>
                  </Show>

                  <details class="den-detections-pack__rules">
                    <summary>
                      <span class="den-disclosure-caret" aria-hidden="true" />
                      {DETECTIONS_COPY.rulesLabel(pack.rules.length)}
                    </summary>
                    <ul>
                      <For each={sortedRules(pack.rules)}>
                        {(rule) => <RuleRow rule={rule} />}
                      </For>
                    </ul>
                  </details>
                </li>
              )}
            </For>
          </ul>
        </section>
      </Show>

      <p class="den-settings-hint">{DETECTIONS_COPY.editHint}</p>

      <Show when={importPath()} keyed>
        {(path) => (
          <DetectionPackImportDialog
            client={props.client}
            path={path}
            onCancel={() => setImportPath(null)}
            onImported={onImported}
          />
        )}
      </Show>
    </div>
  );
}
