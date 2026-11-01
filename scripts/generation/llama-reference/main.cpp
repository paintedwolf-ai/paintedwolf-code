#include "llama.h"
#include "ggml-backend.h"
#include "json.hpp"

#include <algorithm>
#include <chrono>
#include <cstdlib>
#include <fstream>
#include <iostream>
#include <memory>
#include <stdexcept>
#include <sys/resource.h>
#include <vector>

using json = nlohmann::json;
using clock_type = std::chrono::steady_clock;

static double seconds(clock_type::time_point start) {
    return std::chrono::duration<double>(clock_type::now() - start).count();
}

static void decode(llama_context * ctx, const std::vector<llama_token> & tokens,
                   int offset, int count, bool output) {
    auto batch = llama_batch_init(count, 0, 1);
    batch.n_tokens = count;
    for (int i = 0; i < count; ++i) {
        batch.token[i] = tokens[offset + i];
        batch.pos[i] = offset + i;
        batch.n_seq_id[i] = 1;
        batch.seq_id[i][0] = 0;
        batch.logits[i] = output && i == count - 1;
    }
    const int status = llama_decode(ctx, batch);
    llama_batch_free(batch);
    if (status != 0) { throw std::runtime_error("decode failed: " + std::to_string(status)); }
    llama_synchronize(ctx);
}

static llama_token greedy(llama_context * ctx, int vocabulary) {
    const auto sampled = llama_get_sampled_token_ith(ctx, -1);
    if (sampled != LLAMA_TOKEN_NULL) { return sampled; }
    const float * logits = llama_get_logits_ith(ctx, -1);
    if (!logits) { throw std::runtime_error("missing logits"); }
    return static_cast<llama_token>(std::max_element(logits, logits + vocabulary) - logits);
}

static json measure(llama_context * ctx, const std::vector<llama_token> & prompt, int generated, int chunk_size) {
    llama_memory_clear(llama_get_memory(ctx), true);
    struct rusage before {};
    getrusage(RUSAGE_SELF, &before);
    auto start = clock_type::now();
    double prefill_step_max = 0;
    for (int offset = 0; offset < static_cast<int>(prompt.size()); offset += chunk_size) {
        const auto step = clock_type::now();
        const int count = std::min(chunk_size, static_cast<int>(prompt.size()) - offset);
        decode(ctx, prompt, offset, count, offset + count == static_cast<int>(prompt.size()));
        prefill_step_max = std::max(prefill_step_max, seconds(step));
    }
    const double prefill = seconds(start);
    std::vector<llama_token> all = prompt;
    std::vector<llama_token> output;
    const int vocabulary = llama_vocab_n_tokens(llama_model_get_vocab(llama_get_model(ctx)));
    auto token = greedy(ctx, vocabulary);
    start = clock_type::now();
    double decode_step_max = 0;
    for (int i = 0; i < generated; ++i) {
        const auto step = clock_type::now();
        output.push_back(token);
        all.push_back(token);
        decode(ctx, all, static_cast<int>(all.size()) - 1, 1, true);
        token = greedy(ctx, vocabulary);
        decode_step_max = std::max(decode_step_max, seconds(step));
    }
    const double generation = seconds(start);
    struct rusage usage {};
    getrusage(RUSAGE_SELF, &usage);
    uint64_t peak = usage.ru_maxrss;
#ifndef __APPLE__
    peak *= 1024;
#endif
    return {{"prefill_step_max_seconds", prefill_step_max}, {"decode_step_max_seconds", decode_step_max},
            {"prefill_seconds", prefill}, {"decode_seconds", generation},
            {"prompt_tps", prompt.size() / prefill}, {"generation_tps", generated / generation},
            {"process_peak_rss_bytes", peak}, {"tokens", output},
            {"major_faults", usage.ru_majflt - before.ru_majflt},
            {"minor_faults", usage.ru_minflt - before.ru_minflt},
            {"block_inputs", usage.ru_inblock - before.ru_inblock},
            {"backend_sampling", llama_get_sampled_token_ith(ctx, -1) != LLAMA_TOKEN_NULL}};
}

