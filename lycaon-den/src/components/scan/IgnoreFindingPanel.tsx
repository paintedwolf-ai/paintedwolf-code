import { For, Show, createMemo, createSignal } from "solid-js";
import type { FindingIgnoreEntry, FindingLedgerEntry } from "../../api/types.ts";
import { FINDING_IGNORE_JUSTIFICATION_LABELS } from "../../lib/finding-ignore-justification.generated.ts";
import {
  IGNORE_EXPIRY_CHOICES,
  expiryDate,
  ignoreScopesFor,
  ignoreYamlPreview,
} from "../../lib/finding-ignore-scopes.ts";
import { AnchoredSurface } from "../primitives/AnchoredSurface.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { DenRadioControl } from "../primitives/DenRadio.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";

type Props = {
  entries: readonly FindingLedgerEntry[];
  /** The overlay file the host writes, as the catalog reports it. */
  path?: string;
  anchor: () => HTMLElement | undefined;
  pending: boolean;
  onCommit: (entries: FindingIgnoreEntry[]) => void;
  onDismiss: () => void;
};

export function IgnoreFindingPanel(props: Props) {
  const scopes = createMemo(() => ignoreScopesFor(props.entries));
  const [scopeId, setScopeId] = createSignal<string | null>(null);
  const [reason, setReason] = createSignal("");
  const [expiryDays, setExpiryDays] = createSignal<number | null>(90);
  const [justification, setJustification] = createSignal("");

  const scope = () => {
    const chosen = scopes().find((candidate) => candidate.id === scopeId());
    return chosen ?? scopes()[0];
  };

  const draft = createMemo<FindingIgnoreEntry[]>(() => {
    const chosen = scope();
    if (!chosen) return [];
    const expires = expiryDate(expiryDays(), new Date());
    const vex = chosen.advisory ? justification().trim() : "";
    return chosen.entries.map((entry) => ({
      ...entry,
      reason: reason().trim(),
      ...(expires ? { expires_on: expires } : {}),
      ...(vex ? { justification: vex as FindingIgnoreEntry["justification"] } : {}),
    }));
  });

  const ready = () => reason().trim().length > 0 && draft().length > 0;

  return (
    <AnchoredSurface
      class="den-menu-surface"
      role="dialog"
      ariaLabel="Ignore findings"
      tabIndex={-1}
      anchor={props.anchor}
      preferredSide="bottom"
      align="end"
      dismissOnScroll
      onDismiss={props.onDismiss}
      onKeyDown={(event: KeyboardEvent) => {
        if (event.key !== "Escape") return;
        event.preventDefault();
        event.stopPropagation();
        props.onDismiss();
      }}
    >
      <div class="den-finding-ignore__body" data-testid="ignore-panel">
        <p class="den-finding-ignore__group">Ignore</p>
        <For each={scopes()}>
          {(option) => (
            <label class="den-finding-ignore__row">
              <DenRadioControl
                name="ignore-scope"
                checked={scope()?.id === option.id}
                data-testid={`ignore-scope-${option.id}`}
                onChange={() => setScopeId(option.id)}
              />
              <span class="den-finding-ignore__row-text">
                <span class="den-finding-ignore__row-label">{option.label}</span>
                <span class="den-finding-ignore__row-meta">{option.summary}</span>
              </span>
            </label>
          )}
        </For>

        <p class="den-finding-ignore__group">Reason</p>
        <DenInput
          type="text"
          class="den-finding-ignore__reason"
          placeholder="Why this stays"
          aria-label="Reason"
          data-testid="ignore-reason"
          value={reason()}
          onInput={(event) => setReason(event.currentTarget.value)}
        />

        <p class="den-finding-ignore__group">Lapses after</p>
        <div class="den-finding-ignore__choices" role="group" aria-label="Lapses after">
          <For each={IGNORE_EXPIRY_CHOICES}>
            {(choice) => (
              <button
                type="button"
                class="den-browse-filter-toggle"
                classList={{ "den-browse-filter-toggle--on": expiryDays() === choice.days }}
                aria-pressed={expiryDays() === choice.days}
                data-testid={`ignore-expiry-${choice.days ?? "never"}`}
                onClick={() => setExpiryDays(choice.days)}
              >
                {choice.label}
              </button>
            )}
          </For>
        </div>

        {/* Only advisory entries can carry a VEX justification. */}
        <Show when={scope()?.advisory}>
          <p class="den-finding-ignore__group">Why it does not apply</p>
          <DenSelect
            class="den-finding-ignore__justification"
            aria-label="Why it does not apply"
            data-testid="ignore-justification"
            value={justification()}
            onValueChange={setJustification}
            options={[
              { value: "", label: "Not stated — keeps this out of the VEX export" },
              ...Object.entries(FINDING_IGNORE_JUSTIFICATION_LABELS).map(([value, label]) => ({
                value,
                label,
              })),
            ]}
          />
        </Show>

        <Show when={ready()}>
          <p class="den-finding-ignore__group">Writes to {props.path ?? "the project's ignore file"}</p>
          <pre class="den-finding-ignore__preview" data-testid="ignore-preview">
            {ignoreYamlPreview(draft(), props.path)}
          </pre>
        </Show>

        <div class="den-finding-ignore__actions">
          <DenButton variant="secondary" compact onClick={props.onDismiss}>
            Cancel
          </DenButton>
          <DenButton
            variant="primary"
            compact
            disabled={!ready() || props.pending}
            data-testid="ignore-commit"
            onClick={() => props.onCommit(draft())}
          >
            {props.pending ? "Ignoring…" : "Ignore"}
          </DenButton>
        </div>
      </div>
    </AnchoredSurface>
  );
}
