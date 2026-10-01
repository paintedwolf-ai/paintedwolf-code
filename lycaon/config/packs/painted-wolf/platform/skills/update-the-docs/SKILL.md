---
name: update-the-docs
description: Update documentation when behavior, contracts, defaults, or setup changes, or docs disagree with code.
---

# Update the docs

**Entry check:** something landed that a reader of the documentation would now be misled by. Thin documentation is not this skill's problem — only text that is now wrong, or an audience left without something the change requires of them.

## Workflow

1. **Find the documentation sets and who each is for.** A project usually has more than one: in-tree reference for people changing the code, user- or operator-facing pages, API and SDK reference, and often a separate site or repository. Each has its own audience and altitude. Internal detail leaking onto a user page is a defect, not extra value.
2. **Derive the edit list from the change, not from a reading tour.** For each behavior, flag, endpoint, schema, default, or command you altered, find the pages that state the old fact. Search for the old name and the old value — a stale sentence never contains your new spelling. Open every page the search named in one response, and make the edits to distinct pages in one response as well; a page per turn is the slow way to the same list.
3. **Prefer editing an existing page over adding one.** A new top-level document splits the answer across two places and is the most expensive edit to undo. Extend the page that already covers the topic; add a page only when none does.
4. **Write in the present tense.** State what the software does now. No "previously", no "as of this change", no migration note unless the project's own compatibility policy calls for one and real users must act.
5. **Cut while you are here.** If the change removed a capability, remove its documentation in the same edit — a paragraph about a deleted flag is worse than no paragraph. When unsure whether an addition earns its place, leave it out; the reader pays per sentence.
6. **Check what the prose points at.** Links, anchors, file paths, command names, and code samples rot silently and no test catches them. Follow the ones you touched and the ones on the pages you edited.
7. **Run any claim that is runnable.** Setup steps, commands, and examples are testable text. One that has not been executed since it was written is a lead, not a fact.

## Stopping rule

Stop when every page that stated an old fact states the new one. Restructuring a documentation tree, rewriting voice, or filling long-standing gaps is separate work — name it and let the user choose it.

## Boundaries

- Do not carry developer-level internals onto user-facing or marketing pages.
- Do not describe planned or intended behavior as though it ships.
- Do not add a changelog entry, version note, or deprecation banner to a project that keeps none.
- Generated documentation is edited at its source; check a file's header before typing into it.

## Report

The documentation sets you touched and their audiences, the pages changed and what each now states, anything deleted, which claims you verified by running them, and any set you could not reach — a separate repository or site you lack access to is named, never assumed handled.
