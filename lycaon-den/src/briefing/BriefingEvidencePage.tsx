import { For, Show, createResource } from "solid-js";
import type { BriefingRecord, BriefingRecordItem, BriefingRecordSection } from "../api/types.ts";
import type { BriefingsClient } from "../api/http-capabilities/briefings.ts";
import { isApiErrorCode } from "../api/http.ts";
import { Scrollport } from "../components/primitives/Scrollport.tsx";
import { BriefingSummary } from "./BriefingSummary.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { briefingReturnAnchor } from "./briefing-evidence.ts";

const copy = briefingCopy.evidence;

type Props = { projectId: string; recordId: string; client: BriefingsClient | null };

type Loaded = { record: BriefingRecord } | { missing: true } | { failed: true };

/** A read-only evidence record: what each summary section was given and how it was made. */
export function BriefingEvidencePage(props: Props) {
  const [loaded] = createResource(
    () => (props.client ? { client: props.client, projectId: props.projectId, recordId: props.recordId } : null),
    async ({ client, projectId, recordId }): Promise<Loaded> => {
      try {
        return { record: await client.getBriefingRecord(projectId, recordId) };
      } catch (error) {
        return isApiErrorCode(error, ["briefing_record_not_found"]) ? { missing: true } : { failed: true };
      }
    },
  );
  const record = () => {
    const value = loaded();
    return value && "record" in value ? value.record : null;
  };
  const back = () => briefingReturnAnchor(props.recordId);
  return (
    <Scrollport class="den-files-info__body" data-testid="briefing-evidence-page">
      <article class="den-walk-page__article den-briefing-evidence">
        <header class="den-walk-page__head">
          <div class="den-walk-page__heading">
            <h1 class="den-walk-page__title">{copy.title}</h1>
          </div>
          <Show when={back()}>
            {(anchor) => (
              <button type="button" class="den-briefing-evidence__back" onClick={() => anchor().focus()}>
                {copy.back(anchor().label)}
              </button>
            )}
          </Show>
        </header>
        <Show when={loaded() && "missing" in loaded()!}><p class="den-walk-page__note" role="status">{copy.expired}</p></Show>
        <Show when={loaded() && "failed" in loaded()!}><p class="den-walk-page__error" role="alert">{copy.loadFailed}</p></Show>
        <Show when={record()}>{(current) => <RecordBody record={current()} />}</Show>
      </article>
    </Scrollport>
  );
}

function RecordBody(props: { record: BriefingRecord }) {
  const readable = () => props.record.sections.filter((section) => section.available);
  const items = () => readable().flatMap((section) => section.items ?? []);
  const sent = () => items().filter((item) => item.inclusion === "included" || item.inclusion === "truncated");
  const limits = () => items().filter((item) => item.inclusion !== "included");
  const sources = () => [...new Set(items().filter((item) => item.ref).map((item) => item.revision ? `${item.ref} @ ${item.revision}` : item.ref!))];
  return (
    <>
      <section class="den-briefing-evidence__section" aria-labelledby="briefing-evidence-summary">
        <h2 id="briefing-evidence-summary" class="den-walk-page__files-title">{copy.summary}</h2>
        <For each={props.record.sections}>
          {(section) => (
            <Show when={section.available && section.result} fallback={<p class="den-walk-page__note">{copy.unavailableSection}</p>}>
              <BriefingSummary recordId={props.record.record_id}
                sections={[{ id: section.section_id, text: section.result!.text, source: section.result!.source }]} />
            </Show>
          )}
        </For>
      </section>
      <section class="den-briefing-evidence__section" aria-labelledby="briefing-evidence-used">
        <h2 id="briefing-evidence-used" class="den-walk-page__files-title">{copy.used}</h2>
        <For each={sent()}>{(item) => <EvidenceItem item={item} />}</For>
      </section>
      <section class="den-briefing-evidence__section" aria-labelledby="briefing-evidence-coverage">
        <h2 id="briefing-evidence-coverage" class="den-walk-page__files-title">{copy.coverage}</h2>
        <Show when={limits().length > 0} fallback={<p class="den-walk-page__note">{copy.nothingOmitted}</p>}>
          <dl class="den-briefing-evidence__facts">
            <For each={limits()}>
              {(item) => (
                <>
                  <dt>{item.key}</dt>
                  <dd>{copy.inclusion[item.inclusion]}{item.reason ? `: ${item.reason}` : ""}</dd>
                </>
              )}
            </For>
          </dl>
        </Show>
      </section>
      <section class="den-briefing-evidence__section" aria-labelledby="briefing-evidence-sources">
        <h2 id="briefing-evidence-sources" class="den-walk-page__files-title">{copy.sources}</h2>
        <Show when={sources().length > 0} fallback={<p class="den-walk-page__note">{copy.noSources}</p>}>
          <ul class="den-briefing-evidence__sources"><For each={sources()}>{(source) => <li><code>{source}</code></li>}</For></ul>
        </Show>
      </section>
      <section class="den-briefing-evidence__section" aria-labelledby="briefing-evidence-generation">
        <h2 id="briefing-evidence-generation" class="den-walk-page__files-title">{copy.generation}</h2>
        <dl class="den-briefing-evidence__facts">
          <dt>Method</dt><dd>{copy.method[props.record.method]}</dd>
          <dt>Profile</dt><dd>{props.record.profile_id} {props.record.profile_version}</dd>
          <For each={readable()}>{(section) => <GenerationFacts section={section} />}</For>
          <dt>Recorded</dt><dd>{new Date(props.record.created_at).toLocaleString()}</dd>
        </dl>
      </section>
    </>
  );
}

function EvidenceItem(props: { item: BriefingRecordItem }) {
  return (
    <div class="den-briefing-evidence__item" data-inclusion={props.item.inclusion}>
      <p class="den-briefing-evidence__item-head">
        <span>{props.item.key}</span>
        <span class="den-walk-page__note">{copy.inclusion[props.item.inclusion]}</span>
      </p>
      <Show when={props.item.text}><pre class="den-briefing-evidence__excerpt">{props.item.text}</pre></Show>
    </div>
  );
}

function GenerationFacts(props: { section: BriefingRecordSection }) {
  const result = () => props.section.result;
  return (
    <>
      <dt>{props.section.section_id}</dt>
      <dd>
        {props.section.template_id} {props.section.template_version}
        <Show when={result()?.model}>{(model) => <> · {result()!.provider_id} {model()}</>}</Show>
      </dd>
    </>
  );
}
