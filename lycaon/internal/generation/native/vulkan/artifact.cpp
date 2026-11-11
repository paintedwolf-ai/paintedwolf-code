#include "artifact.h"
#include "gguf.h"
#include <cstring>
#include <memory>
#include <stdexcept>
#include <utility>

pw_inventory inspect_e4b(const char * path) {
    auto metadata = std::unique_ptr<gguf_context, decltype(&gguf_free)>(
        gguf_init_from_file(path, {true, nullptr}), gguf_free);
    if (!metadata) throw std::runtime_error("invalid GGUF artifact");
    const auto key = [&](const char * name, gguf_type type) {
        auto id = gguf_find_key(metadata.get(), name);
        if (id < 0 || gguf_get_kv_type(metadata.get(), id) != type)
            throw std::runtime_error("missing E4B architecture field");
        return id;
    };
    if (std::strcmp(gguf_get_val_str(metadata.get(), key("general.architecture", GGUF_TYPE_STRING)), "gemma4"))
        throw std::runtime_error("unsupported model architecture");
    for (const auto & field : {
        std::pair{"gemma4.block_count",42u}, {"gemma4.embedding_length",2560u},
        {"gemma4.feed_forward_length",10240u}, {"gemma4.attention.head_count",8u},
        {"gemma4.attention.head_count_kv",2u}, {"gemma4.attention.shared_kv_layers",18u},
        {"gemma4.attention.sliding_window",512u}, {"gemma4.embedding_length_per_layer_input",256u},
        {"gemma4.attention.key_length",512u}, {"gemma4.attention.value_length",512u},
        {"gemma4.attention.key_length_swa",256u}, {"gemma4.attention.value_length_swa",256u}}) {
        if (gguf_get_val_u32(metadata.get(), key(field.first, GGUF_TYPE_UINT32)) != field.second)
            throw std::runtime_error("unsupported E4B dimensions");
    }
    const auto pattern = key("gemma4.attention.sliding_window_pattern", GGUF_TYPE_ARRAY);
    if (gguf_get_arr_type(metadata.get(),pattern) != GGUF_TYPE_BOOL || gguf_get_arr_n(metadata.get(),pattern) != 42)
        throw std::runtime_error("unsupported attention layout");
    const auto sliding = static_cast<const bool *>(gguf_get_arr_data(metadata.get(),pattern));
    for (int i = 0; i < 42; ++i)
        if (sliding[i] != (i % 6 != 5)) throw std::runtime_error("unsupported attention layout");
    const auto count = gguf_get_n_tensors(metadata.get());
    if (count < 1 || count > 2048) throw std::runtime_error("invalid tensor count");
    pw_inventory inventory{};
    for (int64_t i = 0; i < count; ++i) {
        const auto size = gguf_get_tensor_size(metadata.get(), i);
        if (size > (8ull << 30)) throw std::runtime_error("tensor exceeds its bound");
        if (!std::strcmp(gguf_get_tensor_name(metadata.get(),i),"per_layer_token_embd.weight")) {
            const auto shape = gguf_get_tensor_ne(metadata.get(),i);
            if (shape[0] != 10752 || shape[1] != 262144 || shape[2] != 1 || shape[3] != 1)
                throw std::runtime_error("invalid PLE dimensions");
            inventory.mapped += size;
        } else inventory.resident += ((size + 65535) / 65536) * 65536;
    }
    if (!inventory.mapped || inventory.mapped > (4ull << 30) || inventory.resident > (8ull << 30))
        throw std::runtime_error("artifact exceeds E4B bounds");
    return inventory;
}
