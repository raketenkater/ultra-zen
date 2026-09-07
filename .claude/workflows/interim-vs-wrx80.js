// STRATEGIC DEEP-RESEARCH: Is there a BETTER SHORT-TERM option that reuses the UDIMM now,
// then buys an entirely new rig later? The user is questioning whether sinking ~€1,200 into
// an EOL WRX80/Threadripper-PRO platform is the right money move, vs a cheap interim that
// reuses the free non-ECC UDIMM and saves the budget for a real future rig.
//
// The strategic question: given the workload (MoE inference, models fit in 256GB, memory-BW
// bound) and the MEASURED fact (ggrun: x1/x4 PCIe links cost only ~0.05-0.2% for inference),
// is the WRX80's 8-channel bandwidth (~204GB/s vs current 2ch ~38GB/s) worth €1,200 NOW, or
// should the user keep the i7-10700K, add the GPUs (works fine even on x1/x4), save the money,
// and buy a genuinely future-proof rig (DDR5/PCIe5/CXL/NVLink) when it matters?
export const meta = {
  name: 'interim-vs-wrx80',
  description: 'Strategic: is there a better short-term path (reuse UDIMM, add GPUs, save €1,200) than buying the EOL WRX80 now? What is the real future-rig endgame?',
  phases: [
    { title: 'Paths', detail: 'Cost + capability of every strategic path: keep-i7+GPUs, WRX80 now, cheap interim, full-new-rig now, staged' },
    { title: 'Lever-test', detail: 'Adversarially test: is the 8ch bandwidth upgrade worth €1,200 given x1/x4 links cost ~0.05-0.2%? What actually limits the user today?' },
    { title: 'Endgame', detail: 'The real future rig: when do DDR5/PCIe5/CXL/NVLink platforms make sense, at what cost, does WRX80 investment transfer?' },
    { title: 'Synthesize', detail: 'Decisive: buy WRX80 now, or cheap-interim + save for future rig? With exact numbers.' },
  ],
}

const USER = `User in BONN, Germany. Current rig: i7-10700K (Z490M), 128GB DDR4 (2x Corsair 3200 CL16 + 2x Crucial 3200 CL22 = 4x32GB), 2-channel (~38GB/s real), DeepSeek-V4-Flash measured 5.88 t/s decode. Has 3 GPUs (3090 Ti x16, 3060 x1, 4070 x4) and a 6-GPU ambition. Wants: local MoE inference (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. Reuses ~160GB→256GB non-ECC DDR4 UDIMM (free). Budget ~€1000-1400 cash.
MEASURED FACTS (ggrun): x1/x4 PCIe links cost only ~0.05-0.2% for MoE INFERENCE (KV stays on the owning GPU, only activations cross the bus; compute buffers are the real multi-GPU cost). Memory bandwidth IS the MoE lever (decode is RAM-BW bound when weights exceed VRAM).
STRATEGIC QUESTION: The WRX80/Threadripper-PRO platform is EOL/terminal (socket sWRX8, no DDR5/PCIe5/CXL/NVLink, CPU caps at 5995WX). It costs ~€1,200 (board €888 + CPU €240-499 + cooler). Is buying it NOW the right money move, or should the user do a CHEAP INTERIM (keep i7, add GPUs — which work even on x1/x4 per measurements, reuse the UDIMM in the i7) and SAVE for a genuinely future-proof rig later?`

