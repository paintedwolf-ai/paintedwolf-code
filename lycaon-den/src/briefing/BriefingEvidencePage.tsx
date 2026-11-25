import { For, Show, createResource } from "solid-js";
import type { BriefingRecord, BriefingRecordItem, BriefingRecordSection } from "../api/types.ts";
import type { BriefingsClient } from "../api/http-capabilities/briefings.ts";
import { isApiErrorCode } from "../api/http.ts";
import { Scrollport } from "../components/primitives/Scrollport.tsx";
import { ChatContentLink } from "../components/transcript/ChatContentLink.tsx";
import { SummaryLine } from "./ActivitySummary.tsx";
import { EvidenceInput } from "./EvidenceInput.tsx";
import { briefingCopy } from "./briefing-copy.ts";
import { briefingRecordLinks, briefingReturnAnchor } from "./briefing-evidence.ts";

const copy = briefingCopy.evidence;

type Props = { projectId: string; recordId: string; client: BriefingsClient | null };

type Loaded = { record: BriefingRecord } | { missing: true } | { failed: true };

/** A read-only evidence record: the summary, then what each of its sections was given and how it was made. */
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
            <Show when={record()} keyed>{(current) => <p class="den-walk-page__note">{copy.disclaimer(copy.method[current.method])}</p>}</Show>
          </div>
          <Show when={back()}>
            {(anchor) => (
              <button type="button" class="den-briefing-evidence__back" onClick={() => anchor().focus()}>
                {copy.back(anchor().label)}
              </button>
            )}
          </Show>
        </header>
        <Show when={loaded()}>
          {(state) => (
            <>
              <Show when={"missing" in state()}><p class="den-walk-page__note" role="status">{copy.expired}</p></Show>
              <Show when={"failed" in state()}><p class="den-walk-page__error" role="alert">{copy.loadFailed}</p></Show>
            </>
          )}
        </Show>
        <Show when={record()} keyed>{(current) => <RecordBody record={current} projectId={props.projectId} />}</Show>
      </article>
    </Scrollport>
  );
}

type SummaryLineView = { id: string; label?: string; text: string; source: NonNullable<BriefingRecordSection["result"]>["source"] };

function sectionName(record: BriefingRecord, section: BriefingRecordSection): string {
  return copy.sectionNames[record.profile_id]?.[section.section_id] ?? section.section_id;
}

/** A profile with several sections names each line; a single section shows one line per paragraph. */
function summaryLines(record: BriefingRecord): SummaryLineView[] {
  const named = record.sections.length > 1;
  return record.sections.flatMap((section) => {
    const result = section.available ? section.result : undefined;
    if (!result) return [];
    const name = sectionName(record, section);
    if (named) return [{ id: name.toLowerCase(), label: name, text: result.text, source: result.source }];
    return result.text.split(/\n\s*\n/).filter((paragraph) => paragraph.trim())
      .map((paragraph) => ({ id: name.toLowerCase(), text: paragraph.trim(), source: result.source }));
  });
}

function RecordBody(props: { record: BriefingRecord; projectId: string }) {
  const links = () => briefingRecordLinks(props.record.record_id);
  return (
    <>
      <div class="den-briefing-evidence__lead">
        <div class="den-activity-summary-frame">
          <div class="den-activity-summary" data-testid="briefing-evidence-summary" data-state="ready">
            <div class="den-activity-summary__sections">
              <For each={summaryLines(props.record)}>
                {(line) => <SummaryLine id={line.id} label={line.label} summary={{ text: line.text, source: line.source }} />}
              </For>
            </div>
          </div>
        </div>
        <Show when={links().length > 0}>
          <nav class="den-action-card__links" aria-label={copy.records} data-testid="briefing-evidence-records">
            <For each={links()}>
              {(link) => <ChatContentLink short label={link.label} projectId={props.projectId} document={() => link.document} testId="briefing-evidence-record" />}
            </For>
          </nav>
        </Show>
      </div>
      <For each={props.record.sections}>{(section) => <SectionInputs record={props.record} section={section} />}</For>
      <section class="den-walk-page__files" aria-labelledby="briefing-evidence-generation">
        <h2 id="briefing-evidence-generation" class="den-walk-page__files-title">{copy.generation}</h2>
        <dl class="den-briefing-evidence__facts">
          <dt>Method</dt><dd>{copy.method[props.record.method]}</dd>
          <dt>Profile</dt><dd>{props.record.profile_id} {props.record.profile_version}</dd>
          <For each={props.record.sections.filter((section) => section.available)}>
            {(section) => <GenerationFacts name={sectionName(props.record, section)} section={section} />}
          </For>
          <dt>Recorded</dt><dd>{new Date(props.record.created_at).toLocaleString()}</dd>
        </dl>
      </section>
    </>
  );
}

function inputNames(items: readonly BriefingRecordItem[], withReason: boolean): string {
  return items.map((item) => {
    const name = (copy.inputNames[item.key] ?? item.key).toLowerCase();
    return withReason && item.reason ? `${name} (${item.reason})` : name;
  }).join(", ");
}

/** What one summary section was given. Every input sent is shown; the rest are named, never listed as inputs. */
function SectionInputs(props: { record: BriefingRecord; section: BriefingRecordSection }) {
  const items = () => (props.section.available ? props.section.items ?? [] : []);
  const sent = () => items().filter((item) => item.inclusion === "included" || item.inclusion === "truncated");
  const absent = () => items().filter((item) => item.inclusion === "unavailable");
  const omitted = () => items().filter((item) => item.inclusion === "omitted");
  const count = () => {
    const source = props.section.result?.source;
    if (source === "deterministic") return copy.computedFrom(sent().length);
    return source === "summarizer" ? copy.writtenFrom(sent().length) : copy.inputCount(sent().length);
  };
  const headingId = () => `briefing-evidence-section-${props.section.section_id}`;
  return (
    <section class="den-walk-page__files" aria-labelledby={headingId()} data-testid="briefing-evidence-section" data-section={props.section.section_id}>
      <h2 id={headingId()} class="den-walk-page__files-title">
        {sectionName(props.record, props.section)}
        <Show when={props.section.available}><span class="den-walk-page__count">{count()}</span></Show>
      </h2>
      <Show when={props.section.available} fallback={<p class="den-walk-page__note">{copy.unavailableSection}</p>}>
        <Show when={sent().length > 0}>
          <div class="den-briefing-evidence__inputs"><For each={sent()}>{(item) => <EvidenceInput item={item} />}</For></div>
        </Show>
        <Show when={omitted().length > 0}><p class="den-walk-page__note" data-testid="briefing-evidence-not-sent">{copy.notSent(inputNames(omitted(), true))}</p></Show>
        <Show when={absent().length > 0}><p class="den-walk-page__note" data-testid="briefing-evidence-nothing-to-send">{copy.nothingToSend(inputNames(absent(), false))}</p></Show>
      </Show>
    </section>
  );
}

function GenerationFacts(props: { name: string; section: BriefingRecordSection }) {
  const result = () => props.section.result;
  return (
    <>
      <dt>{props.name}</dt>
      <dd>
        {props.section.template_id} {props.section.template_version}
        <Show when={result()?.model ? result() : undefined}>{(generated) => <> · {generated().provider_id} {generated().model}</>}</Show>
      </dd>
    </>
  );
}
