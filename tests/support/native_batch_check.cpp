#include "inferno/batch_engine.hpp"
#include "inferno/llama_backend.hpp"

#include <algorithm>
#include <atomic>
#include <filesystem>
#include <future>
#include <iostream>
#include <mutex>
#include <stdexcept>

namespace {
class TracedExecutor final : public inferno::BatchExecutor {
public:
    explicit TracedExecutor(std::unique_ptr<inferno::BatchExecutor> inner) : inner_(std::move(inner)) {}
    std::optional<inferno::MemorySnapshot> memory_snapshot() const override { return inner_->memory_snapshot(); }
    std::optional<inferno::ModelCapabilities> model_capabilities() const override { return inner_->model_capabilities(); }
    std::size_t admit(inferno::SequenceId id, const inferno::GenerationRequest& request) override {
        return inner_->admit(id, request);
    }
    std::vector<inferno::BatchStepEvent> step(std::span<const inferno::SequenceId> ids, std::size_t chunk) override {
        if (ids.size() == 2) combined_turns_.fetch_add(1);
        return inner_->step(ids, chunk);
    }
    bool remove(inferno::SequenceId id) noexcept override { return inner_->remove(id); }
    std::size_t combined_turns() const { return combined_turns_.load(); }
private:
    std::unique_ptr<inferno::BatchExecutor> inner_;
    std::atomic<std::size_t> combined_turns_{0};
};

inferno::GenerationRequest request(inferno::RequestId id, std::string prompt, std::uint32_t output = 4) {
    return {id, std::move(prompt), output, inferno::RequestClock::now() + std::chrono::seconds(30), {}};
}

std::uint64_t required(const std::optional<std::uint64_t>& value, const char* name) {
    if (!value) throw std::runtime_error(std::string("Missing safety measurement: ") + name);
    return *value;
}
}

int main(int argc, char** argv) {
    if (argc != 2 || !std::filesystem::is_regular_file(argv[1])) {
        std::cerr << "Usage: inferno_native_batch_check EXISTING_LOCAL_GGUF\n";
        return 2;
    }
    try {
        auto traced = std::make_unique<TracedExecutor>(inferno::make_llama_batch_executor(
            {.path = argv[1], .allow_unqualified_continuous = true}));
        auto* trace = traced.get();
        inferno::ContinuousBatchEngine engine(std::move(traced));
        const auto before = engine.memory_snapshot();
        if (!before || before->configured_sequences != 2) throw std::runtime_error("Two-sequence memory snapshot unavailable");
        const auto ready = std::find_if(before->process_samples.begin(), before->process_samples.end(), [](const auto& sample) {
            return sample.point == inferno::MemorySamplePoint::backend_ready;
        });
        if (ready == before->process_samples.end()) throw std::runtime_error("Backend-ready process measurement unavailable");
        const auto rss = required(ready->resident_bytes, "resident bytes");
        const auto footprint = required(ready->physical_footprint_bytes, "physical footprint");
        if (rss > 12ULL * 1024 * 1024 * 1024 || footprint > 4ULL * 1024 * 1024 * 1024) {
            throw std::runtime_error("Two-sequence allocation exceeds the bounded qualification limit");
        }

        const std::string prompt_a = "What is 2 + 2? Reply with just the number.";
        const std::string prompt_b = "What is 3 + 3? Reply with just the number.";
        const auto serial_a = engine.submit(request(1, prompt_a)).completion.get();
        const auto serial_b = engine.submit(request(2, prompt_b)).completion.get();
        if (serial_a.state != inferno::RequestState::completed || serial_b.state != inferno::RequestState::completed) {
            throw std::runtime_error("Serial reference generation failed: " + serial_a.error + " / " + serial_b.error);
        }
        auto parallel_a = engine.submit(request(3, prompt_a));
        auto parallel_b = engine.submit(request(4, prompt_b));
        const auto result_a = parallel_a.completion.get();
        const auto result_b = parallel_b.completion.get();
        if (result_a.state != inferno::RequestState::completed || result_b.state != inferno::RequestState::completed ||
            result_a.text != serial_a.text || result_b.text != serial_b.text || trace->combined_turns() == 0) {
            throw std::runtime_error("Parallel output or interleaving did not match the serial reference");
        }

        std::promise<void> first_token;
        std::atomic<bool> announced{false};
        auto cancelled = engine.submit(request(5, "Count upward from one using words separated by spaces.", 64),
            [&](inferno::GeneratedPiece) { if (!announced.exchange(true)) first_token.set_value(); });
        auto survivor = engine.submit(request(6, prompt_b, 8));
        first_token.get_future().wait();
        (void)cancelled.control->cancel();
        const auto cancelled_result = cancelled.completion.get();
        const auto survivor_result = survivor.completion.get();
        if (cancelled_result.state != inferno::RequestState::cancelled ||
            survivor_result.state != inferno::RequestState::completed) {
            throw std::runtime_error("Cancellation was not isolated to one active sequence");
        }
        const auto follow_up = engine.submit(request(7, prompt_a)).completion.get();
        if (follow_up.state != inferno::RequestState::completed || follow_up.text != serial_a.text) {
            throw std::runtime_error("Clean follow-up generation failed after cancellation");
        }
        const auto after = engine.memory_snapshot();
        if (!after) throw std::runtime_error("Final memory snapshot unavailable");
        engine.shutdown();
        std::cout << "serial_a=" << serial_a.text << "\nserial_b=" << serial_b.text
                  << "\ncombined_turns=" << trace->combined_turns()
                  << "\nready_rss_bytes=" << rss
                  << "\nready_footprint_bytes=" << footprint
                  << "\ncontext_allocated_bytes=" << required(after->llama_context_allocated_bytes, "context allocation")
                  << "\nstatus=passed\n";
    } catch (const std::exception& error) {
        std::cerr << "Native batch check failed: " << error.what() << '\n';
        return 1;
    }
}
