"""Closed public managed-secret fixture; no app password passes through this path."""
NAME = 'Installed presence fixture'
PURPOSE = 'Checks signed presence in the owned QA profile.'
VALUE = 'installed-public-presence-fixture-value-2026'
FIELDS = (
    ('Name Stripe test key', 'presence-fixture-name-v1', NAME, False),
    ('Purpose Charges the test-mode checkout Required. Every chat in this project can use this secret, '
     'so this is how a later chat tells whether it is the one it needs.',
     'presence-fixture-purpose-v1', PURPOSE, False),
    ('Value', 'presence-fixture-value-v1', VALUE, True),
)
