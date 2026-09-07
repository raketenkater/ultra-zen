// Find the best platform for: reuse user's DDR4 UDIMM (~160-192GB, 6 sticks) +
// 6 GPUs at FULL x16 + path to 2TB server RAM later. Big MoE inference (-training.
// Budget €1000 cash (reused RAM free). DE used market (Bonn/NRW). Ultracode.
//
// The core engineering question this workflow must resolve with hard evidence:
//   Does a platform exist (likely AMD Threadripper PRO sWRX8/eWRX8, maybe Xeon W-3200/W-3300)
//   that (a) runs the user's specific DDR4 UDIMM sticks, (b) gives 128 lanes = 6 GPUs at x16,
//   and (c) has a real 2TB-RDIMM expansion path on the SAME board — OR is the "2TB + UDIMM"
//   combination impossible, forcing a single-socket UDIMM workstation (256-512GB max, RDIMM
//   later) OR a dual-socket RDIMM server (2TB, but can't reuse UDIMM)?
//
// The ECC question is make-or-break: non-ECC consumer UDIMM (if that's what the user has) is
// NOT accepted by Threadripper PRO / Xeon-W which REQUIRE ECC RDIMM/UDIMM. Must classify the
// user's sticks.
export const meta = {
  name: 'udimm-6gpu-platform',
  description: 'Verify the best platform reusing user UDIMM + 6 GPUs at full x16 + 2TB path, real German prices.',
  phases: [
    { title: 'Resolve-tension', detail: 'Verify platform memory-compatibility + lane topology + 2TB path for the user RAM' },
    { title: 'Price-fetch', detail: 'Fetch real German used prices (board + CPU) for each viable platform' },
    { title: 'Verify-prices', detail: 'Adversarially verify prices are obtainable (not China-import bait)' },
    { title: 'Synthesize', detail: 'Decision-tree recommendation: which platform, at what real cost' },
  ],
}

const USER = `Reusable RAM: ~160-192GB DDR4 UDIMM across 6 sticks (mix: 4 lots of 32GB + some 16GB + maybe 1-2 32GB). Form factor: desktop DIMM (not SO-DIMM). ECC status UNKNOWN (to determine if it's ECC or non-ECC — critical). 64GB laptop RAM is SO-DIMM and NOT reusable, ignore it.
Budget: €1000 cash on top of the reused RAM. Germany (Bonn/NRW).
Goal: motherboard that supports 6 GPUs at FULL x16 speed AND supports the user's UDIMM RAM AND a path to ~2TB server RAM AND is good for big MoE inference + ideally training models.
Use case: llama.cpp/ggrun MoE serving (DeepSeek-V4 119GB etc.), CPU+GPU offload, plus training ambition.`

