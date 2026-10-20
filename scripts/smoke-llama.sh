#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
if [ "$#" -ne 1 ]; then
    echo "Usage: $0 /absolute/path/to/existing-model.gguf" >&2
    exit 2
fi
model=$1
if [ ! -f "$model" ]; then
    echo "Cached model missing. Supply a local GGUF path; this script never downloads weights." >&2
    exit 1
fi
# Single-user, no-tools, thinking-disabled form of NVIDIA's chat template.
# The cached Ollama GGUF does not embed a chat template.
prompt='<|im_start|>system
<|im_end|>
<|im_start|>user
Explain what a CPU does in one short sentence.<|im_end|>
<|im_start|>assistant
<think></think>'
exec /usr/bin/time -l ./build/llama-metal/bin/llama-completion \
    -m "$model" --offline --fit off -ngl 99 -c 4096 -b 128 -ub 128 -t 4 \
    -n 64 --seed 42 --temp 0 --no-conversation --simple-io \
    --no-display-prompt --log-verbosity 4 -p "$prompt"
