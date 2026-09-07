// DEEP-RESEARCH: A SMALL RIG with GPUs at full speed + faster CPU + MAX UDIMM + MANY DIMM SLOTS
// so the user can fill it with CHEAP SMALL RAM STICKS (not few expensive big ones).
// The user's evolving requirement is a companion/smaller machine that:
//  - runs GPUs at FULL x16 speed
//  - has a FASTER CPU (more cores / more memory channels than the i7-10700K)
//  - maximizes DDR4 UDIMM capacity (reuse free non-ECC sticks)
//  - has MANY DIMM SLOTS so cheap small sticks (e.g. 8/16GB) fill it cheaply
// The key tension: "many slots for cheap small sticks" means LOW per-slot cost, HIGH slot count.
// 8x16GB = 128GB (cheap), 16x8GB = 128GB (cheapest), vs 8x32GB = 256GB (few slots, expensive).
// We must find the platform that maximizes slots-to-UDIMM (and whether UDIMM even supports high
// slot counts) — plus GPU-x16 + faster CPU. And whether this beats the WRX80 as a small rig.
export const meta = {
  name: 'small-rig-many-slots',
  description: 'Find the best SMALL RIG: GPUs at full x16 + faster CPU + max UDIMM via MANY slots for cheap small sticks. vs WRX80.',
  phases: [
    { title: 'Platform-hunt', detail: 'Every platform with MANY DDR4 DIMM slots + UDIMM + GPU x16 + faster CPU (X299, WRX80, EPYC, Xeon, TRX40)' },
    { title: 'Slot-math', detail: 'The cheap-small-sticks math: 8/16/32GB per slot × slot count × €/GB, per platform' },
    { title: 'Verify', detail: 'Real DE prices + availability + does cheap-small-stick strategy actually work on each' },
    { title: 'Synthesize', detail: 'Is there a better small rig than WRX80? Best cheap-sticks platform? Decisive answer' },
  ],
}

const USER = `User in BONN, Germany. Wants a SMALL RIG (companion or main) that:
  1. Runs GPUs at FULL x16 speed (6-GPU ambition, currently has 3090 Ti x16, 4070 x4, 3060 x1)
  2. Has a FASTER CPU than the i7-10700K (more cores, more memory channels)
  3. MAXIMIZES DDR4 UDIMM (reuse free non-ECC sticks: 2x Corsair 3200 CL16 + 2x Crucial 3200 CL22 + 2x16GB = ~160GB)
  4. Has MANY DIMM SLOTS so it can be filled with CHEAP SMALL RAM STICKS (8/16GB) instead of few expensive 32GB ones
  The key desire: lots of cheap slots rather than a few expensive ones. Budget ~€1000-1400. DE used market.
  Context: WRX80/Threadripper-PRO is the known big-platform option (8 slots, 256GB UDIMM cap). The user wonders if a DIFFERENT platform with MORE slots (e.g. X299 8-slot, dual-socket, or server boards) could be a better small rig — especially if cheap 8/16GB sticks populate it more cheaply.`

