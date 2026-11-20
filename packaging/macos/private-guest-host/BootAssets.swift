import CryptoKit
import Darwin
import Foundation

private struct Manifest: Decodable {
    let schema: Int
    let architecture: String
    let kernel_sha256: String
    let initramfs_sha256: String
}

final class BootAssets {
    private let kernel: FileHandle
    private let initramfs: FileHandle
    var kernelURL: URL { URL(fileURLWithPath: "/dev/fd/\(kernel.fileDescriptor)") }
    var initramfsURL: URL { URL(fileURLWithPath: "/dev/fd/\(initramfs.fileDescriptor)") }

    init() throws {
        guard let executable = Bundle.main.executableURL?.resolvingSymlinksInPath() else {
            throw GuestError.invalidAssets
        }
        let directory = executable.deletingLastPathComponent()
        let manifestFile = try Self.open(directory, "manifest.json", maximum: 64 * 1024)
        let data = try manifestFile.readToEnd() ?? Data()
        let manifest = try JSONDecoder().decode(Manifest.self, from: data)
        #if arch(arm64)
        let architecture = "arm64"
        #else
        let architecture = "amd64"
        #endif
        guard manifest.schema == 1, manifest.architecture == architecture else {
            throw GuestError.invalidAssets
        }
        kernel = try Self.verified(directory, "kernel", digest: manifest.kernel_sha256)
        initramfs = try Self.verified(directory, "initramfs.gz", digest: manifest.initramfs_sha256)
    }

    private static func open(_ directory: URL, _ name: String, maximum: Int64) throws -> FileHandle {
        let fd = Darwin.open(directory.appendingPathComponent(name).path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC | O_NONBLOCK)
        guard fd >= 0 else { throw GuestError.invalidAssets }
        let handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
        var info = stat()
        guard fstat(fd, &info) == 0, (info.st_mode & S_IFMT) == S_IFREG,
              info.st_size > 0, info.st_size <= maximum else { throw GuestError.invalidAssets }
        return handle
    }

    private static func verified(_ directory: URL, _ name: String, digest: String) throws -> FileHandle {
        let file = try open(directory, name, maximum: 1024 * 1024 * 1024)
        var sha = SHA256()
        while let block = try file.read(upToCount: 256 * 1024), !block.isEmpty { sha.update(data: block) }
        guard sha.finalize().map({ String(format: "%02x", $0) }).joined() == digest else {
            throw GuestError.invalidAssets
        }
        try file.seek(toOffset: 0)
        return file
    }
}
