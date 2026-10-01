import { ChromeDragSurface } from "../../shell/ChromeDragSurface.tsx";
import { PreparedSurface } from "../../primitives/PreparedSurface.tsx";
import { chromeProps } from "../../../styling/ui-chrome.ts";
import { For, Show, createSignal, onMount } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import { LycaonApiError } from "../../../api/http.ts";
import type {
  DetectionPack,
  DetectionPackImportResult,
  DetectionRuleSummary,
} from "../../../api/types.ts";
import { createModalFocusTrap } from "../../../platform/interaction/modal-focus-trap.ts";
import { DETECTIONS_COPY } from "../../../settings/security/approvals-settings-copy.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { InlineNotice } from "../../../notices/InlineNotice.tsx";
import { noticeFromCaught, type AppNotice } from "../../../notices/notice-model.ts";
import { APP_SCOPE } from "../../../notices/notice-scope.ts";

type Props = {
  client: LycaonClient;
  path: string;
  onCancel: () => void;
  onImported: (pack: DetectionPack) => void;
};

function normalizeImportPath(folder: string): string {
  if (folder.startsWith("path:")) return folder.slice("path:".length);
  if (folder.startsWith("file:")) return folder.slice("file:".length);
  return folder;
}

function RulePreview(props: { rule: DetectionRuleSummary }) {
  return (
    <li class="den-detections-rule" data-level={props.rule.level}>
      <span class="den-detections-rule__title">{props.rule.title}</span>
      <span class="den-settings-badge" data-level={props.rule.level}>
        {props.rule.level}
      </span>
    </li>
  );
}

