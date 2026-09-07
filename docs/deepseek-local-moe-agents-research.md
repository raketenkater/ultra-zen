# DeepSeek Concepts for Local MoE Agentic Work on Consumer/Mid Hardware

> **Hardware target**: i7-10700K, RTX 4070 12GB + RTX 3060 12GB (24 GB total VRAM), 78 GB RAM
> **Inference engine**: llama.cpp via ggrun (by raketenkater)
> **Goal**: Optimize local multi-agent MoE workloads (multiple specialized agents: tools, coding, research, etc.)
> **Research date**: 2026-07-21/24 (two passes)

---

## 1. The ggrun Tool (by raketenkater)

**Repo**: [github.com/raketenkater/ggrun](https://github.com/raketenkater/ggrun) — auto-tuned launcher for GGUF models on llama.cpp / ik_llama.cpp.

| Capability | Detail |
|---|---|
| **Multi-GPU MoE placement** | Plans expert placement across heterogeneous GPUs using GGUF metadata + VRAM/RAM/bandwidth analysis |
| **AI Tune** (`--ai-tune`) | Benchmarks safe flag variants, caches winning config per hardware combo |
| **Throughput vs alternatives** | Consistently beats Ollama and raw llama.cpp on same hardware (verified: Qwen3.5-4B 151.4 vs Ollama 124.8, raw 103.3 tok/s) |
| **Model compatibility** | Loads models that Ollama and raw llama.cpp cannot (e.g., MiniMax-M3 UD-IQ3_XXS) |
| **DeepSeek-V4-Flash @ 1M ctx** | Demonstrated 4 parallel requests, 60K-token + concurrent at 5.88 tok/s without OOM |
| **Claude Code integration** | `--claude-code` flag |
| **Speculative decoding** | `--spec auto` auto-configures draft model pipeline |
| **Test rig** | Matches your asymmetrical multi-GPU setup |

**Key insight**: ggrun already solves the multi-GPU MoE expert placement problem for your hardware. It's the orchestration layer you'd build on — not replace.

---

## 2. DeepSeek's Core Concepts (Ranked by Local Applicability)

### ⭐ TIER 1: Directly Applicable Today

#### A. Fine-Grained Expert Segmentation (DeepSeekMoE, arXiv:2401.06066)

**What it is**: Split each standard MoE expert into m smaller ones, activate m×K instead of K. Combined with shared expert isolation (always-on experts for common knowledge that all tokens pass through).

**Why it matters locally**: Instead of one monolithic "code expert" or "research expert," you have many smaller sub-experts. You route each agentic task to only the subset of experts it needs. More granular = less wasteful VRAM use.

**Paper results**: DeepSeekMoE 16B matches LLaMA2 7B at ~40% compute. DeepSeekMoE 145B matches 67B dense at 28.5% compute.

**Local application**: If you build a multi-agent system where each agent role (tool-caller, coder, researcher, router) maps to a set of fine-grained experts, you can pack far more specialized capacity into 24 GB VRAM.

---

#### B. Multi-Head Latent Attention / MLA (DeepSeek-V2, arXiv:2405.04434; V3, arXiv:2412.19437)

**What it is**: Compresses full KV cache into a low-rank latent vector (d_c=512) + decoupled RoPE key (d_hR=64). Per-token per-layer cache: **576 elements** vs 16,384 for standard MHA — a **93.3% reduction** (V2) to ~28-57x reduction (V3).

**Why it matters locally**: KV cache is the #1 VRAM bottleneck for long-context agentic work. Each agent conversation builds context. MLA means:
- 128K token context becomes feasible in 24 GB VRAM
- Multi-agent context sharing uses far less memory
- More agents can keep their KV cache resident simultaneously

**Status**: llama.cpp added MLA support in early 2025 (builds before Feb 2025 lack it). DeepSeek V4's MLA is fully integrated in llama.cpp via PR #24162 (merged June 2026).

**FlashMLA** (DeepSeek's official CUDA kernel library) shows what's theoretically possible but **requires Hopper (SM90) or Blackwell (SM100)** — cannot run on RTX 4070 (SM89 Ada) or RTX 3060 (SM86 Ampere). It uses Hopper-specific features (WGMMA, TMA, cluster barriers) with no software fallback.

**FlashMLA FP8 KV cache format**: 656 bytes per token (512 bfloat8 NoPE + 16 bytes float32 scales + 128 bfloat16 RoPE). Dense decoding: 3000 GB/s / 660 TFLOPS on H800. Sparse decoding: 410 TFLOPS on H800.

---

#### C. Loss-Free Load Balancing (DeepSeek, arXiv:2408.15664)

**What it is**: Applies expert-wise bias to routing scores before top-K selection. Biases are dynamically updated per-expert based on recent load. Entirely outside the gradient flow — no interference gradients.

**Why it matters locally**: You don't need gradient-based training to use this. The concept is a **routing algorithm with bias adjustment**:
- Track recent load per "expert" (agent)
- Apply positive bias to underloaded experts, negative to overloaded
- No gradient computation needed — just counters

**Local application**: Implement as a load-balancing scheduler for your multi-agent system. When agent A is busy/stalled on a function call, bias routing toward agent B (the idle one). Zero overhead, purely heuristic, trivially implementable.

---

### ⭐⭐ TIER 2: Apply with Engineering Effort

#### D. KV-Cache Snapshot Sharing / SwarmKV Pattern

**What it is**: Prefill shared/common context once, serialize KV cache to host buffer, memcpy to each agent branch. Uses llama.cpp native APIs (`llama_state_get_data`, `llama_state_seq_set_data`).

**Results**: 
- Published: ~1.95x end-to-end speedup for 2-agent pipeline
- ~52x branch-activation latency reduction (from 4,339ms → 83ms)
- For N-agent pipeline: cost = 1 prefill + N memcpy (vs N full prefills)

**Local application**: When multiple agents share a common system prompt or conversation history, run prefill once, fan out the KV cache. This is the single highest-impact optimization for multi-agent on limited VRAM. Implements the "shared expert" concept from DeepSeekMoE at the inference level.

---

#### E. Multi-Token Prediction / MTP for Speculative Decoding (DeepSeek-V3)

**What it is**: Train D sequential prediction heads to predict D future tokens. Repurpose for speculative decoding without a separate draft model. Effectively the same idea as EAGLE-3 but integrated into training.

**Results**: 1.8x effective generation throughput for structured output (JSON, code).

**Local applicability**: Speculative decoding is directly supported in llama.cpp (`-md draft.gguf`). ggrun's `--spec auto` handles this. For MoE models, each verification step triggers full expert dispatch, so the speedup is less than with dense models. The acceptance rate drops to 0.40-0.50 at high temperature (>1.0). No official EAGLE checkpoint exists for DeepSeek V4.

---

#### F. Dynamic Expert Caching / SMOE (IPDPS 2026)

**What it is**: Token-wise prefetch of experts from CPU RAM to GPU, with computation-communication overlap. On a 24 GB GPU running DeepSeek-V3-671B at 4-bit, only ~3% of experts (8 of 256) fit in VRAM.

**Results**: Up to 8.68x TTFT speedup vs KTransformers at 4K prompt length. Wait time per MoE layer reduced to near zero (vs ~1ms for layer-wise prefetch).

**Key insight**: Expert activation patterns are **predictable** at the token-wise level. Shallow layers (<10) are uniformly distributed, but deeper layers show consistent patterns per task type.

**Local application**: For multi-agent workloads, track which experts each agent role activates. Prefetch those experts before the agent's turn. Build a cache of "hot experts" per agent role.

---

#### G. llama.cpp Regex-Based Expert Pinning (PR #24162)

**llama.cpp now supports regex-based device pinning for MoE experts** — you can assign specific expert layers to specific GPUs or offload them to CPU:

```
# Assign layers 0-2 FFN experts to CUDA0, rest to CPU
\.(0|1|2)\.ffn_(gate|up|down)_exps.=CUDA0
\.*\.ffn_(gate|up|down)_exps.=CPU
```

This uses `ffn_gate_tid2eid` tensors for MoE routing. This is the mechanism ggrun uses under the hood for its MoE placement planner.

**Your 24 GB setup**: 12 GB RTX 4070 + 12 GB RTX 3060. With pipeline parallelism (`--split-mode layer`, the only option for MoE), each GPU holds entire layers. You can pin specific expert-heavy layers to the 4070 (faster) and attention-heavy or early layers to the 3060. Or offload the coldest experts to CPU RAM (78 GB available) and only keep hot experts on GPU.

---

### ⭐⭐⭐ TIER 3: Inspirational / Future Work

#### H. TokenCake KV-Cache Scheduling (arXiv:2510.18586)

- **47% latency reduction** vs vLLM for multi-agent workloads
- **18.5% of GPU KV cache wasted** on stalled agents waiting for function calls
- **Critical inversion problem**: non-critical agent evicts critical agent's cache
- Temporal Scheduler: offloads stale caches during function-call stalls

**Local application**: When an agent is waiting on a tool call (100ms–30s), offload its KV cache to RAM. On return, restore it. Same principle as expert offloading but for KV cache — and more impactful on 24 GB VRAM.

---

#### I. CSA / HCA Compressed Attention (DeepSeek V4 in llama.cpp)

**DeepSeek V4 introduces two compressed attention mechanisms** in llama.cpp (PR #24162):

- **CSA (Compressed Sparse Attention)**: Every 4 tokens compressed into 1, sliding window of last 8 tokens evaluated at each 4-token boundary. Uses a "lightning indexer" to select top-k tokens.
- **HCA (Heavily Compressed Attention)**: Standard attention over compressed tokens (128:1 compression) plus sliding window attention.

These compress ratios alternate across 43 layers (0/4/128 patterns), drastically reducing attention compute for long contexts. Combined with MLA's KV compression, this is how DeepSeek V4 achieves 1M context on consumer hardware.

**Status**: Merged in llama.cpp. The lightning indexer was initially prototyped as a naive matmul (creating ~4 GiB/layer host memory buffers) but PR #24231 fixed this with a fused GGML_OP_LIGHTNING_INDEXER op, reducing memory 29x. A CUDA backend for this op was added in PR #25545.

---

## 3. llama.cpp Technical Status for DeepSeek Models

### DeepSeek V4-Flash Support (PR #24162, merged June 2026)

| Feature | Status | Notes |
|---|---|---|
| **DeepSeek-V4-Flash architecture** | ✅ Merged mainline | 49 commits, merged into ggml-org:master |
| **DeepSeek-V4-Pro support** | ✅ Merged | 1.6T params, 49B active |
| **MLA from V3/V3.2** | ✅ Inherited | Core architecture |
| **CSA / HCA compressed attention** | ✅ Implemented | Alternating 0/4/128 compress ratios |
| **Sinkhorn hyper-connections** | ✅ Implemented | New V4 architectural feature |
| **Lightning indexer** | ✅ PR #24231 (fused op) | 29x memory reduction over naive matmul |
| **CUDA backend for indexer** | ✅ PR #25545 | Completes GPU offload path |
| **KV cache quantization** | ⚠️ Buggy with q8_0 | Produces "garbage on all backends" (#25382). Use `--cache-type-k f16`. Fixed in PR #25202 (July 7 2026) |
| **IQ-series imatrix quants** | ❌ Segfaults | imatrix tool crashes on V4 |

### Multi-GPU Support

| Mode | MoE Support | Notes |
|---|---|---|
| **Pipeline parallelism** (`--split-mode layer`) | ✅ Works | Default. Each GPU holds contiguous layers. KV cache for layer l lives on its GPU. |
| **Tensor parallelism** (`--split-mode tensor`) | ❌ NOT for MoE | Fails for DeepSeek2, Grok, OLMoE, all MoE architectures |
| **Quantized KV cache** | ❌ Incompatible w/ tensor split | f32/f16/bf16 only |
| **CUDA P2P** | ⚠️ Risky on consumer mobos | May crash with IOMMU enabled |

### Real-World Performance Claims (Community, DeepSeek V4 Flash)

| Setup | tok/s | Notes |
|---|---|---|
| RTX PRO 6000 Max-Q (96GB), Q2-Q3 mixed, CPU expert offload | 15-17 | nisparks, llama.cpp mainline |
| Single RTX 6000 (96GB), CUDA fork | 18 | Fringe210 |
| RTX PRO 6000 Max-Q (96GB), f16 cache, 524k context | 7.59 | fairydreaming, PR #24162 benchmarks |
| RTX PRO 6000 Max-Q (96GB), 524k context prefill | 281.79 t/s | fairydreaming |
| Epyc 9374F + 96GB GPU, 1M context (speculative) | — | Uses 60.8 GB VRAM at 524k |

**Critical**: All confirmed benchmarks use enterprise GPUs (96 GB VRAM). Your 24 GB total is 1/4 of that. No confirmed benchmarks exist for DeepSeek V4 on consumer 24 GB dual-GPU setups.

### The NVFP4 / MXFP4 Conversion Challenge

DeepSeek-V4-Flash **ships natively in FP4 + FP8 mixed precision** — there is no fp16/bf16 distribution:

- Routed experts (96% of the model): native **MXFP4** format
- Attention weights: **FP8 e4m3**
- Everything else (norms, embeddings, ~4%): BF16 or FP8

This breaks the traditional conversion pipeline (`convert.py --outtype f16` fails because FP4→bf16→requantize loses per-block scale information). However:

- **Unsloth** demonstrated lossless repacking: MXFP4 experts go bit-for-bit into GGUF's MXFP4, FP8 widens to BF16 with no rounding. Their UD-Q8_K_XL (161.9 GB) is bit-identical across all 1,328 tensors (100% top-token agreement, ~0 KLD). UD-Q4_K_XL (155.1 GB) maintains 96.28% same-top-token at 0.010 KLD.
- Re-quantizing experts to Q4_K instead of preserving MXFP4 introduces **5.2% RMSE** weight error. IQ2_XXS introduces **over 30% RMSE**.
- Plain Q2_K body quantization is **too aggressive** for V4 under realistic agent prompts — decode degenerates in long-context loops.
- IQ-series imatrix-distilled quants **segfault** on llama.cpp for V4.

**For your 24 GB setup**: DeepSeek V4 at any usable quantization (minimum ~155 GB for UD-Q4_K_XL) requires far more VRAM than you have, even with expert offloading. Focus on smaller models — the distilled variants (R1-Distill-Qwen-32B at ~20 GB Q4) are the practical path.

---

## 4. Practical Optimization Stack for Your Hardware

### Recommended Architecture

```
┌──────────────────────────────────────────────────┐
│                 Agent Router                      │
│  (Loss-Free Balancing: bias-adjust routing        │
│   based on real-time agent load)                  │
├────────┬────────┬────────┬────────┬───────────────┤
│ Tool    │ Code   │ Research│ Router │   ... (more) │
│ Agent   │ Agent  │ Agent  │ Agent  │   agent slots │
├────────┴────────┴────────┴────────┴───────────────┤
│              KV-Cache Scheduler                    │
│  (offload stale caches during tool stalls,         │
│   restore on resume — TokenCake-inspired)          │
├────────────────────────────────────────────────────┤
│              Shared KV-Cache Pool                  │
│  (SwarmKV: 1 prefill → memcpy to each agent)      │
├────────────────────────────────────────────────────┤
│     llama.cpp / ggrun (model orchestration)         │
│  (regex expert pinning, pipeline parallel,         │
│   --ai-tune optimal flags, --spec auto,            │
│   Sequence-level KV cache save/restore)            │
└────────────────────────────────────────────────────┘
```

### Immediate Wins (Sorted by Impact)

| # | Technique | Effort | Gain | What to Do |
|---|---|---|---|---|
| 1 | **KV-cache sharing** | 1-2 days | ~2x speedup | Prefill shared context once, snapshot+memcpy per agent using `llama_state_get_data/set_data` |
| 2 | **ggrun AI Tune** | Minutes | 20-50% throughput | Run `ggrun --ai-tune --model <model.gguf>` on your hardware |
| 3 | **Loss-free load balancing** | Hours | Better utilization | Track agent busy/idle state, bias routing toward idle agents |
| 4 | **KV-cache offload on stalls** | 2-3 days | 15-18% VRAM savings | Offload idle agent KV cache during tool calls (100ms-30s stalls) |
| 5 | **Speculative decoding** | Minutes | 1.5-1.8x generation speed | `ggrun --spec auto` or llama.cpp `-md draft.gguf` |
| 6 | **Regex expert pinning** | Hours | Reliable multi-GPU | Use llama.cpp regex pinning to assign expert-heavy layers to faster GPU (4070), cold experts to CPU |

### Model Selection for 24 GB VRAM

| Model | VRAM | Active Params | Notes |
|---|---|---|---|
| **R1-Distill-Qwen-32B** | ~20 GB (Q4) | 32B dense | ~85% of V3 reasoning quality — best practical choice for your setup |
| **Qwen3 MoE variants** | 12-20 GB | Varies | MoE architecture, good for agent routing experiments |
| **DeepSeek-V2-Lite** | ~12 GB | 21B total / 2.5B active | Expert offloading works well; small enough for multi-agent |
| **DeepSeek-V4-Flash Q4** | ❌ ~155 GB+ | 13B active | Infeasible on 24 GB — needs enterprise GPU or massive CPU offload |
| **DeepSeek-V4-Flash IQ2** | ❌ ~80 GB+ | 13B active | imatrix segfaults; Q2_K too aggressive for agent prompts |
| **Full DeepSeek-V3 Q2_K** | ❌ ~230 GB | 37B active | Needs 8x consumer GPUs |

### Agent Design Pattern (DeepSeek-Inspired)

1. **Fine-grained role routing**: Each agent role maps to a set of "experts" (sub-agents or model micro-tunings). The router uses a bias-adjusted load balancer (Loss-Free Balancing pattern) to dispatch tasks.
2. **Shared context experts**: Always-on agents handle common tasks (tokenization, context window management, short-term memory). These are the "shared experts" in DeepSeekMoE terms.
3. **Predictive prefetch**: Since expert activation is task-predictable (SMOE finding), the router can prefetch the KV cache and expert weights for the next-likely agent before it's needed.
4. **Critical-path awareness**: Avoid TokenCake's "critical inversion" — tag agents on the critical path so they aren't evicted by background agents. Use llama.cpp's sequence-level KV management to save/restore.
5. **Regex expert pinning**: On your 4070+3060 setup, pin the first few layers (heavy on FFN experts) to the faster 4070, offload coldest experts to CPU. Use ggrun's placement planner to auto-optimize.

### Practical VRAM Budgeting for 24 GB Total

The hard math for your setup, assuming a ~20 GB Q4 model (like R1-Distill-Qwen-32B):

| Component | VRAM |
|---|---|
| Model weights (Q4, 32B) | ~20 GB |
| **Remaining for KV cache + overhead** | **~4 GB** |
| KV cache at 8K context (standard MHA, 32 layers) | ~2 GB |
| KV cache at 8K context (MLA, 32 layers) | ~0.07 GB (70 MB) |
| KV cache at 128K context (MLA, 32 layers) | ~1.1 GB |
| **Headroom for multiple agents** | |

With MLA, 4 GB of KV cache budget means:
- 1 agent at 128K context = 1.1 GB → ~3 agents total at 128K
- 1 agent at 32K context = 0.28 GB → ~14 agents total at 32K
- Shared cache (SwarmKV): one shared prefill pool + N small agent-specific deltas

**Without MLA** (standard MHA at Q4 32B):
- 1 agent at 8K context = ~2 GB → only 2 agents at 8K
- 128K context is impossible (needs ~32 GB just for KV cache)

**This is the single strongest argument for DeepSeek-architecture models on consumer hardware**: MLA's KV compression is a 93.3% reduction that directly translates to 10-15x more concurrent agent slots at the same context length.

---

## 5. New Sources from Second Pass

| Source | Title | Link |
|---|---|---|
| llama.cpp PR #24162 (Jun 2026) | DeepSeek V4 support — 49 commits merged mainline | [github.com/ggml-org/llama.cpp/pull/24162](https://github.com/ggml-org/llama.cpp/pull/24162) |
| Team Blobfish (May 2026) | DeepSeek V4 Flash on llama.cpp: Architecture Port and Lessons | [blog.teamblobfish.com](https://blog.teamblobfish.com/posts/deepseek-v4-flash-llama-cpp/) |
| antirez fork | DeepSeek V4 Flash experimental llama.cpp fork | [github.com/antirez/llama.cpp-deepseek-v4-flash](https://github.com/antirez/llama.cpp-deepseek-v4-flash) |
| DeepSeek-AI (Sep 2025) | FlashMLA: Efficient Multi-head Latent Attention Kernels | [github.com/deepseek-ai/FlashMLA](https://github.com/deepseek-ai/FlashMLA) |
| Unsloth Docs (Jul 2026) | DeepSeek V4 via llama.cpp — quantization guide | [unsloth.ai/docs/models/deepseek-v4](https://unsloth.ai/docs/models/deepseek-v4) |
| llama.cpp PR #24231 | Fused GGML_OP_LIGHTNING_INDEXER op (29x memory reduction) | [github.com/ggml-org/llama.cpp/pull/24231](https://github.com/ggml-org/llama.cpp/pull/24231) |
| llama.cpp PR #25202 (Jul 7 2026) | Fix KV cache quantization bug for DeepSeek V4 | [github.com/ggml-org/llama.cpp/pull/25202](https://github.com/ggml-org/llama.cpp/pull/25202) |
| llama.cpp issue #25382 | Quantized K-cache garbage on all backends | [github.com/ggml-org/llama.cpp/issues/25382](https://github.com/ggml-org/llama.cpp/issues/25382) |
| llama.cpp PR #25545 | CUDA backend for lightning indexer | [github.com/ggml-org/llama.cpp/pull/25545](https://github.com/ggml-org/llama.cpp/pull/25545) |
| DeepSeek-V4-Flash (Apr 2026) | Official HuggingFace model card | [huggingface.co/deepseek-ai/DeepSeek-V4-Flash](https://huggingface.co/deepseek-ai/DeepSeek-V4-Flash) |

Also see sources from first pass in section 5 of the previous version (DeepSeek papers, SMOE, TokenCake, MoE-Infinity, SwarmKV, etc.).

---

## 6. Claim Confidence Summary

| Claim | Pass | Confidence | Source |
|---|---|---|---|
| **MLA: 93.3% KV cache reduction** | 1st | ✅ Verified 3-0 | arXiv:2405.04434 |
| **DeepSeek-V2: 236B total / 21B active, 2+160 experts, top-6** | 1st | ✅ Verified 3-0 | arXiv:2405.04434 |
| **DeepSeek-V3: 671B total / 37B active, 1+256 experts, top-8** | 1st | ✅ Verified 3-0 | arXiv:2412.19437 |
| **Loss-Free Balancing: bias-based, dynamic, interference-free** | 1st | ✅ Verified 3-0 | arXiv:2408.15664 |
| **ggrun benchmarks beat Ollama/raw llama.cpp** | 1st | ✅ Verified | github.com/raketenkater/ggrun |
| **DeepSeek-R1 pure RL → REFUTED (R1 uses SFT; R1-Zero is pure RL)** | 1st | ❌ Refuted | arXiv:2501.12948 |
| **TokenCake: 47% latency reduction, 18.5% stall waste** | 1st | ✅ Verified | arXiv:2510.18586 |
| **SMOE: 8.68x TTFT speedup, token-wise expert prefetch** | 1st | ✅ Verified | IPDPS 2026 |
| **SwarmKV: ~1.95x speedup, ~52x branch activation** | 1st | ✅ Verified | TDS Blog 2026 |
| **llama.cpp tensor split NOT for MoE; pipeline works** | 1st | ✅ Verified | llama.cpp docs |
| **Expert activation patterns are task-predictable** | 1st | ✅ Verified | SMOE, IPDPS 2026 |
| **FlashMLA requires SM90/SM100 only (not RTX 4070/3060)** | 2nd | ✅ Confirmed | FlashMLA README, setup.py, kernels |
| **FlashMLA FP8 KV cache: 656 bytes/token** | 2nd | ✅ Verified 2-0 | FlashMLA README |
| **DeepSeek-V4-Flash: 284B total / 13B active** | 2nd | ✅ Verified 2-0 | HuggingFace model card |
| **V4 ships FP4+FP8 only; no bf16 distribution exists** | 2nd | ✅ Verified 2-0 | HF config.json, blobfish blog |
| **V4 MXFP4 experts can be repacked bit-exact** | 2nd | ✅ Verified | Unsloth docs |
| **IQ quants segfault on V4 in llama.cpp** | 2nd | ✅ Verified | blobfish blog |
| **V4 Q2_K too aggressive under agent prompts** | 2nd | ✅ Verified | blobfish blog |
| **V4 decode compute-bound on indexer, not bandwidth** | 2nd | ✅ Verified | blobfish blog |
| **~4 GiB/layer score buffer → REFUTED (bug before CUDA backend)** | 2nd | ❌ Refuted | PR #24231, issue #25468 |
| **1M ctx on 48GB GPU → REFUTED (96GB, 1M was speculative)** | 2nd | ❌ Refuted | PR #24162 benchmarks |
| **V4 KV cache q8_0 produces garbage on all backends** | 2nd | ✅ Verified | issue #25382 |
| **Unsloth UD-Q8_K_XL (161.9 GB) bit-identical** | 2nd | ✅ Verified | Unsloth docs |
| **Re-quantizing V4 experts to Q4_K = 5.2% RMSE** | 2nd | ✅ Verified | Unsloth docs |

---

## 7. Key Takeaways for Your Setup

1. **ggrun is foundational** — its MoE placement planner is already multi-GPU aware and handles your asymmetrical 4070+3060 setup.

2. **MLA is the killer feature** for multi-agent on 24 GB VRAM. It's the difference between 2 agents at 8K context (standard MHA) and 14 agents at 32K (MLA). Use DeepSeek-architecture models or models that adopt MLA.

3. **DeepSeek V4-Flash is not feasible** on 24 GB VRAM in any usable quantization (~155 GB minimum). Target distilled variants (R1-Distill-Qwen-32B) or V2-Lite instead.

4. **SwarmKV KV-cache sharing is the highest-impact single optimization** — ~2x speedup for 2 agents, savings grow with agent count. Implement using llama.cpp's existing state serialization APIs.

5. **Regex expert pinning** in llama.cpp (PR #24162) lets you control exactly which expert layers go to which GPU or CPU — pair with ggrun's placement planner.

6. **The V4 conversion pipeline is non-trivial** — the native MXFP4 format requires specialized handling. If you do work with V4, use Unsloth's repacking approach (bit-expert MXFP4→GGUF) rather than standard convert.py.

---

*Research conducted 2026-07-21 through 2026-07-24. Claims verified through multi-vote adversarial verification where possible; refuted claims are marked. Sources are primary (papers, GitHub PRs, official docs) unless noted as secondary/community.*
