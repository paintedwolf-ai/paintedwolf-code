# Desktop asset notices

## Bundled fonts

The app bundle redistributes seven variable faces under the SIL Open Font
License 1.1. Each licence ships beside the face it covers, in
`public/fonts/licenses/` — the complete table, with per-family copyright, is
generated at [`public/fonts/licenses/README.md`](public/fonts/licenses/README.md).

Families: Inter, IBM Plex Sans, Source Sans 3, DM Sans, Space Grotesk, Literata,
JetBrains Mono.

Faces and provenance come from the designkit pack
(`lycaon/internal/browser/designkit/`); `lycaon-den/fonts.yaml` selects which of
them the app carries. Neither the file list nor the attribution is maintained by
hand — run `./task codegen:den-fonts`.

## use-stick-to-bottom

Portions of `src/chat/stream/stream-scroll-spring.ts` are adapted from
[use-stick-to-bottom](https://github.com/samdenty/use-stick-to-bottom) (MIT).

```
MIT License

Copyright (c) StackBlitz

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Wrench icon

Wrench geometry adapted to the host grid from Lucide Icons:
https://github.com/lucide-icons/lucide/blob/main/icons/wrench.svg
ISC License — Copyright (c) 2026 Lucide Icons and Contributors

```
Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```
