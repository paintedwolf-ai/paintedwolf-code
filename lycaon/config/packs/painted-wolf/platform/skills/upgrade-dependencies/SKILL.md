---
name: upgrade-dependencies
description: Upgrade dependencies or fix advisories using the existing package manager, resolver, lockfile, and test gate.
metadata:
  paintedwolf.template_resources: references/upgrade_dotnet_dependencies.md|references/upgrade_go_dependencies.md|references/upgrade_jvm_dependencies.md|references/upgrade_node_dependencies.md|references/upgrade_php_dependencies.md|references/upgrade_python_dependencies.md|references/upgrade_ruby_dependencies.md|references/upgrade_rust_dependencies.md
  host_resources: dotnet|go|gradle|maven|node|php|python|ruby|rust|uv
---

# Upgrade dependencies

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Upgrade dotnet dependencies](references/upgrade_dotnet_dependencies.md) — Upgrade NuGet packages in a .NET project deliberately, including transitive vulnerabilities when bumping a package, clearing an advisory, or resolving a version conflict.
- [Upgrade go dependencies](references/upgrade_go_dependencies.md) — Upgrade Go module dependencies with targeted go get moves when bumping a module, clearing a vulnerability, or untangling go.mod conflicts.
- [Upgrade jvm dependencies](references/upgrade_jvm_dependencies.md) — Upgrade Maven or Gradle dependencies deliberately, resolving the version the build actually picks when bumping a library, clearing a CVE, or untangling a JVM version conflict.
- [Upgrade node dependencies](references/upgrade_node_dependencies.md) — Upgrade JavaScript and TypeScript dependencies in npm, pnpm, yarn, or bun projects when bumping packages, fixing audit findings, or unsticking a lockfile.
- [Upgrade php dependencies](references/upgrade_php_dependencies.md) — Upgrade Composer dependencies deliberately, without letting an install rewrite the lockfile when bumping a package, clearing an advisory, or resolving a PHP platform conflict.
- [Upgrade python dependencies](references/upgrade_python_dependencies.md) — Upgrade Python dependencies in uv, pip, or requirements-file projects when bumping packages, resolving conflicts, or fixing vulnerable pins.
- [Upgrade ruby dependencies](references/upgrade_ruby_dependencies.md) — Upgrade Bundler gems deliberately, keeping the lockfile's platforms intact when bumping a gem, clearing an advisory, or fixing a bundle that resolves differently in CI.
- [Upgrade rust dependencies](references/upgrade_rust_dependencies.md) — Upgrade Cargo dependencies deliberately, distinguishing a lockfile move from a manifest change when bumping a crate, clearing an advisory, or resolving a version conflict.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
