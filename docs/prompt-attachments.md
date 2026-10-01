# Prompt attachments

How user-supplied bytes and jailed path references enter a prompt: a streaming upload, content-addressed materialization, plane-typed caps, framing, and the reject contract.

**See also:** [Host contract](host-contract.md) · [Visual surface](visual-surface.md) (image faces) · [Security](security.md) · [Den](den.md) · [Tools](tools.md) · [Agent contract](agent-contract.md)

**Machine truth:** OpenAPI `uploadAttachment` / `sendPrompt` · `PromptAttachmentPart` / `PromptReferencePart` in [`session/prompts.yaml`](openapi/components/schemas/session/prompts.yaml) · `prompt_attachments` caps in [`prompt-budgets.yaml`](../lycaon/config/packs/painted-wolf/platform/host/prompt-budgets.yaml) · `lycaon/internal/promptattach` · `lycaon/internal/blobstore` · `lycaon/internal/bytebound`

---

> **Bodies are materialized, then referenced.** An attachment is uploaded before it is prompted with: the host streams it into project host data, and the prompt carries only its stable blob ID. Attachments sit above the Visual plane: text and extracted document content travel as text content parts; only genuine images become `VisualArtifact{source: user}` through image ingest.

## Intake-and-route model

Den has one attach surface. Staging a file uploads it; sending a turn references what was staged. The prompt envelope carries blob IDs and prose, never attachment bodies.

Staged attachment metadata survives an app restart alongside the unsent draft. Only project-scoped blob IDs and reference coordinates are saved in app state; body previews, local preview URLs, and secret values are excluded. Removing a chip or sending its message clears the retained reference. Abandoned uploads follow the host staging lifetime; an expired body must be attached again.

```text
composer attach / paste / drag
        │
        │  POST /v1/projects/{id}/attachments      (raw body, streamed)
        │      peek → Detect → Unwrap? → Detect …  (depth ≤ max_unwrap_depth)
        │      stream through the expansion guard into the blob store
        │      video: decode once in the managed browser; refuse what does not play;
        │             keep the overview sheet and facts beside the blob (`.derived/`)
        │      → { blob_id, filename, mime, kind, bytes, video? }
        │
        │  POST /v1/sessions/{id}/prompts          (blob IDs + prose only)
        │      resolve blob_id → re-detect from stored bytes → project
        ├── image      → VisualArtifact{source: user} → provider image input on vision models
        ├── text-family → bounded preview + path= in a labeled fence
        ├── document   → DocumentExtractor → fenced text
        ├── video      → stored overview sheet → VisualArtifact{source: user}; facts + path + view_video hint in a fence
        └── else       → unsupported_attachment | attachment_too_large
```

**Detect / Unwrap / Route.** Detection is pure and total: peek bytes plus filename and reported MIME name one format. A format is terminal or a container; only non-terminal formats reach the unwrap stage. OOXML and ODF are zip archives claimed as documents, so no unwrapper is ever offered one. Single-member containers (gzip, zstd, xz, bzip2) decode to one inner body and re-enter detection under the inner name. Multi-member archives carry a tree rather than one body and are not admitted.

**Content decides, the suffix refines.** Admission comes from bytes. Within the admitted text family the filename suffix picks the specific media type, because `http.DetectContentType` reports JSON, YAML, TOML, and prose all as `text/plain`, and that distinction decides whether the model is told to query a body with `jq` or page it byte-wise. Text admission validates the complete stored body as UTF-8; the detection peek classifies, it never proves the unseen tail.

**Visual-plane invariant:** non-image attachment bytes and extracted prose never become a `VisualArtifact`. Only decoded raster images enter the Visual plane through image ingest. A video's own bytes never do; the frame sheet the host draws from it is a raster and enters like any attached image.

## Large text paste

Plain text stays inline only while it fits the host-advertised composer policy. A UTF-8 paste at or above `composer.auto_attach_paste_bytes`, or a smaller paste whose selection replacement would push the draft past `composer.max_inline_text_bytes`, is intercepted before textarea mutation. Den creates `Pasted text.txt`, shows an uploading chip, and streams it through the ordinary attachment route; the draft and caret do not move. Failed uploads keep the in-memory body behind Retry / Remove controls, and Send stays closed until the chip is staged or removed.

The prompt route independently rejects inline `text` over `composer.max_inline_text_bytes` with `prompt_text_too_large`. This is the hard boundary for alternate clients, restored drafts, and any input path that did not originate in a clipboard event. Tauri's shared-document string ceiling is only an IPC defense above this value, not a second product policy.

