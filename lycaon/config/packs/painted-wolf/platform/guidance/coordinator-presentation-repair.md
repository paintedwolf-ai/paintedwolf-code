[host:coordinator-presentation-repair]

The reply embeds images the host cannot show. Send the whole answer again without the image syntax; it replaces the retained answer. Attempt {{ attempt }}/{{ max_attempts }}.

Rejected: {{ offenders_sample }}{% if offenders_omitted %} +{{ offenders_omitted }} more{% endif %}

Captures are presented with `[artifact:<id>]`: use the ids the capture tools returned and describe in prose what each shows. A bare artifact id in image syntax and an image URL you did not fetch present nothing. Citations and directives go in the answer as usual.
{% if resume_work_allowed %}If the work is not finished, use an offered tool instead, then write a fresh report reflecting its results.{% endif %}
