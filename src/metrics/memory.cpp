#include "inferno/memory.hpp"

#include <chrono>
#ifdef __APPLE__
#include <mach/mach.h>
#include <mach/task_info.h>
#endif

namespace inferno {

ProcessMemorySample capture_process_memory(MemorySamplePoint point) noexcept {
    const auto now = std::chrono::steady_clock::now().time_since_epoch();
    ProcessMemorySample sample{point, MeasurementStatus::unavailable,
        static_cast<std::uint64_t>(std::chrono::duration_cast<std::chrono::nanoseconds>(now).count()),
        std::nullopt, std::nullopt, std::nullopt, std::nullopt};
#ifdef __APPLE__
    task_vm_info_data_t info{};
    mach_msg_type_number_t count = TASK_VM_INFO_COUNT;
    if (task_info(mach_task_self(), TASK_VM_INFO,
                  reinterpret_cast<task_info_t>(&info), &count) == KERN_SUCCESS) {
        sample.status = MeasurementStatus::measured;
        sample.resident_bytes = info.resident_size;
        sample.peak_resident_bytes = info.resident_size_peak;
        if (count >= TASK_VM_INFO_REV1_COUNT) sample.physical_footprint_bytes = info.phys_footprint;
        if (count >= TASK_VM_INFO_REV3_COUNT && info.ledger_phys_footprint_peak >= 0) {
            sample.peak_physical_footprint_bytes =
                static_cast<std::uint64_t>(info.ledger_phys_footprint_peak);
        }
    }
#endif
    return sample;
}

} // namespace inferno
