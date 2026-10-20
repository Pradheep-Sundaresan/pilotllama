#include "inferno/v1/inference.grpc.pb.h"
#include <gtest/gtest.h>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <limits>
#include <string>
#include <type_traits>

namespace {
namespace wire = inferno::v1;

TEST(ProtoBindings, PreservesMissingAndZeroMetrics) {
    wire::RequestMetrics source;
    source.set_runtime_latency_ms(0);
    source.set_ttft_ms(0);
    wire::RequestMetrics copy;
    ASSERT_TRUE(copy.ParseFromString(source.SerializeAsString()));
    EXPECT_TRUE(copy.has_runtime_latency_ms());
    EXPECT_TRUE(copy.has_ttft_ms());
    EXPECT_DOUBLE_EQ(copy.ttft_ms(), 0);
    EXPECT_FALSE(copy.has_queue_wait_ms());
    EXPECT_FALSE(copy.has_decode_tokens_per_second());
}

TEST(ProtoBindings, PreservesRawTokenBytesAndFullWidthIds) {
    wire::GenerateEvent source;
    source.set_request_id(std::numeric_limits<std::uint64_t>::max());
    source.mutable_token()->set_piece(std::string("\xf0\x9f\0", 3));
    source.mutable_token()->set_output_tokens(1);
    wire::GenerateEvent copy;
    ASSERT_TRUE(copy.ParseFromString(source.SerializeAsString()));
    EXPECT_EQ(copy.request_id(), std::numeric_limits<std::uint64_t>::max());
    ASSERT_TRUE(copy.has_token());
    EXPECT_EQ(copy.token().piece(), std::string("\xf0\x9f\0", 3));
    source.mutable_token()->clear_piece();
    ASSERT_TRUE(copy.ParseFromString(source.SerializeAsString()));
    EXPECT_TRUE(copy.has_token());
    EXPECT_TRUE(copy.token().piece().empty());
    EXPECT_EQ(copy.token().output_tokens(), 1U);
}

TEST(ProtoBindings, PreservesMemoryPresenceAndRows) {
    wire::GetStatusResponse source;
    source.set_state(wire::BACKEND_STATE_READY);
    auto* memory = source.mutable_memory();
    auto* sample = memory->add_process_samples();
    sample->set_point(wire::MEMORY_SAMPLE_POINT_BACKEND_READY);
    sample->set_status(wire::MEASUREMENT_STATUS_MEASURED);
    sample->set_monotonic_ns(1);
    sample->set_resident_bytes(0);
    auto* row = memory->add_llama_buffers();
    row->set_name("fixture");
    row->set_context_bytes(20);
    wire::GetStatusResponse copy;
    ASSERT_TRUE(copy.ParseFromString(source.SerializeAsString()));
    ASSERT_TRUE(copy.has_memory());
    ASSERT_EQ(copy.memory().process_samples_size(), 1);
    EXPECT_TRUE(copy.memory().process_samples(0).has_resident_bytes());
    EXPECT_EQ(copy.memory().process_samples(0).resident_bytes(), 0U);
    EXPECT_FALSE(copy.memory().has_model_tensor_bytes());
    ASSERT_EQ(copy.memory().llama_buffers_size(), 1);
    EXPECT_EQ(copy.memory().llama_buffers(0).context_bytes(), 20U);
}

TEST(ProtoBindings, OneofAndStructuredFailureRoundTrip) {
    wire::GenerateEvent source;
    source.set_request_id(42);
    source.mutable_accepted();
    auto* result = source.mutable_finished();
    EXPECT_FALSE(source.has_accepted());
    result->set_state(wire::TERMINAL_STATE_FAILED);
    result->mutable_metrics()->set_output_tokens(1);
    result->mutable_error()->set_request_id(42);
    result->mutable_error()->set_code(wire::ERROR_CODE_BACKEND_FAILURE);
    result->mutable_error()->set_message("backend failure");
    wire::GenerateEvent copy;
    ASSERT_TRUE(copy.ParseFromString(source.SerializeAsString()));
    ASSERT_TRUE(copy.has_finished());
    EXPECT_EQ(copy.finished().state(), wire::TERMINAL_STATE_FAILED);
    EXPECT_EQ(copy.finished().error().request_id(), 42U);
    EXPECT_EQ(copy.finished().error().code(), wire::ERROR_CODE_BACKEND_FAILURE);
    EXPECT_EQ(copy.finished().error().message(), "backend failure");
    EXPECT_TRUE(copy.finished().has_metrics());
}

TEST(ProtoBindings, StreamingServiceAndUnknownFieldsRemainAvailable) {
    const auto* service = google::protobuf::DescriptorPool::generated_pool()
        ->FindServiceByName("inferno.v1.InferenceService");
    ASSERT_NE(service, nullptr);
    ASSERT_EQ(service->method_count(), 3);
    const auto* generate = service->FindMethodByName("Generate");
    ASSERT_NE(generate, nullptr);
    EXPECT_TRUE(generate->server_streaming());
    EXPECT_FALSE(generate->client_streaming());
    static_assert(std::is_base_of_v<grpc::Service, wire::InferenceService::Service>);
    wire::GenerateRequest source;
    source.set_request_id(42);
    source.GetReflection()->MutableUnknownFields(&source)->AddVarint(100, 7);
    wire::GenerateRequest copy;
    ASSERT_TRUE(copy.ParseFromString(source.SerializeAsString()));
    EXPECT_EQ(copy.request_id(), 42U);
    ASSERT_EQ(copy.GetReflection()->GetUnknownFields(copy).field_count(), 1);
    EXPECT_EQ(copy.GetReflection()->GetUnknownFields(copy).field(0).varint(), 7U);
}

// Optional two-way wire check: C++ writes fixtures, Go verifies and replies,
// then C++ verifies the Go-produced messages. No network or model is involved.
TEST(ProtoBindings, CrossLanguageWire) {
    const auto* directory = std::getenv("INFERNO_PROTO_INTEROP_DIR");
    const auto* mode = std::getenv("INFERNO_PROTO_INTEROP_MODE");
    if (!directory || !mode) GTEST_SKIP() << "Run scripts/test-proto-interop.sh";
    const auto root = std::filesystem::path(directory);
    if (std::string(mode) == "write") {
        wire::GenerateEvent token;
        token.set_request_id(std::numeric_limits<std::uint64_t>::max());
        token.mutable_token()->set_piece(std::string("\xf0\x9f\0", 3));
        token.mutable_token()->set_output_tokens(1);
        std::ofstream token_file(root / "cpp-token.bin", std::ios::binary);
        ASSERT_TRUE(token.SerializeToOstream(&token_file));
        token_file.close();
        ASSERT_TRUE(token_file);
        wire::GenerateEvent finished;
        finished.set_request_id(token.request_id());
        auto* result = finished.mutable_finished();
        result->set_state(wire::TERMINAL_STATE_FAILED);
        result->mutable_metrics()->set_output_tokens(1);
        result->mutable_metrics()->set_runtime_latency_ms(5);
        result->mutable_metrics()->set_ttft_ms(0);
        result->mutable_error()->set_request_id(token.request_id());
        result->mutable_error()->set_code(wire::ERROR_CODE_BACKEND_FAILURE);
        result->mutable_error()->set_message("fixture failure");
        std::ofstream result_file(root / "cpp-finished.bin", std::ios::binary);
        ASSERT_TRUE(finished.SerializeToOstream(&result_file));
        result_file.close();
        ASSERT_TRUE(result_file);
    } else {
        ASSERT_EQ(std::string(mode), "check");
        wire::GenerateEvent token;
        std::ifstream token_file(root / "go-token.bin", std::ios::binary);
        ASSERT_TRUE(token_file);
        ASSERT_TRUE(token.ParseFromIstream(&token_file));
        ASSERT_TRUE(token.has_token());
        EXPECT_EQ(token.request_id(), std::numeric_limits<std::uint64_t>::max());
        EXPECT_EQ(token.token().piece(), std::string("\x98\x80\0", 3));
        EXPECT_EQ(token.token().output_tokens(), 2U);
        wire::GenerateEvent finished;
        std::ifstream result_file(root / "go-finished.bin", std::ios::binary);
        ASSERT_TRUE(result_file);
        ASSERT_TRUE(finished.ParseFromIstream(&result_file));
        ASSERT_TRUE(finished.has_finished());
        EXPECT_EQ(finished.request_id(), token.request_id());
        EXPECT_EQ(finished.finished().state(), wire::TERMINAL_STATE_TIMED_OUT);
        const auto& metrics = finished.finished().metrics();
        EXPECT_EQ(metrics.output_tokens(), 1U);
        EXPECT_TRUE(metrics.has_runtime_latency_ms());
        EXPECT_DOUBLE_EQ(metrics.runtime_latency_ms(), 5);
        EXPECT_FALSE(metrics.has_ttft_ms());
        EXPECT_TRUE(metrics.has_queue_wait_ms());
        EXPECT_DOUBLE_EQ(metrics.queue_wait_ms(), 0);
        EXPECT_EQ(finished.finished().error().request_id(), token.request_id());
        EXPECT_EQ(finished.finished().error().code(), wire::ERROR_CODE_DEADLINE_EXCEEDED);
    }
}
} // namespace
