import Darwin
import Foundation
import Virtualization

final class GuestLifetime: NSObject, VZVirtualMachineDelegate {
    private let assets: BootAssets
    private let vm: VZVirtualMachine
    private var connection: VZVirtioSocketConnection?
    private var ending = false
    private let deadline = DispatchTime.now() + .seconds(90)

    init(config: VZVirtualMachineConfiguration, assets: BootAssets) {
        self.assets = assets
        vm = VZVirtualMachine(configuration: config)
        super.init()
        vm.delegate = self
    }

    func start() {
        DispatchQueue.global().async {
            let failed = !waitForPeerClose(3)
            DispatchQueue.main.async { self.finish(failed ? "Private service lost its parent channel" : nil) }
        }
        DispatchQueue.main.asyncAfter(deadline: deadline) {
            if self.connection == nil { self.finish("Private service did not become ready") }
        }
        vm.start { result in
            switch result {
            case .success: self.connect()
            case .failure(let error): self.finish("Private service could not boot: \(error.localizedDescription)")
            }
        }
    }

    private func connect() {
        guard !ending, let device = vm.socketDevices.first as? VZVirtioSocketDevice else { return }
        device.connect(toPort: 2663) { result in
            guard !self.ending else { return }
            switch result {
            case .failure:
                DispatchQueue.main.asyncAfter(deadline: .now() + .milliseconds(100)) { self.connect() }
            case .success(let connection):
                self.connection = connection
                let guest = connection.fileDescriptor
                for (source, destination) in [(Int32(3), guest), (guest, Int32(3))] {
                    DispatchQueue.global().async {
                        let error = relay(from: source, to: destination)
                        DispatchQueue.main.async { self.finish(error) }
                    }
                }
            }
        }
    }

    private func finish(_ error: String?) {
        guard !ending else { return }
        ending = true
        if let error { diagnostic(error) }
        let status = error == nil ? EXIT_SUCCESS : EXIT_FAILURE
        shutdown(3, SHUT_RDWR)
        guard vm.canStop else { exit(status) }
        vm.stop { _ in exit(status) }
        DispatchQueue.main.asyncAfter(deadline: .now() + .seconds(3)) { exit(status) }
    }

    func guestDidStop(_ virtualMachine: VZVirtualMachine) {
        finish(connection == nil ? "Private service stopped during startup" : nil)
    }

    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        finish("Private service stopped: \(error.localizedDescription)")
    }
}

private func ready(_ fd: Int32, _ events: Int16) -> Bool {
    var descriptor = pollfd(fd: fd, events: events, revents: 0)
    while true {
        let n = poll(&descriptor, 1, -1)
        if n >= 0 { return descriptor.revents & events != 0 }
        if errno != EINTR { return false }
    }
}

private func relay(from source: Int32, to destination: Int32) -> String? {
    var buffer = [UInt8](repeating: 0, count: 32 * 1024)
    return buffer.withUnsafeMutableBytes { bytes in
        guard let base = bytes.baseAddress else { return "Private service buffer unavailable" }
        while true {
            let n = read(source, base, bytes.count)
            if n == 0 { return nil }
            if n < 0 {
                if errno == EINTR { continue }
                if errno == EAGAIN && ready(source, Int16(POLLIN)) { continue }
                return "Private service channel closed during read"
            }
            var consumed = 0
            while consumed < n {
                let sent = write(destination, base.advanced(by: consumed), n - consumed)
                if sent > 0 { consumed += sent; continue }
                if sent < 0 && errno == EINTR { continue }
                if sent < 0 && errno == EAGAIN && ready(destination, Int16(POLLOUT)) { continue }
                return "Private service channel closed during write"
            }
        }
    }
}

private func waitForPeerClose(_ fd: Int32) -> Bool {
    let queue = kqueue()
    guard queue >= 0 else { return false }
    defer { close(queue) }
    var watch = kevent(ident: UInt(fd), filter: Int16(EVFILT_READ), flags: UInt16(EV_ADD | EV_CLEAR),
                       fflags: 0, data: 0, udata: nil)
    if kevent(queue, &watch, 1, nil, 0, nil) < 0 { return false }
    while true {
        var event = kevent()
        if kevent(queue, nil, 0, &event, 1, nil) < 0 {
            if errno == EINTR { continue }
            return false
        }
        if event.flags & UInt16(EV_ERROR) != 0 { return false }
        if event.flags & UInt16(EV_EOF) != 0 { return true }
    }
}
