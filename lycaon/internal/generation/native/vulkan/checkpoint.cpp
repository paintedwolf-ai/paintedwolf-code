#include "checkpoint.h"
#include "llama-memory.h"
#include "llama-io.h"
#include "ggml-backend.h"
#include <algorithm>
#include <cerrno>
#include <cstdlib>
#include <filesystem>
#include <stdexcept>
#include <string>
#include <vector>
#include <fcntl.h>
#include <sys/statfs.h>
#include <unistd.h>

namespace {
constexpr size_t block_size = 8 << 20;
constexpr size_t checkpoint_limit = 2ull << 30;

int private_file() {
    const char * root = std::getenv("XDG_CACHE_HOME");
    std::filesystem::path directory;
    if (root && *root && std::filesystem::path(root).is_absolute()) {
        directory = root;
    } else {
        root = std::getenv("HOME");
        if (!root || !*root || !std::filesystem::path(root).is_absolute())
            throw std::runtime_error("checkpoint cache directory unavailable");
        directory = std::filesystem::path(root) / ".cache";
    }
    directory /= "paintedwolf/nativegen";
    std::filesystem::create_directories(directory);
    std::string path = (directory / "checkpoint-XXXXXX").string();
    const int fd = mkostemp(path.data(), O_CLOEXEC);
    if (fd < 0) throw std::runtime_error("checkpoint creation failed");
    if (unlink(path.c_str()) != 0) {
        close(fd);
        throw std::runtime_error("checkpoint unlink failed");
    }
    struct statfs filesystem{};
    if (fstatfs(fd, &filesystem) != 0 || (filesystem.f_type == 0x01021994 || filesystem.f_type == 0x858458f6)) {
        close(fd);
        throw std::runtime_error("checkpoint requires disk-backed storage");
    }
    return fd;
}

class stream : public llama_io_write_i, public llama_io_read_i {
    int fd;
    size_t position = 0;
    std::vector<unsigned char> buffer = std::vector<unsigned char>(block_size);
    void evict(size_t start, size_t size) {
        // Keep checkpoint pages out of the model's resident working set.
        if (posix_fadvise(fd, start, size, POSIX_FADV_DONTNEED) != 0)
            throw std::runtime_error("checkpoint page eviction failed");
    }
public:
    explicit stream(int descriptor) : fd(descriptor) {}
    size_t n_bytes() override { return position; }
    void write(const void * source, size_t size) override {
        if (size > checkpoint_limit - position) throw std::runtime_error("checkpoint exceeds bound");
        auto bytes = static_cast<const unsigned char *>(source);
        const size_t start = position;
        while (size) {
            const auto count = ::write(fd, bytes, std::min(size, block_size));
            if (count < 0 && errno == EINTR) continue;
            if (count <= 0) throw std::runtime_error("checkpoint write failed");
            bytes += count;
            size -= count;
            position += count;
        }
        if (position - start >= block_size) {
            if (fdatasync(fd) != 0) throw std::runtime_error("checkpoint flush failed");
            evict(start, position - start);
        }
    }
    void read(void * destination, size_t size) override {
        if (size > checkpoint_limit - position) throw std::runtime_error("checkpoint exceeds bound");
        auto bytes = static_cast<unsigned char *>(destination);
        const size_t start = position;
        while (size) {
            const auto count = ::read(fd, bytes, std::min(size, block_size));
            if (count < 0 && errno == EINTR) continue;
            if (count <= 0) throw std::runtime_error("checkpoint read failed");
            bytes += count;
            size -= count;
            position += count;
        }
        if (position - start >= block_size) evict(start, position - start);
    }
    void write_tensor(ggml_tensor * tensor, size_t offset, size_t size) override {
        while (size) {
            const auto count = std::min(size, buffer.size());
            ggml_backend_tensor_get(tensor, buffer.data(), offset, count);
            write(buffer.data(), count);
            offset += count;
            size -= count;
        }
    }
    void read_tensor(ggml_tensor * tensor, size_t offset, size_t size) override {
        while (size) {
            const auto count = std::min(size, buffer.size());
            read(buffer.data(), count);
            ggml_backend_tensor_set(tensor, buffer.data(), offset, count);
            offset += count;
            size -= count;
        }
    }
};
}

pw_checkpoint::~pw_checkpoint() { close(fd); }

std::unique_ptr<pw_checkpoint> pw_checkpoint::save(llama_memory_i * memory) {
    auto file = std::unique_ptr<pw_checkpoint>(new pw_checkpoint(private_file()));
    stream output(file->fd);
    memory->state_write(output, 1, 0);
    if (fdatasync(file->fd) != 0 || posix_fadvise(file->fd, 0, 0, POSIX_FADV_DONTNEED) != 0)
        throw std::runtime_error("checkpoint completion failed");
    return file;
}

void pw_checkpoint::restore(llama_memory_i * memory) {
    if (lseek(fd, 0, SEEK_SET) < 0) throw std::runtime_error("checkpoint seek failed");
    stream input(fd);
    memory->state_read(input, 1, 0);
    if (posix_fadvise(fd, 0, 0, POSIX_FADV_DONTNEED) != 0)
        throw std::runtime_error("checkpoint page eviction failed");
}
