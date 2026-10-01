# Report selection

select(records, limit=8) returns records by descending integer score, then ascending case-sensitive name, without changing its input. Equal keys retain input order. A nonnegative integer limit selects the first records; zero and empty input are supported. This task changes only the default limit to 3. Explicit limits continue to work. The input domain consists of JSON records with string names and integer scores; other input types are outside scope.

Run the supplied checks with python3 -B -m unittest discover.

Keep the supplied tests and README unchanged. Use regular project files and the Python standard library; no dependencies or downloads.
