import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { Show } from "solid-js";
import type { JSX } from "solid-js";
import { chromeProps } from "../../styling/ui-chrome.ts";

/** Icon + title header row shared by settings/context editors. */
export function SettingsEditorTitle(props: {
  icon: JSX.Element;
  children: JSX.Element;
  optional?: boolean;
  /** e.g. "Project" on Configuration panels that mirror Settings. */
  scopeBadge?: string;
}) {
  return (
    <div class="den-settings-title-row" {...chromeProps()}>
      <span class="den-settings-title-icon" aria-hidden="true">
        {props.icon}
      </span>
      <h2>{props.children}</h2>
      <Show when={props.scopeBadge}>
        {(badge) => (
          <span class="den-settings-scope-badge" data-testid="settings-scope-badge">
            {badge()}
          </span>
        )}
      </Show>
      <Show when={props.optional}>
        <span class="den-settings-optional-pill">optional</span>
      </Show>
    </div>
  );
}

export function ProvidersIcon() {
  return <ThemeIcon slot="settings-providers" size={18} />;
}

export function ApprovalsIcon() {
  return <ThemeIcon slot="settings-approvals" size={18} />;
}

export function EditReviewIcon() {
  return <ThemeIcon slot="edit" size={18} />;
}

export function BudgetsIcon() {
  return <ThemeIcon slot="settings-budgets" size={18} />;
}

export function CostIcon() {
  return <ThemeIcon slot="stage-cost" size={18} />;
}

export function SecretsIcon() {
  return <ThemeIcon slot="settings-secrets" size={18} />;
}

export function McpIcon() {
  return <ThemeIcon slot="settings-mcp" size={18} />;
}

export function HostResourcesIcon() {
  return <ThemeIcon slot="settings-host-resources" size={18} />;
}

export function WebResearchIcon() {
  return <ThemeIcon slot="settings-web-research" size={18} />;
}

export function SecurityScannersIcon(props?: { size?: number }) {
  return <ThemeIcon slot="settings-scanners" size={props?.size ?? 18} />;
}

export function GeneralIcon() {
  return <ThemeIcon slot="settings-general" size={18} />;
}

export function AdvancedIcon() {
  return <ThemeIcon slot="settings-advanced" size={18} />;
}

export function TestsIcon() {
  return <ThemeIcon slot="settings-tests" size={18} />;
}

export function TrustIcon() {
  return <ThemeIcon slot="settings-trust" size={18} />;
}
