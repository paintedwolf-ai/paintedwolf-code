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
./task check-fast   # local handoff gate
./task check        # ship / closeout gate
```

Pull requests must reference an `accepting-work` issue and pass `check-fast`.

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

A GitHub DCO check enforces the trailer on every pull request.

See `REPORTING.md`, `AGENTS.md`, and `docs/dev-tasks.md` for more.
