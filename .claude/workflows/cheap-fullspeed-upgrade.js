// DEEP-RESEARCH: a CHEAP upgrade for the CURRENT rig that runs the user\'s existing 3 GPUs at FULL
// SPEED now, then later (when the DRAM shortage ends ~2027) the user buys an entirely new rig and
// keeps the current GPUs. The Z490M board currently runs 3090 Ti x16, 4070 x4, 3060 x1 — NOT full
// speed for two of them.
// Goal: cheapest platform that gives the 3 current GPUs full-speed lanes (x16 or at least x8),
// reuses the free non-ECC UDIMM, and is worth owning as an interim (transfers GPUs forward, or
// becomes a second node). NOT the €1,645 WRX80 — this is the budget interim. Evaluate whether this
// "cheap full-speed now + new rig later, keep GPUs" goal is actually better than Option A (WRX80).
export const meta = {
  name: 'cheap-fullspeed-upgrade',
  description: 'Find the cheapest platform that runs the CURRENT 3 GPUs at full speed + reuses UDIMM, as an interim before a full new rig later (keep GPUs). Evaluate vs WRX80.',
  phases: [
    { title: 'Current-gap', detail: 'What does the Z490M actually give the 3 GPUs today, and what would full-speed even buy? (x16/x8 vs x4/x1 for these 3 cards)' },
    { title: 'Platform-hunt', detail: 'Every cheap DDR4 platform with enough full-speed lanes for 3 GPUs + UDIMM + DE used price (X299, TRX40, older HEDT, X99, Ryzen multi-PCIe)' },
    { title: 'Unlocked-CPU', detail: 'Find an UNLOCKED Threadripper PRO CPU (3955WX/3975WX/3945WX) obtainable in DE for the €750 WRX80 board — reject Lenovo-P620-locked pulls' },
    { title: 'Verify', detail: 'Real DE obtainability + prices (browser-verify reminders, no phantoms)' },
    { title: 'Evaluate', detail: 'Is the cheap-full-speed-now + new-rig-later (keep GPUs) goal actually better than WRX80 now? Cost, t/s, transfer value, timing' },
    { title: 'Synthesize', detail: 'Decisive: what to buy now for full-speed current GPUs + the Option A price (€750 board + unlocked CPU + upgrade path)' },
  ],
}

const USER = `User in BONN, Germany. CURRENT rig: i7-10700K + Z490M (micro-ATX). 3 GPUs currently: RTX 3090 Ti x16, RTX 4070 x4, RTX 3060 x1 (measured: x1/x4 links cost only ~0.05-0.2% for MoE INFERENCE per ggrun, but the user wants FULL SPEED — and full-speed matters for training). 128-160GB non-ECC DDR4 UDIMM free (2x Corsair 3200 CL16 + 2x Crucial 3200 CL22 + 2x16GB). Runs DeepSeek-V4 119GB (5.88 t/s decode), Qwen3.5-122B (22.9), MiniMax-M3 (5.59) today.
NEW STRATEGIC GOAL: (1) NOW: a CHEAP upgrade that runs the CURRENT 3 GPUs at FULL SPEED (x16 or at least x8 each), reuses the free UDIMM, cost ~€300-800 (NOT the €1,645 WRX80). (2) LATER (when the 2026-27 DRAM shortage ends ~2027): buy an ENTIRELY NEW rig (DDR5/PCIe5/CXL), KEEP the current 3-4 GPUs (they transfer 100%). The cheap upgrade either transfers forward or becomes a second node.
Evaluate whether this goal is better than Option A (buy WRX80 now). Budget for the interim: €300-800.
NOTE: The user can get the ASUS WRX80E-SAGE SE board for ~€750 (not €888). So Option A (WRX80) now costs €750 board + UNLOCKED Threadripper PRO CPU + cooler. Find the unlocked CPU price to complete this.`

