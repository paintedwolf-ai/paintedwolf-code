#pragma once
#include "checkpoint.h"
#include "llama.h"
#include "llama-ext.h"
#include "ggml-backend.h"
#include <memory>
#include <stdexcept>

struct pw_generation {
    std::unique_ptr<llama_model, decltype(&llama_model_free)> model{nullptr, llama_model_free};
    std::unique_ptr<llama_sampler, decltype(&llama_sampler_free)> sampler{nullptr, llama_sampler_free};
    bool greedy = true;
    std::unique_ptr<llama_context, decltype(&llama_free)> context{nullptr, llama_free};
    uint32_t position = 0;
    uint32_t prefix_position = 0;
    uint32_t limit = 0;
    uint32_t chunk = 0;
    uint32_t prefix_limit = 0;
    uint64_t budget = 0;
    std::unique_ptr<pw_checkpoint> checkpoint;
    std::unique_ptr<pw_checkpoint> diagnostic_prefix;
    void reset_active();
    void clear_sequence(int32_t sequence);
    void save_prefix();
    bool restore_prefix();
    void clear_prefix();
    void spill_prefix();
    void check_memory() const {
        uint64_t used = 0;
        for (const auto & [type, bytes] : llama_get_memory_breakdown(context.get())) {
            if (!ggml_backend_buft_is_host(type)) used += bytes.model + bytes.context + bytes.compute;
        }
        if (used > budget) throw std::runtime_error("execution exceeded its memory reservation");
    }
};

