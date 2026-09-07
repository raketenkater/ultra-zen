// DEEP-RESEARCH: mix in and compare the "RDIMM / server-RAM only" path against the UDIMM-reuse
// paths. The user now wants a full comparison of the three RAM architectures:
//   A. X399 interim (reuse non-ECC UDIMM, cheap full-speed GPUs) → 2027 new rig
//   B. WRX80 (UDIMM now → RDIMM/1TB later on same board) — the unique hybrid
//   C. RDIMM-only server (EPYC SP3 / Xeon, sell the UDIMM, buy RDIMM from day one) — server RAM only
// Decision context: 2027 DDR5/PCIe5/CXL new rig is the eventual endgame; GPUs carry 100% either way.
// The question: does the RDIMM-only server path (C) offer anything the UDIMM paths don't — at what
// real cost, for what capability — and how does each path's money/capability compare to 2027?
export const meta = {
  name: 'compare-rdimm-only',
  description: 'Compare the RDIMM/server-RAM-only path (sell UDIMM, EPYC/Xeon) vs X399-interim vs WRX80-UDIMM, against the 2027 new-rig endgame.',
  phases: [
    { title: 'RDIMM-servers', detail: 'Real RDIMM-only server platforms obtainable in DE: EPYC SP3 / Xeon, 6-GPU x16, UDIMM-sale offset, real cost' },
    { title: 'Capability', detail: 'What RDIMM-only actually buys vs UDIMM paths: bandwidth, capacity, ECC, training, the 2027 transfer' },
    { title: 'Compare', detail: 'Three-path head-to-head: A (X399+UDIMM) vs B (WRX80 hybrid) vs C (RDIMM server), incl. money to 2027' },
    { title: 'Synthesize', detail: 'Decisive: which RAM architecture is best for this user, and what to do now' },
  ],
}

const USER = `User in BONN, Germany. MoE inference (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. 3 GPUs now (3090 Ti x16, 4070 x4, 3060 x1), 6-GPU ambition. Has ~160GB non-ECC DDR4 UDIMM free (2x Corsair 3200 CL16 + 2x Crucial 3200 CL22 + 2x16GB).
ENDGAME: ~2027 DDR5/PCIe5/CXL new rig, GPUs carry 100%.
THREE RAM ARCHITECTURES TO COMPARE:
  A. X399 interim (Threadripper 1950X ~€400): reuses non-ECC UDIMM, 3 GPUs x16/x16/x16, becomes 2nd node in 2027.
  B. WRX80 (ASUS SAGE SE €750 + unlocked 3975WX €499 ≈ €1,306): UDIMM now → RDIMM 1TB later on SAME board (verified, BIOS≥1106). 8ch, 6x x16, 1TB path.
  C. RDIMM-only server (EPYC SP3 single-socket e.g. H12SSL/ROMED8-2T, or Xeon): SELL the non-ECC UDIMM (~€400-600), buy RDIMM from day one (32GB RDIMM ~€85, 64GB ~€400-550, 128GB 3DS ~€1,441). Real 2TB path. 6 GPUs at x16 on ROMED8-2T (7x TRUE x16). But loses the free UDIMM.
The question: does RDIMM-only (C) offer anything the UDIMM paths don't, at what real cost, and is it worth selling the free UDIMM for? Compare all three against the 2027 endgame.`

