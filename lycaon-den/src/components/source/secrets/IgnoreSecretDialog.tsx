import { Show, createResource, createSignal, createUniqueId, onCleanup } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenField } from "../../primitives/DenField.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { DenTextarea } from "../../primitives/DenTextarea.tsx";
import { PreparedSurface } from "../../primitives/PreparedSurface.tsx";
import { ResidentPortal } from "../../primitives/ResidentPortal.tsx";
import { Scrollport } from "../../primitives/Scrollport.tsx";
import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";

export type IgnoreSecretTarget = {
  projectId: string;
  rootId?: string;
  value: () => Promise<string>;
};

type Props = {
  client: LycaonClient;
  target: IgnoreSecretTarget;
  onClose: () => void;
  onSaved?: () => void;
};

const VALUE_ROWS_MIN = 1;
const VALUE_ROWS_MAX = 8;

/** The declaration matches these bytes exactly, so the review states what they are. */
function valueShape(value: string): string {
  const count = Array.from(value).length;
  const characters = `${count} character${count === 1 ? "" : "s"}`;
  const padded = value !== value.trim();
  const lines = value.split("\n").length;
  const span = lines > 1 ? ` · ${lines} lines` : "";
  return padded
    ? `${characters}${span} · matched exactly, including the surrounding whitespace`
    : `${characters}${span} · matched exactly`;
}

function valueRows(value: string): number {
  return Math.min(VALUE_ROWS_MAX, Math.max(VALUE_ROWS_MIN, value.split("\n").length));
}

function today(): string {
  const now = new Date();
  const month = `${now.getMonth() + 1}`.padStart(2, "0");
  return `${now.getFullYear()}-${month}-${`${now.getDate()}`.padStart(2, "0")}`;
}

