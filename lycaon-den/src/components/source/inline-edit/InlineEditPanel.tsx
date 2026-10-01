import { Show, createMemo } from "solid-js";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import { DenButton } from "../../primitives/DenButton.tsx";
import { createOverlayShortcutScope } from "../../../platform/interaction/modal-focus-trap.ts";
import {
  bindingForHandler,
  handlerMatchesEvent,
} from "../../../shortcuts/display-binding-for.ts";
import {
  closeInlineEdit,
  inlineEditError,
  inlineEditInstruction,
  inlineEditOpen,
  inlineEditProgressNoun,
  inlineEditRenameCountPending,
  inlineEditRenameInFileCount,
  inlineEditRenameProjectCount,
  inlineEditRenameScope,
  inlineEditRunning,
  setInlineEditInstruction,
  setInlineEditRenameScope,
  walkInlineEditHistory,
} from "./inline-edit-controller.ts";
import {
  projectCountLabel,
  renameCollisionWarning,
  validRenameInput,
  type RenameScope,
} from "./rename-card-model.ts";

type Props = {
  onSubmit: (instruction: string) => void;
  onCancelRunning?: () => void;
  /** Editor pane host — panel mounts here so anchors stay in-pane. */
  mountEl?: HTMLElement | null;
  /** Current buffer text for the rename collision warning. */
  docText?: () => string;
};

function scopeButton(
  scope: RenameScope,
  current: () => RenameScope,
  title: string,
  detail: string,
  testId: string,
) {
  return (
    <button
      type="button"
      class="den-rename-scope__option"
      classList={{ "den-rename-scope__option--on": current() === scope }}
      role="radio"
      aria-checked={current() === scope}
      data-testid={testId}
      onClick={() => setInlineEditRenameScope(scope)}
    >
      <span class="den-rename-scope__title">{title}</span>
      <span class="den-rename-scope__detail">{detail}</span>
    </button>
  );
}

