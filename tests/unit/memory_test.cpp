#include "inferno/memory.hpp"
#include <gtest/gtest.h>

TEST(Memory, ProcessSampleIsBoundedAndInternallyConsistent) {
    const auto sample = inferno::capture_process_memory(inferno::MemorySamplePoint::request_start);
    EXPECT_EQ(sample.point, inferno::MemorySamplePoint::request_start);
    EXPECT_GT(sample.monotonic_ns, 0U);
#ifdef __APPLE__
    ASSERT_EQ(sample.status, inferno::MeasurementStatus::measured);
    ASSERT_TRUE(sample.resident_bytes);
    ASSERT_TRUE(sample.peak_resident_bytes);
    EXPECT_GT(*sample.resident_bytes, 0U);
    EXPECT_GE(*sample.peak_resident_bytes, *sample.resident_bytes);
    if (sample.peak_physical_footprint_bytes && sample.physical_footprint_bytes) {
        EXPECT_GE(*sample.peak_physical_footprint_bytes, *sample.physical_footprint_bytes);
    }
#else
    EXPECT_EQ(sample.status, inferno::MeasurementStatus::unavailable);
    EXPECT_FALSE(sample.resident_bytes);
#endif
}
