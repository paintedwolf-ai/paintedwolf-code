#!/usr/bin/env bash
# Sourceable: exports the document core built from this checkout's source for
# test runs. Its path names that source, so result reuse follows core changes.
LYCAON_DOCUMENT_CORE_BINARY="$(bash "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/build-document-core.sh")"
export LYCAON_DOCUMENT_CORE_BINARY
