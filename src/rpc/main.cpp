#include "inferno/llama_backend.hpp"
#include "inferno/rpc_server.hpp"
#include <csignal>
#include <fcntl.h>
#include <iostream>
#include <pthread.h>
#include <stdexcept>
#include <string_view>
#include <unordered_set>
#include <unistd.h>

namespace {
void ready_file(const std::string& path, int port) {
    // Opt-in caller-owned output; never replace an existing file.
    const int file = open(path.c_str(), O_WRONLY | O_CREAT | O_EXCL, 0600);
    if (file < 0) throw std::runtime_error("Cannot create ready file (must not exist)");
    const auto text = std::to_string(port) + "\n";
    const auto written = write(file, text.data(), text.size());
    const int closed = close(file);
    if (written != static_cast<ssize_t>(text.size()) || closed != 0)
        throw std::runtime_error("Cannot write ready file");
}
}

int main(int argc, char** argv) {
    if (argc == 2 && std::string_view(argv[1]) == "--help") {
        std::cout << "Usage: inferno_rpc_server --model LOCAL_GGUF [--listen 127.0.0.1:8081]\n"
                     "       [--model-id local-nemotron-4b] [--ready-file NEW_LOCAL_PATH]\n"
                     "       [--batching queue|continuous]\n"
                     "Requires a supported local GGUF; no model downloads or public HTTP API.\n";
        return 0;
    }
    std::string model, address = "127.0.0.1:8081", model_id = "local-nemotron-4b", ready, batching = "queue";
    try {
        std::unordered_set<std::string_view> options;
        for (int i = 1; i < argc; i += 2) {
            const std::string_view key(argv[i]);
            if (i + 1 >= argc || !options.insert(key).second) throw std::invalid_argument("Missing value or duplicate option");
            const std::string value(argv[i + 1]);
            if (key == "--model") model = value;
            else if (key == "--listen") address = value;
            else if (key == "--model-id") model_id = value;
            else if (key == "--ready-file") ready = value;
            else if (key == "--batching") batching = value;
            else throw std::invalid_argument("Unknown option");
        }
        if (model.empty()) throw std::invalid_argument("Require --model LOCAL_GGUF");
        if (batching != "queue" && batching != "continuous") throw std::invalid_argument("Batching must be queue or continuous");
        if (model_id.empty() || model_id.size() > 128 || model_id.find_first_not_of(
                "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != std::string::npos)
            throw std::invalid_argument("Model ID must be 1..128 ASCII letters/digits/dot/dash/underscore");
        inferno::RpcServer::validate_address(address); // Reject unsafe binds before model load.
    } catch (const std::invalid_argument& error) {
        std::cerr << error.what() << ". Use --help.\n"; return 2;
    }
    sigset_t signals;
    sigemptyset(&signals); sigaddset(&signals, SIGTERM); sigaddset(&signals, SIGINT);
    if (pthread_sigmask(SIG_BLOCK, &signals, nullptr) != 0) { std::cerr << "Cannot configure process signal handling\n"; return 1; }
    try {
        std::unique_ptr<inferno::GenerationRuntime> runtime;
        if (batching == "continuous") {
            runtime = std::make_unique<inferno::ContinuousBatchEngine>(
                inferno::make_llama_batch_executor(inferno::LlamaModelConfig{.path = model}));
        } else {
            runtime = std::make_unique<inferno::Runtime>(inferno::make_llama_backend({model}), 2);
        }
        sigset_t pending;
        if (sigpending(&pending) != 0) throw std::runtime_error("Cannot inspect process signals");
        if (sigismember(&pending, SIGTERM) || sigismember(&pending, SIGINT)) return 0;
        inferno::RpcServer server(*runtime, address, 4, true, model_id);
        if (!ready.empty()) ready_file(ready, server.port());
        std::cout << "inferno RPC ready on port " << server.port() << "; model-id=" << model_id << std::endl;
        int signal = 0;
        if (sigwait(&signals, &signal) != 0) throw std::runtime_error("Cannot wait for process shutdown");
        server.shutdown(); runtime->shutdown();
    } catch (const std::exception& error) {
        std::cerr << "Native RPC server failed: " << error.what() << '\n'; return 1;
    }
}
