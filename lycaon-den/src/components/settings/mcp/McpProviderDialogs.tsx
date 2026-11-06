import { For, Show, type Accessor, type Setter } from "solid-js";
import type { McpRecipe } from "../../../api/types.ts";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import type { AppNotice } from "../../../notices/notice-model.ts";
import { confirmAndOpenExternalLink } from "../../../platform/desktop/external-link.ts";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import type { groupRecipesByClass } from "../../../settings/mcp/mcp-provider-model.ts";
import { MCP_SETTINGS_COPY } from "../../../settings/mcp/mcp-settings-copy.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { SettingsListGroup } from "../SettingsListGroup.tsx";
import { emptyAddDraft, type AddDraft } from "./mcp-connection-draft.ts";
import { McpConnectionFields } from "./McpConnectionFields.tsx";

type RecipeGroup = ReturnType<typeof groupRecipesByClass>[number];

export type McpProviderDialogsProps = {
  projectLocal: () => boolean;
  addOpen: () => boolean;
  addStep: () => "pick" | "custom";
  setAddStep: Setter<"pick" | "custom">;
  addCustomClass: () => "local" | "web";
  setAddCustomClass: Setter<"local" | "web">;
  recipeGroups: () => RecipeGroup[];
  addDraft: Accessor<AddDraft>;
  setAddDraft: Setter<AddDraft>;
  addError: () => AppNotice | undefined;
  setAddError: Setter<AppNotice | undefined>;
  addBusy: () => boolean;
  addReady: () => boolean;
  closeAdd: () => void;
  saveAdd: () => Promise<void>;
  addRecipe: (recipe: McpRecipe) => Promise<void>;
  oauthPending: () => { id: string; state: string } | undefined;
  oauthBusyId: () => string | undefined;
  oauthManual: () => boolean;
  setOauthManual: Setter<boolean>;
  oauthCode: () => string;
  setOauthCode: Setter<string>;
  oauthError: () => AppNotice | undefined;
  cancelSignIn: () => Promise<void>;
  completeSignIn: () => Promise<void>;
};

