#include "adapter.h"
#include "engine.h"
#include "artifact.h"
#include "llama-ext.h"
#include "llama-kv-cache-iswa.h"
#include "llama.h"
#include "ggml-backend.h"
#include <algorithm>
#include <cstring>
#include <memory>
#include <mutex>
#include <stdexcept>
#include <string>
#include <vector>


namespace {
template<class F> int guarded(char * error, size_t capacity, F operation) noexcept {
    try { operation(); return 0; }
    catch (const std::exception & e) {
        if (capacity) {
            const auto count = std::min(capacity - 1, std::strlen(e.what()));
            std::memcpy(error, e.what(), count);
            error[count] = 0;
        }
        return -1;
    } catch (...) {
        if (capacity) error[0] = 0;
        return -1;
    }
}

ggml_backend_dev_t device(const char * pci) {
    static std::once_flag initialized;
    std::call_once(initialized, [] { ggml_backend_load_all(); llama_backend_init(); });
    for (size_t i = 0; i < ggml_backend_dev_count(); ++i) {
        auto candidate = ggml_backend_dev_get(i);
        ggml_backend_dev_props properties{};
        ggml_backend_dev_get_props(candidate, &properties);
        const auto registry = ggml_backend_dev_backend_reg(candidate);
        if (std::strcmp(ggml_backend_reg_name(registry), "Vulkan") == 0 &&
            properties.device_id && std::strcmp(properties.device_id, pci) == 0 &&
            (properties.type == GGML_BACKEND_DEVICE_TYPE_GPU ||
             properties.type == GGML_BACKEND_DEVICE_TYPE_IGPU)) return candidate;
    }
    throw std::runtime_error("requested Vulkan PCI device is unavailable");
}

void warm(pw_generation * engine) {
    const auto vocab = llama_model_get_vocab(engine->model.get());
    const auto bos = llama_vocab_bos(vocab);
    std::vector<uint32_t> tokens(std::min(engine->chunk, engine->limit),
                                 bos == LLAMA_TOKEN_NULL ? 0 : static_cast<uint32_t>(bos));
    std::vector<float> logits(262144);
    char error[1024]{};
    uint32_t sampled;
    // Warm growing attention state before admitting requests.
    const auto boundary = std::min(engine->limit - 1, 4096u);
    while (engine->position < boundary) {
        const auto count = std::min(engine->chunk, boundary - engine->position);
        if (pw_generation_forward(engine, tokens.data(), count, 0, logits.data(), &sampled,
                                  error, sizeof(error)) != 0)
            throw std::runtime_error(error);
    }
    for (const int output : {2, 2, 1}) {
        if (pw_generation_forward(engine, tokens.data(), 1, output, logits.data(), &sampled,
                                  error, sizeof(error)) != 0 ||
            pw_generation_cache(engine, 0, nullptr, error, sizeof(error)) != 0)
            throw std::runtime_error(error);
    }
    llama_sampler_reset(engine->sampler.get());
    llama_perf_context_reset(engine->context.get());
}
}

extern "C" int pw_generation_inspect(const char * model, uint64_t * resident, uint64_t * mapped,
                                      char * error, size_t capacity) {
    return guarded(error, capacity, [&] {
        const auto inventory = inspect_e4b(model);
        *resident = inventory.resident;
        *mapped = inventory.mapped;
    });
}

extern "C" int pw_generation_probe(const char * pci, pw_device_info * info,
                                    char * error, size_t capacity) {
    return guarded(error, capacity, [&] {
        ggml_backend_dev_props properties{};
        ggml_backend_dev_get_props(device(pci), &properties);
        info->total_bytes = properties.memory_total;
        info->free_bytes = properties.memory_free;
    });
}