Large text bodies do not receive the ordinary per-body preview: at or above the same threshold, `prompt.max_large_text_preview_bytes` applies. Payload attachments and content-bearing references share one `prompt.max_turn_preview_bytes` allowance, consumed by attachments first in array order, then references in array order. Once exhausted, later fences carry metadata, the truncation marker, materialized path, and read/search hint with no body preview. The complete attachment is never discarded.

## Supportability matrix

| Class | Examples | Routing | Prompt representation |
|-------|----------|---------|-----------------------|
| **Image** | png, jpeg, webp, gif | Visual plane, from the same upload route as everything else | Provider image input on vision models; explicit non-vision notice otherwise; Den projects always |
| **Text-family** | source code, txt, md, json, yaml/yml, toml, csv, tsv, log, html, xml | bounded UTF-8 preview | labeled fenced context block (filename + mime + full-body size; truncation note when capped; path + tool hint always) |
| **Document** | pdf; docx/xlsx/pptx (OOXML); odt/ods/odp (OpenDocument); rtf | `DocumentExtractor` → text | same fenced block for extracted text |
| **Video** | mp4, mov, webm with a codec the managed browser plays (H.264, VP8, VP9, AV1) | decoded once at upload; the overview sheet is kept beside the blob and read back at prompt time | overview sheet of 12 labeled frames as an image, plus a fence with duration, size, frame times, host-data path, and a `view_video` hint ([Visual surface](visual-surface.md#video-intake)) |
| **Reject** | binaries, archives-as-archives, executables, unknown, over-cap | none | structured `unsupported_attachment` / `attachment_too_large` |

Document intake extracts native text only. There is no OCR engine: a PDF or PPTX without extractable text is reported as scanned (`attachment_scanned_no_text`), and the extractor does not rasterize pages or embedded office media.

## Allowlist (content-sniff)

Routing trusts detected content, not the filename extension. The membership tables are host policy in [`promptattach/format/tables.go`](../lycaon/internal/promptattach/format/tables.go) and are not reproduced here.

| Class | Admission rule | Table |
|-------|----------------|-------|
| Image | Sniffed raster MIME only, the same allowlist image ingest uses. `image/svg+xml` and HTML/XML markup are never admitted as images, so active markup cannot reach the Visual plane | `rasterMIME` |
| Text-family | A sniffed text-family MIME, or bytes that sniff ambiguous (`text/plain` / `application/octet-stream`) when the filename hints text. Either way the complete stored body must decode as UTF-8 without NULs | `textFamilyMIME`, `textFamilyExt`, `textFamilyBasename` |
| Document | `application/pdf`, the OOXML package types, the OpenDocument package types, and RTF, claimed as documents before any unwrapper is offered the zip | document handler registry |
| Video | An ISO media `ftyp` box whose brand is not a still-image or audio brand (MP4, QuickTime), a QuickTime file opening on a top-level atom, or an EBML header naming WebM. Whether the codec plays is decided by decoding it at upload | `videoMIME`, `nonVideoBrands` |
| Reject | Everything else: bare `application/zip` that is no recognized OOXML/ODF package after container sniff, multi-member archives, executables, disk images, unknown binaries | — |

**Two filename hints, two shapes.** A suffix hint (`.go`, `.md`, `.yaml`, `.tf`, …) is a key in `textFamilyExt`. A file whose whole name carries the meaning (`Dockerfile`, `Makefile`) matches `textFamilyBasename` on the lowercased basename. Both apply only when the sniff was ambiguous; neither overrides a decided MIME. `.svg` is a text-family suffix only: an SVG admitted this way is UTF-8 source in a fence, never a Visual-plane image.

Within the admitted text family the suffix refines the media type (`textFamilyExtMIME`), and the structured subset (`structuredQueryMIME`, `format.IsStructuredQueryMIME`) earns the `jq` hint line rather than the byte-paging one.

A binary renamed `.txt` that fails UTF-8 or contains NULs is rejected (`unsupported_attachment`), not fenced.

## Caps and accounting

Defaults live in `prompt-budgets.yaml` under `prompt_attachments`, loaded once at boot by `promptattach.LoadCaps` into an immutable `Caps` value. There are no mutable cap globals.

**Three byte planes, three Go types** ([`bytebound`](../lycaon/internal/bytebound)). Consuming a bound on the wrong plane fails to compile. The `composer` group protects model context and therefore carries `bytebound.Prompt` values.

| Group | Protects | Catalog keys |
|-------|----------|--------------|
| `transport` | request handling: bytes accepted off the wire | `max_upload_bytes`, `max_prompt_request_bytes`, `max_image_bytes` |
| `materialization` | the project host data dir: bytes permitted on disk | `max_body_bytes`, `max_turn_bytes`, `max_expansion_ratio`, `max_unwrap_depth` |
| `composer` | inline user prose before it becomes a blob | `auto_attach_paste_bytes`, `max_inline_text_bytes` |
| `prompt` | the context window: bytes that reach the model | `max_body_preview_bytes`, `max_large_text_preview_bytes`, `max_turn_preview_bytes` |
| `counts` | per-turn item counts | `max_attachments`, `max_references`, `max_images` |
| `document` | the killable parser helper | `max_body_bytes`, `max_extracted_bytes`, `max_worker_memory_bytes`, `max_pages`, `max_slides`, `max_parse_seconds` |
| `video` | the in-memory decoder | `max_body_bytes` |

`document.max_body_bytes` and `video.max_body_bytes` are tighter materialization bounds: the video decoder holds the whole file while it draws frames, and every shipped document parser runs in a killable helper process under `max_parse_seconds` and `max_worker_memory_bytes`. Package expansion is checked before that process starts, and `max_extracted_bytes` bounds its returned text. A parser timeout or memory breach kills the worker rather than leaving work or temporary files in the API process.

`Caps.Validate()` asserts at boot what the catalog cannot say:

| Relation | Why |
|----------|-----|
| `prompt.max_body_preview_bytes ≤ materialization.max_body_bytes` | A preview larger than what may land would advertise a path to bytes the host refused to keep |
| `transport.max_upload_bytes ≤ materialization.max_body_bytes` | Accepting more than may be stored means reading bytes only to discard them |
| `materialization.max_body_bytes ≤ materialization.max_turn_bytes` | One attachment must fit in a turn |
| `transport.max_image_bytes ≤ transport.max_upload_bytes` | Rasters arrive through the same door |
| `document.max_body_bytes ≤ materialization.max_body_bytes` | The in-memory bound cannot exceed the on-disk one |
| `video.max_body_bytes ≤ materialization.max_body_bytes` | The same, for the video decoder |
| `document.max_body_bytes ≤ document.max_worker_memory_bytes` | The helper process must hold the body it was handed |
| `document.max_extracted_bytes ≤ document.max_worker_memory_bytes` | The returned text must fit the same memory bound as its input |
| `prompt.max_large_text_preview_bytes ≤ composer.auto_attach_paste_bytes ≤ composer.max_inline_text_bytes` | Large-paste conversion always reduces the first-turn body, and the hard inline ceiling is never below the conversion point |
| `prompt.max_large_text_preview_bytes ≤ prompt.max_body_preview_bytes ≤ prompt.max_turn_preview_bytes` | Each specialized preview fits the per-body ceiling, which fits the turn budget |

| Rule | Detail |
|------|--------|
| Two count planes | `counts.max_attachments` bounds blob-backed attachments; `counts.max_references` is counted separately because a reference fences one hint line and carries no payload. Images are additionally bounded by `counts.max_images`, applied to what detection found, not what the client labelled; a video's overview sheet counts as one image |
| Prompt body limit is its own value | `max_prompt_request_bytes` bounds the prompt envelope independently because it carries prose and blob IDs, not payloads |
| Preview accounting is aggregate | One request-scoped budget is shared by attachment payloads and search-hit snippets; adding blob IDs cannot multiply context admission |
| Expansion is guarded while decoding | The ratio is checked against what the compressed source has yielded so far, so a bomb fails mid-stream rather than after the host has written it. A floor lets small inputs expand freely |

**Storage lifecycle.** The blob ID is `sha256(content_digest + sanitized_filename)`, so identical bytes under different names have different IDs and re-staging the same bytes under the same name resolves to the same ID. Uploaded blobs begin staged and become collectable after 24 hours (`StagedTTL`) unless a prompt or transcript-message retention claim protects them. Admission refreshes one transient guard per blob, commits the durable database claim, then releases the guard; the user-message transaction transfers the claim to the transcript, and terminal failures release unclaimed bodies. Collection checks the indexed claim before deleting bytes and metadata; a guard left by an interrupted admission ages out. Upload and storage-status activity advance bounded database and directory batches, so there are no startup scans or periodic cleanup jobs. Per-upload and per-turn limits bound admission work, but there is no fixed project-history byte cap: admitted attachments stay available until their retaining history or project is deleted. Image blobs remain staged because the admitted turn retains the resulting visual artifact instead.

**Token accounting:** fence text charges through the ordinary prompt token estimate. Attached images charge `compaction.PerceiveImageTokenEstimate` on the Visual plane.

## Prompt framing

Text-family files and document extracts share one user-turn representation, a labeled Markdown fenced block. Both intake paths emit identical structure:

````markdown
```attachment filename="{escaped_name}" mime="{mime}" truncated="{true|false}" bytes="{n}" lines="{n}" path="{rel}"
{body}
```
````

| Field | Rule |
|-------|------|
| Fence language tag | Literal `attachment` (not the file language) |
| `filename` | Display name from the client; `\`, `"`, CR/LF escaped (`\n`) so attributes stay one line |
| `mime` | Sniffed (normalized) MIME string |
| `truncated` | `true` iff the truncation marker line is present |
| `bytes` / `lines` | Size of the full decoded body, before truncation. A trailing newline terminates the last line rather than starting an empty one. A truncated text-family preview omits `lines`, because the full line count was not read. Payload fences only (text-family, document extract, search-hit snippet); coordinate hint fences omit both |
| `path` | Host-data-relative path `prompt-attachments/{blob_id}/{basename}` to the full body, on every payload fence, even when the body fits inline. Never an absolute `~/.config/paintedwolf` path |
| `page="3"` / `slide="2"` / `sheet="4"` | Document extracts only: extracted unit count |
| Body | Raw UTF-8 text. If the body contains a triple-backtick fence, the host wraps with a longer fence (four or more backticks) |

**Truncation marker**, inserted as the final line of the fenced body when a preview ceiling clips content; `{kept}` / `{total}` are decimal byte counts of the UTF-8 body:

```text
[attachment truncated: kept {kept} of {total} bytes]
```

**Hint line**, one line after the closing fence (never inside the body, which would corrupt verbatim content and put host text inside the untrusted region). The tool named derives from the sniffed MIME:

| Body | Line |
|------|------|
| JSON / YAML / TOML (`format.IsStructuredQueryMIME`) | `[full attachment content: jq(path="{rel}") to query, or read(path="{rel}") with offset/limit]` |
| Everything else | `[full attachment content: read(path="{rel}") with offset/limit, or grep(path="{rel}") to search]` |

Structured bodies point at `jq` because byte-offset paging cannot answer a question about a JSON document, and a truncated prefix of one does not parse. `jq` is on every shipped tool profile, so the hint is always actionable.

Native `read`/`grep`/`jq` remap the allowlisted host-data prefixes (`tool-output/`, `promote-spills/`, `prompt-attachments/`) through `projectpaths.resolveHostDataRead`. Absolute leftovers under the host data dir resolve only when the relative part is still one of those prefixes; writes into that tree stay host-only.

The block is untrusted user data, at the same altitude as typed chat and hostile repo text: no host execution of HTML/SVG inside the preview or the prompt.

**Subject binding.** When a turn carries attachment or reference fences, the host inserts a host-authority content part (source `attachment_subject_binding`) between the user's prose and the fences, naming those materials as the default subject of the request. Attachment bodies remain data; only user-authority prose says what to do with them. Den shows user prose and attachment chips; the host line is model-facing.

## Worker assignment

`task()` workers do not inherit the parent user message. The host forwards every parent-session payload attachment and path-file/path-folder reference as metadata only (filename, mime, bytes, path, and any line range) on the worker assignment inject (`promptattach.ForwardedAttachment`). The worker pages the body with `jq` / `read` / `grep` at the stamped path. Host-data paths are not repo-relative focus paths. Visual-plane images stay on the coordinator; they have no agent-readable path. This channel is host-supplied; the coordinator cannot drop it.

## Reject contract

Every failure is visible. A scanned document with no extractable text raises a notice rather than arriving empty; a non-vision model with images raises the non-vision notice. Composer UX may preflight, but server ingest is the gate. Branch on structured `Code` per [Agent contract](agent-contract.md); reject prose is not the contract.

| Code | When |
|------|------|
| `unsupported_attachment` | Sniffed type outside the allowlist; active/unsafe image markup; failed UTF-8 text probe when claiming text-family; document handler cannot open without secrets; any video when no managed browser is available; unknown or missing artifact or search-hit reference |
| `attachment_undecodable` | An admitted video whose codec the managed browser cannot decode; the message names the codecs that play |
| `attachment_too_large` | Per-file / per-turn / count / page / slide / expansion / parse-time caps exceeded |
| `attachment_not_found` | Uploaded blob ID does not resolve in the project's attachment store |
| `attachment_unavailable` | The project attachment store is not configured |
| `reference_out_of_jail` | Path/folder reference does not resolve under an attached project root for the session |
| `prompt_text_too_large` | Inline prompt `text` over `composer.max_inline_text_bytes` |

Attachment and reference rejects return `400`, except `attachment_too_large` and `prompt_text_too_large`, which return `413`; an unavailable store returns `503`. The composer chip shows a human-readable message mapped from `Code`. No user-chosen attachment is dropped silently.

## Ingest boundary

Two rules keep intake from becoming a way into the machine ([Security](security.md)):

| Rule | Requirement |
|------|-------------|
| **Bytes-only ingest** | Client-uploaded bytes only, the same path as image ingest. No URL fetch; no server `open(path)` / `file://` from attachment metadata. Attach is neither a filesystem reader nor an SSRF trampoline |
| **Bounded parse, no parser egress** | Every document handler enforces page/slide count, expansion ratio, wall-clock, and memory bounds, and must not resolve external entities or open network connections from document content. Over-cap, bomb, or malformed input is a typed reject, never a hang or an OOM |

This is intake hygiene for a single-user desktop sidecar. It does not try to stop someone attaching a secret they could equally paste into the composer.

## Attachment references

`attachments[]` carries blob IDs for every uploaded body, rasters included; there is no separate `images[]`. A sibling `references[]` of `PromptReferencePart` carries jailed coordinates only, never bytes and never a host-opened absolute path.

`PromptReferencePart` is a strict discriminated union: a `oneOf` over four variants discriminated by `kind`, each with `additionalProperties: false`. A field belonging to another variant is a rejection, not a field the host ignores (`TestPromptReferencePartRejectsCrossVariantFields`). Go constructs it through the `NewPromptReference*` helpers, so the discriminator is never set by hand.

| `kind` | Required | Also allowed | Host emits |
|--------|----------|--------------|------------|
| `path-file` | `project_id`, `path` | `root_id`, `start_line`, `end_line` | `[User attached file: {rel}]` read-hint fence; `{rel}:{start}` or `{rel}:{start}-{end}` when a 1-based inclusive selection range is attached. The agent reads in place |
| `path-folder` | `project_id`, `path` | `root_id` | `[User attached folder: {rel}]` walk-hint fence; folder contents are never enumerate-uploaded |
| `artifact` | `project_id`, `artifact_id` | — | Re-links the existing `VisualArtifact` onto the turn; no re-upload or re-raster |
| `search-hit` | `project_id`, `source_ref` | `session_id`, `hit_kind` | The snippet the evidence index already stores, capped by `prompt.max_body_preview_bytes`; never client-supplied prose as authority |

Path kinds use a project-relative `path` under an attached root; `root_id` names the root. Every path/folder reference resolves under an attached root before fencing (`IngestReferences`); unresolved is `reference_out_of_jail`, never a silent drop.

**Image rule:** an image is always an uploaded body or an `artifact` re-link, never a path reference.

**Typed stamps, not hint prefixes.** Each payload part stamps its restageable `blob_id`. Each retrieval part is stamped with `MessageContentPart.reference_kind` (`path_file` | `path_folder` | `search_hit`), plus `start_line` / `end_line` for a ranged path-file and `hit_kind` / `source_ref` / `source_session_id` for a search hit. `source` stays the bare project-relative path; the fence, not `source`, carries the ranged display form. Clients branch on the stamp, never on the hint prefixes.

**Den:** one composer sink (`addToChat` / shared pending-attachment store); whole-chat-area drop with a drag-active overlay; **Add to chat** on openable surfaces (shared path menu, search detail, file viewer, transcript/gallery artifacts, folder rows). Desktop drag keeps native paths (`dragDropEnabled`). A file under an attached root stays a jailed reference (except rasters, which follow image ingest); a regular file outside every root streams through the same bytes-only upload route and becomes an immutable project-host-data snapshot whose source path never enters the sidecar, transcript, prompt, or attachment metadata. Folders outside the project are rejected: selecting a folder never implies recursive import or grants a live external path.

**Transcript display:** Den projects `Message.content_parts` into composer-style chips beside the user's prose. Flattened `Message.content` keeps fences for the model and search; the bubble does not scrape that string. Edit restore uses user-instruction prose plus `restored_content_parts` / `restored_artifact_ids`; payloads reuse `blob_id` and search hits reuse `source_session_id`, so restaging preserves the original coordinates without dumping fences into the draft.

**Provider image blocks:** vision-capable models receive native image blocks for user `ArtifactID`s. Non-vision models receive a notice and no image block.