int main(int argc, char ** argv) {
    try {
        if (argc != 3) { throw std::runtime_error("expected model and benchmark input paths"); }
        std::ifstream input(argv[2]);
        const json spec = json::parse(input);
        const auto prompt = spec.at("prompt_tokens").get<std::vector<llama_token>>();
        const int generated = spec.at("generation_tokens");
        const int trials = spec.at("trials");
        const int chunk_size = spec.at("prefill_chunk");
        if (chunk_size < 1 || chunk_size > 256 || prompt.empty() || prompt.size() + generated > 65536 || generated < 1 || generated > 4096 || trials < 1 || trials > 20) {
            throw std::runtime_error("benchmark dimensions exceed their bounds");
        }
        ggml_backend_load_all();
        llama_backend_init();
        auto device = ggml_backend_dev_by_type(GGML_BACKEND_DEVICE_TYPE_GPU);
        if (!device) { device = ggml_backend_dev_by_type(GGML_BACKEND_DEVICE_TYPE_IGPU); }
        if (!device) { throw std::runtime_error("no hardware GPU available"); }
        ggml_backend_dev_t devices[] = {device, nullptr};
        auto mp = llama_model_default_params();
        mp.devices = devices;
        mp.n_gpu_layers = -1;
        mp.lazy_mode = spec.at("ple") == "mapped" ? LLAMA_LAZY_MODE_ON : LLAMA_LAZY_MODE_OFF;
        auto model = std::unique_ptr<llama_model, decltype(&llama_model_free)>(
            llama_model_load_from_file(argv[1], mp), llama_model_free);
        if (!model || llama_model_n_layer(model.get()) != 42 || llama_model_n_embd(model.get()) != 2560) {
            throw std::runtime_error("the reference benchmark only accepts E4B");
        }
        const int vocabulary = llama_vocab_n_tokens(llama_model_get_vocab(model.get()));
        for (const auto token : prompt) {
            if (token < 0 || token >= vocabulary) { throw std::runtime_error("invalid input token"); }
        }
        auto sampler = std::unique_ptr<llama_sampler, decltype(&llama_sampler_free)>(
            llama_sampler_chain_init(llama_sampler_chain_default_params()), llama_sampler_free);
        llama_sampler_chain_add(sampler.get(), llama_sampler_init_greedy());
        llama_sampler_seq_config sampler_config {0, sampler.get()};
        auto cp = llama_context_default_params();
        cp.samplers = &sampler_config;
        cp.n_samplers = 1;
        const char * worker_limit = std::getenv("PW_TEST_WORKERS");
        cp.n_threads = cp.n_threads_batch = worker_limit ? std::max(1, std::atoi(worker_limit)) : 1;
        cp.n_ctx = prompt.size() + generated + 1;
        cp.n_batch = cp.n_ubatch = chunk_size;
        cp.n_seq_max = 1;
        cp.flash_attn_type = LLAMA_FLASH_ATTN_TYPE_ENABLED;
        cp.swa_full = false;
        cp.kv_unified = true;
        cp.type_k = cp.type_v = GGML_TYPE_F16;
        auto ctx = std::unique_ptr<llama_context, decltype(&llama_free)>(
            llama_init_from_model(model.get(), cp), llama_free);
        if (!ctx) { throw std::runtime_error("context allocation failed"); }
        for (int trial = 0; trial <= trials; ++trial) {
            auto result = measure(ctx.get(), prompt, generated, chunk_size);
            result["trial"] = trial;
            result["warmup"] = trial == 0;
            result["ple"] = spec.at("ple");
            result["device"] = ggml_backend_dev_name(device);
            std::cout << result.dump() << std::endl;
        }
    } catch (const std::exception & error) {
        std::cerr << error.what() << std::endl;
        return 1;
    }
    return 0;
}
