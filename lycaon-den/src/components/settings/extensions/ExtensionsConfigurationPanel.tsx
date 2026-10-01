import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { PreparedSurface } from "../../primitives/PreparedSurface.tsx";
import { createMemo, createSignal, For, Show } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type {
  ContributionConfigurationProperty,
  ExtensionConfigurationValue,
} from "../../../api/types.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenNumberInput } from "../../primitives/DenNumberInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { EXTENSIONS_SETTINGS_COPY as C } from "../../../settings/extensions/extensions-settings-copy.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";

type Props = {
  client: LycaonClient;
  expectedRevision: () => string;
  busy: () => boolean;
  onCommitted: () => void;
  onError: (message: string) => void;
};

function parseConfigurationID(id: string): { packId: string; name: string } {
  const cut = id.lastIndexOf(":");
  if (cut <= 0 || cut === id.length - 1) {
    throw new Error(`Invalid configuration property id: ${id}`);
  }
  return { packId: id.slice(0, cut), name: id.slice(cut + 1) };
}

export function ExtensionsConfigurationPanel(props: Props) {
  const [pending, setPending] = createSignal<Record<string, ExtensionConfigurationValue>>({});
  const [saving, setSaving] = createSignal(false);

  const query = createSurfaceQuery({
    name: "extension-configuration",
    source: () => ({ client: props.client, key: "device", revision: props.expectedRevision() }),
    load: ({ client }) => client.getContributions(),
  });
  const frame = query.value;
  const refetch = query.refresh;

  const properties = createMemo(() => frame()?.configuration ?? []);

  const byPack = createMemo(() => {
    const groups = new Map<string, ContributionConfigurationProperty[]>();
    for (const property of properties()) {
      const { packId } = parseConfigurationID(property.id);
      const group = groups.get(packId) ?? [];
      group.push(property);
      groups.set(packId, group);
    }
    return [...groups.entries()]
      .map(([packId, rows]) => ({
        packId,
        rows: [...rows].sort((a, b) => a.id.localeCompare(b.id)),
      }))
      .sort((a, b) => a.packId.localeCompare(b.packId));
  });

  const valueOf = (property: ContributionConfigurationProperty): ExtensionConfigurationValue =>
    (property.id in pending() ? pending()[property.id] : property.value) as ExtensionConfigurationValue;

  const dirty = createMemo(() => Object.keys(pending()).length > 0);

  const stage = (id: string, value: ExtensionConfigurationValue) =>
    setPending((prev) => ({ ...prev, [id]: value }));

  const discard = () => setPending({});

  const save = async () => {
    if (saving()) return;
    setSaving(true);
    const edits = pending();
    const packs = new Map<string, Record<string, ExtensionConfigurationValue>>();
    for (const [id, value] of Object.entries(edits)) {
      const { packId, name } = parseConfigurationID(id);
      const values = packs.get(packId) ?? {};
      values[name] = value;
      packs.set(packId, values);
    }
    try {
      const replacements: Record<string, Record<string, ExtensionConfigurationValue>> = {};
      for (const [packId, values] of packs) {
        const existing: Record<string, ExtensionConfigurationValue> = {};
        for (const property of properties()) {
          const id = parseConfigurationID(property.id);
          if (id.packId !== packId) continue;
          if (property.is_default && !(property.id in edits)) continue;
          existing[id.name] = valueOf(property);
        }
        replacements[packId] = { ...existing, ...values };
      }
      await props.client.updateExtensionConfiguration(
        { expected_revision: props.expectedRevision(), packs: replacements },
      );
      setPending((current) => Object.fromEntries(Object.entries(current).filter(([id, value]) =>
        !(id in edits) || JSON.stringify(value) !== JSON.stringify(edits[id]))));
      void refetch();
      props.onCommitted();
    } catch (err) {
      props.onError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <PreparedSurface name="extension-configuration" ready={query.ready}>
    <div data-testid="extensions-configuration">
      <Show when={query.error()}>{(error) => <p role="alert" class="den-settings-warn">{error()}</p>}</Show>
      <Show
        when={properties().length > 0}
        fallback={
          <Show when={query.value() !== undefined && !query.error()}><div class="den-ext-empty" data-testid="extensions-configuration-empty">
            <p class="den-ext-empty__title">{C.configurationEmptyTitle}</p>
            <p class="den-ext-empty__hint">{C.configurationEmptyHint}</p>
          </div></Show>
        }
      >
        <For each={byPack().map((group) => group.packId)}>
          {(packId) => (
            <section class="den-settings-pref-group" data-testid={`extensions-configuration-pack-${packId}`}>
              <h3 class="den-settings-pref-group-title" {...chromeProps()}>{packId}</h3>
              <For each={byPack().find((group) => group.packId === packId)?.rows.map((property) => property.id) ?? []}>
                {(id) => <ShowLatest when={properties().find((property) => property.id === id)}>
                  {(property) => (
                    <div class="den-settings-pref-row">
                      <div class="den-settings-pref-copy">
                        <span class="den-settings-pref-label">{parseConfigurationID(id).name}</span>
                        <p class="den-settings-hint">{property().description}</p>
                      </div>
                      <div class="den-settings-subsection">
                        <ConfigurationControl property={property()} value={valueOf(property())}
                          disabled={props.busy()} onChange={(value) => stage(id, value)} />
                      </div>
                    </div>
                  )}
                </ShowLatest>}
              </For>
            </section>
          )}
        </For>
        <Show when={dirty()}>
          <div
            class="den-settings-subsection"
            data-testid="extensions-configuration-actions"
          >
            <DenButton
              variant="primary"
              compact
              disabled={props.busy() || saving()}
              data-testid="extensions-configuration-save"
              onClick={() => void save()}
            >
              {C.configurationSave}
            </DenButton>
            <DenButton
              variant="ghost"
              compact
              disabled={props.busy() || saving()}
              data-testid="extensions-configuration-discard"
              onClick={discard}
            >
              {C.configurationDiscard}
            </DenButton>
          </div>
        </Show>
      </Show>
    </div>
    </PreparedSurface>
  );
}

function ConfigurationControl(props: {
  property: ContributionConfigurationProperty;
  value: ExtensionConfigurationValue;
  disabled: boolean;
  onChange: (value: ExtensionConfigurationValue) => void;
}) {
  const testId = `extensions-configuration-input-${props.property.id}`;
  return (
    <Show
      when={props.property.type !== "boolean"}
      fallback={
        <DenCheckbox
          data-testid={testId}
          aria-label={props.property.id}
          checked={props.value === true}
          disabled={props.disabled}
          onChange={(e) => props.onChange(e.currentTarget.checked)}
        />
      }
    >
      <Show
        when={props.property.type !== "enum"}
        fallback={
          <DenSelect
            data-testid={testId}
            aria-label={props.property.id}
            value={typeof props.value === "string" ? props.value : ""}
            disabled={props.disabled}
            options={(props.property.enum ?? []).map((option) => ({
              value: option,
              label: option,
            }))}
            onValueChange={props.onChange}
          />
        }
      >
        <Show
          when={props.property.type !== "number"}
          fallback={
            <DenNumberInput
              data-testid={testId}
              aria-label={props.property.id}
              value={typeof props.value === "number" ? String(props.value) : ""}
              min={props.property.min}
              max={props.property.max}
              disabled={props.disabled}
              onChange={(e) => {
                const next = Number(e.currentTarget.value);
                if (!Number.isNaN(next)) props.onChange(next);
              }}
            />
          }
        >
          <DenInput
            data-testid={testId}
            aria-label={props.property.id}
            value={configurationText(props.value)}
            disabled={props.disabled}
            placeholder={
              props.property.type === "string_list"
                ? C.configurationListPlaceholder
                : undefined
            }
            onChange={(e) =>
              props.onChange(
                props.property.type === "string_list"
                  ? splitList(e.currentTarget.value)
                  : e.currentTarget.value,
              )
            }
          />
        </Show>
      </Show>
    </Show>
  );
}

function configurationText(value: ExtensionConfigurationValue): string {
  if (Array.isArray(value)) return value.join(", ");
  return typeof value === "string" ? value : "";
}

function splitList(raw: string): string[] {
  return raw
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry !== "");
}
