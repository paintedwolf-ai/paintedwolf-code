import Darwin
import Foundation

func mapReadOnly(_ file: FileHandle) throws -> Data {
    var info = stat()
    guard fstat(file.fileDescriptor, &info) == 0 else {
        throw ReferenceFailure("Cannot inspect the tensor file")
    }
    guard info.st_mode & S_IFMT == S_IFREG, info.st_size > 0, info.st_size <= 64 << 30 else {
        throw ReferenceFailure("Tensor mapping requires a bounded regular file")
    }
    let size = Int(info.st_size)
    guard let address = mmap(nil, size, PROT_READ, MAP_PRIVATE, file.fileDescriptor, 0),
          address != MAP_FAILED else {
        throw ReferenceFailure("Cannot map the tensor file")
    }
    return Data(bytesNoCopy: address, count: size, deallocator: .unmap)
}
