[host:coordinator-presentation-repair]

The reply embeds images the host cannot show. Send the whole reply again, body and trailing fence, without the image syntax; it replaces the pinned body. Attempt {{ attempt }}/{{ max_attempts }}.

Rejected: {{ offenders_sample }}{% if offenders_omitted %} +{{ offenders_omitted }} more{% endif %}

Captures are presented through `artifact_ids` in the trailing fence: list the artifact ids the capture tools returned and describe in prose what each shows. A bare artifact id in image syntax and an image URL this chat never fetched both render nothing, so neither stands in for a capture.
{% if resume_work_allowed %}If the work is not finished, use an offered tool instead, then write a fresh report reflecting its results.{% endif %}
