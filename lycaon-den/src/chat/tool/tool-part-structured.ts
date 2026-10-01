import { FILE_EDIT_ARG_KEYS, fileEditFromPart } from "../file-edit/file-edit-model.ts";
import { readLogDigestFromOutput } from "./read-tool-output.ts";
import { isRecord } from "../../utils/type-guards.ts";
import { overlayMergePresentationSections } from "../worker/overlay-merge-presentation.ts";
import type { ToolPartView } from "./tool-part-model.ts";
import { liveToolProcess, toolResultText, toolRunningLabel } from "./tool-part-model.ts";
import { contentSection, extractReadableToolBody, normalizeToolWireOutput, parseToolJsonObject, spillPathFromJsonObject, type StructuredToolPresentation, type StructuredToolSection, type ToolFact } from "./tool-presentation-contract.ts";
import { ByteCache, retainedValueBytes } from "../../utils/byte-cache.ts";
import { TOOL_PATH_ENTRY_KINDS } from "./tool-presentation.generated.ts";
import { formatScalar } from "./structured/format.ts";
import { presentationFromJsonObject, presentationFromJsonArray, presentationFromReadableFileJson, presentationFromDiscoveryToolJson } from "./structured/json.ts";
import { isDiscoveryRequest } from "./structured/discovery.ts";
import { commandIOArgsFacts, COMMAND_IO_ARG_KEYS, factsFromArgs, httpRequestArgsFacts } from "./structured/arguments.ts";

// Cache identity includes every presentation input.
type PresentationCacheEntry = {
  status: ToolPartView["status"];
  tool: string;
  displaySubject: string | undefined;
  kind: ToolPartView["kind"];
  output: string | null;
  outputReference: ToolPartView["outputReference"];
  args: Record<string, unknown> | undefined;
  externalAccess: ToolPartView["externalAccess"];
  outcome: ToolPartView["outcome"];
  codes: ToolPartView["codes"];
  feedback: ToolPartView["feedback"];
  argsRedaction: ToolPartView["argsRedaction"];
  view: StructuredToolPresentation;
};

const presentationCache = new ByteCache<string, PresentationCacheEntry>(8 * 1024 * 1024, 1024);

export function resetStructuredToolPresentationCacheForTests(): void {
  presentationCache.clear();
}

function compactionSection(
  banner: string,
  spillPath?: string,
): StructuredToolSection {
  return { kind: "compaction", banner, spillPath };
}

function prependCompactionSection(
  sections: StructuredToolSection[],
  banner: string | null,
  spillPath?: string,
): void {
  if (!banner) return;
  sections.unshift(compactionSection(banner, spillPath));
}

// Spill paths are host-data-relative and remain plain text.
function spillFact(spillPath: string): ToolFact {
  return {
    label: "Full output",
    value: spillPath,
    wide: true,
    hostDataSpill: true,
  };
}

export function buildStructuredToolPresentation(
  part: ToolPartView,
): StructuredToolPresentation {
  const output = toolResultText(part);
  const cached = presentationCache.get(part.id);
  if (
    cached &&
    cached.status === part.status &&
    cached.tool === part.tool &&
    cached.displaySubject === part.displaySubject &&
    cached.kind === part.kind &&
    cached.output === output &&
    cached.outputReference === part.outputReference &&
    cached.args === part.args &&
    cached.externalAccess === part.externalAccess &&
    cached.outcome === part.outcome &&
    cached.codes === part.codes &&
    cached.feedback === part.feedback &&
    cached.argsRedaction === part.argsRedaction
  ) {
    return cached.view;
  }

  const view = buildStructuredToolPresentationFresh(part, output);
  // Workflow state paths address scaffold fields, not project files.
  if (part.tool === "state_query" || part.tool === "state_update") {
    for (const fact of view.argsFacts) delete fact.path;
    for (const section of view.sections) {
      if (section.kind === "facts") for (const fact of section.facts) delete fact.path;
    }
  }
  const inputPathKind = TOOL_PATH_ENTRY_KINDS[part.tool];
  const inputPath = typeof part.args?.path === "string" ? part.args.path.trim() : "";
  if (inputPathKind && inputPath) {
    for (const fact of view.argsFacts) {
      if (fact.path?.path === inputPath) fact.path.entryKind = inputPathKind;
    }
  }
  const entry: PresentationCacheEntry = {
    status: part.status,
    tool: part.tool,
    displaySubject: part.displaySubject,
    kind: part.kind,
    output,
    outputReference: part.outputReference,
    args: part.args,
    externalAccess: part.externalAccess,
    outcome: part.outcome,
    codes: part.codes,
    feedback: part.feedback,
    argsRedaction: part.argsRedaction,
    view,
  };
  presentationCache.set(part.id, entry, part.id.length * 2 + retainedValueBytes(entry));
  return view;
}

