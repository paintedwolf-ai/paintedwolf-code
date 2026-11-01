import { For, Show } from "solid-js";
import type {
  ExtensionDiagnostic,
  ExtensionPackSummary,
  ExtensionPackUpdateStatus,
  ExtensionUnitDetail,
  ExtensionUnitSummary,
} from "../../../api/types.ts";
import {
  packRowChips,
  unitStatusChip,
  type ExtensionChip,
} from "../../../settings/extensions/extensions-chips.ts";
import { EXTENSIONS_SETTINGS_COPY as C } from "../../../settings/extensions/extensions-settings-copy.ts";
import { formatSentenceCase } from "../../../format/format-sentence-case.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";

export function extTestId(prefix: string, id: string): string {
  return `${prefix}-${encodeURIComponent(id)}`;
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

export function ExtensionChipBadge(props: {
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

export function packSourceLabel(pack: ExtensionPackSummary): string {
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

export function countLabel(n: number, singular: string, plural: string): string {
  return n === 1 ? `1 ${singular}` : `${n} ${plural}`;
}

export const KIND_FILTERS: { id: string; label: string }[] = [
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

export function unitKindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind;
}

export type ExtensionPackDetailCardProps = {
  pack: ExtensionPackSummary;
  diagnostics: ExtensionDiagnostic[];
  packProvides: ExtensionUnitSummary[];
  updatePlanStatus?: ExtensionPackUpdateStatus;
  actionsDisabled: boolean;
  onOpenScanners?: () => void;
  onReloadPack: (pack: ExtensionPackSummary) => void;
  onBeginUpdate: (pack: ExtensionPackSummary) => void;
  onConfirmUpdate: (pack: ExtensionPackSummary) => void;
  onRemovePack: (pack: ExtensionPackSummary) => void;
};

export function ExtensionPackDetailCard(props: ExtensionPackDetailCardProps) {
  return (
    <article
      class="den-settings-provider-card"
      data-testid="extensions-pack-detail"
    >
      <header class="den-settings-provider-head">
        <strong>{props.pack.name}</strong>
        <For each={packRowChips(props.pack, props.diagnostics)}>
          {(chip) => (
            <ExtensionChipBadge
              chip={chip}
              onOpenScanners={props.onOpenScanners}
              onReloadFromDisk={
                props.pack.kind === "path" && props.pack.needs_reload
                  ? () => props.onReloadPack(props.pack)
                  : undefined
              }
            />
          )}
        </For>
        <Show when={props.pack.kind === "git" && props.pack.removable}>
          <DenButton
            variant="secondary"
            compact
            disabled={props.actionsDisabled}
            data-testid="extensions-update-pack"
            onClick={() => props.onBeginUpdate(props.pack)}
          >
            {C.updatePack}
          </DenButton>
        </Show>
        <Show when={props.pack.kind === "path" && props.pack.removable}>
          <DenButton
            variant="secondary"
            compact
            disabled={props.actionsDisabled}
            data-testid="extensions-reload-pack"
            onClick={() => props.onReloadPack(props.pack)}
          >
            {C.reloadFromDisk}
          </DenButton>
        </Show>
        <DenButton
          variant="link"
          class="den-settings-provider-remove"
          disabled={props.actionsDisabled || !props.pack.removable}
          data-tip={!props.pack.removable ? C.removeDisabledStock : undefined}
          data-testid="extensions-remove-pack"
          onClick={() => props.onRemovePack(props.pack)}
        >
          {C.remove}
        </DenButton>
      </header>
      <p class="den-settings-hint">{packSourceLabel(props.pack)}</p>
      <dl class="den-ext-version-facts" data-testid="extensions-pack-version-facts">
        <div>
          <dt>{C.version}</dt>
          <dd>
            <code>{props.pack.version}</code>
          </dd>
        </div>
        <div>
          <dt>{C.installationState}</dt>
          <dd>
            {props.pack.installation_state} · {props.pack.installation_scope}
          </dd>
        </div>
        <div>
          <dt>{C.extensionApi}</dt>
          <dd>
            <code>{props.pack.extension_api}</code>
          </dd>
        </div>
        <Show when={props.pack.resolved_revision}>
          <div>
            <dt>{C.revision}</dt>
            <dd>
              <code>{props.pack.resolved_revision}</code>
            </dd>
          </div>
        </Show>
        <Show when={props.pack.integrity}>
          <div>
            <dt>{C.integrity}</dt>
            <dd>
              <code>{props.pack.integrity}</code>
            </dd>
          </div>
        </Show>
      </dl>
      <Show when={props.diagnostics.length > 0}>
        <h4 class="den-ext-detail-section">{C.diagnosticsHeading}</h4>
        <ul class="den-ext-provides" data-testid="extensions-pack-diagnostics">
          <For each={props.diagnostics}>
            {(d) => (
              <li>
                <span
                  class={`den-ext-diagnostic-severity den-ext-diagnostic-severity-${d.severity}`}
                >
                  {formatSentenceCase(d.severity)}
                </span>{" "}
                {d.message}
                <Show when={d.unit_id}>
                  {" "}
                  — <code>{d.unit_id}</code>
                </Show>
              </li>
            )}
          </For>
        </ul>
      </Show>
      <Show when={Object.entries(props.pack.dependencies ?? {}).length > 0}>
        <h4 class="den-ext-detail-section">{C.dependencies}</h4>
        <ul class="den-ext-provides" data-testid="extensions-pack-dependencies">
          <For
            each={Object.entries(props.pack.dependencies ?? {}).sort(([a], [b]) =>
              a.localeCompare(b),
            )}
          >
            {([id, constraint]) => (
              <li>
                <code>{id}</code> <span>{constraint}</span>
              </li>
            )}
          </For>
        </ul>
      </Show>
      <Show when={props.updatePlanStatus} keyed>
        {(status) => (
          <div data-testid="extensions-update-plan">
            <p class="den-settings-hint">{status.message}</p>
            <ul class="den-ext-provides">
              <For each={status.changes}>
                {(change) => (
                  <li>
                    <code>{change.pack_id}</code> — {change.kind}
                    {change.candidate_version ? ` → ${change.candidate_version}` : ""}
                  </li>
                )}
              </For>
            </ul>
            <Show when={status.available}>
              <DenButton
                variant="primary"
                compact
                disabled={props.actionsDisabled}
                data-testid="extensions-update-confirm"
                onClick={() => props.onConfirmUpdate(props.pack)}
              >
                {C.confirmUpdate}
              </DenButton>
            </Show>
          </div>
        )}
      </Show>
      <Show
        when={
          (props.pack.unmet_requires_scanners?.length ?? 0) > 0
            ? props.pack.unmet_requires_scanners
            : null
        }
        keyed
      >
        {(scannerIds) => (
          <div class="den-ext-mcp-callout" data-testid="extensions-needs-scanner">
            <p class="den-ext-mcp-callout__title">{C.needsScannerTitle}</p>
            <p class="den-settings-hint">{C.needsScannerHint}</p>
            <ul>
              <For each={scannerIds}>
                {(id) => (
                  <li>
                    <code>{id}</code>
                  </li>
                )}
              </For>
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
      <Show when={props.pack.needs_reload}>
        <div class="den-ext-mcp-callout" data-testid="extensions-needs-reload">
          <p class="den-ext-mcp-callout__title">{C.packNeedsReloadTitle}</p>
          <p class="den-settings-hint">{C.packNeedsReloadHint}</p>
        </div>
      </Show>
      <Show when={props.pack.blocked_reason === "invalid"}>
        <div class="den-ext-mcp-callout" data-testid="extensions-pack-invalid">
          <p class="den-ext-mcp-callout__title">{C.packInvalidTitle}</p>
          <p class="den-settings-hint">{C.packInvalidHint}</p>
        </div>
      </Show>
      <Show when={props.pack.blocked_reason === "integrity"}>
        <div class="den-ext-mcp-callout" data-testid="extensions-pack-integrity">
          <p class="den-ext-mcp-callout__title">{C.packIntegrityTitle}</p>
          <p class="den-settings-hint">{C.packIntegrityHint}</p>
        </div>
      </Show>
      <div>
        <h4 class="den-ext-detail-section">{C.providesHeading}</h4>
        <Show
          when={props.packProvides.length > 0}
          fallback={<p class="den-settings-hint">{C.emptyProvides}</p>}
        >
          <ul class="den-ext-provides" data-testid="extensions-pack-provides">
            <For each={props.packProvides}>
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
}

export type ExtensionUnitDetailCardProps = {
  detail: ExtensionUnitDetail;
  isProject: boolean;
  actionsDisabled: boolean;
  packKinds: Record<string, ExtensionPackSummary["kind"]>;
  packNames: Record<string, string>;
  onSetUnitDisabled: (unitId: string, disabled: boolean) => void;
  onSetUnitOwn: (unitId: string, packId: string | null) => void;
};

export function ExtensionUnitDetailCard(props: ExtensionUnitDetailCardProps) {
  return (
    <article
      class="den-settings-provider-card"
      data-testid="extensions-unit-detail"
    >
      <header class="den-settings-provider-head">
        <strong>{props.detail.title || props.detail.id}</strong>
        <ExtensionChipBadge
          chip={unitStatusChip({
            status: props.detail.status,
            contributions: props.detail.contributions,
            packKinds: props.packKinds,
          })}
        />
        <Show
          when={
            props.detail.status === "disabled" &&
            (!props.isProject || props.detail.project_disable_allowed)
          }
        >
          <DenButton
            variant="primary"
            compact
            disabled={props.actionsDisabled}
            data-testid="extensions-unit-reenable"
            onClick={() => props.onSetUnitDisabled(props.detail.id, false)}
          >
            {C.reEnable}
          </DenButton>
        </Show>
        <Show
          when={
            props.detail.status !== "disabled" &&
            (!props.isProject || props.detail.project_disable_allowed)
          }
        >
          <DenButton
            variant="danger"
            compact
            disabled={props.actionsDisabled}
            data-testid="extensions-unit-disable"
            onClick={() => props.onSetUnitDisabled(props.detail.id, true)}
          >
            {C.disable}
          </DenButton>
        </Show>
        <Show when={props.isProject && !props.detail.project_disable_allowed}>
          <span class="den-settings-hint">{C.deviceControlledUnit}</span>
        </Show>
        <Show when={!props.isProject && props.detail.contributions.length > 1}>
          <For each={props.detail.contributions}>
            {(prov) => (
              <DenButton
                variant={
                  props.detail.winner_pack_id === prov.pack_id
                    ? "primary"
                    : "secondary"
                }
                compact
                disabled={props.actionsDisabled}
                data-testid={extTestId("extensions-unit-own", prov.pack_id)}
                onClick={() => props.onSetUnitOwn(props.detail.id, prov.pack_id)}
              >
                {C.selectionPrefix} {props.packNames[prov.pack_id] ?? prov.pack_id}
              </DenButton>
            )}
          </For>
          <Show
            when={props.detail.status === "owned" || props.detail.winner_pack_id}
          >
            <DenButton
              variant="secondary"
              compact
              disabled={props.actionsDisabled}
              data-testid="extensions-unit-clear-own"
              onClick={() => props.onSetUnitOwn(props.detail.id, null)}
            >
              {C.clearSelection}
            </DenButton>
          </Show>
        </Show>
      </header>
      <p class="den-settings-hint">
        {props.detail.id} · {unitKindLabel(props.detail.kind)}
      </p>

      <div>
        <h4 class="den-ext-detail-section">{C.resolution}</h4>
        <ol class="den-ext-resolution" data-testid="extensions-unit-resolution">
          <For each={props.detail.contributions}>
            {(prov, i) => {
              const winner =
                props.detail.winner_pack_id === prov.pack_id &&
                props.detail.status !== "conflict" &&
                props.detail.status !== "disabled";
              return (
                <li>
                  <span
                    class="den-ext-resolution-rank"
                    classList={{ "den-ext-resolution-rank--winner": winner }}
                  >
                    {winner ? C.winnerLabel : C.contributionLabel}
                  </span>
                  <span>{props.packNames[prov.pack_id] ?? prov.pack_id}</span>
                  <code>{prov.pack_id}</code>
                  <ExtensionChipBadge
                    chip={{
                      label:
                        props.packKinds[prov.pack_id] === "stock"
                          ? "Stock"
                          : props.detail.status === "owned" && winner
                            ? "Selected"
                            : "Provided",
                      className:
                        props.packKinds[prov.pack_id] === "stock"
                          ? "stock"
                          : props.detail.status === "owned" && winner
                            ? "selected"
                            : "add",
                    }}
                  />
                  <Show when={i() === props.detail.contributions.length - 1}>
                    <Show when={props.detail.status === "disabled"}>
                      <div class="den-ext-resolution-final">
                        <strong>{C.effectiveDisabled}</strong>{" "}
                        {C.effectiveDisabledDetail}{" "}
                        <ExtensionChipBadge
                          chip={{ label: "Disabled", className: "disabled" }}
                        />
                      </div>
                    </Show>
                    <Show when={props.detail.status === "conflict"}>
                      <div class="den-ext-resolution-final">
                        <strong>{C.effectiveBlocked}</strong>{" "}
                        {C.effectiveConflictDetail}{" "}
                        <ExtensionChipBadge
                          chip={{ label: "Conflict", className: "conflict" }}
                        />
                      </div>
                    </Show>
                  </Show>
                </li>
              );
            }}
          </For>
        </ol>
      </div>

      <Show when={props.detail.kind !== "skills"}>
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
              {props.detail.contributions
                .map(
                  (p) => `── ${p.pack_id} ──\n${p.content ?? "(no body)"}\n`,
                )
                .join("\n")}
            </Scrollport>
          </div>
          <div>
            <h4 class="den-ext-detail-section">{C.effectivePane}</h4>
            <Scrollport
              class="den-ext-body"
              classList={{
                "den-ext-body-disabled": props.detail.status === "disabled",
                "den-ext-body-conflict": props.detail.status === "conflict",
              }}
              contentAs="pre"
              contentClass="den-ext-body__content"
              axis="both"
              data-testid="extensions-unit-effective-body"
            >
              {props.detail.status === "conflict"
                ? C.conflictEffective
                : props.detail.status === "disabled"
                  ? C.disabledEffective
                  : props.detail.content || "(empty)"}
            </Scrollport>
          </div>
        </div>
      </Show>
    </article>
  );
}
