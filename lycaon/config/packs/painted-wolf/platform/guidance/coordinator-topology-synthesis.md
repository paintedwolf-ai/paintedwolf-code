Parallel topology legs finished — synthesize the merged output for the user.

Read the scaffold topology output below (authoritative for this workflow phase). Synthesize findings in concise user-facing prose: what was surveyed, key paths/facts, gaps, and suggested next steps. Shape tone to the workflow — recon pack emphasizes repo layout and build/test paths. Do not paste raw worker logs.

{% if evidence_digest %}
{{ evidence_digest }}

{% endif %}{% if topology_output %}
{{ topology_output }}
{% endif %}

When the summary is complete, the host advances the report phase automatically — do not call the phase-advance tool from coordinator prose.
