// BEST-CPU-FOR-RIG: adversarially sweep EVERY CPU that fits the user's cheap-rig motherboard families and pick the
// genuine best for THEIR use case. The user clarified: "not just the two cpus but any that fit that motherboard."
// The board family is X299 (Intel LGA2066 — ASUS PRIME X299-A II is the verified pick). The user also raised X399
// (AMD TR4/sTR4 — ASUS PRIME X399-A). So sweep BOTH families fully:
//   X299/LGA2066: i7-7740X, i7-7800X, i7-7820X, i9-7900X, i9-7920X, i9-7940X, i9-7960X, i9-7980XE (Skylake-X 2017),
//                  i9-9820X, i9-9900X, i9-9920X, i9-9940X, i9-9960X, i9-9980XE (Skylake-X refresh 2018),
//                  i9-10900X, i9-10920X, i9-10940X, i9-10980XE (Cascade Lake-X 2019).
//   X399/TR4: Threadripper 1900X, 1920X, 1950X (Zen-1 2017); 2920X, 2950X, 2970WX, 2990WX (Zen+ 2018); 3960X, 3970X, 3990X are sTRX4/TRX40 NOT X399 — exclude.
// HARD REQUIREMENTS: 256GB non-ECC UDIMM (8x32GB), 3 GPUs full-speed (x16/x16/x8 min), AVX2 (ik_llama),
// student budget, reuses free ~160GB non-ECC DDR4 UDIMM. Agentic MoE inference (GPU-bound; lanes ~irrelevant).
// THE 256GB UDIMM FILTER is the make-or-break: Intel ARK caps Skylake-X at 128GB; only Cascade Lake-X hits 256GB.
// AMD Threadripper 1st/2nd-gen: check whether the Zen/Zen+ memory controller hits 256GB UDIMM (8x32GB).
export const meta = {
  name: 'best-cpu-for-rig',
  description: 'Sweep EVERY CPU that fits X299 (LGA2066) and X399 (TR4), pick the best for MoE inference + 256GB UDIMM + AVX2 + student budget.',
  phases: [
    { title: 'Sweep', detail: 'Every X299 + X399 CPU on the 256GB UDIMM filter + 3 GPU lanes + AVX2' },
    { title: 'Shortlist', detail: 'The CPUs that pass — rank by value for the use case' },
    { title: 'Price', detail: 'Real DE used prices for the shortlisted CPUs' },
    { title: 'Verify', detail: 'DE availability, no phantoms' },
    { title: 'Synthesize', detail: 'Decisive: the single best CPU for the rig' },
  ],
}

