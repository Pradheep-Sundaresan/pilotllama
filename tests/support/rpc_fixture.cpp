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
    void generate(const inferno::GenerationRequest& request, const DecodeStarted& decode,
                  const TokenCallback& token, const Checkpoint& checkpoint) override {
        checkpoint(); decode();
        if (request.prompt == "gate") {
            token("G"); gate_.wait(); checkpoint();
            if (request.max_output_tokens > 1) token("done");
        } else if (request.prompt == "failure") {
            throw std::runtime_error("private backend path /secret/model");
        } else if (request.prompt == "partial-failure") {
            token("partial");
            throw std::runtime_error("private backend path /secret/model");
        } else if (request.prompt == "oversize") {
            token(std::string(65537, 'x'));
        } else if (request.prompt == "flood") {
            const std::string piece(65536, 'x');
            for (std::uint32_t i = 0; i < request.max_output_tokens; ++i) { checkpoint(); token(piece); }
        } else if (request.prompt != "empty") {
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