// ─── Schemas ───
const TENSION_SCHEMA = {
  type: 'object', required: ['platform', 'acceptsUdim', 'acceptsEccNonEcc', 'gpuX16Count', 'maxRamNow', 'tbPath', 'trainingFit', 'confidence', 'why', 'risk'],
  properties: {
    platform: { type: 'string' },
    acceptsUdim: { type: 'boolean', description: 'Does the board accept the user\'s DDR4 UDIMM sticks?' },
    acceptsEccNonEcc: { type: 'string', enum: ['ecc-only', 'ecc-or-non-ecc', 'non-ecc-only', 'unknown'] },
    gpuX16Count: { type: 'integer', description: 'How many GPUs can run at FULL x16 in a single box?' },
    ux16Full: { type: 'boolean' },
    maxram: { type: 'string', description: 'Max RAM with UDIMM, and with RDIMM later' },
    '2tbPath': { type: 'boolean', description: 'Can this board reach ~2TB (via RDIMM) on the same board?' },
    candidates: { type: 'array', items: { type: 'object', required: ['part', 'note'], properties: { part: { type: 'string' }, note: { type: 'string' } } } },
    verdict: { type: 'string' },
    why: { type: 'string' },
    risk: { type: 'string' },
  },
}
const PRICE_SCHEMA = {
  type: 'object', required: ['component', 'bestPriceEUR', 'obtainable', 'source', 'offers', 'chinaImportTrap'],
  properties: {
    component: { type: 'string' },
    bestPriceEUR: { type: 'number' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    source: { type: 'string' },
    offers: { type: 'array', items: { type: 'object', required: ['priceEUR', 'seller', 'origin', 'availableNow'], properties: { priceEUR: { type: 'number' }, seller: { type: 'string' }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, availableNow: { type: 'boolean' }, note: { type: 'string' } } } },
    chinaImportTrap: { type: 'string' },
  },
}
const VERIFY_PRICE_SCHEMA = {
  type: 'object', required: ['component', 'realistic', 'verdict', 'why'],
  properties: {
    component: { type: 'string' },
    realistic: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-obtainable', 'listed-only', 'import-trap', 'unverifiable', 'too-good', 'too-high'] },
    why: { type: 'string' },
    adjustmentEUR: { type: 'number' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['summary', 'recommendation', 'decisionTree', 'buyPlan', 'risks'],
  properties: {
    summary: { type: 'string' },
    recommendation: { type: 'string' },
    decisionTree: { type: 'string' },
    buying: { type: 'string' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: Resolve the tension ───
phase('Bus-and-tension')
log('Verifying which platforms run user UDIMM + 6 GPUs at x16 + a 2TB RDIMM path...')

const APPROACHES = [
  'AMD Threadripper PRO (sWRX8 / sTRX5? no — the PRO DDR4 board is WRX80, sWRX8 socket): verify (a) 128 PCIe lanes → 6 GPUs at full x16, (b) 8-channel memory supports BOTH ECC UDIMM and non-ECC UDIMM, (c) max RAM via RDIMM (2TB) on the same board, (d) the ECC-only vs ECC-or-non-ECC UDIMM question.',
  'AMD Threadripper (non-PRO, TRX40/sTRX4): 64 lanes, 4-channel, non-ECC-only. Can it do 6 GPUs at x16? Max RAM? Is 2TB impossible?',
  'Intel Xeon W-3300 (LGA4189) / W-3200 (LGA3647): workstation-class, 64 PCIe lanes, 8-channel. ECC RDIMM/UDIMM only (no non-ECC). Is there a 2TB path? Does it accept the user\'s non-ECC UDIMM?',
  'Dual-socket RDIMM server (EPYC SP3/H11DSi, Xeon Scalable): reaches 2TB easily, 6 GPUs at x16 fine, but is UDIMM supported AT ALL? (Almost certainly NOT — RDIMM-only.) Confirm the meaningful answer.',
  'SO-DIMM reuse: the user ALSO has a 64GB DDR4 LAPTOP (SO-DIMM) stick. Can it be used in a desktop/server build via a DDR4 SO-DIMM-to-DIMM adapter? Is a 64GB DDR4 SO-DIMM ECC? For each viable platform, would an adapter actually work (voltage, SPD, ECC, BIOS recognition, board memory-controller tolerance), and is it worth a DIMM slot?',
]
// Keep it simple — run 4 adversarially-checked spec agents, one per platform family.

const specVerdicts = await parallel(APPROACHES.map((a) => () =>
  agent(
    'Adversarially verify this platform class against manufacturer primary sources + forum evidence,\nfor the user\'s specific goal.\n\nPlatform: ' + a + '\n\n' +
    'User context: ' + USER + '\n\n' +
    'Answer these SPECIFIC questions with evidence (source each claim):\n' +
    '  1. Does this board accept the user\'s DDR4 UDIMM memory? (Note: non-ECC UDIMM vs ECC-only.)\n' +
    '  2. How many GPUs can run at FULL x16 in a single config (real lane count, not "slots")?\n' +
    '  3. What is the max RAM with UDIMM, and with RDIMM (the 2TB path)?\n' +
    '  4. Is it good for big MoE inference and training (memory bandwidth, channels)?\n' +
    'Return the structured verdict. Be honest — if the platform does NOT have any UDIMM + x16 + RAM2TB combination, say so plainly (verdict, conf, risk).\n\nReturn fields: platform, acceptsUdim, acceptsEccNonEcc, gpuX16Count, ux16Full, maxram, "2tbPath": true/false, candidates (list compatible boards/CPUs), verdict, why, risk.',
    { label: 'plat:' + a.slice(0, 22), phase: 'Bus-and-tension', schema: TENSION_SCHEMA }
  )
))

const viable = specVerdicts.filter(Boolean).filter((v) => v.acceptsUdim)
log(`Platform viability: ${specVerdicts.filter(Boolean).length} families checked, viable-for-UDIMM: ${viable.length}`)
if (viable.length === 0) {
  log('NO single platform satisfies UDIMM reuse + 6x x16 + 2TB. The recommendation will present the honest tradeoff (workstation UDIMM now vs server 2TB later).')
}

// ─── Phase 2: Price-fetch viable platforms ───
phase('Price-fetch')
log('Fetching real German used prices for the viable board+CPU combinations...')

const priceTargets = (() => {
  const t = []
  // Always price the workstation platform(s) that reuse UDIMM
  for (const v of viable) {
    if (v.acceptsUdim) {
      const label = v.platform.split(' ')[0] + '-' + (v.platform.includes('PRO') ? 'WRX' : v.platform.includes('W-') ? 'W3xx' : 'TRX')
      t.push({ key: 'plat-' + label, query: (v.platform.includes('Threadripper PRO') ? 'Threadripper PRO WRX80 sWRX8 Mainboard gebraucht kaufen Deutschland' : 'Xeon W-3300 LGA4189 Mainboard gebraucht kaufen Deutschland') + ' ' + v.platform, detail: 'Board for ' + v.platform })
    }
  }
  // Also chase the dual-socket server (the 2TB path) so the user has a real comparison price.
  t.push({
    key: 'plat-server-2tb',
    query: 'EPYC H11DSi dual socket Mainboard gebraucht kaufen Deutschland 2TB RAM',
    detail: 'Dual-socket RDIMM server board that reaches 2TB (6 GPUs x16). For the comparison: what the 2TB path actually costs.',
  })
  t.push({
    key: 'cpu-threadripper-pro',
    query: 'Threadripper PRO 3955WX 3975WX 3995WX gebraucht kaufen Deutschland Preis CPU',
    detail: 'Threadripper PRO CPU (sWRX8) — the CPU to pair with a WRX board if that is the viable route.',
  })
  return t
})()

const prices = await parallel(priceTargets.map((c) => () =>
  agent(
    'Research the REAL, OBTAINABLE price of this component on the German used market today.\n' +
    'Region: Germany (Bonn/NRW). Budget €1000 cash (user RAM reused).\n\nComponent: ' + c.key + '\nQuery: ' + c.query + '\nDetail: ' + c.detail + '\n\nUser context: ' + USER + '\n\n' +
    'CRITICAL — the "price trap": search snippets show low prices that are actually CHINA-IMPORT (3-6 wks shipping, wrong variant), auction-start bids, or single off-center listings. For EACH offer mark origin de/eu/china-import/unknown and buy-it-now-available-now.\n\n' +
    'Prefer eBay.de buy-it-now, Kleinanzeigen.de (Bonn/NRW/NRW-wide), DE retailers (Geizhals, MindFactory, servermarket.de, renewtech.de, alpha38.de, Future-X.de).\nUse search + content-fetch to actually LOOK at real listings, not just snippets.\n\n' +
    'Return: component, bestPriceEUR (the BEST real obtainable EUR, or "unknown" if none), obtainable, source, offers (each with priceEUR, seller, origin, availableNow), chinaImportTrap.\n' +
    'If you cannot confirm a real obtainable DE offer, set obtainable:no and give the best listing you saw as source. Be honest — do not invent prices.',
    { label: 'price:' + (c.target_candidate || c.target), phase: 'Price-fetch', schema: PRICE_SCHEMA }
  )
))

const withPrices = prices.filter(Boolean)
log(`Price-fetch: ${withPrices.length}/${priceTargets.length} components got offers`)

// ─── Phase 3: verify prices ───
phase('Verify-prices')
log('Verifying each price is real/obtainable...')

const verified = await parallel(withPrices.map((p) => () =>
  agent(
    'Adversarially verify this German used-market price by trying to REFUTE it.\n\n' +
    'Component: ' + p.component + '\nClaimed best obtainable EUR: ' + p.bestPriceEUR + '\nSource: ' + p.source + '\nOffers: ' + JSON.stringify(p.offers || []) + '\nImport trap note: ' + (p.chinaImportTrap || 'none') + '\n\n' +
    'Is €' + p.bestPriceEUR + ' actually obtainable in Germany (Bonn/Bonn) for cash, not a listing-only, China-import, or auction-start? Would it survive shipping/VAT/no-stock/seller-won\'t-sell realities? Adjust to a realistic obtainable figure if suspect.\n\n' +
    'Return: component, realistic (bool), verdict, why, adjustmentEUR.',
    { label: 'verify:' + p.component, phase: 'Verify-prices', schema: VERIFY_PRICE_SCHEMA }
  )
))
const priceOK = verified.filter(Boolean).filter((v) => v.realistic)
log(`Price verification: ${priceOK.length}/${verified.filter(Boolean).length} survive`)

// ─── Phase 4: Synthesize ───
phase('Synthesize')
const synth = await agent(
  'Synthesize the FINAL platform recommendation for :' + USER + '\n\n' +
  'Spec/violation results (per platform): ' + JSON.stringify(specVerdicts.filter(Boolean)) + '\n\n' +
  'Price-fetch results: ' + JSON.stringify(withPrices.map((p) => ({ component: p.component, bestPriceEUR: p.bestPriceEUR, obtainable: p.obtainable, source: p.source }))) + '\n\n' +
  'Price-verification results: ' + JSON.stringify(verified.filter(Boolean)) + '\n\n' +
  'Write the decision recommendation:\n' +
  '  1. THE honest answer on the central Q: Can one board run (a) the user\'s ~160-192GB DDR4 UDIMM, (b) 6 GPUs at full x16, (c) reach 2TB? Or must it be a two-tier (workstation UDIMM now / server 2TB later)?\n' +
  '  2. The recommended platform (specific board + CPU), how much it costs in real obtainable EUR given the €1000 cash budget + reused RAM.\n' +
  '  3. The non-ECC/ECC UDIMM question — what the user must check on their sticks, and what happens if they are non-ECC vs ECC, for each candidate.\n' +
  '  4. A clear decision-tree (e.g. IF your sticks are non-ECC THEN...; IF ECC THEN...).\n' +
  '  5. Risks.\n\n' +
  'IMPORTANT PERFORMANCE CONTEXT (measured on the user\'s current rig via ggrun docs) — frame the platform choice against the REAL objective:\n' +
  '  - PCIe lane width is NOT the big lever for inference. Measured: x1/x4 links cost ~0.05-0.2% (activations only cross the bus; KV stays on the owning GPU; compute buffers are the real multi-GPU cost).\n' +
  '  - Current-rig baseline (WITH x1/x4 active): DeepSeek-V4-Flash ~128GiB = 5.88 t/s decode / 32.24 prefill; MiniMax-M3 ~149GiB = 5.59/15.3; Qwen3.5-122B = 22.9/19.5.\n' +
  '  - The REAL wins: (a) more memory channels (i7-10700K 2ch → 8ch Threadripper PRO/Xeon-W) = faster prefill + better CPU-expert MoE (`--n-cpu-moe`, experts-on-CPU is memory-BW-bound); (b) more lanes = capacity for MORE GPUs at full x16 (the 6-GPU goal).\n' +
  '  - So: prefer the platform that maximizes CPU memory channels + lane count for GPU expansion, NOT one that merely "leaves PCIe behind". A faster CPU with more channels for experts-on-CPU is the actual throughput lever for the MoE workload.\n' +
  '  - Note the workload (DeepSeek-V4 119GB, Qwen3.5-122B ~73GB Q4, MiniMax-M3) runs with CPU-expert offload; 256GB UDIMM from the user covers it. 1TB+ is future RDIMM expansion.\n\n' +
  'Be honest and conference-verifiable. No padding.\n\n' +
  'Return the structured synthesis.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA }
)

return {
  spec: specVerdicts.filter(Boolean),
  prices: verified.filter(Boolean),
  synthesis: synth,
}