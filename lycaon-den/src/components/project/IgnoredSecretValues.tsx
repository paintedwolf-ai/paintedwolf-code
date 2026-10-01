import { For, Show, createMemo, createSignal, createUniqueId } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import type { SecretIgnoreList } from "../../api/types.ts";
import { paginateSlice } from "../../list/pagination.ts";
import { confirmDestructive } from "../../platform/interaction/confirm-dialog.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import { createSurfaceQuery } from "../../ui/surface-query.ts";
import { ListSurfaceInbox } from "../list/ListSurfaceInbox.tsx";
import { TablePager } from "../list/TablePager.tsx";
import { DenButton } from "../primitives/DenButton.tsx";
import { DenField } from "../primitives/DenField.tsx";
import { DenInput } from "../primitives/DenInput.tsx";
import { DenSelect } from "../primitives/DenSelect.tsx";
import { DenTextarea } from "../primitives/DenTextarea.tsx";
import { ShowLatest } from "../primitives/ShowLatest.tsx";
import { SettingsListBackChrome } from "../settings/SettingsListBackChrome.tsx";
import { SettingsListChrome } from "../settings/SettingsListChrome.tsx";
import { SettingsListPanel } from "../settings/SettingsListPanel.tsx";
import { SettingsListRow } from "../settings/SettingsListRow.tsx";

const PAGE_SIZE = 10;
const statusLabel = { active: "Active", expired: "Expired", protected: "Protected", untrusted: "Trust disabled", invalid: "Invalid" };
const statusHint = {
  active: "This exact public value is ignored across this project.",
  expired: "This declaration has expired and no longer applies.",
  protected: "Protected credential evidence takes precedence. This value is not ignored.",
  untrusted: "The project’s Scanning and ignores trust setting is disabled. This declaration does not apply.",
  invalid: "This declaration has a syntax or validation defect and is not in force.",
};

function messageOf(caught: unknown, fallback: string) {
  return caught instanceof Error && caught.message ? caught.message : fallback;
}

