#!/usr/bin/env bash
# @redocly/cli version from its lycaon-den devDependency pin (override with REDOCLY_VERSION=…).
REDOCLY_VERSION="${REDOCLY_VERSION:-$(sed -nE 's/^ *"@redocly\/cli": "([^"]+)".*/\1/p' "$(dirname "${BASH_SOURCE[0]}")/../lycaon-den/package.json")}"
export REDOCLY_VERSION