export function McpProviderDialogs(props: McpProviderDialogsProps) {
  let addDialogRef: HTMLDivElement | undefined;
  let oauthDialogRef: HTMLDivElement | undefined;

  createModalFocusTrap(props.addOpen, () => addDialogRef, {
    onEscape: props.closeAdd,
  });
  createModalFocusTrap(
    () => props.oauthPending() !== undefined,
    () => oauthDialogRef,
    {
      onEscape: () => {
        if (!props.oauthBusyId()) void props.cancelSignIn();
      },
    },
  );

  return (
    <>
      <Show when={props.addOpen()}>
        <ResidentPortal mount={document.body}>
          <div
            class="den-dialog-backdrop den-dialog-backdrop--viewport"
            data-testid="mcp-add-dialog"
            onClick={(e) => {
              if (e.target === e.currentTarget) props.closeAdd();
            }}
          >
            <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
            <div
              ref={addDialogRef}
              class={
                props.addStep() === "custom"
                  ? "den-dialog"
                  : "den-dialog den-dialog--sheet"
              }
              role="dialog"
              aria-modal="true"
              aria-labelledby="mcp-editor-title"
            >
              <header class="den-dialog__header" {...chromeProps()}>
                <h2 id="mcp-editor-title">
                  {props.addStep() === "custom"
                    ? props.addCustomClass() === "web"
                      ? MCP_SETTINGS_COPY.addDialogCustomWebTitle
                      : MCP_SETTINGS_COPY.addDialogCustomLocalTitle
                    : MCP_SETTINGS_COPY.addDialogTitle}
                </h2>
              </header>
              <Scrollport
                class="den-settings-add-dialog__body"
                contentClass="den-settings-add-dialog__body-content"
                eager
              >
                <Show
                  when={props.addStep() === "custom"}
                  fallback={
                    <>
                      <p class="den-dialog__hint">{MCP_SETTINGS_COPY.addPickHint}</p>
                      <div data-testid="mcp-recipe-list">
                        <For each={props.recipeGroups()}>
                          {(group) => (
                            <SettingsListGroup
                              testId={`mcp-recipe-group-${group.kind}`}
                              label={group.label}
                            >
                              <ul class="den-settings-mcp-recipes">
                                <For each={group.recipes}>
                                  {(recipe) => (
                                    <li>
                                      <button
                                        type="button"
                                        class="den-settings-mcp-recipe"
                                        data-testid={`mcp-recipe-${recipe.id}`}
                                        disabled={props.addBusy()}
                                        onClick={() => void props.addRecipe(recipe)}
                                      >
                                        <span class="den-settings-mcp-recipe__label">
                                          {recipe.label}
                                        </span>
                                        <Show when={recipe.added}>
                                          <span class="den-settings-hint">
                                            {MCP_SETTINGS_COPY.recipeAdded}
                                          </span>
                                        </Show>
                                        <span class="den-settings-hint">
                                          {recipe.hint}
                                        </span>
                                        <Show when={(recipe.env_keys ?? []).length > 0}>
                                          <span
                                            class="den-settings-hint"
                                            data-testid={`mcp-recipe-env-${recipe.id}`}
                                          >
                                            {MCP_SETTINGS_COPY.recipeEnvKeys(
                                              (recipe.env_keys ?? []).map(
                                                (key) => key.label,
                                              ),
                                            )}
                                          </span>
                                        </Show>
                                      </button>
                                      <Show when={recipe.docs_url} keyed>
                                        {(url) => (
                                          <button
                                            type="button"
                                            class="den-settings-mcp-recipe__docs"
                                            data-testid={`mcp-recipe-docs-${recipe.id}`}
                                            disabled={props.addBusy()}
                                            onClick={() =>
                                              void confirmAndOpenExternalLink(url)
                                            }
                                          >
                                            {MCP_SETTINGS_COPY.recipeDocs}
                                          </button>
                                        )}
                                      </Show>
                                    </li>
                                  )}
                                </For>
                                <li>
                                  <button
                                    type="button"
                                    class="den-settings-mcp-recipe"
                                    data-testid={
                                      group.kind === "web"
                                        ? "mcp-recipe-custom-web"
                                        : "mcp-recipe-custom-local"
                                    }
                                    disabled={props.addBusy()}
                                    onClick={() => {
                                      props.setAddError(undefined);
                                      props.setAddCustomClass(group.kind);
                                      props.setAddDraft({
                                        ...emptyAddDraft(),
                                        transport: "http",
                                      });
                                      props.setAddStep("custom");
                                    }}
                                  >
                                    <span class="den-settings-mcp-recipe__label">
                                      {group.kind === "web"
                                        ? MCP_SETTINGS_COPY.addCustomWeb
                                        : MCP_SETTINGS_COPY.addCustomLocal}
                                    </span>
                                    <span class="den-settings-hint">
                                      {group.kind === "web"
                                        ? MCP_SETTINGS_COPY.addCustomWebHint
                                        : MCP_SETTINGS_COPY.addCustomLocalHint}
                                    </span>
                                  </button>
                                </li>
                              </ul>
                            </SettingsListGroup>
                          )}
                        </For>
                      </div>
                      <InlineNotice notice={props.addError()} testId="mcp-editor-error" />
                    </>
                  }
                >
                  <p class="den-settings-hint">
                    {props.addCustomClass() === "web"
                      ? MCP_SETTINGS_COPY.addCustomWebHint
                      : MCP_SETTINGS_COPY.addCustomLocalHint}
                  </p>
                  <McpConnectionFields
                    draft={props.addDraft}
                    setDraft={(updater) =>
                      props.setAddDraft((d) => ({ ...d, ...updater(d) }))
                    }
                    includeId
                    projectScope={props.projectLocal()}
                    customClass={props.addCustomClass()}
                    idValue={() => props.addDraft().id}
                    onIdChange={(v) => props.setAddDraft((d) => ({ ...d, id: v }))}
                  />
                  <InlineNotice notice={props.addError()} testId="mcp-editor-error" />
                </Show>
              </Scrollport>
              <footer class="den-settings-add-dialog__footer">
                <Show
                  when={props.addStep() === "custom"}
                  fallback={
                    <DenButton
                      variant="secondary"
                      compact
                      disabled={props.addBusy()}
                      onClick={() => props.closeAdd()}
                    >
                      {MCP_SETTINGS_COPY.cancel}
                    </DenButton>
                  }
                >
                  <DenButton
                    variant="secondary"
                    compact
                    disabled={props.addBusy()}
                    onClick={() => {
                      props.setAddError(undefined);
                      props.setAddStep("pick");
                    }}
                  >
                    {MCP_SETTINGS_COPY.pickBack}
                  </DenButton>
                  <DenButton
                    variant="primary"
                    compact
                    data-testid="mcp-editor-save"
                    disabled={props.addBusy() || !props.addReady()}
                    onClick={() => void props.saveAdd()}
                  >
                    {MCP_SETTINGS_COPY.save}
                  </DenButton>
                </Show>
              </footer>
            </div>
          </div>
        </ResidentPortal>
      </Show>

      <Show when={props.oauthPending()} keyed>
        {(pending) => (
          <ResidentPortal mount={document.body}>
            <div
              class="den-dialog-backdrop den-dialog-backdrop--viewport"
              data-testid="mcp-oauth-complete-dialog"
              onClick={(e) => {
                if (e.target === e.currentTarget && !props.oauthBusyId()) {
                  void props.cancelSignIn();
                }
              }}
            >
              <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
              <div
                ref={oauthDialogRef}
                class="den-dialog den-dialog--sheet"
                role="dialog"
                aria-modal="true"
                aria-labelledby="mcp-oauth-title"
              >
                <header class="den-dialog__header" {...chromeProps()}>
                  <h2 id="mcp-oauth-title">{MCP_SETTINGS_COPY.oauthCodeTitle}</h2>
                </header>
                <Scrollport
                  class="den-settings-add-dialog__body"
                  contentClass="den-settings-add-dialog__body-content"
                  eager
                >
                  <Show
                    when={props.oauthManual()}
                    fallback={
                      <>
                        <p class="den-dialog__hint" data-testid="mcp-oauth-waiting">
                          {MCP_SETTINGS_COPY.oauthWaitingHint}
                        </p>
                        <DenButton
                          variant="ghost"
                          compact
                          data-testid="mcp-oauth-manual"
                          onClick={() => props.setOauthManual(true)}
                        >
                          {MCP_SETTINGS_COPY.oauthManualToggle}
                        </DenButton>
                      </>
                    }
                  >
                    <p class="den-dialog__hint">{MCP_SETTINGS_COPY.oauthCodeHint}</p>
                    <DenField label={MCP_SETTINGS_COPY.fieldOAuthCode}>
                      <DenInput
                        id="mcp-oauth-code"
                        data-testid="mcp-oauth-code"
                        autocomplete="off"
                        value={props.oauthCode()}
                        onInput={(e) => props.setOauthCode(e.currentTarget.value)}
                      />
                    </DenField>
                  </Show>
                  <InlineNotice notice={props.oauthError()} testId="mcp-oauth-error" />
                </Scrollport>
                <footer class="den-settings-add-dialog__footer">
                  <DenButton
                    variant="secondary"
                    compact
                    disabled={Boolean(props.oauthBusyId())}
                    onClick={() => void props.cancelSignIn()}
                  >
                    {MCP_SETTINGS_COPY.cancel}
                  </DenButton>
                  <Show when={props.oauthManual()}>
                    <DenButton
                      variant="primary"
                      compact
                      data-testid={`mcp-oauth-complete-${pending.id}`}
                      disabled={Boolean(props.oauthBusyId()) || !props.oauthCode().trim()}
                      onClick={() => void props.completeSignIn()}
                    >
                      {MCP_SETTINGS_COPY.oauthComplete}
                    </DenButton>
                  </Show>
                </footer>
              </div>
            </div>
          </ResidentPortal>
        )}
      </Show>
    </>
  );
}
