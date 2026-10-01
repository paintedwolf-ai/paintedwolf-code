# Name selection
normalize(name) in library.py strips surrounding whitespace and casefolds the result. unique(values) in distinct.py removes duplicates while keeping first-occurrence order. select(names) in consumer.py composes both functions. Empty inputs are supported.

Worker assignments: Name normalization writes library.py. Stable deduplication writes distinct.py. Consumer selection writes consumer.py and starts from the integrated library changes. Each assignment is scoped to its named file.
Keep the supplied tests and this README unchanged. Use Python standard library only. Run the supplied checks with `python3 -B -m unittest discover`.
