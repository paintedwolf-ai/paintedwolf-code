import { For, Show, createSignal } from "solid-js";
import { APPROVALS_COPY } from "../../settings/security/approvals-copy.ts";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenCheckboxControl } from "../primitives/DenCheckbox.tsx";
import { openFilesSurface } from "../../platform/navigation/open-files-surface.ts";
import { resolveSourceRequest } from "../../platform/navigation/open-source.ts";
import { ActionsZone, approvalKeyHandler, GrantMenu, RedirectRail, ShellCard, ShellHeader, ShellScroll, useApprovalCollapse } from "./ApprovalShell.tsx";
import { checkpointRowLabel } from "./checkpoint-row-label.ts";
import type { ApprovalCardProps } from "./approval-card-types.ts";

export function ContentApplyBody(props: ApprovalCardProps) {
  const copy = APPROVALS_COPY.card;
  const payload = () => props.checkpoint.content_apply;
  const collapse = useApprovalCollapse(props);
  const [skipOpen, setSkipOpen] = createSignal(false);
  const [selectedHunks, setSelectedHunks] = createSignal<Set<string>>(
    new Set(payload()?.hunks?.map((hunk) => hunk.id) ?? []),
  );

  const hunks = () => payload()?.hunks ?? [];
  const hasHunks = () => hunks().length > 0;

  const approve = () => {
    const ids = [...selectedHunks()];
    if (hasHunks() && ids.length > 0 && ids.length < hunks().length) {
      props.onContentApply({ decision: "approve_partial", approvedHunks: ids });
      return;
    }
    if (ids.length > 0) props.onContentApply({ decision: "approve" });
  };

  const no = (withGuidance: boolean) =>
    props.onContentApply({ decision: "reject", withGuidance });

  const openInFiles = () => {
    const p = payload();
    const projectId = props.projectId?.trim();
    const path = p?.path?.trim();
    if (!p || !projectId || !path) return;
    const resolved = resolveSourceRequest({
      projectId,
      path,
      intent: "transient",
    });
    const rootId = resolved.status === "resolved" ? resolved.request.rootId : "";
    openFilesSurface({
      kind: "file-change-preview",
      projectId,
      rootId,
      path,
      before: p.before ?? null,
      after: p.after,
      previewId: `${props.checkpoint.checkpointId}:${path}`,
      title: "Proposed file change",
      detail: path,
    });
  };

  const onCardKeyDown = approvalKeyHandler({
    enabled: () =>
      !props.resolving && !collapse.minimized() && selectedHunks().size > 0,
    onPrimary: approve,
    onNo: () => no(false),
    onGrantMenu: props.onSkipReviewPath ? () => setSkipOpen(true) : undefined,
  });

  return (
    <ShellCard
      testid="content-apply-card"
      cardId={props.checkpoint.checkpointId}
      onKeyDown={onCardKeyDown}
      projectId={props.projectId}
      sessionId={props.sessionId ?? props.checkpoint.sessionId}
      inert={props.inert}
      collapse={collapse}
      peek={checkpointRowLabel(props.checkpoint)}
      header={
        <ShellHeader
          state={copy.state.editReview}
          action={copy.action.applyChanges}
          chip={payload()?.tool}
          path={payload()?.path}
        />
      }
    >
      <ShellScroll>
      <div class="den-approval-card-diff-zone">
        <div class="den-approval-card-diff-toolbar">
          <Show when={props.projectId && payload()?.path}>
            <DenButton
              variant="ghost"
              compact
              data-testid="content-apply-review-in-files"
              onClick={openInFiles}
            >
              {copy.viewDiff}
            </DenButton>
          </Show>
          <span class="den-approval-card-actions-spacer" />
          <Show when={hasHunks()}>
            <span class="den-settings-hint">
              {selectedHunks().size} of {hunks().length} hunks
            </span>
          </Show>
        </div>
        <Show when={hasHunks()}>
          <ul class="den-checkpoint-card-hunks" data-testid="content-apply-hunks">
            <For each={hunks()}>{(hunk, index) => (
              <li>
                <label>
                  <DenCheckboxControl
                    checked={selectedHunks().has(hunk.id)}
                    onChange={(e) => {
                      const next = new Set(selectedHunks());
                      if (e.currentTarget.checked) next.add(hunk.id);
                      else next.delete(hunk.id);
                      setSelectedHunks(next);
                    }}
                  />
                  {hunk.path} · Hunk {index() + 1}
                </label>
              </li>
            )}</For>
          </ul>
        </Show>
      </div>
      </ShellScroll>

      <ActionsZone
        sessionId={props.sessionId}
        resolving={props.resolving}
        no={{
          label: copy.no,
          onNo: no,
          testid: "content-apply-no",
        }}
        middle={
          <Show when={props.onSkipReviewPath}>
            <GrantMenu
              face={{ title: copy.grantMenu.applyAndSkip }}
              items={[
                {
                  id: "day",
                  title: copy.grantMenu.skipDay,
                  meta: copy.grantMenu.skipDayMeta,
                },
                {
                  id: "always",
                  title: copy.grantMenu.skipAlways,
                  meta: copy.grantMenu.skipAlwaysMeta,
                },
              ]}
              disabled={props.resolving}
              open={skipOpen()}
              onOpenChange={setSkipOpen}
              onPick={(mode) =>
                props.onSkipReviewPath?.(mode === "day" ? "day" : "always")
              }
              testid="content-apply-skip-face"
            />
          </Show>
        }
        primary={{
          label: copy.apply,
          onPrimary: approve,
          disabled: selectedHunks().size === 0,
          testid: "content-apply-approve",
        }}
      />
      <RedirectRail sessionId={props.sessionId} />
    </ShellCard>
  );
}
