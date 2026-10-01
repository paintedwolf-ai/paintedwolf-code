>>> promote order
Overlay `{{ overlay_id }}` — **{{ promote_order }}**{% if promote_after %} after {% for id in promote_after %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %} promote{% endif %}{% if blocked_by %} before {% for id in blocked_by %}`{{ id }}`{% if not forloop.Last %}, {% endif %}{% endfor %} land{% endif %}{% if promote_order_note %} — {{ promote_order_note }}{% endif %}
{% if promote_order == "sequential" %}Promote one overlay at a time on shared paths; `preview_overlay` again after each landing.{% elif promote_order == "clean_if_first" %}Promote this overlay before listed siblings, or re-preview after they land.{% elif promote_order == "clean_after" %}Wait for listed siblings to promote, then `preview_overlay({{ overlay_id }})` again.{% endif %}
Ordering does not replace review: inspect delivery, current evidence and validation limits; resume unfinished work before promotion.
Code: BANNER_PROMOTE_ORDER