const PATH_SCHEMA = {
  type: 'object', required: ['path', 'costNow', 'capability', 'bandwidth', 'gpuSupport', 'futurePath', 'scalabilityScore', 'verdict'],
  properties: {
    path: { type: 'string', description: 'The strategic path name.' },
    costNow: { type: 'number', description: 'Real cash outlay NOW.' },
    capability: { type: 'string', description: 'What it runs today (models, t/s estimate, training).' },
    bandwidth: { type: 'string', description: 'Memory bandwidth (GB/s) + channels.' },
    gpuSupport: { type: 'string', description: 'How many GPUs, at what link width.' },
    futurePath: { type: 'string', description: 'What happens when you outgrow it — does money transfer or is it sunk?' },
    scalabilityScore: { type: 'number', description: '0-100 future scalability.' },
    verdict: { type: 'string', description: 'The honest assessment of this path.' },
  },
}
const LEVER_SCHEMA = {
  type: 'object', required: ['finding', 'limitsToday', 'is8chWorthIt', 'verdict'],
  properties: {
    finding: { type: 'string', description: 'What ACTUALLY limits the user today (bandwidth? GPUs? capacity?).' },
    limitsToday: { type: 'string', description: 'Is the 2ch ~38GB/s the bottleneck, or is it GPU count/VRAM?' },
    is8chWorthIt: { type: 'boolean', description: 'Is 8ch ~204GB/s worth €1,200 now?' },
    verdict: { type: 'string' },
  },
}
const ENDGAME_SCHEMA = {
  type: 'object', required: ['whenFutureRig', 'futurePlatforms', 'futureCost', 'udimmValueThen', 'wrx80Transfer', 'verdict'],
  properties: {
    whenFutureRig: { type: 'string', description: 'When does a genuinely future-proof rig become worth it (DDR5/PCIe5/CXL/NVLink)?' },
    futurePlatforms: { type: 'string', description: 'The real future platforms: Threadripper 7000 (sTRX5/WRX90), EPYC 9004/9005 (SP5), Xeon 6, consumer DDR5.' },
    futureCost: { type: 'string', description: 'What a future-proof rig costs (ballpark).' },
    udimmValueThen: { type: 'string', description: 'What is the user\'s DDR4 UDIMM worth when DDR5 is the norm? (Answer: near-zero — it dies with the DDR4 era.)' },
    wrx80Transfer: { type: 'string', description: 'Does the €1,200 WRX80 investment transfer to a future rig, or is it 100% sunk at the swap?' },
    verdict: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['decision', 'recommendedPath', 'comparisonTable', 'buyAction', 'risks'],
  properties: {
    decision: { type: 'string', description: 'The decisive strategic recommendation.' },
    recommendedPath: { type: 'string', description: 'The path to take, with exact cost + capability.' },
    comparisonTable: { type: 'string', description: 'Markdown: path | cost now | bandwidth | GPUs | what it runs | future path | scalability.' },
    buyAction: { type: 'string', description: 'Concrete: buy X, or buy nothing and add GPUs, what to save for.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: cost + capability of every strategic path ───
phase('Paths')
log('Costing + assessing every strategic path...')

const PATHS = [
  'KEEP i7-10700K + add GPUs (the CHEAP INTERIM): reuse the 128-160GB UDIMM in the current board (2ch), add the 3 GPUs now (3090 Ti x16, 4070 x4, 3060 x1 — x1/x4 links cost only 0.05-0.2% per ggrun), buy the 4U chassis + PSU for the future. Cost: ~€150-200 (chassis+PSU). What t/s does this give on DeepSeek-V4 with 3 GPUs + 2ch CPU offload? Is it enough to defer the €1,200?',
  'BUY WRX80 NOW (the current plan): €1,200 platform, 8ch ~204GB/s, 6x x16, 1TB path, but EOL/terminal (no DDR5/PCIe5/CXL/NVLink). Runs DeepSeek-V4 ~8-12 t/s (est). Is the bandwidth worth the money now, given the platform is a dead end?',
  'CHEAP USED X299 HEDT interim (quad-channel UDIMM): e.g. i9-7960X/X299 board ~€200-300 used, runs the UDIMM at quad-channel (~70-80GB/s), 44-48 lanes for ~4 GPUs at x16. Better than i7 2ch, still EOL, no 2TB. Worth it as an interim? Real DE price + capability.',
  'FULL NEW RIG NOW (DDR5): Threadripper 7000 (WRX90/sTRX5) or EPYC 9004 (SP5) or high-end consumer DDR5. Zero UDIMM reuse (DDR4 doesn\'t fit), DDR5 RDIMM is expensive, 6 GPUs at x16 needs WRX90/EPYC (€1,500-4,000 board alone). Is this the "buy once, buy right" path if the user drops the UDIMM-reuse requirement? Real cost.',
  'STAGED HYBRID: cheap interim now (i7 + GPUs, reuse UDIMM), save €1,200+ for a real future rig in 1-3 years when DDR5/PCIe5/CXL platforms mature + prices drop. What does the user actually lose by deferring the WRX80? (The 8ch bandwidth boost, the 6-GPU capacity — but per measurements GPUs work on x1/x4, and 2ch is enough to serve the current 3 GPUs.)',
]
const paths = await parallel(PATHS.map((p) => () =>
  agent(
    'Cost + assess this strategic path for the user. Be rigorous and honest.\n\n' +
    'Path: ' + p + '\n\nUser: ' + USER + '\n\n' +
    'Assess: costNow (real DE cash), capability (what it runs today + t/s estimate), bandwidth, gpuSupport (count + link width), futurePath (does money transfer or sink?), scalabilityScore (0-100), verdict.\n\n' +
    'Use real DE used-market prices. For the keep-i7 path: what does 3-GPU + 2ch actually deliver on DeepSeek-V4 (reference ggrun measured 5.88 t/s at 2ch)? Is it enough to defer the upgrade?\n\n' +
    'Return the structured path assessment.',
    { label: 'path:' + p.slice(0, 20), phase: 'Paths', schema: PATH_SCHEMA, stallMs: 2147483647 }
  )
))
const pathsOK = paths.filter(Boolean)
log(`Paths assessed: ${pathsOK.length}/${PATHS.length}`)

// ─── Phase 2: the lever test ───
phase('Lever-test')
log('Adversarially testing: is the 8ch bandwidth worth €1,200 given x1/x4 links cost ~0.05-0.2%?...')

const lever = await agent(
  'Adversarially test the CENTRAL strategic assumption: is the WRX80\'s 8-channel memory bandwidth (~204GB/s vs current 2ch ~38GB/s) worth €1,200 NOW for this user?\n\n' +
  'User: ' + USER + '\n\n' +
  'Research and decide:\n' +
  '  1. What ACTUALLY limits the user today? Is it memory bandwidth (2ch ~38GB/s), GPU count/VRAM (3 GPUs, 48GB), or model capacity (119GB models don\'t fit 48GB VRAM, so ~70GB lives in CPU RAM)?\n' +
  '  2. With the CURRENT 3 GPUs + 2ch CPU offload, what is the real bottleneck? Would adding MORE GPUs to the i7 (even on x1/x4) help more than an 8ch platform? ggrun measured x1/x4 links cost only 0.05-0.2% for inference — so the PCIe link is NOT the wall.\n' +
  '  3. The 5.88 t/s baseline: is that capped by 2ch bandwidth, or by the GPU config? Would 8ch + the SAME 3 GPUs give 8-12 t/s as projected, or is the projection optimistic?\n' +
  '  4. Is €1,200 for ~1.5-2x decode speed worth it NOW, given the platform is EOL (no transfer value)? Or is the money better spent on a 4th GPU / more VRAM / saving for DDR5?\n\n' +
  'Be brutally honest — the user is deciding whether €1,200 of EOL hardware is worth it.',
  { label: 'lever-test', phase: 'Lever-test', schema: LEVER_SCHEMA, stallMs: 2147483647 }
)
log(`Lever test: 8ch worth it=${lever && lever.is8chWorthIt}`)

// ─── Phase 3: the future-rig endgame ───
phase('Endgame')
log('Mapping the real future-rig endgame (DDR5/PCIe5/CXL/NVLink) + UDIMM value + WRX80 transfer...')

const endgame = await agent(
  'Map the REAL future-rig endgame for this user.\n\n' +
  'User: ' + USER + '\n\n' +
  'Research:\n' +
  '  1. WHEN does a genuinely future-proof rig become worth it? (DDR5/PCIe5/CXL/NVLink platforms: Threadripper 7000 sTRX5/WRX90, EPYC 9004/9005 SP5, Intel Xeon 6, consumer DDR5.) Time horizon + price trajectory.\n' +
  '  2. What does a future-proof 6-GPU MoE/training rig cost? (Board + CPU + DDR5 RDIMM. Ballpark, DE prices.)\n' +
  '  3. What is the user\'s DDR4 UDIMM worth when DDR5 is the norm? (Honest answer: it dies with the DDR4 era — near-zero transfer value, or sellable on the shrinking DDR4 market now for ~€400-600.)\n' +
  '  4. Does the €1,200 WRX80 investment TRANSFER to a future rig? (Answer: essentially 100% sunk — EOL socket, DDR4, no resale to DDR5 builders. It\'s a 2-3 year serving platform, not a stepping stone.)\n' +
  '  5. VERDICT: given the UDIMM has ~€400-600 sell value NOW and ~€0 in 2-3 years, and the WRX80 is 100% sunk at the swap, is the "buy WRX80 now, 2-3 years of service, then full new rig" plan actually better than "cheap interim now + save, buy future rig sooner"?\n\n' +
  'Return the structured endgame analysis. Be honest about the sunk-cost reality.',
  { label: 'endgame', phase: 'Endgame', schema: ENDGAME_SCHEMA, stallMs: 2147483647 }
)
log(`Endgame: WRX80 transfer=${endgame && endgame.wrx80Transfer}`)

// ─── Phase 4: synthesize the strategic decision ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the decisive strategic recommendation: buy the WRX80 now, or do a cheap interim + save for a future rig?\n\n' +
  'User: ' + USER + '\n\n' +
  'Paths assessed: ' + JSON.stringify(pathsOK) + '\n\n' +
  'Lever test (is 8ch worth €1,200?): ' + JSON.stringify(lever) + '\n\n' +
  'Future endgame (UDIMM value, WRX80 transfer, DDR5 timing): ' + JSON.stringify(endgame) + '\n\n' +
  'Write the FINAL decision:\n' +
  '  1. DECISION: buy WRX80 now, or cheap-interim + save, or something else? Clear, decisive, with the reasoning.\n' +
  '  2. RECOMMENDED PATH: the exact path with cost + what it delivers (t/s, models, GPUs).\n' +
  '  3. COMPARISON TABLE: path | cost now | bandwidth | GPUs | what it runs | future path | scalability.\n' +
  '  4. BUY ACTION: concrete — buy X now / buy nothing and add GPUs / save €Y for Z.\n' +
  '  5. RISKS.\n\n' +
  'Key considerations to weigh honestly: (a) the user\'s models fit in 256GB and decode is memory-BW bound; (b) ggrun measured x1/x4 links cost ~0.05-0.2% so GPUs work on the i7; (c) WRX80 is EOL — €1,200 is 100% sunk at the future swap; (d) the DDR4 UDIMM has ~€400-600 sell value now, ~€0 in 2-3 years; (e) a real DDR5/PCIe5 rig is 1-3 years out and expensive. Weigh whether the 8ch bandwidth boost NOW is worth the sunk cost, vs keeping the i7 + GPUs (which per measurements work fine on x1/x4) and saving.\n\n' +
  'Be decisive and honest. This is the user\'s biggest money decision in this project.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  paths: pathsOK,
  lever: lever,
  endgame: endgame,
  synthesis: synth,
}
