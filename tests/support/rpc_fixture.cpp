// Model-free transport fixture only. Never use this backend for demos/benchmarks.
#include "inferno/rpc_server.hpp"
#include <condition_variable>
#include <csignal>
#include <fstream>
#include <iostream>
#include <pthread.h>

namespace {
struct Gate {
    std::mutex mutex;
    std::condition_variable changed;
    bool open = false;
    void wait() { std::unique_lock lock(mutex); changed.wait(lock, [&] { return open; }); }
    void release() { std::lock_guard lock(mutex); open = true; changed.notify_all(); }
};
class FixtureBackend final : public inferno::InferenceBackend {
public:
    explicit FixtureBackend(Gate& gate) : gate_(gate) {}
    std::optional<inferno::MemorySnapshot> memory_snapshot() const override {
        inferno::MemorySnapshot result;
        result.process_samples.push_back({inferno::MemorySamplePoint::backend_ready,
            inferno::MeasurementStatus::measured, 1000, 100, 120, 80, 90});
        result.model_tensor_bytes = 64;
        result.llama_buffers.push_back({"fixture", 60, 20, 10});
        result.llama_model_allocated_bytes = 60;
        result.llama_context_allocated_bytes = 20;
        result.llama_compute_allocated_bytes = 10;
        result.configured_context_tokens = 4096;
        result.configured_batch_tokens = 128;
        result.configured_sequences = 1;
        result.configured_gpu_layers = 0;
        return result;
    }
    std::optional<inferno::ModelCapabilities> model_capabilities() const override {
        inferno::ModelCapabilities result;
        result.architecture = "fixture";
        result.parameter_count = 1;
        result.trained_context_tokens = 4096;
        result.configured_context_tokens = 4096;
        result.configured_max_output_tokens = 512;
        result.has_decoder = true;
        result.has_embedded_chat_template = true;
        result.queue_mode = inferno::CapabilitySupport::verified;
        result.continuous_mode = inferno::CapabilitySupport::unavailable;
        result.system_messages = inferno::CapabilitySupport::detected;
        result.multi_turn = inferno::CapabilitySupport::detected;
        result.thinking = inferno::CapabilitySupport::verified;
        return result;
    }
    void generate(const inferno::GenerationRequest& request, const DecodeStarted& decode,
                  const TokenCallback& token, const Checkpoint& checkpoint) override {
        checkpoint(); decode();
        const auto& prompt = request.messages.empty() ? request.prompt : request.messages.back().content;
        if (prompt == "reasoning" && request.enable_thinking) {
            token({"because", inferno::OutputChannel::reasoning});
            if (request.max_output_tokens > 1) token({"answer", inferno::OutputChannel::content});
        } else if (prompt == "sampling") {
            if (request.temperature != 0.7F || request.top_p != 0.8F || request.top_k != 40 ||
                request.seed != 42 || request.stop != std::vector<std::string>{"END", "###"})
                throw std::runtime_error("sampling controls not preserved");
            token("sampling-ok");
        } else if (prompt == "tools") {
            if (request.tool_choice != inferno::ToolChoice::required || request.tools.size() != 1 ||
                request.tools[0].name != "weather" || request.tools[0].description != "Read weather" ||
                request.tools[0].parameters_json != R"({"type":"object"})")
                throw std::runtime_error("tool contract not preserved");
            token("tools-ok");
        } else if (prompt == "tool-call") {
            if (request.tool_choice != inferno::ToolChoice::required || request.tools.size() != 1 || request.tools[0].name != "weather")
                throw std::runtime_error("tool-call request not preserved");
            token({std::string{}, inferno::OutputChannel::content, true});
            token({R"({"city":"Paris"})", inferno::OutputChannel::tool_call, false, 0, "call_weather", "weather"});
		} else if (prompt == "sunny") {
			if (request.messages.size() != 3 || request.messages[1].tool_calls.size() != 1 ||
				request.messages[1].tool_calls[0].id != "call_weather" || request.messages[2].tool_call_id != "call_weather")
				throw std::runtime_error("tool result history not preserved");
			token("It is sunny");
		} else if (prompt == "structured") {
			if (request.response_format != inferno::ResponseFormat::json_schema || request.response_schema_json.find("answer") == std::string::npos)
				throw std::runtime_error("structured response not preserved");
			token(R"({"answer":4})");
        } else if (prompt == "gate") {
            token("G"); gate_.wait(); checkpoint();
            if (request.max_output_tokens > 1) token("done");
        } else if (prompt == "failure") {
            throw std::runtime_error("private backend path /secret/model");
        } else if (prompt == "partial-failure") {
            token("partial");
            throw std::runtime_error("private backend path /secret/model");
        } else if (prompt == "oversize") {
            token(std::string(65537, 'x'));
        } else if (prompt == "flood") {
            const std::string piece(65536, 'x');
            for (std::uint32_t i = 0; i < request.max_output_tokens; ++i) { checkpoint(); token(piece); }
        } else if (prompt != "empty") {
            const std::string pieces[] = {std::string("\xf0\x9f", 2), std::string("\x98\x80", 2), "", "!"};
            for (std::uint32_t i = 0; i < 4 && i < request.max_output_tokens; ++i) { checkpoint(); token(pieces[i]); }
        }
        checkpoint();
    }
private:
    Gate& gate_;
};
} // namespace

int main(int argc, char** argv) {
    if (argc < 2 || argc > 3) return 2;
    sigset_t signals;
    sigemptyset(&signals); sigaddset(&signals, SIGTERM); sigaddset(&signals, SIGINT); sigaddset(&signals, SIGUSR1);
    if (pthread_sigmask(SIG_BLOCK, &signals, nullptr) != 0) return 1;
    try {
        Gate gate;
        inferno::Runtime runtime(std::make_unique<FixtureBackend>(gate), 1);
        inferno::RpcServer server(runtime, "127.0.0.1:0", 4, argc == 2, "model-free-test-fixture");
        std::ofstream ready(argv[1]); ready << server.port() << '\n'; ready.close();
        if (!ready) return 1;
        std::cerr << "MODEL-FREE RPC TEST FIXTURE; no inference model loaded\n";
        for (;;) {
            int signal = 0;
            if (sigwait(&signals, &signal) != 0) return 1;
            gate.release();
            if (signal != SIGUSR1) break;
        }
        server.shutdown(); runtime.shutdown();
    } catch (const std::exception& error) {
        std::cerr << error.what() << '\n'; return 1;
    }
}
