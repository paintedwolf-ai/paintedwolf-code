#include "engine.h"
#include "llama-kv-cache-iswa.h"
#include <algorithm>
#include <cstdio>
#include <cstdlib>

void pw_generation::reset_active() {
    auto memory = llama_get_memory(context.get());
    if (!prefix_position) memory->clear(true);
    else clear_sequence(0);
    position = 0;
}

void pw_generation::clear_sequence(int32_t sequence) {
    auto * cache = dynamic_cast<llama_kv_cache_iswa *>(llama_get_memory(context.get()));
    if (!cache) throw std::runtime_error("E4B requires separate global and sliding caches");
    // Attention can read freed cells; clear their tensors as well as metadata.
    cache->get_base()->state_clear(sequence);
    cache->get_swa()->state_clear(sequence);
}

void pw_generation::clear_prefix() {
    clear_sequence(1);
    checkpoint.reset();
    diagnostic_prefix.reset();
    prefix_position = 0;
}

void pw_generation::save_prefix() {
    if (!position || position > prefix_limit)
        throw std::runtime_error("prefix exceeds its allocation");
    clear_prefix();
    auto * cache = dynamic_cast<llama_kv_cache_iswa *>(llama_get_memory(context.get()));
    if (!cache) throw std::runtime_error("E4B requires separate global and sliding caches");
    cache->get_base()->seq_cp(0, 1, -1, -1);
    // Only live sliding rows may be pinned by the retained prefix.
    cache->get_swa()->seq_cp(0, 1, std::max(0, static_cast<int>(position) - 511), -1);
    prefix_position = position;
    // Cold and reused tails start from the same live window and row layout.
    clear_sequence(0);
    llama_memory_seq_cp(llama_get_memory(context.get()), 1, 0, -1, -1);
    if (const auto debug = std::getenv("LLAMA_KV_CACHE_DEBUG"); debug && std::atoi(debug) > 0)
        diagnostic_prefix = pw_checkpoint::save(llama_get_memory(context.get()));
}

void pw_generation::spill_prefix() {
    if (!prefix_position || checkpoint) return;
    auto memory = llama_get_memory(context.get());
    if (diagnostic_prefix) diagnostic_prefix->verify(memory);
    try {
        checkpoint = pw_checkpoint::save(memory);
    } catch (const std::exception & error) {
        std::fprintf(stderr, "nativegen: prefix checkpoint unavailable: %s\n", error.what());
        clear_prefix();
        return;
    }
    memory->clear(true);
}

bool pw_generation::restore_prefix() {
    reset_active();
    if (!prefix_position) return false;
    auto memory = llama_get_memory(context.get());
    if (!checkpoint && diagnostic_prefix) diagnostic_prefix->verify(memory);
    if (checkpoint) {
        try {
            checkpoint->restore(memory);
            checkpoint.reset();
        } catch (const std::exception & error) {
            std::fprintf(stderr, "nativegen: prefix restore unavailable: %s\n", error.what());
            clear_prefix();
            return false;
        }
    }
    llama_memory_seq_cp(memory, 1, 0, -1, -1);
    position = prefix_position;
    check_memory();
    return true;
}