export function IgnoreSecretDialog(props: Props) {
  const titleId = createUniqueId();
  const valueHintId = createUniqueId();
  const reasonHintId = createUniqueId();
  const folderHintId = createUniqueId();
  const expiryHintId = createUniqueId();
  const entryId = crypto.randomUUID();
  let alive = true;
  onCleanup(() => { alive = false; });
  const [reason, setReason] = createSignal("");
  const [rootId, setRootId] = createSignal(props.target.rootId ?? "");
  const [expires, setExpires] = createSignal("");
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  let dialog: HTMLFormElement | undefined;
  const close = () => { if (!busy()) props.onClose(); };
  createModalFocusTrap(() => true, () => dialog, { onEscape: close });
  const [review] = createResource(async () => {
    const [project, value] = await Promise.all([props.client.getProject(props.target.projectId), props.target.value()]);
    return { roots: project.roots, value };
  });
  const data = () => review.error ? undefined : review();
  const selectedRoot = () => rootId() || data()?.roots[0]?.id || "";
  const canSave = () => !!data() && !!reason().trim() && !!selectedRoot() && !busy();
  // The trap focuses the footer while the review is still loading, and the prepared surface
  // stays unfocusable until it paints, so the reason claims focus over the frames after it mounts.
  let focused = false;
  const claimFocus = (field: HTMLInputElement) => {
    let frames = 0;
    const attempt = () => {
      const resting = document.activeElement;
      if (focused || !(resting === document.body || dialog?.contains(resting))) return;
      field.focus({ preventScroll: true });
      if (document.activeElement === field) focused = true;
      else if ((frames += 1) < 8) requestAnimationFrame(attempt);
    };
    requestAnimationFrame(attempt);
  };
  const save = async (event: SubmitEvent) => {
    event.preventDefault();
    const current = data();
    if (!current || !canSave()) return;
    setBusy(true);
    setError("");
    try {
      await props.client.createProjectSecretIgnore(props.target.projectId, {
        root_id: selectedRoot(),
        entry: { id: entryId, value: current.value, reason: reason().trim(), expires_on: expires() || undefined },
      });
      if (alive) { props.onSaved?.(); props.onClose(); }
    } catch (caught) {
      if (alive) setError(caught instanceof Error ? caught.message : "Could not save the ignore declaration.");
    } finally {
      if (alive) setBusy(false);
    }
  };

  return (
    <ResidentPortal mount={document.body}>
      <div class="den-dialog-backdrop den-dialog-backdrop--viewport" onClick={(event) => { if (event.target === event.currentTarget) close(); }}>
        <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
        <form ref={dialog} class="den-dialog" role="dialog" aria-modal="true" aria-labelledby={titleId} data-testid="ignore-secret-dialog" onSubmit={(event) => void save(event)}>
          <header class="den-dialog__header" {...chromeProps()}>
            <h2 id={titleId}>Ignore this value in this project</h2>
          </header>
          <Scrollport class="den-dialog__body" contentClass="den-ignore-secret__body">
            <Show
              when={!review.error}
              fallback={<p role="alert" class="den-ignore-secret__error">This value is no longer available for review. Open the detection again.</p>}
            >
              <PreparedSurface name="secret-ignore-review" ready={() => !review.loading}>
                <div class="den-ignore-secret__review" data-review={review.loading ? "loading" : "settled"}>
                  <Show when={data()} keyed>{(current) => <>
                    <p class="den-ignore-secret__lede">Save a public example or fixture in the project’s ignore file.</p>
                    <DenField label="Exact public value" hint={valueShape(current.value)} hintId={valueHintId}>
                      <DenTextarea
                        readonly spellcheck={false} rows={valueRows(current.value)} value={current.value}
                        aria-label="Exact public value" aria-describedby={valueHintId}
                        class="den-ignore-secret__value" data-testid="ignore-secret-value"
                      />
                    </DenField>
                    <DenField label="Reason" hint="Recorded beside the value, so the next reader knows why it is safe." hintId={reasonHintId}>
                      <DenInput
                        ref={claimFocus} required maxlength={240} value={reason()} placeholder="Published example from the AWS docs"
                        aria-label="Reason" aria-describedby={reasonHintId} data-testid="ignore-secret-reason"
                        onInput={(event) => setReason(event.currentTarget.value)}
                      />
                    </DenField>
                    <DenField label="Folder" hint="Written to .paintedwolf/ignores.yaml in this folder." hintId={folderHintId}>
                      <DenSelect
                        aria-label="Folder" aria-describedby={folderHintId} value={selectedRoot()} onValueChange={setRootId}
                        options={current.roots.map((root) => ({ value: root.id, label: root.label || root.path, description: root.path }))}
                      />
                    </DenField>
                    <DenField label="Expiry (optional)" hint="Ends the exception at midnight UTC on this date." hintId={expiryHintId}>
                      <DenInput
                        type="date" min={today()} value={expires()} aria-label="Expiry (optional)" aria-describedby={expiryHintId}
                        onInput={(event) => setExpires(event.currentTarget.value)}
                      />
                    </DenField>
                    <p class="den-ignore-secret__caution">The value is written in plaintext. Save only what is safe to commit and share.</p>
                    <p class="den-ignore-secret__note">Applies to secret screening across this project. Protected credentials stay protected, and saving does not release a held request.</p>
                    <Show when={error()}><p role="alert" class="den-ignore-secret__error" data-testid="ignore-secret-error">{error()}</p></Show>
                    <Show when={current.roots.length === 0}>
                      <p role="alert" class="den-ignore-secret__error">Attach a project folder before saving an ignore.</p>
                    </Show>
                  </>}</Show>
                </div>
              </PreparedSurface>
            </Show>
          </Scrollport>
          <footer class="den-dialog__footer">
            <DenButton variant="ghost" disabled={busy()} onClick={close}>{review.error ? "Close" : "Cancel"}</DenButton>
            <Show when={!review.error}>
              <DenButton variant="primary" type="submit" disabled={!canSave()}>{busy() ? "Saving…" : "Save project ignore"}</DenButton>
            </Show>
          </footer>
        </form>
      </div>
    </ResidentPortal>
  );
}