const SERVER_SCHEMA = {
  type: 'object', required: ['platform', 'boardCpu', 'udimmSale', 'rdimmCost', 'totalCost', 'gpuX16', 'maxRam', 'ecc', 'training', 'transfer2027', 'verdict'],
  properties: {
    platform: { type: 'string' },
    boardCpu: { type: 'string', description: 'Board + CPU real DE price.' },
    udimmSale: { type: 'number', description: 'What the ~160GB UDIMM sells for (offset).' },
    rdimCost: { type: 'string', description: 'RDIMM config + real cost (e.g. 4x32GB=128GB @ €85 = €340; 8x64=512GB).' },
    totalCost: { type: 'number', description: 'Net cash (board+CPU+RDIMM - UDIMM sale).' },
    gpuX16: { type: 'string', description: 'GPUs at full x16.' },
    maxRam: { type: 'string', description: 'Real max RDIMM.' },
    ecc: { type: 'string', description: 'ECC benefit for training.' },
    training: { type: 'string', description: 'Training capability.' },
    transfer2027: { type: 'string', description: 'What transfers to the 2027 rig.' },
    verdict: { type: 'string' },
  },
}
const CAP_SCHEMA = {
  type: 'object', required: ['path', 'bandwidth', 'capacity', 'ecc', 'gpuFull', 'training', 'moE', 'verdict'],
  properties: {
    path: { type: 'string', description: 'A (X399+UDIMM) / B (WRX80 hybrid) / C (RDIMM server).' },
    bandwidth: { type: 'string', description: 'Real memory bandwidth (GB/s).' },
    capacity: { type: 'string', description: 'Real max capacity now + path.' },
    ecc: { type: 'string', description: 'ECC or not.' },
    gpuFull: { type: 'string', description: 'GPUs at full x16.' },
    training: { type: 'string', description: 'Training fit.' },
    moE: { type: 'string', description: 'MoE inference fit.' },
    verdict: { type: 'string' },
  },
}
const COMPARE_SCHEMA = {
  type: 'object', required: ['path', 'costNow', 'netTo2027', 'capability', 'transfer2027', 'scalability', 'verdict'],
  properties: {
    path: { type: 'string' },
    costNow: { type: 'number', description: 'Real cash now (net of UDIMM sale for C).' },
    netTo2027: { type: 'string', description: 'Money/asset position at 2027 (sunk, saved, transferred, 2nd node).' },
    capability: { type: 'string', description: 't/s + models + training today.' },
    transfer2027: { type: 'string', description: 'What carries to the 2027 rig.' },
    scalability: { type: 'number', description: '0-100.' },
    verdict: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['decision', 'bestPath', 'comparisonTable', 'buyAction', 'risks'],
  properties: {
    decision: { type: 'string', description: 'Which RAM architecture is best for this user, and why.' },
    bestPath: { type: 'string', description: 'The best path now (A/B/C) with exact cost.' },
    comparisonTable: { type: 'string', description: 'Markdown: A vs B vs C on cost | UDIMM | bandwidth | capacity | ECC | GPUs x16 | training | to-2027.' },
    buyAction: { type: 'string', description: 'Concrete: buy X now, sell UDIMM or not, save for 2027.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: real RDIMM-only server platforms ───
phase('RDIMM-servers')
log('Researching real RDIMM-only server platforms (EPYC/Xeon) obtainable in DE...')

const SERVERS = [
  'Single-socket EPYC SP3 (DDR4): Supermicro H12SSL-i/H12SSL-NT (~€761-1,071 new), ASRock Rack ROMED8-2T (7x TRUE x16, ~€729-806) + EPYC 7302P/7313P (24-32c, ~€150-300 used). RDIMM-only. Real DE price for board+CPU. UDIMM sale offset. Real RDIMM cost for 128/256/512GB.',
  'Dual-socket EPYC SP3: H11DSi/H12DSi-NT6 (2TB+, RDIMM-only, but only 2-3x PCIe3 x16 on cheap boards). AS-4124GS-TNR (9x x16, €4-7k complete). Real comparison.',
  'Intel Xeon Scalable: X11SPA-T/X12SPA-T (6-8ch, RDIMM-only, 6 GPUs at x8 via bifurcation on some, or x16 on others). Real DE price.',
  'The WRX80/RDIMM-only angle: the SAME WRX80 board but buying RDIMM instead of reusing UDIMM — i.e. is RDIMM-on-WRX80 (1TB verified, ~€9.6-11.5k for 8x128GB) a meaningful path, or is the real value of WRX80 the UDIMM-reuse + later-swap?',
]
const servers = await parallel(SERVERS.map((s) => () =>
  agent(
    'Evaluate this RDIMM-only server platform for the user, including the UDIMM-sale offset.\n\n' +
    'Platform: ' + s + '\n\nUser: ' + USER + '\n\n' +
    'Research real DE prices: boardCpu (board + CPU), udimmSale (what ~160GB non-ECC UDIMM sells for — earlier research: ~€400-600), rdimCost (RDIMM configs at real prices: 32GB ~€85, 64GB ~€400-550, 128GB ~€1,441), totalCost (net), gpuX16 (full-speed GPUs), maxRam (real), ecc, training, transfer2027 (what carries to the 2027 rig), verdict.\n\n' +
    'The user is deciding whether SELLING the free UDIMM for RDIMM is worth it. Be honest about whether the RDIMM-only path offers anything the UDIMM paths don\'t.',
    { label: 'server:' + s.slice(0, 22), phase: 'RDIMM-servers', schema: SERVER_SCHEMA, stallMs: 2147483647 }
  )
))
const serversOK = servers.filter(Boolean)
log(`RDIMM servers: ${serversOK.length}/${SERVERS.length}`)

// ─── Phase 2: capability of each RAM architecture ───
phase('Capability')
log('Mapping what each RAM architecture (UDIMM/hybrid/RDIMM) actually buys...')

const CAPS = [
  'A. X399 interim + UDIMM (Threadripper 1950X, 4ch DDR4, 3x x16): reuses free UDIMM at quad-channel (~100GB/s). 128GB (4x32GB, drop 2x16GB). No ECC. Training: x16/x16/x16 + no ECC. MoE: ~6.5-8 t/s. Real capability.',
  'B. WRX80 hybrid (SAGE SE + 3975WX, 8ch): UDIMM now (160→256GB, ~170GB/s), then RDIMM 1TB later on same board (BIOS≥1106). 6x x16. MoE: ~8-12 t/s. Training: full-speed + (later) ECC via RDIMM. Real capability + the 1TB path.',
  'C. RDIMM server (EPYC SP3 ROMED8-2T, 8ch): RDIMM from day one, sell UDIMM. 7x x16. ECC. 2TB path real. MoE: ~8-12 t/s (8ch) + ECC. Training: best (ECC + big RAM + full lanes). But loses free UDIMM (~€400-600 sale) and costs more.',
]
const caps = await parallel(CAPS.map((c) => () =>
  agent(
    'Map the real capability of this RAM architecture for the user.\n\n' +
    'Path: ' + c + '\n\nUser: ' + USER + '\n\n' +
    'Assess: path, bandwidth (real GB/s), capacity (now + path), ecc, gpuFull (x16), training, moE (t/s), verdict.\n\n' +
    'Be honest about what ECC + capacity actually buy for MoE inference and training, and whether the UDIMM-reuse value justifies losing it.',
    { label: 'cap:' + c.slice(0, 12), phase: 'Capability', schema: CAP_SCHEMA, stallMs: 2147483647 }
  )
))
const capsOK = caps.filter(Boolean)
log(`Capabilities: ${capsOK.length}/${CAPS.length}`)

// ─── Phase 3: three-path comparison to 2027 ───
phase('Compare')
log('Comparing the three paths (X399 / WRX80 / RDIMM-server) against the 2027 endgame...')

const COMPS = [
  'A. X399 interim + UDIMM (~€400): cheap, reuses UDIMM, 3x x16, becomes 2nd node in 2027. Net to 2027: GPUs transfer + ~€850-900 saved (not sunk in WRX80). Scalability ~50.',
  'B. WRX80 hybrid (~€1,306): 8ch + UDIMM-now + RDIMM-1TB-later + 6x x16. Net to 2027: GPUs transfer + ~€500-700 sunk (platform dies), but 1TB served 2025-27. Scalability ~60 (if 1TB matters) / ~30 (vs 2TB impossible).',
  'C. RDIMM server (EPYC ROMED8-2T, ~€1,200-1,500 net): 8ch RDIMM, ECC, 2TB path, 7x x16. Sell UDIMM. Net to 2027: GPUs transfer + ~€500-800 sunk + UDIMM gone. Scalability ~65 (real 2TB + ECC).',
]
const comps = await parallel(COMPS.map((c) => () =>
  agent(
    'Evaluate this path against the 2027 DDR5/PCIe5/CXL endgame.\n\n' +
    'Path: ' + c + '\n\nUser: ' + USER + '\n\n' +
    'Assess: path, costNow, netTo2027 (money/asset position at 2027), capability (t/s + models + training), transfer2027 (what carries), scalability (0-100), verdict.\n\n' +
    'Be honest about the sunk cost vs the capability gained in the 2025-2027 window, and the UDIMM-sale tradeoff for C.',
    { label: 'comp:' + c.slice(0, 12), phase: 'Compare', schema: COMPARE_SCHEMA, stallMs: 2147483647 }
  )
))
const compsOK = comps.filter(Boolean)
log(`Comparisons: ${compsOK.length}/${COMPS.length}`)

// ─── Phase 4: synthesize ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the decisive recommendation on RAM architecture.\n\n' +
  'User: ' + USER + '\n\n' +
  'RDIMM server platforms (real DE prices + UDIMM-sale offset): ' + JSON.stringify(serversOK) + '\n\n' +
  'RAM architecture capabilities: ' + JSON.stringify(capsOK) + '\n\n' +
  'Three-path comparison to 2027: ' + JSON.stringify(compsOK) + '\n\n' +
  'Write the FINAL answer:\n' +
  '  1. DECISION: which RAM architecture is best for this user — A (X399+UDIMM), B (WRX80 hybrid), or C (RDIMM-only server)? Decisive.\n' +
  '  2. BEST PATH: the concrete best path now with exact cost.\n' +
  '  3. COMPARISON TABLE: A vs B vs C on cost | UDIMM (reuse/sell) | bandwidth | capacity | ECC | GPUs x16 | training | to-2027.\n' +
  '  4. BUY ACTION: buy X now, sell UDIMM or not, save for 2027.\n' +
  '  5. RISKS.\n\n' +
  'Key context: the 2027 DDR5/PCIe5/CXL rig is the endgame; GPUs carry 100% either way; UDIMM is worth ~€400-600 now, ~€0 in 2027. Weigh: is ECC + real 2TB (C) worth selling the free UDIMM + sinking more? Or is the cheap X399 (A) + saving enough? Where does WRX80 (B) fit? Be decisive and honest.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  servers: serversOK,
  caps: capsOK,
  comps: compsOK,
  synthesis: synth,
}
