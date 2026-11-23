import { ThemeIcon } from "../primitives/ThemeIcon.tsx";
import { NavFold } from "../nav/NavFold.tsx";
import { SettingsNavSidebar } from "../settings/SettingsNavSidebar.tsx";

export function OnboardingSettingsTeachNav() {
  return (
    <div
      class="den-shell-aside-main onboarding-gate__teach-aside"
      inert
      aria-hidden="true"
      data-testid="onboarding-welcome-settings-dock"
    >
      <div class="den-shell-nav-dock">
        <NavFold open>
          <SettingsNavSidebar section="providers" />
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
