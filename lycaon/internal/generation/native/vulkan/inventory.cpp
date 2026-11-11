#include "inventory.h"
#include <vulkan/vulkan.h>
#include <algorithm>
#include <cstdio>
#include <cstring>
#include <limits>
#include <stdexcept>
#include <vector>

namespace {
void check(VkResult result) {
    if (result != VK_SUCCESS) throw std::runtime_error("Vulkan inventory query failed");
}

struct Instance {
    VkInstance value = VK_NULL_HANDLE;
    ~Instance() { if (value) vkDestroyInstance(value, nullptr); }
};

bool supports(const std::vector<VkExtensionProperties> & extensions, const char * name) {
    return std::any_of(extensions.begin(), extensions.end(), [name](const auto & item) {
        return std::strcmp(item.extensionName, name) == 0;
    });
}

void describe(VkPhysicalDevice device, pw_gpu_inventory & output) {
    uint32_t count = 0;
    check(vkEnumerateDeviceExtensionProperties(device, nullptr, &count, nullptr));
    if (count > 4096) throw std::runtime_error("Vulkan extension inventory exceeds limit");
    std::vector<VkExtensionProperties> extensions(count);
    check(vkEnumerateDeviceExtensionProperties(device, nullptr, &count, extensions.data()));
    if (!supports(extensions, VK_EXT_PCI_BUS_INFO_EXTENSION_NAME)) return;
    VkPhysicalDevicePCIBusInfoPropertiesEXT pci{VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_PCI_BUS_INFO_PROPERTIES_EXT};
    VkPhysicalDeviceProperties2 properties{VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_PROPERTIES_2};
    properties.pNext = &pci;
    vkGetPhysicalDeviceProperties2(device, &properties);
    const auto kind = properties.properties.deviceType;
    if (kind != VK_PHYSICAL_DEVICE_TYPE_DISCRETE_GPU && kind != VK_PHYSICAL_DEVICE_TYPE_INTEGRATED_GPU) return;
    if (pci.pciBus > 255 || pci.pciDevice > 31 || pci.pciFunction > 7) return;
    std::snprintf(output.pci, sizeof(output.pci), "%04x:%02x:%02x.%x", pci.pciDomain, pci.pciBus, pci.pciDevice, pci.pciFunction);
    std::memcpy(output.name, properties.properties.deviceName, sizeof(output.name));
    output.name[sizeof(output.name) - 1] = 0;
    output.unified_memory = kind == VK_PHYSICAL_DEVICE_TYPE_INTEGRATED_GPU;

    VkPhysicalDeviceMemoryBudgetPropertiesEXT budget{VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_MEMORY_BUDGET_PROPERTIES_EXT};
    VkPhysicalDeviceMemoryProperties2 memory{VK_STRUCTURE_TYPE_PHYSICAL_DEVICE_MEMORY_PROPERTIES_2};
    output.availability_known = supports(extensions, VK_EXT_MEMORY_BUDGET_EXTENSION_NAME);
    if (output.availability_known) memory.pNext = &budget;
    vkGetPhysicalDeviceMemoryProperties2(device, &memory);
    for (uint32_t i = 0; i < memory.memoryProperties.memoryHeapCount; ++i) {
        const auto & heap = memory.memoryProperties.memoryHeaps[i];
        if (!(heap.flags & VK_MEMORY_HEAP_DEVICE_LOCAL_BIT)) continue;
        if (heap.size > std::numeric_limits<uint64_t>::max() - output.total_bytes)
            throw std::runtime_error("Vulkan memory inventory overflow");
        output.total_bytes += heap.size;
        if (output.availability_known) {
            const auto limit = std::min(heap.size, budget.heapBudget[i]);
            output.available_bytes += limit - std::min(limit, budget.heapUsage[i]);
        }
    }
}
}

extern "C" int pw_generation_inventory(pw_gpu_inventory * output, size_t capacity,
                                        size_t * count, char * error, size_t error_capacity) {
    *count = 0;
    try {
        VkApplicationInfo app{VK_STRUCTURE_TYPE_APPLICATION_INFO};
        app.pApplicationName = "Painted Wolf Code";
        app.apiVersion = VK_API_VERSION_1_2;
        VkInstanceCreateInfo create{VK_STRUCTURE_TYPE_INSTANCE_CREATE_INFO};
        create.pApplicationInfo = &app;
        Instance instance;
        check(vkCreateInstance(&create, nullptr, &instance.value));
        uint32_t available = 0;
        check(vkEnumeratePhysicalDevices(instance.value, &available, nullptr));
        if (available > 32) throw std::runtime_error("Vulkan device inventory exceeds limit");
        std::vector<VkPhysicalDevice> devices(available);
        check(vkEnumeratePhysicalDevices(instance.value, &available, devices.data()));
        for (auto device : devices) {
            pw_gpu_inventory candidate{};
            describe(device, candidate);
            if (!candidate.pci[0] || !candidate.total_bytes) continue;
            if (*count == capacity) throw std::runtime_error("Vulkan inventory buffer is full");
            output[(*count)++] = candidate;
        }
        return 0;
    } catch (const std::exception & exception) {
        if (error_capacity) std::snprintf(error, error_capacity, "%s", exception.what());
        return -1;
    } catch (...) {
        if (error_capacity) error[0] = 0;
        return -1;
    }
}
