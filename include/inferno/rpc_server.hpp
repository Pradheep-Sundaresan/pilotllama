#pragma once
#include "inferno/runtime.hpp"
#include <cstddef>
#include <memory>
#include <string>

namespace inferno {
// Runtime must outlive the server. Shutdown cancels RPC work and waits for native
// completion/reactor cleanup; a stuck backend can still block it indefinitely.
class RpcServer {
public:
    RpcServer(GenerationRuntime& runtime, std::string loopback_address, std::size_t max_requests,
              bool ready = false, std::string model_id = {});
    ~RpcServer();
    RpcServer(const RpcServer&) = delete;
    RpcServer& operator=(const RpcServer&) = delete;
    [[nodiscard]] int port() const;
    static void validate_address(const std::string& address);
    void shutdown();
private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};
} // namespace inferno