/** Anchored inline edit and rename surface. */
export function InlineEditPanel(props: Props) {
  createOverlayShortcutScope(() => inlineEditOpen() != null);
  const rename = () => {
    const state = inlineEditOpen();
    return state?.mode.kind === "rename" ? state.mode : null;
  };
  const missedTarget = () => {
    const state = inlineEditOpen();
    return state?.mode.kind === "rename" && state.mode.symbolName == null;
  };
  const collision = createMemo(() => {
    const current = rename();
    if (!current?.symbolName) return null;
    return renameCollisionWarning(
      props.docText?.() ?? "",
      current.symbolName,
      inlineEditInstruction(),
    );
  });
  const submittable = () => {
    const state = inlineEditOpen();
    return state?.mode.kind === "rename"
      ? validRenameInput(inlineEditInstruction())
      : inlineEditInstruction().trim().length > 0;
  };
  const submit = () => {
    if (!submittable()) return;
    props.onSubmit(inlineEditInstruction().trim());
  };

  return (
    <Show when={inlineEditOpen()} keyed>
      {(s) => {
        return (
          <Show when={props.mountEl} keyed>
            {(mount) => (
              <ResidentPortal mount={mount}>
                <div
                  class="pointer-events-none absolute inset-0 z-30"
                  data-testid="inline-edit-panel-host"
                >
                  <div
                    class="pointer-events-auto absolute flex max-w-xl flex-col gap-1.5 rounded-md border border-[var(--den-border)] bg-[var(--den-surface)] p-2.5 shadow-md"
                    style={{
                      top: `${Math.max(8, s.anchor.top - 52)}px`,
                      left: `${Math.max(8, s.anchor.left)}px`,
                      width: `${Math.min(480, Math.max(300, s.anchor.width))}px`,
                    }}
                    data-testid="inline-edit-panel"
                    role="dialog"
                    aria-label={
                      s.mode.kind === "rename" ? "Rename symbol" : "Inline edit"
                    }
                  >
                    <Show when={s.mode.kind === "rename"}>
                      <div class="flex items-baseline justify-between">
                        <span class="text-sm font-semibold text-[var(--den-text)]">
                          Rename
                          <Show when={rename()?.symbolName}>
                            {(sym) => (
                              <code class="ml-1.5 rounded bg-[var(--den-background)] px-1 font-mono text-xs text-[var(--den-accent-text)]">
                                {sym()}
                              </code>
                            )}
                          </Show>
                        </span>
                        <span class="text-[11px] text-[var(--den-text-muted)]">
                          {bindingForHandler("editor.renameSymbol")}
                        </span>
                      </div>
                    </Show>

                    <Show when={missedTarget()}>
                      <p
                        class="text-sm text-[var(--den-text-muted)]"
                        data-testid="rename-miss-hint"
                      >
                        Place the caret on a name to rename.
                      </p>
                    </Show>

                    <Show when={inlineEditRunning()}>
                      <div
                        class="flex items-center justify-between gap-2 text-sm text-[var(--den-text-muted)]"
                        data-testid="inline-edit-progress"
                      >
                        <span>{inlineEditProgressNoun()}</span>
                        <DenButton
                          variant="ghost"
                          data-testid="inline-edit-cancel"
                          onClick={() => props.onCancelRunning?.()}
                        >
                          Cancel
                        </DenButton>
                      </div>
                    </Show>

                    <Show when={!inlineEditRunning() && !missedTarget()}>
                      <input
                        class="w-full rounded border border-[var(--den-border)] bg-[var(--den-background)] px-2 py-1.5 font-mono text-sm text-[var(--den-text)] outline-none focus:border-[var(--den-accent)]"
                        data-testid="inline-edit-input"
                        autofocus
                        placeholder={
                          s.mode.kind === "rename"
                            ? "New name…"
                            : "Describe the change…"
                        }
                        value={inlineEditInstruction()}
                        onInput={(e) =>
                          setInlineEditInstruction(e.currentTarget.value)
                        }
                        onKeyDown={(e) => {
                          if (handlerMatchesEvent("overlay.dismiss", e)) {
                            e.preventDefault();
                            e.stopPropagation();
                            closeInlineEdit();
                            return;
                          }
                          if (
                            s.mode.kind === "rename" &&
                            handlerMatchesEvent("editor.renameScopeFile", e)
                          ) {
                            e.preventDefault();
                            setInlineEditRenameScope("file");
                            return;
                          }
                          if (
                            s.mode.kind === "rename" &&
                            handlerMatchesEvent(
                              "editor.renameScopeEverywhere",
                              e,
                            )
                          ) {
                            e.preventDefault();
                            setInlineEditRenameScope("everywhere");
                            return;
                          }
                          if (
                            s.mode.kind !== "rename" &&
                            handlerMatchesEvent("list.up", e)
                          ) {
                            e.preventDefault();
                            walkInlineEditHistory(1);
                            return;
                          }
                          if (
                            s.mode.kind !== "rename" &&
                            handlerMatchesEvent("list.down", e)
                          ) {
                            e.preventDefault();
                            walkInlineEditHistory(-1);
                            return;
                          }
                          if (handlerMatchesEvent("list.confirm", e)) {
                            e.preventDefault();
                            submit();
                          }
                        }}
                      />
                    </Show>

                    <Show when={s.mode.kind === "rename" && !missedTarget()}>
                      <div
                        class="flex items-center gap-2 px-0.5 text-xs text-[var(--den-text-muted)] tabular-nums"
                        data-testid="rename-counts"
                      >
                        <span>
                          <b class="font-semibold text-[var(--den-text)]">
                            {inlineEditRenameInFileCount()}
                          </b>{" "}
                          in this file
                        </span>
                        <span aria-hidden="true">·</span>
                        <Show
                          when={inlineEditRenameProjectCount()}
                          fallback={
                            <span data-testid="rename-project-count-pending">
                              {inlineEditRenameCountPending()
                                ? "counting project matches…"
                                : "project count unavailable"}
                            </span>
                          }
                        >
                          {(count) => (
                            <span data-testid="rename-project-count">
                              {projectCountLabel(count())}
                            </span>
                          )}
                        </Show>
                      </div>
                      <div
                        class="den-rename-scope"
                        role="radiogroup"
                        aria-label="Rename scope"
                        data-testid="rename-scope"
                      >
                        {scopeButton(
                          "file",
                          inlineEditRenameScope,
                          "This file",
                          "syntax-aware",
                          "rename-scope-file",
                        )}
                        {scopeButton(
                          "everywhere",
                          inlineEditRenameScope,
                          "Everywhere",
                          "exact text · preview first",
                          "rename-scope-everywhere",
                        )}
                      </div>
                      <Show when={collision()}>
                        {(warning) => (
                          <p
                            class="text-[11px] text-[var(--den-warning)]"
                            data-testid="rename-collision-warning"
                          >
                            {warning()}
                          </p>
                        )}
                      </Show>
                    </Show>

                    <Show when={inlineEditError()}>
                      {(err) => (
                        <p
                          class="text-xs text-[var(--den-danger)]"
                          data-testid="inline-edit-error"
                        >
                          {err()}
                        </p>
                      )}
                    </Show>

                    <p class="text-[11px] text-[var(--den-text-muted)]">
                      <Show
                        when={s.mode.kind === "rename"}
                        fallback={
                          <>
                            {bindingForHandler("list.confirm")} runs ·{" "}
                            {bindingForHandler("overlay.dismiss")} cancels ·{" "}
                            {bindingForHandler("list.up")}/
                            {bindingForHandler("list.down")} history
                          </>
                        }
                      >
                        <Show
                          when={!missedTarget()}
                          fallback={
                            <>{bindingForHandler("overlay.dismiss")} closes</>
                          }
                        >
                          {inlineEditRenameScope() === "everywhere"
                            ? `${bindingForHandler("list.confirm")} previews every change`
                            : `${bindingForHandler("list.confirm")} renames in this file`}
                          {" · "}
                          {bindingForHandler("overlay.dismiss")} cancels ·{" "}
                          {bindingForHandler("editor.renameScopeFile")} /{" "}
                          {bindingForHandler("editor.renameScopeEverywhere")} scope
                        </Show>
                      </Show>
                    </p>
                  </div>
                </div>
              </ResidentPortal>
            )}
          </Show>
        );
      }}
    </Show>
  );
}
