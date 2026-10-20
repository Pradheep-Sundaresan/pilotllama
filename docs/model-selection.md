# Initial model selection

The verified reference path used a user-supplied NVIDIA Nemotron 4B Q4_K_M GGUF.
PilotLlama never downloads, copies or redistributes model weights.

## Official model

- Repository: [nvidia/NVIDIA-Nemotron-3-Nano-4B-BF16](https://huggingface.co/nvidia/NVIDIA-Nemotron-3-Nano-4B-BF16).
- Model card names the NVIDIA Nemotron Open Model License as governing terms.
- Architecture: Nemotron-H hybrid, combining Mamba-2, MLP, and four attention layers.
- Official configuration and local metadata identify approximately 3.97B parameters,
  42 layers, and a maximum context of 262144 tokens. Start with 4096, not the maximum.

## Verified reference artifact

- Format: GGUF v3; architecture: nemotron_h; quantization: Q4_K_M.
- About 3.97B parameters and 263 tensors.
- The original NVIDIA and conversion revisions were unknown; no provenance is
  invented beyond the locally inspected file.

Pass an absolute path to your own existing model. Reference it read-only and never
commit weights. Being readable GGUF with matching metadata is not an inference test.

## Alternatives and compatibility evidence

[Unsloth's GGUF repository](https://huggingface.co/unsloth/NVIDIA-Nemotron-3-Nano-4B-GGUF/tree/main)
offers `NVIDIA-Nemotron-3-Nano-4B-Q4_K_M.gguf` (about 2.90 GB) and Q8_0 (about
4.23 GB). These are third-party quantizations, not the identical cached artifact.
Any download needs approval and a pinned revision plus checksum.

Upstream has a [Nemotron-H model implementation](https://github.com/ggml-org/llama.cpp/blob/master/src/models/nemotron-h.cpp)
and [Metal SSM operations](https://github.com/ggml-org/llama.cpp/blob/master/ggml/src/ggml-metal/ggml-metal-ops.cpp).
These moving-source references establish implementation evidence, not verified
compatibility of a pinned build with this model on this Mac. Phase 0C must confirm
model loading, tokenization, coherent generation, and GPU execution from logs.

If that test fails for a model-specific reason, the fallback candidate is official
[Qwen/Qwen3-4B-GGUF](https://huggingface.co/Qwen/Qwen3-4B-GGUF), Q4_K_M, whose model
page documents llama.cpp use. Verify its artifact revision before requesting a
download; do not silently substitute it.

## Memory plan

For one request at a 4096-token context, reserve roughly 4–6 GiB as a provisional
planning allowance, not a measurement or upper bound. Weights are only part of
usage: attention cache, recurrent state, compute buffers, and backend allocations
also contribute. Measure actual peak/steady memory and memory pressure during the
smoke test. Do not extrapolate all-attention cache formulas to every hybrid layer.

## Phase 0C result

The pinned llama.cpp build successfully ran this artifact on Metal with all 43/43
offloadable layers reported on GPU. Peak process RSS was 3,151,659,008 bytes (2.94
GiB) for the short 4096-context smoke test; this does not establish a bound for
other workloads. The GGUF has no chat template, so the smoke script supplies the
single-user, no-tools, thinking-disabled prompt structure observed in NVIDIA's
[tokenizer configuration](https://huggingface.co/nvidia/NVIDIA-Nemotron-3-Nano-4B-BF16/blob/main/tokenizer_config.json).
The exact smoke input is preserved in the script. Original weight/conversion
revision provenance remains unknown.
