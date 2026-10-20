#include "inferno/rpc_server.hpp"
#include "inferno/model_prompt.hpp"
#include "inferno/v1/inference.grpc.pb.h"
#include <grpcpp/grpcpp.h>
#include <arpa/inet.h>
#include <algorithm>
#include <charconv>
#include <condition_variable>
#include <limits>
#include <stdexcept>
#include <unordered_map>

namespace inferno {
namespace {
namespace wire = inferno::v1;
constexpr int message_limit = 128 * 1024;
constexpr std::size_t piece_limit = 64 * 1024;
using grpc::Status;
using grpc::StatusCode;

void check_loopback_address(const std::string& address) {
    const auto split = address.rfind(':');
    if (split == std::string::npos) throw std::invalid_argument("Require loopback IP:port");
    auto host = address.substr(0, split);
    int port = -1;
    const auto text = std::string_view(address).substr(split + 1);
    const auto parsed = std::from_chars(text.data(), text.data() + text.size(), port);
    if (parsed.ec != std::errc{} || parsed.ptr != text.data() + text.size() || port < 0 || port > 65535)
        throw std::invalid_argument("Invalid port");
    in_addr v4{}; in6_addr v6{};
    if (inet_pton(AF_INET, host.c_str(), &v4) == 1 && (ntohl(v4.s_addr) >> 24) == 127) return;
    if (host.size() >= 2 && host.front() == '[' && host.back() == ']') {
        host = host.substr(1, host.size() - 2);
        if (inet_pton(AF_INET6, host.c_str(), &v6) == 1 && IN6_IS_ADDR_LOOPBACK(&v6)) return;
    }
    throw std::invalid_argument("Only explicit loopback IPs are supported");
}

Status error(grpc::CallbackServerContext* ctx, RequestId id, StatusCode status,
             wire::ErrorCode code, const char* message) {
    wire::InferenceError detail;
    detail.set_request_id(id); detail.set_code(code); detail.set_message(message);
    ctx->AddTrailingMetadata("inferno-error-bin", detail.SerializeAsString());
    return Status(status, message);
}

void metrics(const RequestMetrics& source, wire::RequestMetrics* target) {
    target->set_output_tokens(source.output_tokens);
    target->set_runtime_latency_ms(source.runtime_latency_ms);
#define COPY_OPTIONAL(name) if (source.name) target->set_##name(*source.name)
    COPY_OPTIONAL(queue_wait_ms); COPY_OPTIONAL(backend_execution_ms);
    COPY_OPTIONAL(prefill_phase_ms); COPY_OPTIONAL(decode_phase_ms);
    COPY_OPTIONAL(ttft_ms); COPY_OPTIONAL(mean_inter_token_ms);
    COPY_OPTIONAL(decode_tokens_per_second);
#undef COPY_OPTIONAL
}

wire::MemorySamplePoint memory_point(MemorySamplePoint point) {
    switch (point) {
    case MemorySamplePoint::backend_ready: return wire::MEMORY_SAMPLE_POINT_BACKEND_READY;
    case MemorySamplePoint::request_start: return wire::MEMORY_SAMPLE_POINT_REQUEST_START;
    case MemorySamplePoint::prefill_complete: return wire::MEMORY_SAMPLE_POINT_PREFILL_COMPLETE;
    case MemorySamplePoint::request_complete_before_cleanup: return wire::MEMORY_SAMPLE_POINT_REQUEST_COMPLETE_BEFORE_CLEANUP;
    case MemorySamplePoint::request_complete_after_cleanup: return wire::MEMORY_SAMPLE_POINT_REQUEST_COMPLETE_AFTER_CLEANUP;
    }
    return wire::MEMORY_SAMPLE_POINT_UNSPECIFIED;
}

void memory(const MemorySnapshot& source, wire::MemorySnapshot* target) {
    for (const auto& value : source.process_samples) {
        auto* sample = target->add_process_samples();
        sample->set_point(memory_point(value.point));
        sample->set_status(value.status == MeasurementStatus::measured
            ? wire::MEASUREMENT_STATUS_MEASURED : wire::MEASUREMENT_STATUS_UNAVAILABLE);
        sample->set_monotonic_ns(value.monotonic_ns);
#define COPY_MEMORY_OPTIONAL(name) if (value.name) sample->set_##name(*value.name)
        COPY_MEMORY_OPTIONAL(resident_bytes);
        COPY_MEMORY_OPTIONAL(peak_resident_bytes);
        COPY_MEMORY_OPTIONAL(physical_footprint_bytes);
        COPY_MEMORY_OPTIONAL(peak_physical_footprint_bytes);
#undef COPY_MEMORY_OPTIONAL
    }
    if (source.model_tensor_bytes) target->set_model_tensor_bytes(*source.model_tensor_bytes);
    for (const auto& value : source.llama_buffers) {
        auto* row = target->add_llama_buffers();
        row->set_name(value.name);
        row->set_model_bytes(value.model_bytes);
        row->set_context_bytes(value.context_bytes);
        row->set_compute_bytes(value.compute_bytes);
    }
    if (source.llama_model_allocated_bytes) target->set_llama_model_allocated_bytes(*source.llama_model_allocated_bytes);
    if (source.llama_context_allocated_bytes) target->set_llama_context_allocated_bytes(*source.llama_context_allocated_bytes);
    if (source.llama_compute_allocated_bytes) target->set_llama_compute_allocated_bytes(*source.llama_compute_allocated_bytes);
    target->set_configured_context_tokens(source.configured_context_tokens);
    target->set_configured_batch_tokens(source.configured_batch_tokens);
    target->set_configured_sequences(source.configured_sequences);
    target->set_configured_gpu_layers(source.configured_gpu_layers);
}

class Adapter;
class Stream final : public grpc::ServerWriteReactor<wire::GenerateEvent> {
public:
    Stream(Adapter& owner, grpc::CallbackServerContext* context, RequestId id,
           RequestClock::time_point deadline)
        : owner_(owner), context_(context), id_(id), deadline_(deadline) {}
    void start(Submission submission);
    void emit(std::string_view piece);
    void reject(Status status) { writing_ = false; Finish(std::move(status)); }
    void OnWriteDone(bool ok) override;
    void OnCancel() override;
    void OnDone() override;
    void wake_cancel() { std::lock_guard lock(mutex_); cancel_tokens_ = true; changed_.notify_all(); }
    bool registered = false; // Set before returning the reactor to gRPC.
private:
    void stop_locked();
    void complete();
    Adapter& owner_;
    grpc::CallbackServerContext* context_;
    RequestId id_;
    RequestClock::time_point deadline_;
    std::mutex mutex_;
    std::condition_variable changed_;
    bool writing_ = true; // Reserve accepted before allowing native callbacks.
    bool stopped_ = false;
    bool deadline_stop_ = false;
    bool cancel_tokens_ = false;
    std::uint32_t count_ = 0;
    wire::GenerateEvent event_;
    std::shared_ptr<RequestControl> control_;
    std::future<GenerationResult> completion_;
    std::thread waiter_;
};

class Adapter final : public wire::InferenceService::CallbackService {
public:
    Adapter(GenerationRuntime& runtime, std::size_t limit, bool ready, std::string model)
        : runtime_(runtime), limit_(limit), ready_(ready), model_(std::move(model)) {
        if (limit == 0 || limit > 64 || model_.size() > 256) throw std::invalid_argument("Invalid RPC configuration");
    }
    grpc::ServerUnaryReactor* GetStatus(grpc::CallbackServerContext* context,
            const wire::GetStatusRequest*, wire::GetStatusResponse* response) override {
        std::lock_guard lock(mutex_);
        response->set_state(draining_ ? wire::BACKEND_STATE_DRAINING :
            (ready_ ? wire::BACKEND_STATE_READY : wire::BACKEND_STATE_NO_MODEL));
        if (!model_.empty()) response->set_model_id(model_);
        const auto snapshot = runtime_.scheduler_snapshot();
        auto* scheduler = response->mutable_scheduler();
        scheduler->set_waiting_requests(static_cast<std::uint32_t>(snapshot.waiting_requests));
        scheduler->set_executing_requests(static_cast<std::uint32_t>(snapshot.executing_requests));
        scheduler->set_queue_capacity(static_cast<std::uint32_t>(snapshot.queue_capacity));
        if (const auto memory_snapshot = runtime_.memory_snapshot()) {
            memory(*memory_snapshot, response->mutable_memory());
        }
        auto* reactor = context->DefaultReactor(); reactor->Finish(Status::OK); return reactor;
    }
    grpc::ServerUnaryReactor* Cancel(grpc::CallbackServerContext* context,
            const wire::CancelRequest* request, wire::CancelResponse* response) override {
        std::lock_guard lock(mutex_);
        auto* reactor = context->DefaultReactor();
        if (!request->request_id()) {
            reactor->Finish(error(context, 0, StatusCode::INVALID_ARGUMENT, wire::ERROR_CODE_INVALID_REQUEST, "Invalid request ID"));
        } else if (const auto entry = active_.find(request->request_id()); entry == active_.end()) {
            reactor->Finish(error(context, request->request_id(), StatusCode::NOT_FOUND, wire::ERROR_CODE_UNKNOWN_REQUEST_ID, "Unknown request ID"));
        } else {
            response->set_request_id(request->request_id());
            switch (entry->second.control->request_cancel()) {
            case RequestControl::CancelStatus::requested:
                entry->second.stream->wake_cancel();
                response->set_disposition(wire::CANCEL_DISPOSITION_REQUESTED); break;
            case RequestControl::CancelStatus::already_requested: response->set_disposition(wire::CANCEL_DISPOSITION_ALREADY_REQUESTED); break;
            case RequestControl::CancelStatus::finished: response->set_disposition(wire::CANCEL_DISPOSITION_FINISHED); break;
            }
            reactor->Finish(Status::OK);
        }
        return reactor;
    }
    grpc::ServerWriteReactor<wire::GenerateEvent>* Generate(grpc::CallbackServerContext* context,
            const wire::GenerateRequest* request) override {
        const auto wall_now = std::chrono::system_clock::now();
        const auto remaining = context->deadline() - wall_now;
        using WallDuration = std::chrono::system_clock::duration;
        const auto deadline = RequestClock::now() + std::chrono::duration_cast<RequestClock::duration>(
            std::clamp(remaining, WallDuration::zero(), std::chrono::duration_cast<WallDuration>(std::chrono::hours(1))));
        auto stream = std::make_unique<Stream>(*this, context, request->request_id(), deadline);
        auto reject = [&](StatusCode status, wire::ErrorCode code, const char* message) {
            stream->reject(error(context, request->request_id(), status, code, message)); return stream.release();
        };
        if (context->deadline() == std::chrono::system_clock::time_point::max() || remaining > std::chrono::hours(1))
            return reject(StatusCode::INVALID_ARGUMENT, wire::ERROR_CODE_INVALID_REQUEST, "Require deadline at most one hour");
        if (remaining <= decltype(remaining)::zero())
            return reject(StatusCode::DEADLINE_EXCEEDED, wire::ERROR_CODE_DEADLINE_EXCEEDED, "Request deadline exceeded");
        if (!request->request_id() || !request->max_output_tokens() || request->max_output_tokens() > 512)
            return reject(StatusCode::INVALID_ARGUMENT, wire::ERROR_CODE_INVALID_REQUEST, "Invalid generation request");
        try { validate_user_prompt(request->prompt()); }
        catch (const std::invalid_argument&) { return reject(StatusCode::INVALID_ARGUMENT, wire::ERROR_CODE_INVALID_REQUEST, "Invalid prompt"); }
        std::lock_guard lock(mutex_);
        if (draining_ || !ready_) return reject(StatusCode::UNAVAILABLE, wire::ERROR_CODE_BACKEND_UNAVAILABLE, "Backend unavailable");
        if (active_.contains(request->request_id())) return reject(StatusCode::ALREADY_EXISTS, wire::ERROR_CODE_DUPLICATE_REQUEST_ID, "Duplicate request ID");
        if (active_.size() >= limit_) return reject(StatusCode::RESOURCE_EXHAUSTED, wire::ERROR_CODE_QUEUE_FULL, "RPC admission full");
        const auto [entry, inserted] = active_.emplace(request->request_id(), Entry{});
        (void) inserted;
        Submission submission;
        try {
            submission = runtime_.submit({request->request_id(), request->prompt(), request->max_output_tokens(), deadline},
                [reactor = stream.get()](std::string_view piece) { reactor->emit(piece); });
        } catch (...) {
            active_.erase(entry);
            return reject(StatusCode::INTERNAL, wire::ERROR_CODE_BACKEND_FAILURE, "Admission failed");
        }
        if (submission.status != SubmitStatus::accepted) {
            active_.erase(entry);
            switch (submission.status) {
            case SubmitStatus::full: return reject(StatusCode::RESOURCE_EXHAUSTED, wire::ERROR_CODE_QUEUE_FULL, "Request queue full");
            case SubmitStatus::closed: return reject(StatusCode::UNAVAILABLE, wire::ERROR_CODE_BACKEND_UNAVAILABLE, "Runtime closed");
            case SubmitStatus::expired: return reject(StatusCode::DEADLINE_EXCEEDED, wire::ERROR_CODE_DEADLINE_EXCEEDED, "Request deadline exceeded");
            default: return reject(StatusCode::INVALID_ARGUMENT, wire::ERROR_CODE_INVALID_REQUEST, "Invalid request");
            }
        }
        entry->second = {submission.control, stream.get()};
        stream->registered = true;
        stream->start(std::move(submission));
        return stream.release();
    }
    void remove(RequestId id) { std::lock_guard lock(mutex_); active_.erase(id); idle_.notify_all(); }
    void drain() {
        std::lock_guard lock(mutex_); draining_ = true;
        for (const auto& [id, entry] : active_) {
            (void) id; (void) entry.control->cancel(); entry.stream->wake_cancel();
        }
    }
    void wait_idle() { std::unique_lock lock(mutex_); idle_.wait(lock, [&] { return active_.empty(); }); }
private:
    GenerationRuntime& runtime_;
    std::size_t limit_;
    bool ready_, draining_ = false;
    std::string model_;
    std::mutex mutex_;
    std::condition_variable idle_;
    struct Entry { std::shared_ptr<RequestControl> control; Stream* stream = nullptr; };
    std::unordered_map<RequestId, Entry> active_;
};

void Stream::stop_locked() {
    if (!stopped_) deadline_stop_ = RequestClock::now() >= deadline_ ||
        std::chrono::system_clock::now() >= context_->deadline();
    stopped_ = true;
    // A transport deadline keeps its native deadline cause; explicit cancellation
    // before that deadline records cancellation intent.
    if (control_ && !deadline_stop_) (void) control_->cancel();
    changed_.notify_all();
}
void Stream::OnCancel() { std::lock_guard lock(mutex_); stop_locked(); }
void Stream::OnWriteDone(bool ok) {
    std::lock_guard lock(mutex_); writing_ = false;
    if (!ok) stop_locked();
    changed_.notify_all();
}
void Stream::start(Submission submission) {
    control_ = std::move(submission.control); completion_ = std::move(submission.completion);
    try { waiter_ = std::thread([this] { complete(); }); }
    catch (...) {
        { std::lock_guard lock(mutex_); writing_ = false; stop_locked(); }
        // Retain reactor until its native callback is gone, even if publication failed.
        try { (void) completion_.get(); } catch (...) {}
        reject(error(context_, id_, StatusCode::RESOURCE_EXHAUSTED, wire::ERROR_CODE_QUEUE_FULL, "RPC worker unavailable"));
        return;
    }
    std::lock_guard lock(mutex_);
    event_.set_request_id(id_); event_.mutable_accepted(); StartWrite(&event_);
}
void Stream::emit(std::string_view piece) {
    std::unique_lock lock(mutex_);
    changed_.wait(lock, [&] { return !writing_ || stopped_ || cancel_tokens_; });
    if (stopped_ || cancel_tokens_) {
        control_->checkpoint();
        if (deadline_stop_) throw RequestStopped(RequestStopped::Reason::timed_out);
        throw RequestStopped(RequestStopped::Reason::cancelled);
    }
    if (piece.size() > piece_limit) throw std::length_error("Token piece exceeds transport bound");
    event_.Clear(); event_.set_request_id(id_);
    if (!piece.empty()) event_.mutable_token()->set_piece(piece.data(), piece.size());
    event_.mutable_token()->set_output_tokens(++count_);
    writing_ = true; StartWrite(&event_);
    changed_.wait(lock, [&] { return !writing_ || stopped_ || cancel_tokens_; });
    if (stopped_ || cancel_tokens_) {
        control_->checkpoint();
        if (deadline_stop_) throw RequestStopped(RequestStopped::Reason::timed_out);
        throw RequestStopped(RequestStopped::Reason::cancelled);
    }
}
void Stream::complete() {
    try {
        auto result = completion_.get();
        std::unique_lock lock(mutex_);
        changed_.wait(lock, [&] { return !writing_; });
        if (stopped_) {
            Finish(Status(deadline_stop_ ? StatusCode::DEADLINE_EXCEEDED : StatusCode::CANCELLED, "RPC stopped"));
            return;
        }
        event_.Clear(); event_.set_request_id(id_);
        auto* finished = event_.mutable_finished();
        auto code = wire::ERROR_CODE_BACKEND_FAILURE;
        switch (result.state) {
        case RequestState::completed: finished->set_state(wire::TERMINAL_STATE_COMPLETED); break;
        case RequestState::cancelled: finished->set_state(wire::TERMINAL_STATE_CANCELLED); code = wire::ERROR_CODE_CANCELLED; break;
        case RequestState::timed_out: finished->set_state(wire::TERMINAL_STATE_TIMED_OUT); code = wire::ERROR_CODE_DEADLINE_EXCEEDED; break;
        default: finished->set_state(wire::TERMINAL_STATE_FAILED); break;
        }
        metrics(result.metrics, finished->mutable_metrics());
        if (result.state != RequestState::completed) {
            auto* detail = finished->mutable_error(); detail->set_request_id(id_); detail->set_code(code);
            detail->set_message(result.state == RequestState::cancelled ? "Request cancelled" :
                (result.state == RequestState::timed_out ? "Request deadline exceeded" : "Backend request failed"));
        }
        StartWriteAndFinish(&event_, grpc::WriteOptions(), Status::OK);
    } catch (...) {
        std::unique_lock lock(mutex_); changed_.wait(lock, [&] { return !writing_; });
        Finish(error(context_, id_, StatusCode::INTERNAL, wire::ERROR_CODE_BACKEND_FAILURE, "RPC completion failed"));
    }
}
void Stream::OnDone() {
    if (waiter_.joinable()) waiter_.join();
    if (registered) owner_.remove(id_);
    delete this;
}
} // namespace

struct RpcServer::Impl {
    Adapter adapter;
    std::unique_ptr<grpc::Server> server;
    std::mutex shutdown_mutex;
    int selected_port = 0;
    Impl(GenerationRuntime& runtime, const std::string& address, std::size_t limit, bool ready, std::string model)
        : adapter(runtime, limit, ready, std::move(model)) {
        check_loopback_address(address);
        grpc::ServerBuilder builder;
        builder.SetMaxReceiveMessageSize(message_limit); builder.SetMaxSendMessageSize(message_limit);
        builder.AddChannelArgument(GRPC_ARG_MAX_CONCURRENT_STREAMS, 32);
        builder.AddChannelArgument(GRPC_ARG_MAX_METADATA_SIZE, 8 * 1024);
        grpc::ResourceQuota quota; quota.Resize(8 * 1024 * 1024).SetMaxThreads(16);
        builder.SetResourceQuota(quota);
        builder.RegisterService(&adapter);
        builder.AddListeningPort(address, grpc::InsecureServerCredentials(), &selected_port);
        server = builder.BuildAndStart();
        if (!server || !selected_port) throw std::runtime_error("Unable to bind local RPC server");
    }
};
RpcServer::RpcServer(GenerationRuntime& runtime, std::string address, std::size_t limit, bool ready, std::string model)
    : impl_(std::make_unique<Impl>(runtime, address, limit, ready, std::move(model))) {}
RpcServer::~RpcServer() { shutdown(); }
int RpcServer::port() const { return impl_->selected_port; }
void RpcServer::validate_address(const std::string& address) { check_loopback_address(address); }
void RpcServer::shutdown() {
    std::lock_guard lock(impl_->shutdown_mutex);
    if (!impl_->server) return;
    impl_->adapter.drain();
    impl_->server->Shutdown(std::chrono::system_clock::now() + std::chrono::seconds(5));
    impl_->server->Wait(); impl_->adapter.wait_idle(); impl_->server.reset();
}
} // namespace inferno
