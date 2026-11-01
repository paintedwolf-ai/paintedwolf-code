#pragma once

#include "llama.h"
#include "json.hpp"

nlohmann::json memory_buffers(const llama_context * context);
