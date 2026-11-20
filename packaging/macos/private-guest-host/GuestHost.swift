import Darwin
import Foundation
import Virtualization

enum GuestError: Error {
    case invalidChannel, unsupportedMachine, invalidAssets, invalidArguments
}

@main
struct GuestHost {
    static func main() {
        do {
            guard CommandLine.arguments.count == 1 else { throw GuestError.invalidArguments }
            try verifyChannel(3)
            guard VZVirtualMachine.isSupported else { throw GuestError.unsupportedMachine }
            signal(SIGPIPE, SIG_IGN)
            let assets = try BootAssets()
            let boot = VZLinuxBootLoader(kernelURL: assets.kernelURL)
            boot.initialRamdiskURL = assets.initramfsURL
            boot.commandLine = "rdinit=/init panic=1 quiet"
            let config = VZVirtualMachineConfiguration()
            config.bootLoader = boot
            config.cpuCount = 2
            config.memorySize = 1024 * 1024 * 1024
            config.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
            config.socketDevices = [VZVirtioSocketDeviceConfiguration()]
            try config.validate()
            let controller = GuestLifetime(config: config, assets: assets)
            withExtendedLifetime(controller) {
                controller.start()
                dispatchMain()
            }
        } catch {
            diagnostic("Private service startup failed: \(error)")
            exit(EXIT_FAILURE)
        }
    }
}

func diagnostic(_ message: String) {
    FileHandle.standardError.write(Data((message + "\n").utf8))
}

func verifyChannel(_ fd: Int32) throws {
    var address = sockaddr_un()
    var length = socklen_t(MemoryLayout.size(ofValue: address))
    let connected = withUnsafeMutablePointer(to: &address) { ptr in
        ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { getpeername(fd, $0, &length) }
    }
    var kind: Int32 = 0
    var size = socklen_t(MemoryLayout.size(ofValue: kind))
    guard connected == 0, address.sun_family == AF_UNIX,
          getsockopt(fd, SOL_SOCKET, SO_TYPE, &kind, &size) == 0, kind == SOCK_STREAM else {
        throw GuestError.invalidChannel
    }
}