const USER = `User in BONN, Germany. STUDENT. Cheap-rig build: reuse free ~160GB non-ECC DDR4 UDIMM (2x Corsair 3200 CL16 + 2x Crucial 3200 CL22 + 2x 16GB = 6 sticks), 3 GPUs (3090 Ti x16, 4070 x16, 3060 x8), agentic MoE inference on ik_llama/llama.cpp (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3). GPU-compute-bound; PCIe lanes ~irrelevant for inference (ggrun: x1/x4 cost 0.05-0.2%). 256GB non-ECC UDIMM required (8x32GB = hard DDR4 UDIMM max). AVX2 REQUIRED for ik_llama. Student budget ~EUR 400-700 platform.
BOARD FAMILIES: X299 (Intel LGA2066) and X399 (AMD TR4). Sweep ALL CPUs that fit both.
KNOWN: Intel Skylake-X (i9-7900X..9980XE) caps at 128GB UDIMM per Intel ARK — likely FAILS the 256GB filter. Only Cascade Lake-X (i9-10900X/10920X/10940X/10980XE) hits 256GB. AMD Threadripper Zen-1/Zen+ (1900X..2990WX) memory controller 256GB-UDIMM capability is UNKNOWN — must verify (it may cap at 128GB or 256GB, and may have quirks with 8x32GB UDIMM).`
const SWEEP_SCHEMA = {
  type: 'object', required: ['cpu', 'generation', 'maxMemoryUdim', 'gpuLanes', 'avx2', 'memoryChannels', 'singleThreadPerf', 'priceClass', 'meetsSpec', 'verdict'],
  properties: {
    cpu: { type: 'string' },
    generation: { type: 'string', description: 'Architecture + year.' },
    maxMemoryUdim: { type: 'string', description: 'Max non-ECC UDIMM (256GB = 8x32GB reachable? What does the official spec say?).' },
    gpuLanes: { type: 'string', description: 'How many GPUs at TRUE x16, and the 3rd slot width.' },
    avx2: { type: 'boolean', description: 'AVX2 support.' },
    memoryChannels: { type: 'string' },
    singleThreadPerf: { type: 'string', description: 'Relative single-thread / per-core perf (matters for prompt processing).' },
    priceClass: { type: 'string', description: 'Rough used DE price class.' },
    meetsSpec: { type: 'boolean', description: 'Meets 256GB + 3 GPU + AVX2?' },
    verdict: { type: 'string' },
  },
}
const SHORT_SCHEMA = {
  type: 'object', required: ['ranking', 'bestValue', 'bestPerf', 'rejected', 'verdict'],
  properties: {
    ranking: { type: 'string', description: 'Markdown ranking of the CPUs that pass the filter, best value first.' },
    bestValue: { type: 'string', description: 'The best value CPU for the use case.' },
    bestPerf: { type: 'string', description: 'The best-performance CPU (if different).' },
    rejected: { type: 'string', description: 'The CPUs that FAIL the 256GB UDIMM filter and why.' },
    verdict: { type: 'string' },
  },
}
const PRICE_SCHEMA = {
  type: 'object', required: ['cpu', 'realPriceEUR', 'source', 'obtainable', 'note'],
  properties: {
    cpu: { type: 'string' },
    realPriceEUR: { type: 'number', description: 'Real DE used price.' },
    source: { type: 'string', description: 'Source URL / marketplace.' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    note: { type: 'string' },
  },
}
const VERIFY_SCHEMA = {
  type: 'object', required: ['item', 'claimedPrice', 'realistic', 'verdict', 'why'],
  properties: {
    item: { type: 'string' },
    claimedPrice: { type: 'number' },
    realistic: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-obtainable', 'listed-only', 'import-trap', 'too-high', 'unverifiable'] },
    why: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['bestCpu', 'runnerUp', 'rejected', 'totalCost', 'comparison', 'buyAction', 'risks'],
  properties: {
    bestCpu: { type: 'string', description: 'The single best CPU for the rig.' },
    runnerUp: { type: 'string', description: 'The runner-up, if any.' },
    rejected: { type: 'string', description: 'Why the cheaper/more-core options fail (the 256GB UDIMM wall).' },
    totalCost: { type: 'number' },
    comparison: { type: 'string', description: 'Markdown: all passing CPUs on memory | lanes | AVX2 | speed | price.' },
    buyAction: { type: 'string', description: 'Concrete.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: sweep every X299 + X399 CPU ───
phase('Sweep')
log('Sweeping every X299 + X399 CPU against the 256GB UDIMM + 3 GPU + AVX2 filter...')

const GROUPS = [
  'X299 Skylake-X (2017): i7-7740X, i7-7800X, i7-7820X, i9-7900X, i9-7920X, i9-7940X, i9-7960X, i9-7980XE. All 4-core to 18-core, 28-44 lanes. CRITICAL: which (if any) reach 256GB non-ECC UDIMM? Intel ARK says max memory — verify each. AVX2? Lane config for 3 GPUs on ASUS PRIME X299-A II (needs x16/x16/x8, i.e. 44-lane CPU minimum, or x16/x8/x0 on 28-lane).',
  'X299 Skylake-X refresh (2018): i9-9820X, i9-9900X, i9-9920X, i9-9940X, i9-9960X, i9-9980XE. 10-18 core, 44 lanes. Same CRITICAL question: 256GB UDIMM? (Most cap at 128GB per ARK — verify which, if any, hit 256GB.) AVX2. Lane config.',
  'X299 Cascade Lake-X (2019): i9-10900X, i9-10920X, i9-10940X, i9-10980XE. The known 256GB UDIMM family. Verify each hits 256GB, lane config (10900X=48 lanes x16/x16/x8, 10980XE=48 lanes), AVX2, price class. Which is the best value?',
  'X399 Threadripper Zen-1 (2017): 1900X, 1920X, 1950X. 8/12/16 core, 64 lanes. CRITICAL: does the Zen-1 memory controller hit 256GB non-ECC UDIMM (8x32GB)? AMD spec + real-world reports. AVX2. Lane config on ASUS PRIME X399-A (x16/x16/x16? x16/x16/x8?).',
  'X399 Threadripper Zen+ (2018): 2920X, 2950X, 2970WX, 2990WX. 12/16/24/32 core, 64 lanes. Same CRITICAL: 256GB UDIMM? AVX2. Lane config. (Note: TR4 X399 boards do NOT take sTRX4/TRX40 3960X/3970X — those are a different socket. Exclude.)',
]
const sweep = await parallel(GROUPS.map((g) => () =>
  agent(
    'Sweep this group of CPUs against the user\'s hard spec (256GB non-ECC UDIMM + 3 GPUs full speed + AVX2).\n\n' +
    'CPU group: ' + g + '\n\nUser: ' + USER + '\n\n' +
    'For EACH CPU in the group, return: cpu, generation, maxMemoryUdim (does it reach 256GB = 8x32GB non-ECC UDIMM? — the make-or-break, check the official Intel ARK / AMD spec), gpuLanes (TRUE x16 count + 3rd slot on the relevant board), avx2, memoryChannels, singleThreadPerf, priceClass, meetsSpec, verdict.\n\n' +
    'Verify with mcp__ddg-search__search + mcp__ddg-search__fetch_content. Use official spec pages. The 256GB-UDIMM question is the filter — the 128GB-capped CPUs (most Skylake-X, possibly all Zen-1/Zen+ TR) FAIL the build. Be rigorous.',
    { label: 'sweep:' + g.slice(0, 18), phase: 'Sweep', schema: SWEEP_SCHEMA, stallMs: 2147483647 }
  )
))
const sweepOK = sweep.filter(Boolean)
const passing = sweepOK.filter((s) => s.meetsSpec)
log(`Swept: ${sweepOK.length}/${GROUPS.length} groups; ${passing.length} CPUs pass the 256GB+3GPU+AVX2 filter`)

// ─── Phase 2: rank the passers by value for the use case ───
phase('Shortlist')
log('Ranking the passing CPUs by value for agentic MoE + student budget...')

const short = await agent(
  'Rank the CPUs that PASS the filter, by value for THIS user.\n\n' +
  'User: ' + USER + '\n\n' +
  'Passing CPUs: ' + JSON.stringify(passing) + '\n\n' +
  'Sweep detail (all, incl. failures for context): ' + JSON.stringify(sweepOK) + '\n\n' +
  'Rank by best-value-first for this use case (agentic MoE = GPU-bound, so CPU cores matter little; what matters is meeting 256GB UDIMM + AVX2 + full GPU lanes at the lowest price). Identify:\n' +
  '  ranking (markdown),\n' +
  '  bestValue (the value king),\n' +
  '  bestPerf (the fastest, if different),\n' +
  '  rejected (which fail and why — the 256GB UDIMM wall),\n' +
  '  verdict.',
  { label: 'shortlist', phase: 'Shortlist', schema: SHORT_SCHEMA, stallMs: 2147483647 }
)
log(`Best value: ${short && short.bestValue}`)

// ─── Phase 3: real DE prices for the shortlisted CPUs ───
phase('Price')
log('Pricing the shortlisted CPUs at real DE used prices...')

const price = await parallel(passing.map((p) => () =>
  agent(
    'Price this CPU for the cheap rig at real DE used prices.\n\n' +
    'CPU: ' + p.cpu + '\n\nUser: ' + USER + '\n\n' +
    'Research (mcp__ddg-search__search + fetch): realPriceEUR (real DE used price — eBay.de/Kleinanzeigen), source, obtainable, note.\n\n' +
    'PRICING RULES: Kleinanzeigen = listed-only (phantom). eBay.de German sellers = reliable. No China-import traps. Single unit price.',
    { label: 'price:' + p.cpu.slice(0, 16), phase: 'Price', schema: PRICE_SCHEMA, stallMs: 2147483647 }
  )
))
const priceOK = price.filter(Boolean)
log(`Priced: ${priceOK.length}/${passing.length} passing CPUs`)

// ─── Phase 4: verify DE availability of the top pick ───
phase('Verify')
log('Verifying DE availability of the top CPU pick — no phantoms...')

const verify = await agent(
  'Verify the real DE availability + price of the best CPU for the cheap rig.\n\n' +
  'User: ' + USER + '\n\n' +
  'Prices: ' + JSON.stringify(priceOK) + '\n\n' +
  'Shortlist: ' + JSON.stringify(short) + '\n\n' +
  'Is the best-value CPU actually obtainable in Germany at a sane price? Use mcp__ddg-search__search + fetch.\n\n' +
  'CRITICAL: Kleinanzeigen = listed-only (phantom). eBay.de German sellers = more reliable.\n\n' +
  'Return: item, claimedPrice, realistic, verdict (confirmed-obtainable / listed-only / import-trap / too-high / unverifiable), why.',
  { label: 'verify-cpu', phase: 'Verify', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
)
log(`Verify: ${verify && verify.verdict}`)

// ─── Phase 5: synthesize ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the decisive answer: the SINGLE best CPU for the cheap rig, from ALL that fit the board family.\n\n' +
  'User: ' + USER + '\n\n' +
  'Full sweep (every X299 + X399 CPU): ' + JSON.stringify(sweepOK) + '\n\n' +
  'Shortlist / ranking: ' + JSON.stringify(short) + '\n\n' +
  'Real DE prices: ' + JSON.stringify(priceOK) + '\n\n' +
  'Verify: ' + JSON.stringify(verify) + '\n\n' +
  'Write the FINAL answer:\n' +
  '  1. BEST CPU: the single best CPU for the rig for THIS use case (agentic MoE, GPU-bound, 256GB UDIMM, AVX2, student budget).\n' +
  '  2. RUNNER-UP: the next-best, if any.\n' +
  '  3. REJECTED: why the cheaper/more-core options fail (the 256GB UDIMM wall — list them).\n' +
  '  4. TOTAL COST: board + best CPU.\n' +
  '  5. COMPARISON TABLE: all passing CPUs on memory | lanes | AVX2 | speed | price | obtainability.\n' +
  '  6. BUY ACTION: concrete.\n' +
  '  7. RISKS.\n\n' +
  'Be decisive and honest. The user wants to KNOW the 256GB UDIMM wall disqualifies most CPUs (Skylake-X 128GB cap; possibly Zen-1/Zen+ TR too), so the real candidates are the Cascade Lake-X family. Pick the winner among them (likely the 10900X as value king, or 10920X if the extra cores are worth it — but for GPU-bound work they probably are not).',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  sweep: sweepOK,
  shortlist: short,
  price: priceOK,
  verify: verify,
  synthesis: synth,
}