#include "memory.h"
#include "llama-ext.h"
#include "ggml-backend.h"

nlohmann::json memory_buffers(const llama_context * context) {
    auto buffers = nlohmann::json::array();
    for (const auto & [type, bytes] : llama_get_memory_breakdown(context)) {
        auto device = ggml_backend_buft_get_device(type);
        ggml_backend_dev_props properties {};
        if (device) { ggml_backend_dev_get_props(device, &properties); }
        buffers.push_back({
            {"buffer_type", ggml_backend_buft_name(type)},
            {"host_buffer", ggml_backend_buft_is_host(type)},
            {"device_type", device ? static_cast<int>(properties.type) : -1},
            {"device_id", properties.device_id ? properties.device_id : ""},
            {"model_bytes", bytes.model},
            {"context_bytes", bytes.context},
            {"compute_bytes", bytes.compute},
        });
    }
    return buffers;
}
