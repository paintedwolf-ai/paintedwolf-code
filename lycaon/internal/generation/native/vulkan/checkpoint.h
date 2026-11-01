#pragma once
#include <memory>
struct llama_memory_i;

class pw_checkpoint {
    int fd;
    explicit pw_checkpoint(int descriptor) : fd(descriptor) {}
public:
    ~pw_checkpoint();
    pw_checkpoint(const pw_checkpoint &) = delete;
    pw_checkpoint & operator=(const pw_checkpoint &) = delete;
    static std::unique_ptr<pw_checkpoint> save(llama_memory_i * memory);
    void restore(llama_memory_i * memory);
};
