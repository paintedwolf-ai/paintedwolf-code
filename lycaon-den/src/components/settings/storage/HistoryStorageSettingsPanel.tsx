import { createEffect, createSignal, For, on, onCleanup, Show } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { HistoryProtection, HistoryRetentionPolicy, HistoryRetentionPreview, HistoryRetentionRule, SessionSummary } from "../../../api/types.ts";
import { confirmDestructive } from "../../../platform/interaction/confirm-dialog.ts";
import { HISTORY_CLASSES, RETENTION_MODES, historyBytes, historyLaneLabel, historyLaneUsage, retentionLosses, retentionRule, retentionValid, type HistoryClass } from "../../../settings/storage/history-storage-model.ts";
import { createSurfaceQuery } from "../../../ui/surface-query.ts";
import { DenButton } from "../../primitives/DenButton.tsx";
import { DenCheckbox } from "../../primitives/DenCheckbox.tsx";
import { DenInput } from "../../primitives/DenInput.tsx";
import { DenSelect } from "../../primitives/DenSelect.tsx";
import { ShowLatest } from "../../primitives/ShowLatest.tsx";
import { settingAnchor, settingLabel } from "../../../settings/settings-registry.ts";

export function HistoryStorageSettingsPanel(props: { client: LycaonClient | null | undefined }) {
  const query = createSurfaceQuery({
    name: "history-storage", source: () => props.client ? { client: props.client, key: "device" } : null,
    load: ({ client }) => client.getHistoryStorage(),
  });
  const projects = createSurfaceQuery({
    name: "history-projects", source: () => props.client ? { client: props.client, key: "device" } : null,
    load: ({ client }) => client.listProjects(), required: false,
  });
  const [draft, setDraft] = createSignal<HistoryRetentionPolicy>();
  const [preview, setPreview] = createSignal<HistoryRetentionPreview>();
  const [scope, setScope] = createSignal("");
  const [busy, setBusy] = createSignal<string>();
  const [message, setMessage] = createSignal<string>();
  const [error, setError] = createSignal<string>();
  const [protectionProject, setProtectionProject] = createSignal("");
  const [protectionTask, setProtectionTask] = createSignal("");
  const [archived, setArchived] = createSignal(false);
  const [tasks, setTasks] = createSignal<SessionSummary[]>([]);
  const [taskCursor, setTaskCursor] = createSignal<string>();
  let stopPruning = false;
  let lifetime = 0;
  onCleanup(() => { lifetime++; stopPruning = true; });

  createEffect(on(() => props.client, () => {
    lifetime++; setBusy(undefined); setMessage(undefined); setError(undefined);
    setDraft(undefined); setPreview(undefined); setScope("");
    setTasks([]); setTaskCursor(undefined); setProtectionProject(""); setProtectionTask("");
    stopPruning = true;
  }));
  createEffect(on(() => query.value()?.policy, (policy) => {
    if (policy && (!draft() || draft()?.revision !== policy.revision)) {
      setDraft(structuredClone(policy));
      setPreview(undefined);
    }
  }));
  const changeRule = (id: HistoryClass, rule: HistoryRetentionRule) => {
    setDraft((policy) => policy ? { ...policy, [id]: rule } : policy);
    setPreview(undefined);
    setMessage(undefined);
  };
  const request = (policy: HistoryRetentionPolicy) => ({ policy, ...(scope() ? { project_id: scope() } : {}) });
  const run = async (name: string, operation: (current: () => boolean) => Promise<void>) => {
    if (busy()) return;
    const started = lifetime; const client = props.client;
    const current = () => lifetime === started && props.client === client;
    setBusy(name); setError(undefined); setMessage(undefined);
    try { await operation(current); }
    catch (cause) {
      if (!current()) return;
      setPreview(undefined);
      setError(`${cause instanceof Error ? cause.message : String(cause)} Review a new preview before saving or pruning.`);
    } finally { if (current()) setBusy(undefined); }
  };
  const review = () => run("preview", async (current) => {
    const client = props.client; const policy = draft();
    if (!client || !policy || !retentionValid(policy)) return;
    const reviewed = await client.previewHistoryRetention(request(policy));
    if (current()) setPreview(reviewed);
  });
  const save = () => run("save", async (current) => {
    const client = props.client; const reviewed = preview(); const policy = draft();
    if (!client || !reviewed || !policy || scope()) return;
    const losses = retentionLosses(policy);
    if (losses && !await confirmDestructive({ title: "Enable automatic history pruning?", okLabel: "Save retention policy", message: `This policy can permanently remove history items each day. Lost capabilities: ${losses}. Task text, audit summaries, usage and cost totals, settings, and credentials remain.` })) return;
    if (!current()) return;
    const saved = await client.updateHistoryStorage({ ...request(policy), preview_token: reviewed.token });
    if (!current()) return;
    setDraft(saved); setPreview(undefined);
    await query.refresh();
    if (current()) setMessage(losses ? "Retention policy saved. Eligible history can now be pruned automatically." : "All history will be kept indefinitely.");
  });
  const prune = () => run("prune", async (current) => {
    const client = props.client; const reviewed = preview(); const policy = draft();
    if (!client || !reviewed || !policy || reviewed.eligible_count === 0) return;
    const selection = request(policy);
    stopPruning = false;
    if (!await confirmDestructive({ title: "Permanently prune reviewed history?", okLabel: "Prune history", message: `Remove ${reviewed.eligible_count} eligible history items in ${selection.project_id ? "the selected project" : "all projects"}? Lost capabilities: ${retentionLosses(policy)}. Task text, audit summaries, usage and cost totals, settings, and credentials remain. This cannot be undone.` })) return;
    if (stopPruning || !current()) return;
    let token = reviewed.token; let count = 0; let bytes = 0; let more = false;
    do {
      const result = await client.pruneHistory({ ...selection, preview_token: token });
      if (!current()) return;
      count += result.removed_count; bytes += result.released_bytes;
      more = !result.complete; token = result.preview_token;
      setMessage(`Removed ${count} history items; ${historyBytes(bytes)} of history references released.`);
    } while (more && !stopPruning);
    setPreview(undefined);
    await query.refresh();
    if (current() && more) setMessage(`Stopped after ${count} history items. Review again to continue.`);
  });
  const protect = (protection: HistoryProtection) => run("protect", async (current) => {
    if (!props.client) return;
    if (protection.protected) await props.client.createHistoryProtection(protection);
    else await props.client.deleteHistoryProtection(protection.scope_id);
    if (!current()) return;
    setPreview(undefined); await query.refresh();
    if (current()) setMessage(protection.protected ? "History is protected from pruning." : "History protection removed.");
  });
  const loadTasks = async (project: string, archivedTasks: boolean, append = false) => {
    const client = props.client; if (!client || !project) return;
    await run("tasks", async (current) => {
      if (!append) { setTasks([]); setTaskCursor(undefined); }
      const page = await client.listProjectSessions(project, { archived: archivedTasks, limit: 100, ...(append && taskCursor() ? { cursor: taskCursor() } : {}) });
      if (!current()) return;
      setTasks((previous) => append ? [...previous, ...page.sessions] : page.sessions);
      setTaskCursor(page.next_cursor ?? undefined);
    });
  };
  const projectOptions = () => (projects.value() ?? []).map((project) => ({ value: project.id, label: project.name || "Untitled project" }));
  const protectionLabel = (protection: HistoryProtection) => {
    const project = projects.value()?.find((row) => row.id === protection.scope_id);
    const task = tasks().find((row) => row.id === protection.scope_id);
    return protection.scope_type === "project" ? (project?.name || `Project ${protection.scope_id}`) : (task?.title || `Task ${protection.scope_id}`);
  };

  return <section class="den-settings-section" data-testid="history-storage-settings" {...settingAnchor("history-storage")}>
    <h3>{settingLabel("history-storage")}</h3>
    <p class="den-settings-hint">History is kept indefinitely by default. Retention removes selected history items while keeping task text, audit summaries, settings, and credentials. Protected and actively used history is excluded.</p>
    <Show when={!props.client}><p class="den-settings-hint">Connect to the engine to manage retained history.</p></Show>
    <Show when={query.error()}><p role="alert">{query.error()}</p></Show>
    <ShowLatest when={draft()}>{(policy) => <>
      <Show when={policy().suspended}><p role="status">Imported retention is paused. Review and save a policy to enable automatic pruning.</p></Show>
      <Show when={query.value()?.lanes.length} fallback={<p class="den-settings-hint">Storage measurements are not yet available.</p>}>
        <p class="den-settings-hint">Measurements refresh hourly in the background and can lag recent changes. They describe retained content; filesystem allocation can differ, and recovery snapshots can share disk space.</p>
        <For each={query.value()?.lanes}>{(lane) => <p class="den-settings-hint">{historyLaneLabel(lane.id)}: {historyLaneUsage(lane)}</p>}</For>
      </Show>
      <For each={HISTORY_CLASSES}>{(row) => <div class="den-settings-pref-row">
        <div class="den-settings-pref-copy"><span class="den-settings-pref-label">{row.label}</span><p class="den-settings-hint">Pruning removes: {row.loss.toLowerCase()}.<Show when={row.id === "receipt_detail"}> Usage and cost totals remain.</Show></p></div>
        <DenSelect aria-label={`${row.label} retention`} data-testid={`history-${row.id}-mode`} options={RETENTION_MODES} value={policy()[row.id].mode} disabled={!!busy()} onValueChange={(mode) => changeRule(row.id, retentionRule(mode as HistoryRetentionRule["mode"]))} />
        <Show when={policy()[row.id].mode !== "forever"}>
          <label>{policy()[row.id].mode === "max_age" ? "Days" : "GiB"}
            <DenInput aria-label={`${row.label} limit`} inputmode="numeric" disabled={!!busy()} value={policy()[row.id].mode === "max_age" ? policy()[row.id].max_age_days : (policy()[row.id].max_bytes ?? 0) / 1024 ** 3} onInput={(event) => {
              const value = Number(event.currentTarget.value);
              changeRule(row.id, policy()[row.id].mode === "max_age" ? { mode: "max_age", max_age_days: value } : { mode: "max_bytes", max_bytes: Math.round(value * 1024 ** 3) });
            }} />
          </label>
        </Show>
      </div>}</For>
      <Show when={!retentionValid(policy())}><p role="alert">Enter a positive whole number of days or a positive storage limit.</p></Show>
      <DenSelect aria-label="Preview and prune scope" options={[{ value: "", label: "All projects" }, ...projectOptions()]} value={scope()} disabled={!!busy()} onValueChange={(value) => { setScope(value); setPreview(undefined); }} />
      <DenButton variant="secondary" data-testid="history-preview" disabled={!!busy() || !retentionValid(policy())} onClick={() => void review()}>Review eligible history</DenButton>
      <Show when={preview()} keyed>{(reviewed) => <div data-testid="history-preview-result">
        <p>{reviewed.eligible_count} history items eligible · up to {historyBytes(reviewed.reclaimable_bytes)} of history references releasable.</p>
        <p class="den-settings-hint">{historyBytes(reviewed.shared_protected_bytes)} remain shared or protected. Released references do not guarantee an immediate reduction in disk use.</p>
        <details><summary><span class="den-disclosure-caret" aria-hidden="true" />Review selected history</summary><ul><For each={reviewed.candidates}>{(candidate) => <li>{HISTORY_CLASSES.find((row) => row.id === candidate.class)?.label ?? "Historical body"} · {projects.value()?.find((project) => project.id === candidate.project_id)?.name ?? "Project"} · {new Date(candidate.created_at).toLocaleDateString()} · {historyBytes(candidate.reclaimable_bytes)}</li>}</For></ul><Show when={reviewed.eligible_count > reviewed.candidates.length}><p class="den-settings-hint">Showing the first {reviewed.candidates.length} of {reviewed.eligible_count} eligible bodies. Pruning applies to the entire reviewed selection.</p></Show></details>
        <Show when={!reviewed.complete}><p class="den-settings-hint">Pruning will process the reviewed selection in batches.</p></Show>
        <DenButton variant="primary" data-testid="history-save" disabled={!!busy() || !!scope()} onClick={() => void save()}>Save automatic retention</DenButton>
        <Show when={scope()}><p class="den-settings-hint">Review all projects before saving an automatic retention policy.</p></Show>
        <DenButton variant="danger" data-testid="history-prune" disabled={!!busy() || reviewed.eligible_count === 0} onClick={() => void prune()}>Prune reviewed history now</DenButton>
      </div>}</Show>
      <Show when={busy() === "prune"}><DenButton variant="secondary" onClick={() => { stopPruning = true; }}>Stop after this batch</DenButton></Show>
    </>}</ShowLatest>
    <h3>Protected history</h3>
    <DenSelect aria-label="Project to protect" options={[{ value: "", label: "Choose a project" }, ...projectOptions()]} value={protectionProject()} disabled={!!busy()} onValueChange={(id) => { setProtectionProject(id); setProtectionTask(""); setTasks([]); setTaskCursor(undefined); if (id) void loadTasks(id, archived()); }} />
    <Show when={protectionProject()}>
      <DenCheckbox checked={archived()} disabled={!!busy()} onChange={(event) => { setArchived(event.currentTarget.checked); setProtectionTask(""); void loadTasks(protectionProject(), event.currentTarget.checked); }}>Show archived tasks</DenCheckbox>
      <DenSelect aria-label="Task to protect" options={[{ value: "", label: "Entire project" }, ...tasks().map((task) => ({ value: task.id, label: task.title || "Untitled task" }))]} value={protectionTask()} disabled={!!busy()} onValueChange={setProtectionTask} />
      <Show when={taskCursor()}><DenButton variant="secondary" disabled={!!busy()} onClick={() => void loadTasks(protectionProject(), archived(), true)}>Load more tasks</DenButton></Show>
      <DenButton variant="secondary" disabled={!!busy()} onClick={() => void protect({ scope_type: protectionTask() ? "session" : "project", scope_id: protectionTask() || protectionProject(), protected: true })}>Protect history</DenButton>
    </Show>
    <For each={query.value()?.protections}>{(protection) => <div class="den-settings-pref-row"><span>{protectionLabel(protection)}</span><DenButton variant="secondary" disabled={!!busy()} onClick={() => void protect({ ...protection, protected: false })}>Remove protection</DenButton></div>}</For>
    <Show when={message()}><p role="status">{message()}</p></Show>
    <Show when={error()}><p role="alert">{error()}</p></Show>
  </section>;
}