/** Previews the files and actions in a device detection pack before installation. */
export function DetectionPackImportDialog(props: Props) {
  const [dialogEl, setDialogEl] = createSignal<HTMLElement | undefined>();
  const [loading, setLoading] = createSignal(true);
  const [replace, setReplace] = createSignal(false);
  const [preview, setPreview] = createSignal<DetectionPackImportResult | undefined>();
  const [error, setError] = createSignal<AppNotice | undefined>();
  const catchNotice = (err: unknown) =>
    noticeFromCaught(err, APP_SCOPE, {
      title: DETECTIONS_COPY.addError,
      message: DETECTIONS_COPY.addError,
    });
  const [terminal, setTerminal] = createSignal(false);
  const [committing, setCommitting] = createSignal(false);

  createModalFocusTrap(() => true, dialogEl, {
    onEscape: () => {
      if (!committing()) props.onCancel();
    },
  });

  const runPreview = async (withReplace: boolean) => {
    setLoading(true);
    setError(undefined);
    setTerminal(false);
    try {
      const result = await props.client.importDetectionPack({
        path: normalizeImportPath(props.path),
        dry_run: true,
        replace: withReplace || undefined,
      });
      setReplace(withReplace);
      setPreview(result);
    } catch (err) {
      if (
        err instanceof LycaonApiError &&
        err.code === "detection_pack_id_collision" &&
        !withReplace
      ) {
        // Device collision is recoverable via replace; a bundled pack is not.
        try {
          const result = await props.client.importDetectionPack({
            path: normalizeImportPath(props.path),
            dry_run: true,
            replace: true,
          });
          setReplace(true);
          setPreview(result);
          return;
        } catch (retryErr) {
          setTerminal(true);
          setError(catchNotice(retryErr));
          return;
        }
      }
      setTerminal(true);
      setError(catchNotice(err));
    } finally {
      setLoading(false);
    }
  };

  onMount(() => {
    void runPreview(false);
  });

  const inactiveRules = () =>
    (preview()?.pack.rules ?? []).filter((r) => !r.supported);
  const rejectedRules = () => preview()?.rejected_rules ?? [];
  const ignored = () => preview()?.ignored ?? [];
  const rehearsal = () => preview()?.rehearsal ?? [];
  const showInactive = () =>
    inactiveRules().length > 0 || rejectedRules().length > 0;

  const confirm = async () => {
    const current = preview();
    if (!current || terminal()) return;
    setCommitting(true);
    setError(undefined);
    try {
      const result = await props.client.importDetectionPack({
        path: normalizeImportPath(props.path),
        dry_run: false,
        replace: replace() || undefined,
      });
      props.onImported(result.pack);
    } catch (err) {
      setError(catchNotice(err));
    } finally {
      setCommitting(false);
    }
  };

  return (
    <div
      class="den-dialog-backdrop"
      data-testid="detection-import-dialog"
      onClick={(e) => {
        if (e.target === e.currentTarget && !committing()) props.onCancel();
      }}
    >
      <ChromeDragSurface class="den-dialog-backdrop__chrome-drag" />
      <div
        ref={setDialogEl}
        class="den-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="detection-import-title"
      >
        <header class="den-dialog__header" {...chromeProps()}>
          <h2 id="detection-import-title">{DETECTIONS_COPY.addTitle}</h2>
        </header>

        <PreparedSurface name="detection-import-preview" ready={() => !loading()}>
        <Show when={!loading() && preview()} keyed>
          {(result) => (
            <>
              <p class="den-dialog__hint" data-testid="detection-import-found">
                {DETECTIONS_COPY.addFound(
                  result.pack.label,
                  result.pack.rules.length,
                )}
              </p>
              <Show when={result.pack.description}>
                <p class="den-settings-hint">{result.pack.description}</p>
              </Show>

              <Show when={replace()}>
                <p
                  class="den-dialog__hint"
                  data-testid="detection-import-replace-hint"
                >
                  {DETECTIONS_COPY.addReplaceHint}
                </p>
              </Show>

              <ul class="den-detections-rules" data-testid="detection-import-rules">
                <For each={result.pack.rules.filter((r) => r.supported)}>
                  {(rule) => <RulePreview rule={rule} />}
                </For>
              </ul>

              <Show when={showInactive()}>
                <h3 class="den-settings-overline">
                  {DETECTIONS_COPY.addInactiveHeading}
                </h3>
                <ul
                  class="den-detections-rules"
                  data-testid="detection-import-inactive"
                >
                  <For each={inactiveRules()}>
                    {(rule) => (
                      <li class="den-detections-rule">
                        <span class="den-detections-rule__title">
                          {rule.title}
                        </span>
                        <span class="den-settings-badge">
                          {DETECTIONS_COPY.unsupportedBadge}
                        </span>
                        <Show when={rule.unsupported_reason}>
                          <p class="den-settings-hint">
                            {rule.unsupported_reason}
                          </p>
                        </Show>
                      </li>
                    )}
                  </For>
                  <For each={rejectedRules()}>
                    {(row) => (
                      <li class="den-detections-rule">
                        <span class="den-detections-rule__title">{row.file}</span>
                        <p class="den-settings-hint">{row.reason}</p>
                      </li>
                    )}
                  </For>
                </ul>
              </Show>

              <Show when={rehearsal().length > 0}>
                <h3 class="den-settings-overline">
                  {DETECTIONS_COPY.addRehearsalHeading}
                </h3>
                <p class="den-settings-hint">
                  {DETECTIONS_COPY.addRehearsalHint}
                </p>
                <ul data-testid="detection-import-rehearsal">
                  <For each={rehearsal()}>
                    {(line) => <li class="den-settings-hint">{line}</li>}
                  </For>
                </ul>
              </Show>

              <Show when={ignored().length > 0}>
                <h3 class="den-settings-overline">
                  {DETECTIONS_COPY.addIgnoredHeading}
                </h3>
                <p class="den-settings-hint">{DETECTIONS_COPY.addIgnoredHint}</p>
                <ul data-testid="detection-import-ignored">
                  <For each={ignored()}>
                    {(name) => <li class="den-settings-hint">{name}</li>}
                  </For>
                </ul>
              </Show>
            </>
          )}
        </Show>

        <InlineNotice notice={error()} testId="detection-import-error" />

        </PreparedSurface>
        <footer class="den-dialog__footer">
          <DenButton
            variant="ghost"
            disabled={committing()}
            data-testid="detection-import-cancel"
            onClick={() => props.onCancel()}
          >
            {DETECTIONS_COPY.addCancel}
          </DenButton>
          <Show when={!terminal() && preview()}>
            <DenButton
              variant="primary"
              disabled={committing() || loading()}
              data-testid="detection-import-confirm"
              onClick={() => void confirm()}
            >
              {replace()
                ? DETECTIONS_COPY.addReplace
                : DETECTIONS_COPY.addConfirm}
            </DenButton>
          </Show>
        </footer>
      </div>
    </div>
  );
}
