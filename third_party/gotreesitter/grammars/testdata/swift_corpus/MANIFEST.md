# Swift corpus manifest

This corpus holds real-world Swift files for gotreesitter's Swift grammar
tests. Each file comes from a reputable, pinned upstream source. Every file
passes `swiftc -parse` before it enters the corpus. The validation command
and result appear per file below.

Total corpus size: 322065 bytes (about 314 KB), across 12 files.

## Sources

- **swiftlang/swift** at tag `swift-6.3-RELEASE` (commit
  `aa782beb23b8bd83bd16fca831532a05dd6cea39`). The Swift language
  implementation itself. Files come from `stdlib/public/core/`.
- **apple/swift-algorithms** at tag `1.2.1` (commit
  `87e50f483c54e6efd60e885f7f5aa946cee68023`), the latest release tag at
  fetch time. Files come from `Sources/Algorithms/` and
  `Tests/SwiftAlgorithmsTests/`. Several open gotreesitter issues (#556,
  #558, #559, #560, #561) cite files from this repository as real-world
  parse failures, so this corpus includes those exact files.

All files carry the Apache License v2.0 with Runtime Library Exception, as
stated in each file's own header. Headers are unmodified.

## Files

| File | Origin repo | Pinned ref | Upstream path | Size (bytes) | Size band |
|---|---|---|---|---|---|
| `stdlib_ASCII.swift` | swiftlang/swift | `swift-6.3-RELEASE` | `stdlib/public/core/ASCII.swift` | 3115 | small |
| `stdlib_Repeat.swift` | swiftlang/swift | `swift-6.3-RELEASE` | `stdlib/public/core/Repeat.swift` | 3812 | small |
| `swift-algorithms_AdjacentPairsTests.swift` | apple/swift-algorithms | `1.2.1` | `Tests/SwiftAlgorithmsTests/AdjacentPairsTests.swift` | 2580 | small |
| `swift-algorithms_Stride.swift` | apple/swift-algorithms | `1.2.1` | `Sources/Algorithms/Stride.swift` | 7887 | small |
| `swift-algorithms_FlattenCollection.swift` | apple/swift-algorithms | `1.2.1` | `Sources/Algorithms/FlattenCollection.swift` | 9196 | small |
| `swift-algorithms_Windows.swift` | apple/swift-algorithms | `1.2.1` | `Sources/Algorithms/Windows.swift` | 11706 | medium |
| `stdlib_CollectionAlgorithms.swift` | swiftlang/swift | `swift-6.3-RELEASE` | `stdlib/public/core/CollectionAlgorithms.swift` | 24056 | medium |
| `stdlib_Stride.swift` | swiftlang/swift | `swift-6.3-RELEASE` | `stdlib/public/core/Stride.swift` | 26410 | medium |
| `swift-algorithms_Chunked.swift` | apple/swift-algorithms | `1.2.1` | `Sources/Algorithms/Chunked.swift` | 27814 | medium |
| `stdlib_Optional.swift` | swiftlang/swift | `swift-6.3-RELEASE` | `stdlib/public/core/Optional.swift` | 33867 | medium |
| `stdlib_Collection.swift` | swiftlang/swift | `swift-6.3-RELEASE` | `stdlib/public/core/Collection.swift` | 66941 | medium-large |
| `stdlib_FloatingPointToString.swift` | swiftlang/swift | `swift-6.3-RELEASE` | `stdlib/public/core/FloatingPointToString.swift` | 104681 | large |

Note: the task brief named `stdlib/public/core/Collection.swift` as an
example "100 KB+" file. Its actual pinned size is 66941 bytes (about 65 KB),
so it lands in the medium-large band instead. `FloatingPointToString.swift`
(104681 bytes) supplies the genuine 100 KB+ sample.

Two files share a base name across the two source repositories
(`Stride.swift` from stdlib and from swift-algorithms). Each carries a
`stdlib_` or `swift-algorithms_` prefix to keep them distinct on disk.

## Fetch method

Every file was fetched with:

```
curl -fsSL https://raw.githubusercontent.com/<repo>/<tag>/<path> -o <name>
```

using the pinned tag for each repository, listed above.

## Validation

Every file was checked with:

```
swiftc -parse <file>
```

All 12 files exit 0 (no diagnostics). Run from
`grammars/testdata/swift_corpus/`:

```
$ for f in *.swift; do swiftc -parse "$f" || echo "FAIL: $f"; done
```

reports no failures.

## Why these files

- The five `swift-algorithms` files match the exact real-world failure
  sites the open issues cite:
  - `Chunked.swift` and `FlattenCollection.swift`: issue #558 (nested
    `if let` plus nested comparisons).
  - `Windows.swift` and `Stride.swift` (swift-algorithms): issue #560
    (`if`/`else` with a comparison condition and a parenthesised member
    access in the then-branch).
  - `AdjacentPairsTests.swift`: issues #559 (triple-nested generic type
    arguments) and #561 (a `for...in <range>` method followed by another
    method inside a type body).
- The seven `stdlib` files supply size variety (3 KB to 105 KB) from
  idiomatic, heavily-reviewed Swift, independent of the algorithms
  repository's known failure modes. They exercise generics, protocol
  extensions, operators, and string formatting at a scale the inline test
  snippets cannot reach.
