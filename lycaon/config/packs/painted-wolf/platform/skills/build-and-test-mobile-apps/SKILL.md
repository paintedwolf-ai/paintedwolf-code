---
name: build-and-test-mobile-apps
description: Build and test Android, Flutter, and Apple-platform apps with the project toolchain and device or simulator.
metadata:
  paintedwolf.template_resources: references/build_and_test_android.md|references/build_and_test_flutter.md|references/build_and_test_ios.md
  host_resources: android-sdk|flutter|xcode
---

# Build and test mobile apps

Use the request and observed project/environment to choose the matching procedure below. Read that procedure before acting; load only the variants needed for this task. Resource paths are relative to this skill directory.

- [Build and test android](references/build_and_test_android.md) — Build, install, and test Android apps with the gradle wrapper, adb, and emulators when an Android build must run, device tests must pass, or a build failure needs diagnosis.
- [Build and test flutter](references/build_and_test_flutter.md) — Build and test Flutter apps using the tool's machine-readable output when a Flutter build must run, tests must pass, or doctor reports problems.
- [Build and test ios](references/build_and_test_ios.md) — Build and test Apple-platform projects with xcodebuild and simulators when an iOS or macOS scheme must build, tests must run, or a build failure needs diagnosis.

## Boundaries

Follow project policy and the user’s requested scope. A procedure does not grant permission or imply that every listed toolchain is installed. Use only available host resources and tools; delegate or report a missing capability. Preserve original evidence, identify its source, and report verification limits.
