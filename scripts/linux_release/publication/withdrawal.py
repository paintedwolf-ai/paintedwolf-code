"""Bind halt notifications to authenticated immutable signed distribution identities."""
import hashlib
from .website import verify_metadata


def digests(bad, good, manifest_sha256):
    from scripts.release_distribution import read_storage_bytes
    def selected(version, expected_updater=None):
        raw = read_storage_bytes("releases/v" + version + "/distribution.v1.json")
        signature = read_storage_bytes("releases/v" + version + "/distribution.v1.json.sig")
        updater = read_storage_bytes("updates/releases/" + version + ".json")
        if raw is None or signature is None or updater is None:
            raise ValueError("document-aware halt requires every selected immutable signed release")
        digest = hashlib.sha256(updater).hexdigest()
        if expected_updater is not None and digest != expected_updater:
            raise ValueError("halt replacement updater differs from independently selected bytes")
        verify_metadata(raw, signature.decode(), version, digest)
        return hashlib.sha256(raw).hexdigest()
    return {"distribution_sha256": selected(good, manifest_sha256) if good is not None else None,
            "bad_distribution_sha256": selected(bad)}
