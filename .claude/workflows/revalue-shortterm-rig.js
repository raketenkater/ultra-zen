// RE-EVALUATE the short-term rig with the user's CLARIFIED requirements:
//   - RAM: 8 DIMM slots, UDIMM upgradeable to 256GB (8x32GB — the hard DDR4 UDIMM max; 512GB UDIMM is impossible)
//   - GPUs: at least 4 at FULL SPEED (x16). User has 4 GPUs.
//   - Reuses the free non-ECC UDIMM (160GB now, upgradeable to 256GB)
//   - Cheap + short-term + upgradeable (room to grow RAM + GPUs)
//   - Bridges to the ~2027 DDR5/PCIe5/CXL new rig (GPUs carry 100%, platform becomes 2nd node or sunk-cheap)
// This RULES OUT X399 (128GB optimal, 3x x16 max). Qualifiers: WRX80 (8 slots → 256GB UDIMM + RDIMM 512GB-1TB later, 7x x16) and TRX40 (8 slots, 256GB UDIMM, 4x x16). X299 = x16/x16/x8 (fails 4x). X99-E WS = PLX 256GB.
export const meta = {
  name: 'revalue-shortterm-rig',
  description: 'Re-evaluate the short-term rig: 8-slot 256GB UDIMM max + 4 GPUs at full speed + UDIMM reuse + cheap + bridges to 2027.',
  phases: [
    { title: 'Spec-match', detail: 'Which platforms: 8 slots → 256GB UDIMM + 4 GPUs x16 + UDIMM reuse?' },
    { title: 'Price', detail: 'Real DE prices: board + CPU + RAM to 256GB' },
    { title: 'Bridge', detail: '2027 bridge: sunk cost vs 2nd-node value, GPUs carry' },
    { title: 'Synthesize', detail: 'Best short-term rig: config, cost, upgrade path, 2027 bridge' },
  ],
}

const USER = `User in BONN, Germany. Wants a SHORT-TERM rig now (until the ~2027 DDR5/PCIe5/CXL new rig, GPUs carry 100%). Has 4 GPUs now (3090 Ti, 4070, 3060, + 1 more) and ~160GB non-ECC DDR4 UDIMM free (2x Corsair 3200 CL16 + 2x Crucial 3200 CL22 + 2x16GB).
CLARIFIED HARD MINIMUM SPEC for the short-term rig:
  - RAM: 8 DIMM slots, UDIMM upgradeable to 256GB (8x32GB — the hard DDR4 UDIMM max; 512GB pure UDIMM is impossible, that needs RDIMM). User confirmed 256GB UDIMM is the target.
  - GPUs: at least 4 at FULL SPEED (x16). User has 4 GPUs now.
  - Reuses the free non-ECC UDIMM (160GB now, upgradeable to 256GB).
  - Cheap + short-term + upgradeable (a little room to grow RAM + GPUs).
  - Bridges to the 2027 new rig: GPUs carry 100%, platform becomes a 2nd node or is sunk-cheap.
This RULES OUT X399 (128GB optimal, 3x x16 max). Qualifying candidates to verify: WRX80 (8 slots → 256GB UDIMM + RDIMM 512GB-1TB later on SAME board, 7x TRUE x16), TRX40 (8 slots, 256GB UDIMM, 4x x16 on Prime TRX40-Pro), X299 (8 slots 256GB but x16/x16/x8 — may FAIL 4x), X99-E WS (PLX, 256GB, 3-4x x16).`

