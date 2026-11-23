import Darwin
import Foundation
import Security

func verifyLauncher() throws {
    var running: SecCode?
    var ownCode: SecStaticCode?
    var signing: CFDictionary?
    guard SecCodeCopySelf([], &running) == errSecSuccess, let running,
          SecCodeCopyStaticCode(running, [], &ownCode) == errSecSuccess, let ownCode,
          SecCodeCopySigningInformation(ownCode, SecCSFlags(rawValue: kSecCSSigningInformation), &signing) == errSecSuccess,
          let info = signing as? [String: Any] else { throw GuestError.invalidLauncher }

    guard let team = info[kSecCodeInfoTeamIdentifier as String] as? String else {
        // Local fixture builds carry ad-hoc signatures.
        guard let flags = info[kSecCodeInfoFlags as String] as? NSNumber,
              SecCodeSignatureFlags(rawValue: flags.uint32Value).contains(.adhoc) else { throw GuestError.invalidLauncher }
        return
    }
    let parent = getppid()
    let identity = "anchor apple generic and identifier \"dev.paintedwolf.code.engine\" and certificate leaf[subject.OU] = \"\(team)\""
    let attributes = [kSecGuestAttributePid as String: NSNumber(value: parent)] as CFDictionary
    var requirement: SecRequirement?
    var launcher: SecCode?
    guard parent > 1,
          SecRequirementCreateWithString(identity as CFString, [], &requirement) == errSecSuccess, let requirement,
          SecCodeCopyGuestWithAttributes(nil, attributes, [], &launcher) == errSecSuccess, let launcher,
          SecCodeCheckValidity(launcher, [], requirement) == errSecSuccess,
          getppid() == parent else { throw GuestError.invalidLauncher }
}