function tryParseToolJsonWire(
  wireBody: string,
): { parsed: unknown; jsonBody: string } | null {
  const trimmed = wireBody.trim();
  const object = parseToolJsonObject(trimmed);
  if (object) return object;
  if (!trimmed.startsWith("[")) return null;
  try {
    return { parsed: JSON.parse(trimmed) as unknown, jsonBody: trimmed };
  } catch {
    return null;
  }
}

/** Separates file metadata from readable content. */

function finalizeJsonPresentation(
  sections: StructuredToolSection[],
  parsed: unknown,
  compactionBanner: string | null,
): void {
  const spillPath = isRecord(parsed) ? spillPathFromJsonObject(parsed) : undefined;
  if (compactionBanner) {
    prependCompactionSection(sections, compactionBanner, spillPath);
    return;
  }
  if (!spillPath) return;
  const factsSection = sections.find((section) => section.kind === "facts");
  if (factsSection?.kind === "facts") {
    factsSection.facts.push(spillFact(spillPath));
    return;
  }
  sections.unshift({ kind: "facts", facts: [spillFact(spillPath)] });
}

const MANAGED_SECRET_TOOLS = new Set([
  "secret_generate",
  "secret_list",
  "secret_revoke",
]);

function managedSecretMetadataFacts(value: unknown): ToolFact[] | null {
  if (!isRecord(value) || typeof value.reference !== "string") return null;
  const facts: ToolFact[] = [
    { label: "Reference", value: value.reference, wide: true },
  ];
  for (const [key, label] of [
    ["name", "Name"],
    ["purpose", "Purpose"],
    ["scope", "Scope"],
    ["chat_title", "Owning chat"],
    ["format", "Format"],
    ["entropy_bits", "Entropy bits"],
    ["created_at", "Created"],
    ["expires_at", "Expires"],
    ["state", "State"],
  ] as const) {
    const item = value[key];
    if (item === null || item === undefined || item === "") continue;
    facts.push({ label, value: formatScalar(item), wide: key === "purpose" });
  }
  return facts;
}

function managedSecretSections(
  tool: string,
  parsed: Record<string, unknown>,
): StructuredToolSection[] {
  const sections: StructuredToolSection[] = [];
  const values = tool === "secret_list"
    ? Array.isArray(parsed.items) ? parsed.items : []
    : [parsed.secret];
  for (const value of values) {
    const facts = managedSecretMetadataFacts(value);
    if (facts) sections.push({ kind: "facts", facts });
  }
  if (sections.length === 0) {
    sections.push({
      kind: "note",
      text: tool === "secret_list"
        ? "No secret references are visible."
        : "Secret metadata is unavailable.",
    });
  }
  sections.push({
    kind: "note",
    text: "The protected value was not disclosed.",
  });
  return sections;
}

