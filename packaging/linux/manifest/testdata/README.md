# Shared producer manifest corpus

The architecture manifests are the exact output of the real native packaging
producer's `generate`, `seal-tree` and `verify-tree` operations over these bytes.
`test_manifest_producer_contract_execution.py` reconstructs both staged trees and
compares every manifest byte through `./task test:runner`. Producer format,
required-role, inventory or serialization changes require explicit regeneration.
The protected launch catalog is also retained under `payload/` so native reader
fixtures materialize the exact sealed bytes. The 64-byte ELF fixtures carry only
the native ELF identification fields the readers inspect; they are never executed.

Go `pkgmanifest`, the Tauri shell's `sidecar::manifest`, the desktop-settings
observer and the Nix AppArmor generator consume the same architecture manifest.
Tests call the real admission and executable verification entry points and reject
unknown versions, missing payload seals and changed executable bytes. Physical
reader fixtures relocate only the install root and its executable path prefix;
architecture, payload paths, digests, sizes, modes and links remain unchanged.
The observer uses the existing test anchor/owner seam around real temporary files.

Installed campaign and native package-probe consumers must join this contract
when their owning changes land. This corpus does not claim installed GUI or
package qualification.
