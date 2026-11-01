---
name: upgrade-dependencies
description: Upgrade dependencies or fix advisories using the existing package manager, resolver, lockfile, and test gate.
metadata:
  paintedwolf.template_resources: references/upgrade_dotnet_dependencies.md|references/upgrade_go_dependencies.md|references/upgrade_jvm_dependencies.md|references/upgrade_node_dependencies.md|references/upgrade_php_dependencies.md|references/upgrade_python_dependencies.md|references/upgrade_ruby_dependencies.md|references/upgrade_rust_dependencies.md
  host_resources: dotnet|go|gradle|maven|node|php|python|ruby|rust|uv
---

# Upgrade dependencies

Run the project's own package manager in `command` and declare its id in `capability_request.host_resources` on that call.

## Rules for every ecosystem

1. **Use the manager the project already uses**, identified from its lockfile or manifest. Never switch managers, add a second lockfile, or install into a system interpreter.
2. **Read what actually resolves before changing anything**: the resolved tree or the outdated and vulnerable lists, including transitive packages. The declared version is not necessarily the one you get.
3. **Upgrade one package, or one tightly coupled group, at a time** to an explicit version. A blanket update buries the change that breaks the build.
4. **Know whether you are moving the lock or the requirement.** An update inside the allowed range moves the lock; a new major changes the manifest constraint. Prefer fixing the source of a transitive version over forcing or pinning over it.
5. **Let the tool write the lockfile, then review its diff.** New transitive packages, changed registries, install hooks, and very fresh or maintainer-changed releases are supply-chain signals to report before going further.
6. **Read release notes between versions** and state breaking changes first.
7. **Verify each move with the project's build and test gate** before the next one. Red means fix or revert this move. Confirm an advisory fix with the ecosystem's audit tool when it is already installed; do not install tools to get one.
8. **Report** each package with from and to versions, the reason, breaking changes, the lockfile diff summary, gate results, and any advisory still open.

## Ecosystems

Open the one the project uses:

- [.NET (NuGet)](references/upgrade_dotnet_dependencies.md): ranges resolve to the lowest version; command forms differ by SDK major.
- [Go modules](references/upgrade_go_dependencies.md): `go get mod@version`, major-version import paths, `go mod tidy`, `govulncheck`.
- [JVM (Maven, Gradle)](references/upgrade_jvm_dependencies.md): resolved trees, BOMs, constraints, and `dependencyInsight`.
- [Node (npm, pnpm, yarn, bun)](references/upgrade_node_dependencies.md): lockfile-driven installs and `--ignore-scripts`.
- [PHP (Composer)](references/upgrade_php_dependencies.md): `install` versus `update`, and `-w`/`-W` widening.
- [Python (uv, pip)](references/upgrade_python_dependencies.md): project virtual environments and tool-driven pins.
- [Ruby (Bundler)](references/upgrade_ruby_dependencies.md): `--conservative` and lockfile platforms.
- [Rust (Cargo)](references/upgrade_rust_dependencies.md): requirement versus lock and expected duplicate majors.
