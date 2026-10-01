import { Show, createEffect, createMemo, untrack } from "solid-js";
import { projectTrustView, openProjectTrust } from "../../settings/security/project-trust.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { useResidentInteractive } from "../../ui/resident-presence-context.tsx";
import { TRUST_COPY } from "../../settings/security/trust-copy.ts";
import { ReviewFileGroups, type ReviewFileGroup } from "../../files/review/ReviewFileGroups.tsx";
import { Scrollport } from "../primitives/Scrollport.tsx";
import { openTrustChange } from "./trust-review-navigation.ts";

export function TrustReviewPage(props: { projectId: string }) {
  const state = () => projectTrustView(props.projectId);
  const trust = () => state().value;
  const review = () => trust()?.review;
  const interactive = useResidentInteractive();
  createEffect(() => {
    if (!interactive()) return;
    const id = props.projectId;
    untrack(() => {
      const client = getLycaonClient();
      if (client && (!trust() || trust()?.unread_count)) void openProjectTrust(client, id)
        .catch(() => {});
    });
  });
  const files = createMemo(() => (review()?.changes ?? [])
    .map(change => ({ ...change, op: change.kind === "added" ? "create" as const : change.kind === "removed" ? "delete" as const : "write" as const })));
  const groups = createMemo(() => {
    const rows = new Map<string, ReviewFileGroup<ReturnType<typeof files>[number]>>();
    for (const file of files()) {
      const directory = file.path.slice(0, Math.max(0, file.path.lastIndexOf("/")));
      const key = `${file.root_id}/${directory}`;
      const group = rows.get(key) ?? { path: key, label: [file.root_label, directory].filter(Boolean).join(" / "), files: [] };
      group.files.push(file); rows.set(key, group);
    }
    return [...rows.values()];
  });
  return <Scrollport class="den-files-info__body" data-testid="trust-review-page">
    <article class="den-walk-page__article">
      <header class="den-walk-page__head">
        <div class="den-walk-page__heading">
          <p class="den-walk-page__subtitle">Project configuration</p>
          <h1 class="den-walk-page__title">Trust changes</h1>
        </div>
      </header>
      <Show when={state().error}>{message => <p class="den-walk-page__error" role="alert">{message()}</p>}</Show>
      <Show when={!trust() && !state().error}><p class="den-walk-page__note">Loading trust changes…</p></Show>
      <Show when={trust()}>{current => <section class="den-walk-page__files den-git-review__files" aria-label="Trust review files">
        <h2 class="den-walk-page__files-title" data-testid="trust-review-count">{TRUST_COPY.reviewCount(current().review.changes.length)}</h2>
        <p class="den-walk-page__note">{TRUST_COPY.reviewStatus(current().unread_count)}</p>
        <Show when={files().length === 0}><p class="den-walk-page__empty" data-testid="trust-review-empty">No trust changes to review.</p></Show>
        <ReviewFileGroups groups={groups()} fileTestId="trust-review-file" onOpen={file => {
          const snapshot = review();
          const change = snapshot?.changes.find(change => change.id === file.id);
          if (snapshot && change) openTrustChange(snapshot, change);
        }} />
      </section>}</Show>
    </article>
  </Scrollport>;
}