extern "C" int pw_generation_load(const char * path, const char * pci, uint32_t context,
                                   uint32_t chunk, uint32_t prefix, uint64_t budget, pw_generation ** result,
                                   char * error, size_t capacity) {
    *result = nullptr;
    return guarded(error, capacity, [&] {
        if (context < 2 || context > 65536 || !chunk || chunk > 256 || prefix >= context)
            throw std::runtime_error("invalid execution limits");
        inspect_e4b(path);
        auto engine = std::make_unique<pw_generation>();
        ggml_backend_dev_t devices[] = {device(pci), nullptr};
        auto mp = llama_model_default_params();
        mp.devices = devices;
        mp.n_gpu_layers = -1;
        mp.lazy_mode = LLAMA_LAZY_MODE_ON;
        engine->model.reset(llama_model_load_from_file(path, mp));
        if (!engine->model || llama_model_n_layer(engine->model.get()) != 42 ||
            llama_model_n_embd(engine->model.get()) != 2560 ||
            llama_vocab_n_tokens(llama_model_get_vocab(engine->model.get())) != 262144)
            throw std::runtime_error("artifact is not an E4B model");
        engine->sampler.reset(llama_sampler_chain_init(llama_sampler_chain_default_params()));
        llama_sampler_chain_add(engine->sampler.get(), llama_sampler_init_greedy());
        llama_sampler_seq_config sampler_config{0, engine->sampler.get()};
        auto cp = llama_context_default_params();
        cp.samplers = &sampler_config;
        cp.n_samplers = 1;
        cp.n_ctx = context;
        cp.n_batch = cp.n_ubatch = chunk;
        cp.n_seq_max = 2;
        cp.n_threads = cp.n_threads_batch = 1;
        cp.flash_attn_type = LLAMA_FLASH_ATTN_TYPE_ENABLED;
        cp.kv_unified = true;
        cp.swa_full = false;
        cp.type_k = cp.type_v = GGML_TYPE_F16;
        engine->context.reset(llama_init_from_model(engine->model.get(), cp));
        if (!engine->context) throw std::runtime_error("context allocation failed");
        engine->budget = budget;
        engine->check_memory();
        engine->limit = context;
        engine->chunk = chunk;
        engine->prefix_limit = prefix;
        warm(engine.get());
        *result = engine.release();
    });
}

extern "C" int pw_generation_forward(pw_generation * engine, const uint32_t * tokens,
                                      size_t count, int output, float * logits, uint32_t * sampled,
                                      char * error, size_t capacity) {
    return guarded(error, capacity, [&] {
        if (!count || count > engine->chunk || count > engine->limit - engine->position)
            throw std::runtime_error("forward exceeds execution limits");
        for (size_t i = 0; i < count; ++i)
            if (tokens[i] >= 262144) throw std::runtime_error("invalid input token");
        if (engine->position == 0) engine->spill_prefix();
        *sampled = UINT32_MAX;
        if (output && engine->greedy != (output == 2)) {
            if (output == 2) {
                // Backend sampler chains can only be initialized once.
                engine->sampler.reset(llama_sampler_chain_init(llama_sampler_chain_default_params()));
                llama_sampler_chain_add(engine->sampler.get(), llama_sampler_init_greedy());
            }
            if (!llama_set_sampler(engine->context.get(), 0, output == 2 ? engine->sampler.get() : nullptr))
                throw std::runtime_error("sampler transition failed");
            engine->greedy = output == 2;
        }
        auto batch = llama_batch_init(static_cast<int32_t>(count), 0, 1);
        batch.n_tokens = static_cast<int32_t>(count);
        for (size_t i = 0; i < count; ++i) {
            batch.token[i] = tokens[i];
            batch.pos[i] = engine->position + i;
            batch.n_seq_id[i] = 1;
            batch.seq_id[i][0] = 0;
            batch.logits[i] = output && i + 1 == count;
        }
        const int status = llama_decode(engine->context.get(), batch);
        llama_batch_free(batch);
        llama_synchronize(engine->context.get());
        if (status) throw std::runtime_error("Vulkan decode failed: " + std::to_string(status));
        engine->check_memory();
        engine->position += count;
        if (output) {
            const auto token = llama_get_sampled_token_ith(engine->context.get(), -1);
            if (output == 2 && token != LLAMA_TOKEN_NULL) {
                *sampled = static_cast<uint32_t>(token);
                return;
            }
            const auto values = llama_get_logits_ith(engine->context.get(), -1);
            if (!values) throw std::runtime_error("missing output logits");
            std::copy(values, values + 262144, logits);
        }
    });
}

extern "C" int pw_generation_cache(pw_generation * engine, int operation, int * restored,
                                    char * error, size_t capacity) {
    if (restored) *restored = 0;
    return guarded(error, capacity, [&] {
        switch (operation) {
        case 0: engine->reset_active(); break;
        case 1: engine->save_prefix(); break;
        case 2:
            if (!restored) throw std::runtime_error("missing checkpoint result");
            *restored = engine->restore_prefix() ? 1 : 0;
            break;
        case 3: engine->clear_prefix(); break;
        default: throw std::runtime_error("invalid cache operation");
        }
    });
}

extern "C" void pw_generation_free(pw_generation * engine) { delete engine; }
