# Contributing to Painted Wolf Code

Thanks for contributing. A few things to know before you start.

## Pull requests are gated

Right now, **only collaborators can open pull requests**, and code changes need an issue labelled `accepting-work` first. This is temporary while the first release stabilises.

## Other ways to help

- File precise bug reports and reproduce others’
- Link duplicate issues
- Answer questions in Discussions
- Test prereleases
- Write an extension pack

## Reporting bugs

Use the issue forms. Route questions and feature requests to Discussions; security issues to the private advisory flow in `SECURITY.md`. In-bounds agent reports use *The agent did something wrong* and are not automatically defects — see `REPORTING.md`. There is no support contract. Do not upload diagnostics bundles publicly — share filenames only.

## Developer workflow

Run everything through the repo-local runner:

```bash
./task setup-dev    # one-time setup
./task check-fast   # the local handoff gate
./task check        # the full gate
```

Pull requests must reference an `accepting-work` issue. CI runs a fast tier of
`check-fast`'s quicker stages on every ready pull request. A maintainer merges
through the merge queue, which runs the integration gate, `check` scoped to the
change, on the exact commit that lands. You do not need to run either gate
yourself before pushing.

## Developer Certificate of Origin

Contributions are accepted under the [Developer Certificate of Origin](https://developercertificate.org/) (DCO). The DCO is a statement you make about the provenance of what you submit — that you wrote it, or that you have the right to submit it under the project's license. It is not a copyright assignment: you keep copyright in your contribution.

Sign off every commit:

```bash
git commit -s
```

That adds the trailer the DCO requires, using your `user.name` and `user.email`:

```
Signed-off-by: Jane Developer <jane@example.com>
```

Use a real name and a reachable address. To sign off a branch you have already written, `git rebase --signoff <base>`.

Contributions are licensed under Apache-2.0 for application code and CC BY 4.0 for first-party rule YAML, matching the surface you are editing — see [Licensing](docs/licensing.md). Published copyright lines read `Copyright Painted Wolf LLC and contributors`; notable authors are listed in `AUTHORS`.

The required `DCO-owned` check uses an immutable version of the public [Painted Wolf DCO checker](https://github.com/paintedwolf-ai/dco-checker) to certify every original pull request commit, including all pages of large stacks. A `Signed-off-by` trailer must appear in the final trailer block and match the author or committer name/email pair, ignoring case. A line quoted in the message body is not a sign-off. Merge commits and GitHub-identified bot authors are exempt; the check records those exemptions. There are no owner or organization-member exemptions or remediation commits. Merge groups certify every member pull request rather than GitHub's synthetic commits.

The [certification workflow](.github/workflows/dco.yml) executes only the pinned action. It obtains commit evidence through GitHub's API and publishes a check on the captured pull request head or merge-group SHA. Drafts receive no certification. A trusted follow-up after PR CI handles Dependabot runs whose initial token cannot write checks; it consumes no artifacts or PR code. All certification runs share one non-cancelling concurrency group with FIFO pending events so older CI completions cannot interrupt or displace newer certifications. The checker rejects obsolete events and changed or incomplete evidence.

A failure identifies the affected commits and the policy or API error. Correct sign-offs on your own branch with `git rebase --signoff <base>` and push the corrected history, coordinating first when others share the branch. Appending a separate remediation commit does not certify earlier unsigned commits. For an API outage or interrupted run, maintainers can dispatch **Original commit certification** from the default branch with the ready pull request's number. Check that the completed result names the current base/head and checker revision. Checker updates, protection changes, and rollback follow the [DCO operations procedure](docs/operations/release.md#dco-certification).

See `REPORTING.md`, `AGENTS.md`, and `docs/dev-tasks.md` for more.

## Issue automation exemptions

Issues authored by `chris-beckman` are exempt from automated intake labels and
replies, lifecycle reminders, stale marking, closing, and locking. To exempt
additional authors, set the repository Actions variable
`ISSUE_AUTOMATION_EXEMPT_USERS` to comma- or whitespace-separated GitHub usernames.
Matching ignores case and accepts an optional `@` prefix. The issue author is
checked, so a maintainer editing or labeling someone else's issue does not exempt it.

The policy applies to existing issues on subsequent runs. It does not remove old
comments or labels, reopen closed issues, or unlock issues. Manual triage remains
available. The shared policy lives in
[`.github/scripts/issue-policy.cjs`](.github/scripts/issue-policy.cjs).