const MATCH_SCHEMA = {
  type: 'object', required: ['platform', 'meetsMinSpec', 'dimmSlots', 'ramMaxUdim', 'gpuX16Count', 'udimmReuse', 'upgradeable', 'why', 'verdict'],
  properties: {
    platform: { type: 'string' },
    meetsMinSpec: { type: 'boolean', description: '8 slots → 256GB UDIMM + 4 GPUs x16 + UDIMM reuse?' },
    dimmSlots: { type: 'integer' },
    ramMaxUdim: { type: 'string', description: 'Real max UDIMM (256GB = 8x32GB) at what speed.' },
    gpuX16Count: { type: 'string', description: 'How many GPUs at TRUE full x16.' },
    udimmReuse: { type: 'string', description: 'Reuses non-ECC UDIMM now, upgradeable to 256GB?' },
    upgradeable: { type: 'string', description: 'Room to grow (RAM + GPUs).' },
    why: { type: 'string' },
    verdict: { type: 'string' },
  },
}
const PRICE_SCHEMA = {
  type: 'object', required: ['platform', 'boardCpu', 'ramTo256', 'totalCost', 'obtainable', 'note'],
  properties: {
    platform: { type: 'string' },
    boardCpu: { type: 'string', description: 'Board + CPU real DE price.' },
    ramTo256: { type: 'string', description: 'Cost from free 160GB to 256GB UDIMM (2 more 32GB sticks = ~€430-640, or 8x32GB).' },
    totalCost: { type: 'number', description: 'Platform + 256GB UDIMM total.' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    note: { type: 'string' },
  },
}
const BRIDGE_SCHEMA = {
  type: 'object', required: ['platform', 'sunkAt2027', 'secondNode', 'gpuTransfer', 'netValue', 'verdict'],
  properties: {
    platform: { type: 'string' },
    sunkAt2027: { type: 'string', description: 'How much is sunk at the 2027 swap.' },
    secondNode: { type: 'string', description: 'Becomes a useful 2nd node?' },
    gpuTransfer: { type: 'string', description: 'GPUs carry 100%?' },
    netValue: { type: 'string', description: 'Net asset/money position at 2027.' },
    verdict: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['bestShortTerm', 'config', 'totalCost', 'upgradePath', 'bridge2027', 'buyAction', 'risks'],
  properties: {
    bestShortTerm: { type: 'string', description: 'The best short-term rig meeting the min spec: name, cost.' },
    config: { type: 'string', description: 'Full config: board, CPU, RAM (160GB → 256GB), GPUs (4 x x16).' },
    totalCost: { type: 'number' },
    upgradePath: { type: 'string', description: 'RAM (→256GB) + GPU growth.' },
    bridge2027: { type: 'string', description: 'How it bridges to the 2027 new rig.' },
    buyAction: { type: 'string', description: 'Concrete: buy X now at €Y, upgrade path, save for 2027.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: which platforms meet the clarified min spec ───
phase('Spec-match')
log('Checking which platforms meet: 8 slots → 256GB UDIMM + 4 GPUs x16 + UDIMM reuse...')

const PLATFORMS = [
  'WRX80 / Threadripper PRO (ASUS SAGE SE, €750-user or €888-verified): 8 slots → 256GB UDIMM now, RDIMM 512GB-1TB later on SAME board. 7x TRUE x16 (6-7 GPUs full speed). Reuses free UDIMM. Meets 8-slot 256GB + 4-GPU-x16? Cost? Bridge to 2027?',
  'TRX40 / Threadripper 3000 (sTRX4): 8 slots → 256GB UDIMM (quad-channel). 4x x16 on ASUS Prime TRX40-Pro (x16/x16/x16/x16?). Reuses UDIMM. Meets 256GB + 4-GPU? Real DE price (earlier: boards scarce ~€665+).',
  'X299 / LGA2066 (i9-10980XE 48-lane): 8 slots → 256GB UDIMM. But GPUs: x16/x16/x8 max on most boards (4 GPUs would be x16/x16/x8/x8). FAILS the 4-GPU-full-x16 min? Verify if ANY X299 does 4x x16 (WS X299 SAGE PLX ~€1,000+).',
  'X99-E WS (dual PLX 8747): 8 slots → 256GB UDIMM. 3-4x x16 via PLX on a 40-lane CPU. Does 4 GPUs at x16 work (40 lanes / PLX)? Real cost.',
  'X399 / Threadripper 1950X (the previous pick): 8 slots but 128GB bandwidth-optimal (Zen-1 drops with 8 sticks), 3x x16 max. CONFIRM it FAILS the clarified min spec (256GB + 4 GPUs).',
]
const match = await parallel(PLATFORMS.map((p) => () =>
  agent(
    'Does this platform meet the user\'s clarified hard minimum spec for a short-term rig?\n\n' +
    'Platform: ' + p + '\n\nUser: ' + USER + '\n\n' +
    'Check: meetsMinSpec (8 slots → 256GB UDIMM + 4 GPUs TRUE x16 + UDIMM reuse), dimmSlots, ramMaxUdim (real, at what speed), gpuX16Count (TRUE x16), udimmReuse, upgradeable, why, verdict.\n\n' +
    'Be rigorous — the X399 must be CONFIRMED failing (128GB optimal, 3x x16), and each candidate honestly assessed for 4 GPUs at TRUE x16 + 256GB UDIMM.',
    { label: 'match:' + p.slice(0, 20), phase: 'Spec-match', schema: MATCH_SCHEMA, stallMs: 2147483647 }
  )
))
const matchOK = match.filter(Boolean)
const qualifiers = matchOK.filter((m) => m.meetsMinSpec)
log(`Spec-match: ${matchOK.length} checked, ${qualifiers.length} qualify`)

// ─── Phase 2: price the qualifying platforms ───
phase('Price')
log('Pricing the qualifying platforms (board + CPU + RAM to 256GB)...')

const price = await parallel(qualifiers.map((q) => () =>
  agent(
    'Price this qualifying platform for the user\'s short-term rig, in real DE used/new prices.\n\n' +
    'Platform: ' + q.platform + '\n\nUser: ' + USER + '\n\n' +
    'Research: boardCpu (board + CPU real DE price), ramTo256 (cost from free 160GB to 256GB UDIMM — 2 more 32GB sticks ~€216-320 each, or replace with 8x32GB), totalCost (platform + 256GB), obtainable, note.\n\n' +
    'Use real DE prices. Reminder: Kleinanzeigen links must be browser-verified (phantom listings). Be honest about obtainability.',
    { label: 'price:' + q.platform.slice(0, 18), phase: 'Price', schema: PRICE_SCHEMA, stallMs: 2147483647 }
  )
))
const priceOK = price.filter(Boolean)
log(`Priced: ${priceOK.length}/${qualifiers.length}`)

// ─── Phase 3: bridge to 2027 ───
phase('Bridge')
log('Assessing how each qualifying platform bridges to the 2027 new rig...')

const bridge = await parallel(qualifiers.map((q) => () =>
  agent(
    'How does this qualifying platform bridge to the user\'s 2027 DDR5/PCIe5/CXL new rig?\n\n' +
    'Platform: ' + q.platform + '\n\nUser: ' + USER + '\n\n' +
    'Assess: sunkAt2027 (how much is sunk), secondNode (becomes a useful 2nd node?), gpuTransfer (GPUs carry 100%?), netValue (net asset/money position at 2027), verdict.\n\n' +
    'The 2027 rig keeps the GPUs. The short-term platform either becomes a 2nd node or is sunk. Be honest about which platforms earn their cost as a 2nd node.',
    { label: 'bridge:' + q.platform.slice(0, 18), phase: 'Bridge', schema: BRIDGE_SCHEMA, stallMs: 2147483647 }
  )
))
const bridgeOK = bridge.filter(Boolean)
log(`Bridge assessed: ${bridgeOK.length}/${qualifiers.length}`)

// ─── Phase 4: synthesize ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the best SHORT-TERM rig meeting the user\'s clarified hard minimum spec.\n\n' +
  'User: ' + USER + '\n\n' +
  'Spec-match results: ' + JSON.stringify(matchOK) + '\n\n' +
  'Real DE prices: ' + JSON.stringify(priceOK) + '\n\n' +
  '2027 bridge: ' + JSON.stringify(bridgeOK) + '\n\n' +
  'Write the FINAL answer:\n' +
  '  1. BEST SHORT-TERM: the best short-term rig meeting the min spec (8 slots → 256GB UDIMM + 4 GPUs x16 + UDIMM reuse) — name, cost.\n' +
  '  2. CONFIG: full config (board, CPU, RAM 160GB→256GB, 4 GPUs at x16).\n' +
  '  3. TOTAL COST.\n' +
  '  4. UPGRADE PATH: the RAM (→256GB, or RDIMM 512GB later on WRX80) + GPU growth.\n' +
  '  5. BRIDGE 2027: how it bridges (2nd node? sunk-cheap? GPUs carry).\n' +
  '  6. BUY ACTION: concrete.\n' +
  '  7. RISKS.\n\n' +
  'Be decisive. The X399 is ruled out (128GB optimal, 3x x16). The winner is likely WRX80 (8 slots, 256GB UDIMM + RDIMM 512GB-1TB later, 7x x16) or TRX40 (256GB, 4x x16). Weigh cost vs capability vs 2027 bridge.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  match: matchOK,
  price: priceOK,
  bridge: bridgeOK,
  synthesis: synth,
}
