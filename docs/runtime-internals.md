# Initial runtime observations

Inspected revision: `2a667032dc788c3a071d538e0b572e45a3f12731`.
Paths below are relative to `third_party/llama.cpp`; no upstream code was changed.

1. **Load:** `tools/completion/completion.cpp` calls `common_init_from_params`.
   `common/common.cpp` loads model metadata/tensors with `llama_model_load_from_file`
   and constructs the execution context with `llama_init_from_model`.
2. **Format and tokenize:** completion formats chat messages when a template is
   available, then calls `common_tokenize`. Our cached model lacks that template;
   the smoke script supplies an already-formatted prompt in non-conversation mode.
3. **Prefill:** the prompt is evaluated in batches, filling attention and recurrent
   state. The execution API name `llama_decode` covers prompt evaluation too; its
   name does not mean every call is a single generated token.
4. **Generate:** completion samples with `common_sampler_sample`, accepts the token
   into sampler history, converts it to text with `common_token_to_piece`, and
   evaluates new tokens until the limit or an end token is reached.
5. **Hybrid memory:** `src/models/nemotron-h.cpp` builds a hybrid graph and selects
   Mamba-2, attention, or feed-forward work per layer. Attention and recurrent state
   are distinct; this run allocated 64.00 MiB KV and 81.09 MiB recurrent buffers.
6. **Metal:** `ggml/src/ggml-metal/CMakeLists.txt` embeds shader source when
   `GGML_METAL_EMBED_LIBRARY=ON`. Runtime Metal compilation uses Apple's existing
   framework; no standalone shader compiler or driver installation was needed.
   `ggml-metal-ops.cpp` includes SSM convolution and scan operation dispatch.

The successful run demonstrates model loading, prompt tokenization, generation,
and GPU offload for one workload. It does not validate all kernels or establish
accuracy parity with CPU/BF16.

Interview topics from this phase: model weights versus execution context; token
formatting versus tokenization; prefill versus autoregressive decode; shared memory
versus process RSS; hybrid recurrent state versus attention KV cache; and why a
successful build is weaker evidence than a verified runtime execution.
