#include "artifact.h"
#include "gguf.h"
#include <cstring>
#include <memory>
#include <stdexcept>
#include <utility>

pw_inventory inspect_gemma4(const char * path) {
    auto metadata = std::unique_ptr<gguf_context, decltype(&gguf_free)>(
        gguf_init_from_file(path, {true, nullptr}), gguf_free);
    if (!metadata) throw std::runtime_error("invalid GGUF artifact");
    const auto key = [&](const char * name, gguf_type type) {
        auto id = gguf_find_key(metadata.get(), name);
        if (id < 0 || gguf_get_kv_type(metadata.get(), id) != type)
            throw std::runtime_error("missing model architecture field");
        return id;
    };
    if (std::strcmp(gguf_get_val_str(metadata.get(), key("general.architecture", GGUF_TYPE_STRING)), "gemma4"))
        throw std::runtime_error("unsupported model architecture");
    const auto value = [&](const char * name) {
        return gguf_get_val_u32(metadata.get(), key(name, GGUF_TYPE_UINT32));
    };
    const auto layers = value("gemma4.block_count");
    const bool ple = layers == 42, dense = layers == 48, moe = layers == 30;
    if (!ple && !dense && !moe) throw std::runtime_error("unsupported Gemma 4 profile");
    for (const auto & field : {
        std::pair{"gemma4.embedding_length", ple ? 2560u : dense ? 3840u : 2816u},
        {"gemma4.feed_forward_length", ple ? 10240u : dense ? 15360u : 2112u},
        {"gemma4.attention.head_count", ple ? 8u : 16u},
        {"gemma4.attention.shared_kv_layers", ple ? 18u : 0u},
        {"gemma4.attention.sliding_window", ple ? 512u : 1024u},
        {"gemma4.embedding_length_per_layer_input", ple ? 256u : 0u},
        {"gemma4.attention.key_length",512u}, {"gemma4.attention.value_length",512u},
        {"gemma4.attention.key_length_swa",256u}, {"gemma4.attention.value_length_swa",256u}}) {
        if (value(field.first) != field.second) throw std::runtime_error("unsupported Gemma 4 dimensions");
    }
    if (moe && (value("gemma4.expert_count") != 128 || value("gemma4.expert_used_count") != 8 ||
                value("gemma4.expert_feed_forward_length") != 704))
        throw std::runtime_error("unsupported expert routing dimensions");
    const auto pattern = key("gemma4.attention.sliding_window_pattern", GGUF_TYPE_ARRAY);
    if (gguf_get_arr_type(metadata.get(),pattern) != GGUF_TYPE_BOOL || gguf_get_arr_n(metadata.get(),pattern) != layers)
        throw std::runtime_error("unsupported attention layout");
    const auto sliding = static_cast<const bool *>(gguf_get_arr_data(metadata.get(),pattern));
    const auto head_key = "gemma4.attention.head_count_kv";
    const uint32_t *heads = nullptr;
    if (!ple) {
        const auto id = key(head_key, GGUF_TYPE_ARRAY);
        if (gguf_get_arr_type(metadata.get(),id) != GGUF_TYPE_INT32 || gguf_get_arr_n(metadata.get(),id) != layers)
            throw std::runtime_error("invalid KV head layout");
        heads = static_cast<const uint32_t *>(gguf_get_arr_data(metadata.get(),id));
    } else if (value(head_key) != 2) throw std::runtime_error("unsupported KV head count");
    for (uint32_t i = 0; i < layers; ++i) {
        if (sliding[i] != (i % 6 != 5)) throw std::runtime_error("unsupported attention layout");
        if (heads && heads[i] != (sliding[i] ? 8u : dense ? 1u : 2u))
            throw std::runtime_error("unsupported KV head count");
    }
    const auto count = gguf_get_n_tensors(metadata.get());
    if (count < 1 || count > 2048) throw std::runtime_error("invalid tensor count");
    pw_inventory inventory{};
    inventory.layers = layers;
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
    if ((inventory.mapped != 0) != ple || inventory.mapped > (4ull << 30) || inventory.resident > ((ple ? 8ull : dense ? 10ull : 18ull) << 30))
        throw std::runtime_error("artifact exceeds model bounds");
    return inventory;
}
