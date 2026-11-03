Add a focused test for the code at {{ path }} lines {{ start_line }}–{{ end_line }}.

Use native file reads, staged redirects, and scoped patches through `command`. Place the test in the same directory as the file — the host rejects writes anywhere else. Do not launch external programs or use network tools. When the test file is written, stop.
