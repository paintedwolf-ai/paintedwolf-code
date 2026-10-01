import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { NavFold } from "../nav/NavFold.tsx";
import { SettingsNavSidebar } from "../settings/SettingsNavSidebar.tsx";

/**
 * Non-interactive copy of the Shell's Settings fold for the welcome page. The
 * selected row is AI providers, the section the welcome copy names.
 */
export function OnboardingSettingsTeachNav() {
  return (
    <div
      class="den-shell-aside-main onboarding-gate__teach-aside"
      // Decorative clone of live Settings chrome — no focus or clicks.
      inert
      aria-hidden="true"
      data-testid="onboarding-welcome-settings-dock"
    >
      <div class="den-shell-nav-dock">
        <NavFold open>
          <SettingsNavSidebar
            section="providers"
            onSectionChange={() => undefined}
          />
        </NavFold>
        <div class="den-shell-nav-dock-row">
          <button
            type="button"
            class="den-shell-dock-link den-shell-dock-link-active"
            aria-expanded="true"
            tabindex={-1}
          >
            <ThemeIcon
              slot="settings"
              class="den-shell-dock-link__icon"
              size={15}
            />
            <span class="den-shell-dock-link__label">Settings</span>
          </button>
        </div>
      </div>
    </div>
  );
}
