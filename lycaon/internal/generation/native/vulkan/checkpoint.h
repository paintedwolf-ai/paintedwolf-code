#pragma once
#include <memory>
#include "llama-kv-cache.h"
struct llama_memory_i;

class pw_checkpoint {
    int fd;
    llama_kv_cache::slot_info_vec_t global_layout;
    llama_kv_cache::slot_info_vec_t sliding_layout;
    uint32_t global_head = 0;
    uint32_t sliding_head = 0;
    explicit pw_checkpoint(int descriptor) : fd(descriptor) {}
public:
    ~pw_checkpoint();
    pw_checkpoint(const pw_checkpoint &) = delete;
    pw_checkpoint & operator=(const pw_checkpoint &) = delete;
    static std::unique_ptr<pw_checkpoint> save(llama_memory_i * memory);
    void restore(llama_memory_i * memory);
};