function buildStructuredToolPresentationFresh(
  part: ToolPartView,
  output: string | null,
): StructuredToolPresentation {
  const guidance =
    part.outcome === "rejected" ||
    (part.codes?.length ?? 0) > 0 ||
    (part.feedback?.length ?? 0) > 0;
  const fileEdit = fileEditFromPart(part);
  const ioSkip =
    part.kind === "command" ? COMMAND_IO_ARG_KEYS : undefined;
  const skipExtra = new Set([
    ...(fileEdit ? FILE_EDIT_ARG_KEYS : []), ...(ioSkip ?? []),
    ...(isDiscoveryRequest(part.tool, part.args) ? ["need"] : []),
  ]);
  const argsField = part.argsRedaction?.fieldPrefix;
  const argsFacts = [
    ...(part.displaySubject?.trim() ? [{ label: "Target", value: part.displaySubject, wide: true, redactionField: "tool_result.display_subject" }] : []),
    ...(part.tool === "http_request"
      ? httpRequestArgsFacts(part.args, argsField)
      : factsFromArgs(part.args, skipExtra, argsField)),
    ...(part.kind === "command"
      ? commandIOArgsFacts(part.args, argsField)
      : []),
  ];
  const sections: StructuredToolSection[] = [];
  const emptyView = (
    extra: Partial<StructuredToolPresentation> = {},
  ): StructuredToolPresentation => {
    const view: StructuredToolPresentation = {
      argsFacts,
      sections,
      evidenceHandle: null,
      rawOutputAvailable: false,
      guidance,
      ...extra,
    };
    attachExternalAccessSection(part, view.sections);
    return view;
  };

  if (part.outputReference) {
    const process = liveToolProcess(part);
    if (process) sections.push({ kind: "background_process", handle: process.handle, running: process.running });
    return emptyView({ rawOutputAvailable: true });
  }
  if (!output) {
    if (part.status === "running") {
      sections.push({ kind: "note", text: toolRunningLabel(part) });
    }
    return emptyView();
  }

  const { compactionBanner, evidenceHandle, body } = normalizeToolWireOutput(output);

  if (guidance) {
    sections.push(contentSection(body));
    return emptyView({ evidenceHandle });
  }

  const pathHint =
    typeof part.args?.path === "string"
      ? part.args.path
      : typeof part.args?.file_path === "string"
        ? part.args.file_path
        : undefined;

  const jsonWire = tryParseToolJsonWire(body);
  if (
    (part.kind === "read" || part.kind === "command" || part.kind === "write") &&
    !jsonWire
  ) {
    if (!fileEdit) {
      // Recover content from truncated compaction residue.
      const recovered = compactionBanner ? extractReadableToolBody(null, body) : null;
      if (recovered) {
        sections.push(contentSection(recovered));
        prependCompactionSection(
          sections,
          compactionBanner,
          undefined,
        );
        return emptyView({
          evidenceHandle,
          sections,
          rawOutputAvailable: true,
        });
      }
      sections.push(contentSection(body));
    }
    prependCompactionSection(sections, compactionBanner, undefined);
    return emptyView({
      evidenceHandle,
      sections,
      rawOutputAvailable: true,
    });
  }

  if (jsonWire) {
    if (part.kind === "read") {
      const logDigest = readLogDigestFromOutput(jsonWire.jsonBody);
      if (logDigest) {
        sections.push({ kind: "log_digest", view: logDigest });
        finalizeJsonPresentation(sections, jsonWire.parsed, compactionBanner);
        return emptyView({
          evidenceHandle,
          sections,
          rawOutputAvailable: true,
        });
      }
    }
    const parsed = jsonWire.parsed;
    if (Array.isArray(parsed)) {
      sections.push(...presentationFromJsonArray(parsed));
      finalizeJsonPresentation(sections, parsed, compactionBanner);
      return emptyView({
        evidenceHandle,
        sections,
        rawOutputAvailable: true,
      });
    }
    if (isRecord(parsed)) {
      if (MANAGED_SECRET_TOOLS.has(part.tool)) {
        sections.push(...managedSecretSections(part.tool, parsed));
        finalizeJsonPresentation(sections, parsed, compactionBanner);
        return emptyView({
          evidenceHandle,
          sections,
          rawOutputAvailable: false,
          rawOutputProtected: true,
        });
      }
      const live = liveToolProcess(part);
      if (live && part.kind === "command") {
        sections.push({
          kind: "background_process",
          handle: live.handle,
          running: live.running,
        });
      }
      if (part.tool === "request_tools" || (part.tool === "skills_read" && isRecord(parsed.discovery))) {
        sections.push(...presentationFromDiscoveryToolJson(parsed));
        finalizeJsonPresentation(sections, parsed, compactionBanner);
        return emptyView({
          evidenceHandle,
          sections,
          rawOutputAvailable: true,
        });
      }
      if (part.tool === "preview_overlay" || part.tool === "promote_overlay") {
        const overlaySections = overlayMergePresentationSections(parsed);
        if (overlaySections.length) {
          sections.push(...overlaySections);
        }
        finalizeJsonPresentation(sections, parsed, compactionBanner);
        return emptyView({
          evidenceHandle,
          sections,
          rawOutputAvailable: true,
        });
      }
      if (
        part.kind === "read" ||
        (typeof parsed.content === "string" &&
          (typeof parsed.path === "string" || !!pathHint))
      ) {
        sections.push(
          ...presentationFromReadableFileJson(
            parsed,
            jsonWire.jsonBody,
          ),
        );
        finalizeJsonPresentation(sections, parsed, compactionBanner);
        return emptyView({
          evidenceHandle,
          sections,
          rawOutputAvailable: true,
        });
      }
      sections.push(...presentationFromJsonObject(parsed, jsonWire.jsonBody));
      finalizeJsonPresentation(sections, parsed, compactionBanner);
      return emptyView({
        evidenceHandle,
        sections,
        rawOutputAvailable: true,
      });
    }
  }

  // Recover unbalanced JSON residue with a content field.
  const recoveredBody = extractReadableToolBody(null, body);
  if (recoveredBody) {
    sections.push(contentSection(recoveredBody));
    prependCompactionSection(sections, compactionBanner, undefined);
    return emptyView({
      evidenceHandle,
      sections,
      rawOutputAvailable: true,
    });
  }

  sections.push(contentSection(body));
  prependCompactionSection(sections, compactionBanner, undefined);
  return emptyView({
    evidenceHandle,
    sections,
    rawOutputAvailable: true,
  });
}

function attachExternalAccessSection(
  part: ToolPartView,
  sections: StructuredToolSection[],
): void {
  const ea = part.externalAccess ?? null;
  if (!ea) return;
  sections.push({ kind: "network", externalAccess: ea });
}
