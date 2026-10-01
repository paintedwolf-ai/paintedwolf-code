/** Typography settings for interface and code roles. */
import { For, Show, createMemo, createSignal } from "solid-js";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { DenInput } from "../../primitives/DenInput.tsx";
import {
  bundledFontsInRole,
  isGuaranteedFont,
  resolveFontStack,
  sanitizeFontFamily,
  SYSTEM_FONT,
} from "../../../fonts/font-catalog.ts";
import type { DenFontRole } from "../../../fonts/font-catalog.generated.ts";
import { probeFontAvailability } from "../../../fonts/font-availability.ts";
import {
  fontSelection,
  setFontSelection,
} from "../../../settings/appearance/font-prefs.ts";
import { PRODUCT_TEXT_SCALES } from "../../../../shared/app-state-types.ts";
import {
  productTextScale,
  setProductTextScale,
} from "../../../settings/appearance/text-scale-prefs.ts";
import {
  settingAnchor,
  settingLabel,
  type SettingId,
} from "../../../settings/settings-registry.ts";

const CUSTOM = "custom";

const ROLES: readonly {
  role: DenFontRole;
  setting: SettingId;
  hint: string;
  systemLabel: string;
  sample: string;
  placeholder: string;
}[] = [
  {
    role: "ui",
    setting: "interface-font",
    hint: "Menus, lists, chat, and every label in the window.",
    systemLabel: "System font",
    sample: "The quick brown fox jumps over the lazy dog",
    placeholder: "e.g. Helvetica Neue",
  },
  {
    role: "mono",
    setting: "code-font",
    hint: "The editor, diffs, tool output, and file paths.",
    systemLabel: "System mono",
    sample: "const wolf = { paws: 4 }; // 0O1lI",
    placeholder: "e.g. Cascadia Code",
  },
];

export function TypographySettings() {
  return (
    <div class="den-settings-pref-group" data-testid="typography-settings">
      <h3 class="den-settings-pref-group-title" {...chromeProps()}>Type</h3>
      <div class="den-settings-pref-row" {...settingAnchor("text-size")}>
        <div class="den-settings-pref-copy">
          <span class="den-settings-pref-label">{settingLabel("text-size")}</span>
          <p class="den-settings-hint">
            Adjusts the app&apos;s reading size in addition to your Mac&apos;s accessibility text size.
          </p>
        </div>
        <DenSelect
          data-testid="product-text-scale"
          aria-label={settingLabel("text-size")}
          value={String(productTextScale())}
          options={PRODUCT_TEXT_SCALES.map((value) => ({
            value: String(value),
            label: value === 1 ? "Default" : `${Math.round(value * 100)}%`,
          }))}
          onValueChange={(value) => {
            const next = PRODUCT_TEXT_SCALES.find((candidate) => String(candidate) === value);
            if (next !== undefined) void setProductTextScale(next);
          }}
        />
      </div>
      <For each={ROLES}>{(spec) => <FontRow {...spec} />}</For>
    </div>
  );
}

function FontRow(props: {
  role: DenFontRole;
  setting: SettingId;
  hint: string;
  systemLabel: string;
  sample: string;
  placeholder: string;
}) {
  const selection = () => fontSelection(props.role);
  const bundled = createMemo(() => bundledFontsInRole(props.role));

  const isCustom = () => !isGuaranteedFont(props.role, selection());

  // Keep the custom field open until its value is saved.
  const [customOpen, setCustomOpen] = createSignal(isCustom());
  const showCustom = () => customOpen() || isCustom();
  const selectValue = () => (showCustom() ? CUSTOM : selection());

  // Keep the draft independent from saved preference updates.
  const [draft, setDraft] = createSignal(isCustom() ? selection() : "");

  const description = () => {
    const found = bundled().find((f) => f.family === selection());
    if (found) return found.description;
    if (selection() === SYSTEM_FONT) return props.hint;
    return "";
  };

  // Probe only user-provided font names.
  const availability = createMemo(() => {
    if (!isCustom()) return "available" as const;
    return probeFontAvailability(selection());
  });

  const label = () => settingLabel(props.setting);
  const previewStyle = () => ({
    "font-family": resolveFontStack(props.role, selection()),
  });

  return (
    <>
      <div class="den-settings-pref-row" {...settingAnchor(props.setting)}>
        <div class="den-settings-pref-copy">
          <span class="den-settings-pref-label">{label()}</span>
          <p
            class="den-settings-hint"
            style={previewStyle()}
            data-testid={`font-preview-${props.role}`}
          >
            {props.sample}
          </p>
          <Show when={description()}>
            <p class="den-settings-hint">{description()}</p>
          </Show>
        </div>
        <div class="den-settings-subsection">
          <DenSelect
            data-testid={`font-select-${props.role}`}
            aria-label={label()}
            value={selectValue()}
            options={[
              ...bundled().map((font) => ({
                value: font.family,
                label: font.family,
              })),
              { value: SYSTEM_FONT, label: props.systemLabel },
              { value: CUSTOM, label: "Custom…" },
            ]}
            onValueChange={(next) => {
              if (next === CUSTOM) {
                setCustomOpen(true);
                // Save only usable font names.
                const typed = sanitizeFontFamily(draft());
                if (typed) void setFontSelection(props.role, typed);
                return;
              }
              setCustomOpen(false);
              void setFontSelection(props.role, next);
            }}
          />
          <Show when={showCustom()}>
            <DenInput
              data-testid={`font-custom-${props.role}`}
              aria-label={`${label()} family name`}
              value={draft()}
              placeholder={props.placeholder}
              onInput={(e) => setDraft(e.currentTarget.value)}
              onChange={(e) => {
                const typed = sanitizeFontFamily(e.currentTarget.value);
                if (typed) void setFontSelection(props.role, typed);
              }}
            />
          </Show>
        </div>
      </div>

      <Show when={availability() === "unavailable"}>
        <p
          class="den-settings-hint den-settings-pref-note"
          data-testid={`font-missing-${props.role}`}
        >
          <strong>{selection()}</strong> is not installed on this machine, so
          text is rendering in the fallback. Your choice is kept and returns on a
          machine that has it.
        </p>
      </Show>
    </>
  );
}