const HUNT_SCHEMA = {
  type: 'object', required: ['platform', 'dimmSlots', 'udimmMax', 'cheapSticks', 'gpuX16', 'fasterCpu', 'realPriceEUR', 'obtainable', 'verdict'],
  properties: {
    platform: { type: 'string' },
    dimmSlots: { type: 'integer', description: 'Number of DDR4 DIMM slots.' },
    udimmMax: { type: 'string', description: 'Max UDIMM capacity (real, with which sticks).' },
    cheapSticks: { type: 'string', description: 'Can cheap 8/16GB sticks populate it well? Best cheap config (e.g. 8x8GB=64GB, 8x16GB=128GB) + cost.' },
    gpuX16: { type: 'string', description: 'GPUs at full x16 — how many TRUE x16 slots.' },
    fasterCpu: { type: 'string', description: 'CPU options (faster than i7-10700K) + real price.' },
    realPriceEUR: { type: 'number', description: 'Board + CPU real DE price.' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    verdict: { type: 'string', description: 'Is this a better small rig than WRX80 for cheap-small-sticks?' },
  },
}
const SLOT_SCHEMA = {
  type: 'object', required: ['platform', 'configs', 'bestCheapConfig', 'costPerGB', 'totalCost', 'capacity', 'verdict'],
  properties: {
    platform: { type: 'string' },
    configs: { type: 'string', description: 'Cheap-stick configs: 8x8GB, 8x16GB, 4x8+4x16, etc + capacity + cost.' },
    bestCheapConfig: { type: 'string', description: 'The best capacity-per-euro config.' },
    costPerGB: { type: 'number', description: '€/GB for the cheap config.' },
    totalCost: { type: 'number', description: 'Total RAM cost.' },
    capacity: { type: 'string' },
    verdict: { type: 'string', description: 'Is the many-cheap-slots strategy actually worth it here?' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['bestSmallRig', 'comparison', 'verdict', 'buyAction', 'risks'],
  properties: {
    bestSmallRig: { type: 'string', description: 'The best small rig for GPUs-full-x16 + faster CPU + max-UDIMM-via-cheap-sticks.' },
    comparison: { type: 'string', description: 'Markdown: WRX80 vs X299 vs other, on slots | cheap-stick cost | UDIMM max | x16 | CPU | total.' },
    verdict: { type: 'string', description: 'Does the cheap-many-slots strategy make a small rig better than the WRX80?' },
    buyAction: { type: 'string', description: 'Concrete buy recommendation.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: hunt platforms with many slots ───
phase('Platform-hunt')
log('Hunting every platform with MANY DDR4 DIMM slots + UDIMM + GPU x16 + faster CPU...')

const PLATFORMS = [
  'X299 (Intel HEDT, LGA2066): 8 DIMM slots, quad-channel UDIMM, 44-48 lanes. i9-7960X/7980XE/10980XE. Can 8 slots of cheap 8/16GB UDIMM populate cheaply? GPU x16 count? Real DE price. Faster than i7-10700K?',
  'WRX80 / Threadripper PRO (the known option): 8 slots, 8-channel, 256GB UDIMM cap, 7x TRUE x16. The cheap-small-sticks angle: 8x16GB = 128GB vs 8x32GB = 256GB. Does the 8-slot WRX80 favor cheap sticks?',
  'TRX40 / Threadripper 3000 (sTRX4): 8 slots, 4-channel UDIMM, 64 lanes (4 GPUs x16). 3970X/3990X. Cheap-stick friendly? Real DE price.',
  'DUAL-SOCKET boards with UDIMM (rare): any Xeon/EPYC board that accepts non-ECC UDIMM and has MANY slots (e.g. 12-16)? Almost none do UDIMM. Confirm which (if any) dual-socket boards run non-ECC UDIMM + many slots.',
  'Super Micro / Gigabyte / ASRock boards with UDIMM + MANY slots: any server/workstation board with 8+ DIMM slots that runs NON-ECC UDIMM (not just ECC)? e.g. X10/X11 with UDIMM support, or consumer 4-slot (max 128GB).',
]
const hunt = await parallel(PLATFORMS.map((p) => () =>
  agent(
    'Evaluate this platform as a SMALL RIG for the user: GPUs at full x16 + faster CPU + max UDIMM via MANY cheap-small-stick slots.\n\n' +
    'Platform: ' + p + '\n\nUser: ' + USER + '\n\n' +
    'Research: dimmSlots (count), udimmMax (real, non-ECC), cheapSticks (can 8/16GB sticks populate it cheaply? best config + cost), gpuX16 (TRUE x16 count), fasterCpu (options + price), realPriceEUR (board+CPU), obtainable, verdict (better small rig than WRX80 for cheap-small-sticks?).\n\n' +
    'The user wants MANY slots filled with CHEAP small sticks. Be honest about whether the platform actually favors that (e.g. 8 slots × 16GB is 128GB — is that enough? does the platform support many low-density sticks?).',
    { label: 'hunt:' + p.slice(0, 22), phase: 'Platform-hunt', schema: HUNT_SCHEMA, stallMs: 2147483647 }
  )
))
const huntOK = hunt.filter(Boolean)
log(`Platforms hunted: ${huntOK.length}/${PLATFORMS.length}`)

// ─── Phase 2: the cheap-small-sticks slot math ───
phase('Slot-math')
log('Computing the cheap-small-sticks math per platform...')

const SLOTS = [
  'WRX80 (8 slots, 8ch): fill with 8x8GB=64GB, 8x16GB=128GB, 8x32GB=256GB. €/GB + total for each. Does 8-channel need 8 matched sticks? Are 8x16GB cheap sticks (used ~€10-15) worth it vs 8x32GB (~€150 each)?',
  'X299 (8 slots, 4ch): 8x8GB=64GB, 8x16GB=128GB. Quad-channel works with 4 or 8 sticks. Cheap used 8/16GB sticks. Total cost for 128GB. Is 128GB 4ch better value than WRX80 8ch?',
  'TRX40 (8 slots, 4ch): same slot math as X299. 8x16GB=128GB. Real price.',
  'If any dual-socket/many-slot UDIMM board exists: 12-16 slots × cheap sticks = huge capacity. Real cost.',
]
const slotMath = await parallel(SLOTS.map((s) => () =>
  agent(
    'Compute the cheap-small-sticks math for this platform as a small rig.\n\n' +
    'Config: ' + s + '\n\nUser: ' + USER + '\n\n' +
    'Work out: configs (8x8GB/8x16GB/etc + capacity + cost using REAL DE used-market stick prices), bestCheapConfig, costPerGB, totalCost, capacity, verdict (is the many-cheap-slots strategy actually worth it on this platform vs fewer expensive sticks?).\n\n' +
    'Use real DE used prices for 8GB (~€5-10), 16GB (~€10-20), 32GB (~€110-210) DDR4 UDIMM. Be honest about whether cheap sticks bottleneck or whether you need matched kits for the memory controller.',
    { label: 'slot:' + s.slice(0, 20), phase: 'Slot-math', schema: SLOT_SCHEMA, stallMs: 2147483647 }
  )
))
const slotMathOK = slotMath.filter(Boolean)
log(`Slot math: ${slotMathOK.length}/${SLOTS.length}`)

// ─── Phase 3: verify real DE prices + availability ───
phase('Verify')
log('Verifying real DE prices + availability of the candidate small-rig platforms...')

const VERIFY_SCHEMA = {
  type: 'object', required: ['item', 'realistic', 'verdict', 'why'],
  properties: {
    item: { type: 'string' },
    realistic: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-obtainable', 'listed-only', 'import-trap', 'too-good', 'too-high', 'unverifiable'] },
    why: { type: 'string' },
  },
}

const verify = await parallel(huntOK.filter((h) => h.obtainable !== 'no').map((h) => () =>
  agent(
    'Verify the real DE obtainability of this small-rig platform.\n\n' +
    'Platform: ' + h.platform + '\nClaimed price: €' + h.realPriceEUR + '\n\nUser: ' + USER + '\n\n' +
    'Is it actually obtainable in Germany (DE used/new), at a sane price, not a China-import trap? Use DDG search + content-fetch. Reminder: Kleinanzeigen links must be user-verified in a browser (phantom listings) — mark remote-fetched links as listed-unknown.\n\n' +
    'Return: item, realistic, verdict, why.',
    { label: 'verify:' + h.platform.slice(0, 22), phase: 'Verify', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
  )
))
const verifyOK = verify.filter(Boolean)
log(`Verify: ${verifyOK.filter((v) => v.realistic).length}/${verifyOK.length} obtainable`)

// ─── Phase 4: synthesize ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the verdict: is there a better SMALL RIG than the WRX80 — GPUs at full x16 + faster CPU + max UDIMM via MANY CHEAP SMALL STICKS?\n\n' +
  'User: ' + USER + '\n\n' +
  'Platform hunt: ' + JSON.stringify(huntOK) + '\n\n' +
  'Cheap-sticks slot math: ' + JSON.stringify(slotMathOK) + '\n\n' +
  'DE availability verify: ' + JSON.stringify(verifyOK) + '\n\n' +
  'Write the FINAL verdict:\n' +
  '  1. BEST SMALL RIG: the best small rig for the user\'s actual goal (full-x16 GPUs + faster CPU + max-UDIMM-via-cheap-sticks). Is it WRX80, X299, TRX40, or something else? Name it + cost + config.\n' +
  '  2. COMPARISON: WRX80 vs X299 vs TRX40 on slots | cheap-stick config | UDIMM max | x16 | CPU | total cost.\n' +
  '  3. VERDICT: does the "many cheap slots" strategy actually beat the WRX80, or is the WRX80\'s 8-channel + 256GB still the best value? Honest — cheap small sticks mean LOW density (128GB max cheaply), which may not serve MoE (119GB models) well.\n' +
  '  4. BUY ACTION: concrete.\n' +
  '  5. RISKS.\n\n' +
  'Be decisive and honest. The user wants the best small rig, not a menu.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  hunt: huntOK,
  slotMath: slotMathOK,
  verify: verifyOK,
  synthesis: synth,
}
