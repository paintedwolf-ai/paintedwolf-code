#pragma once
#include <cstdint>
struct pw_inventory { uint64_t resident; uint64_t mapped; uint32_t layers; };
pw_inventory inspect_gemma4(const char * path);