export function IgnoredSecretValues(props: { client: LycaonClient; projectId: string }) {
  const projection = createSurfaceQuery({
    name: "project-secret-ignores",
    source: () => ({ client: props.client, key: props.projectId }),
    load: async ({ client, key }) => {
      const [catalog, project] = await Promise.all([client.listProjectSecretIgnores(key), client.getProject(key)]);
      return { catalog, roots: project.roots };
    },
  });
  const [query, setQuery] = createSignal("");
  const [page, setPage] = createSignal(0);
  const [selected, setSelected] = createSignal("");
  const [adding, setAdding] = createSignal(false);
  const [busy, setBusy] = createSignal(false);
  const [error, setError] = createSignal("");
  const [value, setValue] = createSignal("");
  const [reason, setReason] = createSignal("");
  const [expires, setExpires] = createSignal("");
  const [draftRoot, setDraftRoot] = createSignal("");
  const valueHintId = createUniqueId();
  const reasonHintId = createUniqueId();
  const folderHintId = createUniqueId();
  const expiryHintId = createUniqueId();
  const rules = () => projection.value()?.catalog.rules ?? [];
  const validRules = () => rules().filter((r) => r.status !== "invalid");
  const invalidRules = () => rules().filter((r) => r.status === "invalid");
  const roots = () => projection.value()?.roots ?? [];
  const key = (entry: { root_id: string; id: string }) => `${entry.root_id}:${entry.id}`;
  const current = () => validRules().find((entry) => key(entry) === selected());
  const detailOpen = () => adding() || current() != null;
  const filtered = createMemo(() => validRules().filter((entry) =>
    `${entry.value}\n${entry.reason}\n${entry.path}`.toLowerCase().includes(query().toLowerCase()),
  ));
  const paged = createMemo(() => paginateSlice(filtered(), page(), PAGE_SIZE));
  const folder = (rootId: string) => {
    const root = roots().find((entry) => entry.id === rootId);
    return root?.label || root?.path || "Project folder";
  };
  const openFile = (rootId: string) => void openSourceLocation({
    projectId: props.projectId, rootId, path: ".paintedwolf/ignores.yaml", intent: "permanent",
  });

  // Both writes answer with the file's current declarations, so the list needs no reread.
  const absorb = (catalog: SecretIgnoreList) =>
    projection.update((previous) => ({ catalog, roots: previous?.roots ?? [] }));

  const openAdd = () => {
    setSelected("");
    setError("");
    setValue("");
    setReason("");
    setExpires("");
    setDraftRoot(roots()[0]?.id ?? "");
    setAdding(true);
  };
  const closeDetail = () => {
    setAdding(false);
    setSelected("");
    setError("");
  };
  const draftRootId = () => draftRoot() || roots()[0]?.id || "";
  const canSave = () => !busy() && !!value() && !!reason().trim() && !!draftRootId();

  const save = async (event: SubmitEvent) => {
    event.preventDefault();
    if (!canSave()) return;
    setBusy(true);
    setError("");
    try {
      absorb(await props.client.createProjectSecretIgnore(props.projectId, {
        root_id: draftRootId(),
        entry: { id: crypto.randomUUID(), value: value(), reason: reason().trim(), expires_on: expires() || undefined },
      }));
      // A filtered list would otherwise hide the declaration that was just saved.
      setQuery("");
      setPage(0);
      closeDetail();
    } catch (caught) {
      setError(messageOf(caught, "Could not save the declaration."));
    } finally {
      setBusy(false);
    }
  };

  const withdraw = async (entry: { id: string; root_id: string; reason: string }) => {
    const confirmed = await confirmDestructive({
      title: "Stop ignoring this value?",
      message: `“${entry.reason}” is removed from the ignore file in ${folder(entry.root_id)}. This value is screened as a secret again.`,
      okLabel: "Remove declaration",
    });
    if (!confirmed) return;
    setBusy(true);
    setError("");
    try {
      await props.client.deleteProjectSecretIgnore(props.projectId, entry.id, entry.root_id);
      projection.update((previous) => ({
        catalog: {
          rules: (previous?.catalog.rules ?? []).filter((r) => !(r.id === entry.id && r.root_id === entry.root_id)),
        },
        roots: previous?.roots ?? [],
      }));
      closeDetail();
    } catch (caught) {
      setError(messageOf(caught, "Could not remove the declaration."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section class="den-settings-section" aria-label="Ignored values">
      <p class="den-settings-hint">
        Public values declared in <code>.paintedwolf/ignores.yaml</code>. Add and remove them here or from a detection in the editor or chat. The file stays the source of truth: editing it directly does the same thing, and reload shows what it holds now.
      </p>
      <Show when={projection.error()}>
        <p role="alert" class="den-settings-hint">Could not load ignored values. Reload to try again.</p>
      </Show>
      <SettingsListPanel
        class="den-managed-secrets-panel"
        chrome={detailOpen()
          ? <SettingsListBackChrome label="Back to ignored values" onBack={closeDetail} />
          : <SettingsListChrome
              count={`${rules().length} ignored value${rules().length === 1 ? "" : "s"}`}
              secondaryAction={<DenButton variant="secondary" compact disabled={projection.loading()} onClick={() => void projection.refresh()}>Reload</DenButton>}
              action={<DenButton variant="primary" compact disabled={roots().length === 0} onClick={openAdd}>Add value</DenButton>}
            />}
      >
        <Show when={!detailOpen()}>
          <div class="den-settings-detail">
            <DenInput
              aria-label="Search ignored values" type="search" placeholder="Search values or reasons" value={query()}
              onInput={(event) => { setQuery(event.currentTarget.value); setPage(0); }}
            />
          </div>
        </Show>
        <ListSurfaceInbox
          scrollport detailOpen={detailOpen()} isEmpty={filtered().length === 0}
          empty={<p class="den-settings-hint den-settings-list-inbox__empty">{validRules().length ? "No values match this search." : "No ignored public values."}</p>}
          list={<>
            <For each={paged().slice}>{(entry) => (
              <SettingsListRow
                primary={entry.reason}
                secondary={`${folder(entry.root_id)} · ${entry.expires_on ? `Expires ${entry.expires_on}` : "No expiry"}`}
                status={statusLabel[entry.status]} aria-label={`Review ignored value: ${entry.reason}`}
                onSelect={() => { setError(""); setSelected(key(entry)); }}
              />
            )}</For>
            <TablePager page={paged().page} pageSize={PAGE_SIZE} total={filtered().length} onPageChange={setPage} compact ariaLabel="Ignored values pagination" />
          </>}
          detail={<>
            <Show when={adding()}>
              <form class="den-settings-detail" onSubmit={(event) => void save(event)}>
                <h3>Ignore a public value</h3>
                <DenField label="Exact public value" hint="Matched exactly, whitespace included. There are no patterns, prefixes, or paths." hintId={valueHintId}>
                  <DenTextarea
                    class="den-settings-api-key-input" rows={4} required spellcheck={false}
                    aria-label="Exact public value" aria-describedby={valueHintId}
                    value={value()} onInput={(event) => setValue(event.currentTarget.value)}
                  />
                </DenField>
                <DenField label="Reason" hint="Recorded beside the value, so the next reader knows why it is safe." hintId={reasonHintId}>
                  <DenInput aria-label="Reason" aria-describedby={reasonHintId} required maxlength={240} value={reason()} placeholder="Published example from the vendor docs" onInput={(event) => setReason(event.currentTarget.value)} />
                </DenField>
                <DenField label="Folder" hint="Written to .paintedwolf/ignores.yaml in this folder." hintId={folderHintId}>
                  <DenSelect
                    aria-label="Folder" aria-describedby={folderHintId} value={draftRootId()} onValueChange={setDraftRoot}
                    options={roots().map((root) => ({ value: root.id, label: root.label || root.path, description: root.path }))}
                  />
                </DenField>
                <DenField label="Expiry (optional)" hint="Ends the exception at midnight UTC on this date." hintId={expiryHintId}>
                  <DenInput type="date" aria-label="Expiry (optional)" aria-describedby={expiryHintId} value={expires()} onInput={(event) => setExpires(event.currentTarget.value)} />
                </DenField>
                <p class="den-settings-hint">The value is written in plaintext. Declare only what is safe to commit and share. Protected credentials stay protected.</p>
                <Show when={error()}><p role="alert" class="den-settings-hint">{error()}</p></Show>
                <div class="den-settings-actions">
                  <DenButton variant="primary" type="submit" disabled={!canSave()}>{busy() ? "Saving…" : "Save declaration"}</DenButton>
                  <DenButton variant="ghost" type="button" disabled={busy()} onClick={closeDetail}>Cancel</DenButton>
                </div>
              </form>
            </Show>
            <ShowLatest when={current()} by={key}>{(entry) => (
              <div class="den-settings-detail">
                <h3>{entry().reason}</h3>
                <p class="den-settings-hint">{statusHint[entry().status]}</p>
                <DenField label="Exact public value">
                  <DenTextarea class="den-settings-api-key-input" readonly rows={4} value={entry().value} />
                </DenField>
                <p class="den-settings-hint">{folder(entry().root_id)} · {entry().expires_on ? `Expires at midnight UTC on ${entry().expires_on}` : "No expiry"}</p>
                <p class="den-settings-hint"><code class="break-all">{entry().path}</code></p>
                <Show when={error()}><p role="alert" class="den-settings-hint">{error()}</p></Show>
                <DenButton variant="secondary" onClick={() => openFile(entry().root_id)}>Open ignore file</DenButton>
                <section class="den-settings-detail__danger">
                  <h4>Stop ignoring this value</h4>
                  <p class="den-settings-hint">Removes the entry from the ignore file. This value is screened as a secret again, without a rescan.</p>
                  <DenButton variant="danger" compact disabled={busy()} onClick={() => void withdraw(entry())}>{busy() ? "Removing…" : "Remove declaration"}</DenButton>
                </section>
              </div>
            )}</ShowLatest>
          </>}
        />
      </SettingsListPanel>
      <Show when={invalidRules().length}>
        <div class="den-settings-detail">
          <h3>Not in force</h3>
          <For each={invalidRules()}>{(defect) => <p class="den-settings-hint">{defect.reason}</p>}</For>
          <p class="den-settings-hint">A refused entry has no listing row. Open the ignore file to correct or delete it.</p>
        </div>
      </Show>
    </section>
  );
}
