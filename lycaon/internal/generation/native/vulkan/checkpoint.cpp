#include "checkpoint.h"
#include "llama-memory.h"
#include "llama-kv-cache-iswa.h"
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

llama_kv_cache::slot_info_vec_t layout(llama_kv_cache * cache) {
    if (cache->get_n_stream() != 1) throw std::runtime_error("checkpoint requires unified KV");
    llama_kv_cache::slot_info_vec_t result(1);
    auto & slots = result[0];
    slots.s0 = 0;
    slots.s1 = 1;
    slots.resize(1);
    slots.strm[0] = 0;
    const auto & cells = cache->get_cells(1);
    for (uint32_t index = 0; index < cells.size(); ++index) {
        if (cells.seq_has(index, 1)) slots.idxs[0].push_back(index);
    }
    return result;
}

llama_kv_cache_iswa * e4b_cache(llama_memory_i * memory) {
    auto * cache = dynamic_cast<llama_kv_cache_iswa *>(memory);
    if (!cache) throw std::runtime_error("checkpoint requires E4B caches");
    return cache;
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
    auto * cache = e4b_cache(memory);
    file->global_layout = layout(cache->get_base());
    file->sliding_layout = layout(cache->get_swa());
    file->global_head = cache->get_base()->get_allocation_head(0);
    file->sliding_head = cache->get_swa()->get_allocation_head(0);
    stream output(file->fd);
    cache->get_base()->state_write(output, 1, 0);
    cache->get_swa()->state_write(output, 1, 0);
    if (fdatasync(file->fd) != 0 || posix_fadvise(file->fd, 0, 0, POSIX_FADV_DONTNEED) != 0)
        throw std::runtime_error("checkpoint completion failed");
    return file;
}

void pw_checkpoint::restore(llama_memory_i * memory) {
    if (lseek(fd, 0, SEEK_SET) < 0) throw std::runtime_error("checkpoint seek failed");
    stream input(fd);
    auto * cache = e4b_cache(memory);
    // Physical row placement preserves the attention reduction order.
    cache->get_base()->state_read_sinfo(input, 1, 0, nullptr, &global_layout);
    cache->get_swa()->state_read_sinfo(input, 1, 0, nullptr, &sliding_layout);
    // Continue allocating rows from the checkpoint's original cursor.
    cache->get_base()->set_allocation_head(0, global_head);
    cache->get_swa()->set_allocation_head(0, sliding_head);
    if (posix_fadvise(fd, 0, 0, POSIX_FADV_DONTNEED) != 0)
        throw std::runtime_error("checkpoint page eviction failed");
}
