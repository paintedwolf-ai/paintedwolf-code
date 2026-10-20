"""Protected public-cache activation is separate from Nix source qualification."""
from pathlib import Path

from ..release_admission import document

POLICY = Path(__file__).resolve().parents[3] / "packaging/linux/nix-cache-trust.json"


def load(path=POLICY):
    declaration,_ = document(path)
    if (not isinstance(declaration,dict) or type(declaration.get("schema_version")) is not int
            or declaration["schema_version"]!=1 or type(declaration.get("enabled")) is not bool):
        raise ValueError("protected Nix cache declaration requires schema_version1 and explicit enabled state")
    if not declaration["enabled"]:
        if set(declaration)!={"schema_version","enabled"}:
            raise ValueError("disabled public Nix cache cannot declare URLs, keys or trust")
        return None
    if set(declaration)!={"schema_version","enabled","trust_policy"}:
        raise ValueError("enabled public Nix cache requires its explicit protected trust policy")
    policy=declaration["trust_policy"]
    if (not isinstance(policy,dict) or set(policy)!={"schema_version","mode","public_keys"}
            or type(policy["schema_version"]) is not int or policy["schema_version"]!=1 or policy["mode"]!="production"
            or not isinstance(policy["public_keys"],list) or not policy["public_keys"]
            or any(not isinstance(key,str) or not key for key in policy["public_keys"])
            or len(set(policy["public_keys"]))!=len(policy["public_keys"])):
        raise ValueError("enabled public Nix cache requires nonempty protected production trust")
    return policy