const GAP_SCHEMA = {
  type: 'object', required: ['assessment', 'currentLanes', 'fullSpeedGain', 'trainingImpact', 'verdict'],
  properties: {
    assessment: { type: 'string', description: 'What the Z490M gives the 3 GPUs today.' },
    currentLanes: { type: 'string', description: 'x16/x4/x1 config + what it means.' },
    fullSpeedGain: { type: 'string', description: 'What full-speed lanes actually buy for MoE inference (measured ~0.05-0.2%) and training.' },
    trainingImpact: { type: 'string', description: 'Does full-speed matter for training (NCCL/gradient sync over PCIe)?' },
    verdict: { type: 'string' },
  },
}
const HUNT_SCHEMA = {
  type: 'object', required: ['platform', 'fullSpeedGpus', 'udimmReuse', 'lanes', 'realPriceEUR', 'obtainable', 'transferValue', 'verdict'],
  properties: {
    platform: { type: 'string' },
    fullSpeedGpus: { type: 'string', description: 'How many GPUs at TRUE full speed (x16), and at x8.' },
    udimmReuse: { type: 'string', description: 'Can it use the free non-ECC UDIMM?' },
    lanes: { type: 'string', description: 'PCIe version + lane count + wiring.' },
    realPriceEUR: { type: 'number', description: 'Board + CPU real DE used price.' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    transferValue: { type: 'string', description: 'Does the platform transfer to the future rig or become a second node?' },
    verdict: { type: 'string' },
  },
}
const CPU_SCHEMA = {
  type: 'object', required: ['bestUnlockedCpu', 'priceEUR', 'source', 'why', 'unlockConfirmed', 'alternatives'],
  properties: {
    bestUnlockedCpu: { type: 'string', description: 'The best UNLOCKED Threadripper PRO CPU for the €750 WRX80 board: model, price, source.' },
    priceEUR: { type: 'number' },
    source: { type: 'string', description: 'URL + seller. Browser-verify.' },
    why: { type: 'string', description: 'Why this CPU (price-perf, unlocked confirmed, 8ch bandwidth, 4-CCD vs 2-CCD).' },
    unlockConfirmed: { type: 'string', description: 'How to confirm it is UNLOCKED (not Lenovo-P620-locked). Ask the seller in writing.' },
    alternatives: { type: 'string', description: 'Other unlocked options: 3945WX/3955WX/3975WX in DE, and the vendor-locked traps to avoid.' },
  },
}
const EVAL_SCHEMA = {
  type: 'object', required: ['path', 'costNow', 'capability', 'fullSpeedGpus', 'transferToFuture', 'scalability', 'verdict'],
  properties: {
    path: { type: 'string' },
    costNow: { type: 'number' },
    capability: { type: 'string', description: 't/s + models + training with full-speed GPUs.' },
    fullSpeedGpus: { type: 'string' },
    transferToFuture: { type: 'string', description: 'What carries to the 2027 new rig (GPUs, platform?, money saved).' },
    scalability: { type: 'number', description: '0-100.' },
    verdict: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['decision', 'interimPick', 'futurePlan', 'comparison', 'buyAction', 'risks'],
  properties: {
    decision: { type: 'string', description: 'Is the cheap-full-speed-now + new-rig-later-keep-GPUs goal better than WRX80 now? Decisive answer.' },
    interimPick: { type: 'string', description: 'The best cheap interim platform for full-speed current GPUs: name, cost, URL.' },
    futurePlan: { type: 'string', description: 'The 2027 new-rig plan, keeping GPUs.' },
    comparison: { type: 'string', description: 'Markdown: WRX80-now vs cheap-interim-now vs keep-i7 on cost | full-speed GPUs | t/s | transfer | total-to-2027.' },
    buyAction: { type: 'string', description: 'Concrete: buy X now at €Y, save for Z.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: what does full-speed even buy ───
phase('Current-gap')
log('Assessing what the Z490M gives the 3 GPUs today, and what full-speed would buy...')

const gap = await agent(
  'Assess the CURRENT GPU-speed gap for the user.\n\n' +
  'User: ' + USER + '\n\n' +
  'The Z490M (micro-ATX, 16 CPU PCIe3 lanes + ~24 chipset lanes) runs: 3090 Ti x16, 4070 x4 (chipset), 3060 x1 (chipset). ggrun measured x1/x4 links cost ~0.05-0.2% for MoE INFERENCE (KV stays on the owning GPU, only activations cross the bus).\n\n' +
  'Research/decide: (1) what the current x16/x4/x1 config actually means for each GPU. (2) What would full-speed (all x16 or x8) buy for MoE inference given the ~0.05-0.2% measured cost? (3) Does full-speed matter for TRAINING (gradient all-reduce / NCCL over PCIe — x8 vs x4 is a real comms difference)? (4) Verdict: is the "full-speed" desire justified by training ambition, or by the psychological want for clean lanes?\n\n' +
  'Return the structured assessment. Be honest.',
  { label: 'gap', phase: 'Current-gap', schema: GAP_SCHEMA, stallMs: 2147483647 }
)
log(`Gap: full-speed buys=${gap && gap.verdict}`)

// ─── Phase 2: hunt cheap full-speed platforms ───
phase('Platform-hunt')
log('Hunting every cheap DDR4 platform with full-speed lanes for 3 GPUs + UDIMM...')

const PLATFORMS = [
  'X299 / LGA2066 HEDT (i9-7960X 44-lane / 7980XE 44-lane / 10980XE 48-lane): 3 GPUs at x16/x16/x8 (44 lanes) or x16/x16/x16 (48 lanes, 10980XE). Quad-channel UDIMM reuse. Real DE used price (board ~€200-300 + CPU ~€240-400). PCIe 3.0.',
  'TRX40 / Threadripper 3000 (sTRX4): 64 lanes → 4 GPUs at x16. 4-channel UDIMM. Real DE price (board ~€350 + 3960X ~€400-500). PCIe 4.0. (Earlier finding: TRX40 boards scarce in DE — verify.)',
  'X99 / LGA2011-v3 HEDT (i7-5960X 40-lane / Xeon E5-1680v3): 3 GPUs at x16/x16/x8 (40 lanes). Quad-channel UDIMM. Cheap (~€150-250 total). PCIe 3.0. Does it reuse non-ECC UDIMM? Yes.',
  'Ryzen AM5 / AM4 with multiple x16 slots (e.g. X570/Gen4, B650E): consumer boards usually have 1x x16 (CPU) + x4 chipset. Only some X570/X670E split into x8/x8. Can a consumer Ryzen run 3 GPUs at x8 each? Real DE price.',
  'Used server/workstation: Supermicro X10/X11 with UDIMM + enough lanes? (Earlier: most are RDIMM-only or 6x x16 only.) Any cheap board with 3+ full x16 + non-ECC UDIMM?',
]
const hunt = await parallel(PLATFORMS.map((p) => () =>
  agent(
    'Evaluate this platform as the CHEAP interim for running the user\'s 3 current GPUs at FULL SPEED + reusing free UDIMM.\n\n' +
    'Platform: ' + p + '\n\nUser: ' + USER + '\n\n' +
    'Research: fullSpeedGpus (how many at TRUE x16, how many at x8), udimmReuse (non-ECC UDIMM), lanes (PCIe version + count + wiring for 3 GPUs), realPriceEUR (board + CPU, real DE used), obtainable, transferValue (to future rig or 2nd node), verdict.\n\n' +
    'The user wants CHEAP (€300-800) full-speed for the 3 current GPUs. Be honest about the cheapest true full-speed option.',
    { label: 'hunt:' + p.slice(0, 20), phase: 'Platform-hunt', schema: HUNT_SCHEMA, stallMs: 2147483647 }
  )
))
const huntOK = hunt.filter(Boolean)
log(`Platforms hunted: ${huntOK.length}/${PLATFORMS.length}`)

// ─── Phase 3: find an UNLOCKED Threadripper PRO CPU for the €750 WRX80 board ───
phase('Unlocked-CPU')
log('Finding an UNLOCKED Threadripper PRO CPU for the €750 WRX80 board...')

const unlockedCpu = await agent(
  'Find the best UNLOCKED AMD Threadripper PRO CPU obtainable in Germany for the user\'s €750 WRX80 board.\n\n' +
  'User: ' + USER + '\n\n' +
  'The user can get an ASUS WRX80E-SAGE SE board for ~€750. Now they need the CPU to complete Option A. The CRITICAL trap: MANY cheap Threadripper PRO CPUs are Lenovo-P620 or OEM-system LOCKED (vendor-locked = will NOT boot on a retail WRX80 board).\n\n' +
  'Find REAL, currently-obtainable DE offers for an UNLOCKED Threadripper PRO:\n' +
  '  - 3955WX (16c/32t, 2-CCD — half bandwidth, ~€240-700 depending on unlocked-status)\n' +
  '  - 3975WX (32c/64t, 4-CCD — FULL 8ch bandwidth, the recommended one; ~€499 OEM tray aphextwinde eBay.de was found earlier)\n' +
  '  - 3945WX (12c/24t, 2-CCD, ~€129-180)\n\n' +
  'For each candidate: is it UNLOCKED (retail/OEM-tray unlocked, will boot on WRX80) or LOCKED (Lenovo-P620 pull)? How do you tell? Ask the seller in writing. What is the real DE price for a CONFIRMED-unlocked unit?\n\n' +
  'Return: bestUnlockedCpu (model + price + source URL, browser-verify), priceEUR, source, why (price-perf + 4-CCD/8ch bandwidth), unlockConfirmed (how to verify), alternatives (other unlocked options + the locked traps to avoid).',
  { label: 'unlocked-cpu', phase: 'Unlocked-CPU', schema: CPU_SCHEMA, stallMs: 2147483647 }
)
log(`Unlocked CPU: ${unlockedCpu && unlockedCpu.bestUnlockedCpu}`)

// ─── Phase 4: verify real DE prices ───
phase('Verify')
log('Verifying real DE availability + prices...')

const VERIFY_SCHEMA = {
  type: 'object', required: ['item', 'realistic', 'verdict', 'why'],
  properties: {
    item: { type: 'string' },
    realistic: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-obtainable', 'listed-only', 'import-trap', 'too-high', 'unverifiable'] },
    why: { type: 'string' },
  },
}
const verify = await parallel(huntOK.filter((h) => h.obtainable !== 'no').map((h) => () =>
  agent(
    'Verify the real DE availability + price of this cheap full-speed interim platform.\n\n' +
    'Platform: ' + h.platform + '\nClaimed: €' + h.realPriceEUR + '\n\nUser: ' + USER + '\n\n' +
    'Is it actually obtainable in DE at a sane price, not a China-import trap? Use DDG search + content-fetch. Reminder: Kleinanzeigen links must be user-verified in a browser (phantom listings).\n\n' +
    'Return: item, realistic, verdict, why.',
    { label: 'verify:' + h.platform.slice(0, 22), phase: 'Verify', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
  )
))
const verifyOK = verify.filter(Boolean)
log(`Verify: ${verifyOK.filter((v) => v.realistic).length}/${verifyOK.length}`)

// ─── Phase 4: evaluate the strategy ───
phase('Evaluate')
log('Evaluating: is cheap-fullspeed-now + new-rig-later (keep GPUs) better than WRX80 now?...')

const EVALS = [
  'CHEAP INTERIM NOW + NEW RIG LATER (the proposed goal): buy the cheap full-speed platform (e.g. X299/X99, EUR 300-800) now, run 3 GPUs at full speed + reuse UDIMM, then in ~2027 (shortage ends) buy the DDR5/PCIe5/CXL rig and KEEP the GPUs. Total money: interim EUR 300-800 + future EUR 8-12k, GPUs carry over. Evaluate: is the interim worth owning vs just keeping the i7? Does the cheap interim money sink, or does it become a 2nd node?',
  'WRX80 NOW (Option A): €1,645 platform, 8ch, 6x x16, 1TB path, but EOL and ~€500-700 sunk. Compare: does the €1,645 WRX80 give the 3 current GPUs full speed AND a 1TB path that the cheap interim can\'t, justifying the 2x cost? Or is it overkill when the GPUs transfer anyway and a 2027 rig replaces the whole platform?',
  'KEEP i7 + 3 GPUs (€0): the GPUs run on x16/x4/x1 (measured ~0.05-0.2% inference cost), all models work at 5.88 t/s. The ONLY thing the cheap interim adds is full-speed lanes (training benefit) + more channels (if X299 quad). Is that worth €300-800 now, or is the money better saved for 2027?',
  'X299 specifically as the interim (if it wins): i9-10980XE 48-lane → 3 GPUs at x16/x16/x16 (full speed!) + quad-channel + 256GB UDIMM. Cost ~€800. It BECOMES the 2nd node in 2027 (draft/spec node, or a 4-GPU inference box). Is this the best bridge? Or is X99 (€250, 40-lane, x16/x16/x8) enough for the same GPU goal?',
]
const evals = await parallel(EVALS.map((e) => () =>
  agent(
    'Evaluate this strategic path honestly.\n\n' +
    'Path: ' + e + '\n\nUser: ' + USER + '\n\n' +
    'Assess: costNow, capability (t/s + models + training), fullSpeedGpus, transferToFuture (GPUs/platform/money that carries to 2027), scalability (0-100), verdict.\n\n' +
    'Be brutally honest about whether the cheap interim is worth it vs just keeping the i7, and whether it beats the WRX80 for this user.',
    { label: 'eval:' + e.slice(0, 20), phase: 'Evaluate', schema: EVAL_SCHEMA, stallMs: 2147483647 }
  )
))
const evalsOK = evals.filter(Boolean)
log(`Evaluations: ${evalsOK.length}/${EVALS.length}`)

// ─── Phase 5: synthesize ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the decisive recommendation: is the "cheap full-speed interim now + new rig later (keep GPUs)" goal the right one, and what should the user buy NOW?\n\n' +
  'User: ' + USER + '\n\n' +
  'Current-gap assessment: ' + JSON.stringify(gap) + '\n\n' +
  'Cheap platform hunt: ' + JSON.stringify(huntOK) + '\n\n' +
  'Unlocked Threadripper PRO CPU for the €750 WRX80 board: ' + JSON.stringify(unlockedCpu) + '\n\n' +
  'DE availability verify: ' + JSON.stringify(verifyOK) + '\n\n' +
  'Strategic evaluations: ' + JSON.stringify(evalsOK) + '\n\n' +
  'Write the FINAL answer:\n' +
  '  1. DECISION: is the cheap-fullspeed-now + new-rig-later-keep-GPUs goal better than WRX80 now? Clear yes/no/nuanced.\n' +
  '  2. INTERIM PICK: the best cheap platform for full-speed current GPUs — name, cost, URL (browser-verify). X299/10980XE? X99? TRX40? Something else?\n' +
  '  3. FUTURE PLAN: the 2027 new-rig plan keeping the GPUs (DDR5/PCIe5/CXL), what the interim becomes.\n' +
  '  4. OPTION A PRICE (with the €750 board): the user can get the WRX80 board for ~€750. Add the UNLOCKED CPU price from the Unlocked-CPU phase. Give the full Option A platform price (board + unlocked CPU + cooler) with the upgrade paths (1TB RDIMM later) and future support (EOL but 1TB-verified).\n' +
  '  5. COMPARISON: WRX80-now vs cheap-interim-now vs keep-i7 on cost | full-speed GPUs | t/s | transfer to 2027 | total-to-2027.\n' +
  '  6. BUY ACTION: concrete — buy X at €Y now, save €Z for 2027.\n' +
  '  7. RISKS.\n\n' +
  'Be decisive and honest. This is the user\'s near-term hardware decision.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  gap: gap,
  hunt: huntOK,
  unlockedCpu: unlockedCpu,
  verify: verifyOK,
  evals: evalsOK,
  synthesis: synth,
}
