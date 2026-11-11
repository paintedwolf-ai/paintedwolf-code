import { Show } from "solid-js";
import type { ExtensionPackSummary } from "../../../api/types.ts";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import type { AppNotice } from "../../../notices/notice-model.ts";
import { EXTENSIONS_SETTINGS_COPY as C } from "../../../settings/extensions/extensions-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";

export type ExtensionInstallModalsProps = {
  busy: () => boolean;
  actionsDisabled: () => boolean;
  addOpen: () => boolean;
  setAddOpen: (open: boolean) => void;
  addUrl: () => string;
  setAddUrl: (url: string) => void;
  addVersion: () => string;
  setAddVersion: (version: string) => void;
  addRef: () => string;
  setAddRef: (ref: string) => void;
  addError: () => AppNotice | undefined;
  onInstallFromGit: () => void;
  suiteInstallOpen: () => boolean;
  setSuiteInstallOpen: (open: boolean) => void;
  suiteInstallUrl: () => string;
  setSuiteInstallUrl: (url: string) => void;
  suiteInstallVersion: () => string;
  setSuiteInstallVersion: (version: string) => void;
  suiteInstallRef: () => string;
  setSuiteInstallRef: (ref: string) => void;
  suiteInstallError: () => AppNotice | undefined;
  onInstallSuite: () => void;
  profileOpen: () => boolean;
  setProfileOpen: (open: boolean) => void;
  profilePacks: () => ExtensionPackSummary[];
  profilePackId: () => string;
  setProfilePackId: (id: string) => void;
  profileName: () => string;
  setProfileName: (name: string) => void;
  profileError: () => AppNotice | undefined;
  onApplyProfile: () => void;
};

export function ExtensionInstallModals(props: ExtensionInstallModalsProps) {
  let addDialogRef: HTMLDivElement | undefined;
  let suiteDialogRef: HTMLDivElement | undefined;
  let profileDialogRef: HTMLDivElement | undefined;

  createModalFocusTrap(props.addOpen, () => addDialogRef, {
    onEscape: () => {
      if (!props.busy()) props.setAddOpen(false);
    },
  });
  createModalFocusTrap(props.suiteInstallOpen, () => suiteDialogRef, {
    onEscape: () => {
      if (!props.busy()) props.setSuiteInstallOpen(false);
    },
  });
  createModalFocusTrap(props.profileOpen, () => profileDialogRef, {
    onEscape: () => {
      if (!props.busy()) props.setProfileOpen(false);
    },
  });

  return (
    <>
      <Show when={props.addOpen()}>
        <div
          class="den-dialog-backdrop"
          data-testid="extensions-add-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget && !props.busy()) props.setAddOpen(false);
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={addDialogRef}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="ext-add-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="ext-add-title">{C.addDialogTitle}</h2>
            </header>
            <p class="den-dialog__hint">{C.addDialogBody}</p>
            <label class="den-dialog__label" for="ext-add-url">
              {C.addUrlLabel}
            </label>
            <DenInput
              id="ext-add-url"
              class="den-dialog__field"
              data-testid="extensions-add-url"
              value={props.addUrl()}
              onInput={(e) => props.setAddUrl(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-add-version">
              {C.addVersionLabel}
            </label>
            <DenInput
              id="ext-add-version"
              class="den-dialog__field"
              data-testid="extensions-add-version"
              value={props.addVersion()}
              placeholder="^1.0.0"
              onInput={(e) => props.setAddVersion(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-add-ref">
              {C.addRefLabel}
            </label>
            <DenInput
              id="ext-add-ref"
              class="den-dialog__field"
              data-testid="extensions-add-ref"
              value={props.addRef()}
              onInput={(e) => props.setAddRef(e.currentTarget.value)}
            />
            <InlineNotice notice={props.addError()} testId="extensions-add-error" />
            <footer class="den-dialog__footer">
              <DenButton
                variant="ghost"
                disabled={props.busy()}
                onClick={() => props.setAddOpen(false)}
              >
                {C.addCancel}
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="extensions-add-install"
                disabled={props.actionsDisabled()}
                onClick={props.onInstallFromGit}
              >
                {C.addInstall}
              </DenButton>
            </footer>
          </div>
        </div>
      </Show>

      <Show when={props.suiteInstallOpen()}>
        <div
          class="den-dialog-backdrop"
          data-testid="extensions-install-suite-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget && !props.busy()) {
              props.setSuiteInstallOpen(false);
            }
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={suiteDialogRef}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="ext-suite-install-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="ext-suite-install-title">{C.suiteInstallTitle}</h2>
            </header>
            <p class="den-dialog__hint">{C.suiteInstallBody}</p>
            <label class="den-dialog__label" for="ext-suite-install-url">
              {C.suiteInstallUrlLabel}
            </label>
            <DenInput
              id="ext-suite-install-url"
              class="den-dialog__field"
              data-testid="extensions-install-suite-url"
              value={props.suiteInstallUrl()}
              onInput={(e) => props.setSuiteInstallUrl(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-suite-install-version">
              {C.suiteInstallVersionLabel}
            </label>
            <DenInput
              id="ext-suite-install-version"
              class="den-dialog__field"
              data-testid="extensions-install-suite-version"
              value={props.suiteInstallVersion()}
              placeholder="^1.0.0"
              onInput={(e) => props.setSuiteInstallVersion(e.currentTarget.value)}
            />
            <label class="den-dialog__label" for="ext-suite-install-ref">
              {C.suiteInstallRefLabel}
            </label>
            <DenInput
              id="ext-suite-install-ref"
              class="den-dialog__field"
              data-testid="extensions-install-suite-ref"
              value={props.suiteInstallRef()}
              onInput={(e) => props.setSuiteInstallRef(e.currentTarget.value)}
            />
            <InlineNotice
              notice={props.suiteInstallError()}
              testId="extensions-install-suite-error"
            />
            <footer class="den-dialog__footer">
              <DenButton
                variant="ghost"
                disabled={props.busy()}
                onClick={() => props.setSuiteInstallOpen(false)}
              >
                {C.addCancel}
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="extensions-install-suite-submit"
                disabled={props.actionsDisabled()}
                onClick={props.onInstallSuite}
              >
                {C.suiteInstallSubmit}
              </DenButton>
            </footer>
          </div>
        </div>
      </Show>

      <Show when={props.profileOpen()}>
        <div
          class="den-dialog-backdrop"
          data-testid="extensions-profile-dialog"
          onClick={(e) => {
            if (e.target === e.currentTarget && !props.busy()) props.setProfileOpen(false);
          }}
        >
          <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
          <div
            ref={profileDialogRef}
            class="den-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="ext-profile-title"
          >
            <header class="den-dialog__header" {...chromeProps()}>
              <h2 id="ext-profile-title">{C.profileDialogTitle}</h2>
            </header>
            <p class="den-dialog__hint">{C.profileDialogBody}</p>
            <label class="den-dialog__label" for="ext-profile-pack">
              {C.profilePackLabel}
            </label>
            <DenSelect
              id="ext-profile-pack"
              class="den-dialog__select"
              aria-label={C.profilePackLabel}
              data-testid="extensions-profile-pack"
              value={props.profilePackId()}
              options={props.profilePacks().map((pack) => ({
                value: pack.id,
                label: pack.name,
              }))}
              onValueChange={props.setProfilePackId}
            />
            <label class="den-dialog__label" for="ext-profile-name">
              {C.profileNameLabel}
            </label>
            <DenInput
              id="ext-profile-name"
              class="den-dialog__field"
              data-testid="extensions-profile-name"
              value={props.profileName()}
              onInput={(e) => props.setProfileName(e.currentTarget.value)}
            />
            <InlineNotice notice={props.profileError()} testId="extensions-profile-error" />
            <footer class="den-dialog__footer">
              <DenButton
                variant="ghost"
                disabled={props.busy()}
                onClick={() => props.setProfileOpen(false)}
              >
                {C.profileCancel}
              </DenButton>
              <DenButton
                variant="primary"
                data-testid="extensions-profile-apply"
                disabled={props.actionsDisabled()}
                onClick={props.onApplyProfile}
              >
                {C.profileApplyDevice}
              </DenButton>
            </footer>
          </div>
        </div>
      </Show>
    </>
  );
}
