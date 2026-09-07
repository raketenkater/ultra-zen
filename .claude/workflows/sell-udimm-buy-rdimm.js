// What if we do NOT reuse the UDIMM — sell the current DDR4 sticks now and buy
// server RDIMM directly? Verify REAL eBay.de RDIMM prices (adversarially, no
// LRDIMM/16GBx2/China bait), price the user's UDIMM sell value, and cost the
// alternative best-rig builds that open up (dual-EPYC / Xeon Scalable / WRX80+RDIMM).
// Compare against the WRX80 + reuse-UDIMM path (~€956) to decide which is best.
export const meta = {
  name: 'sell-udimm-buy-rdimm',
  description: 'Verify real eBay.de RDIMM prices + cost the "sell UDIMM, buy server RAM" best-rig alternatives vs the WRX80 reuse path.',
  phases: [
    { title: 'RDIMM-prices', detail: 'Audit real, obtainable eBay.de RDIMM prices per density/speed — flag LRDIMM/16GBx2/China traps' },
    { title: 'UDIMM-sell-value', detail: 'What the user\'s 6 DDR4 UDIMM sticks would actually fetch on the DE used market' },
    { title: 'Server-builds', detail: 'Cost alternative best-rig builds assuming UDIMM is sold and RDIMM is bought' },
    { title: 'Compare', detail: 'Head-to-head: keep-UDIMM/WRX80 path vs sell-and-buy-server path, with a final call' },
  ],
}

const USER = `User has: i7-10700K rig (Z490M, 128GB DDR4 across 4x32GB: 2x Corsair CMK64GX4M2E3200C16 kit [DDR4-3200 CL16, 1.35V XMP] + 2x Crucial CP32G4DFRA32A [DDR4-3200 CL22, 1.2V JEDEC]), plus up to 2 more 16GB DDR4 UDIMM sticks (160-192GB total). Also 2x32GB laptop SO-DIMMs (excluded here). Current rig: DeepSeek-V4-Flash ~128GiB = 5.88 t/s decode, 2ch memory (~38GB/s). Goals: big MoE inference (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition, 6 GPUs at full x16, path to 2TB RAM, budget €1000 cash. Germany (Bonn/NRW), DE used market.
The currently-documented best build (Path A) = AMD Threadripper PRO 3955WX + ASUS WRX80E-SAGE SE + Noctua cooler = ~€956, REUSING the user's free UDIMM.
NEW QUESTION: what if instead we SELL the UDIMM now and BUY server RDIMM directly (Path B) — which platforms open up, at what REAL cost? Is Path B better for the 6-GPU + 2TB goals?`

// ─── Schemas ───
const RDIMM_SCHEMA = {
  type: 'object', required: ['target', 'bestRealEUR', 'obtainable', 'offers', 'trapCheck', 'realPriceRangeEUR'],
  properties: {
    target: { type: 'string', description: 'The exact module target (density/speed/ECC type/part no).' },
    bestRealEUR: { type: 'number', description: 'The BEST real obtainable DE price per module (buy-it-now, deliverable, correct part).' },
    realPriceRangeEUR: { type: 'array', items: { type: 'number' }, description: '[low, high] realistic obtainable range per module.' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    source: { type: 'string' },
    offers: { type: 'array', items: { type: 'object', required: ['priceEUR', 'seller', 'origin', 'moduleCount'], properties: { priceEUR: { type: 'number' }, seller: { type: 'string' }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, moduleCount: { type: 'number', description: 'How many modules the price buys (1x vs 2x/4x kit).' }, url: { type: 'string' }, note: { type: 'string' } } } },
    trapCheck: { type: 'string', description: 'Confirm this is NOT LRDIMM, NOT a 2x kit sold as 1 module, NOT DDR4-2666 bait, NOT China-import.' },
  },
}
const SELL_SCHEMA = {
  type: 'object', required: ['kit', 'whatItIs', 'sellLowEUR', 'sellHighEUR', 'bestSellChannel', 'why'],
  properties: {
    kit: { type: 'string' },
    whatItIs: { type: 'string', description: 'Exact part + spec.' },
    sellLowEUR: { type: 'number' },
    sellHighEUR: { type: 'number' },
    bestSellChannel: { type: 'string', description: 'Kleinanzeigen vs eBay.de vs trade-in — where it actually moves.' },
    why: { type: 'string' },
  },
}
const BUILD_SCHEMA = {
  type: 'object', required: ['build', 'summary', 'parts', 'totalEUR', 'gpuX16Count', 'maxRam', 'verdict'],
  properties: {
    build: { type: 'string' },
    summary: { type: 'string' },
    parts: { type: 'array', items: { type: 'object', required: ['part', 'priceEUR', 'source'], properties: { part: { type: 'string' }, priceEUR: { type: 'number' }, source: { type: 'string' }, url: { type: 'string' }, origin: { type: 'string' } } } },
    totalEUR: { type: 'number', description: 'Total cash needed (platform + RAM), before GPU/PSU/chassis.' },
    gpuX16Count: { type: 'integer' },
    maxRam: { type: 'string' },
    verdict: { type: 'string' },
  },
}
const VERIFY_SCHEMA = {
  type: 'object', required: ['target', 'realistic', 'verdict', 'why', 'adjustedEUR'],
  properties: {
    target: { type: 'string' },
    realistic: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-obtainable', 'listed-only', 'import-trap', 'wrong-part', 'too-good', 'too-high', 'unverifiable'] },
    why: { type: 'string' },
    adjustedEUR: { type: 'number' },
  },
}
const COMPARE_SCHEMA = {
  type: 'object', required: ['winner', 'pathA', 'pathB', 'comparisonTable', 'finalCall', 'whenPathBWins'],
  properties: {
    winner: { type: 'string', enum: ['pathA-keep-udimm', 'pathB-sell-buy-server', 'depends'] },
    pathA: { type: 'string', description: 'WRX80 + reuse UDIMM: real total, capability, weaknesses.' },
    pathB: { type: 'string', description: 'Sell UDIMM + buy server RDIMM: the best alternative build, real total (net of sale), capability.' },
    comparisonTable: { type: 'string', description: 'Rows: platform, RAM total, 2TB path, 6x x16, MoE fit, training fit, real cash needed, net cash after UDIMM sale.' },
    finalCall: { type: 'string' },
    whenPathBWins: { type: 'string', description: 'Under which conditions (RAM >2TB, more GPUs, training focus) does selling UDIMM win.' },
  },
}

// ─── Phase 1: audit real eBay.de RDIMM prices ───
phase('RDIMM-prices')
log('Auditing real, obtainable RDIMM prices on eBay.de (adversarially, trap-aware)...')

const RDIMM_TARGETS = [
  { key: '64GB-DDR4-2933-RDIMM', part: '64GB DDR4-2933 ECC RDIMM (Samsung M393A8G40AB2-CWE / SK Hynix HMAA8GR7CJR4N-XN)', query: '64GB DDR4 2933 ECC RDIMM 288-pin Samsung M393A8G40AB2 kaufen eBay.de', detail: 'The workhorse 64GB module for the 2TB path — 4 of these = 256GB.' },
  { key: '64GB-DDR4-3200-RDIMM', part: '64GB DDR4-3200 ECC RDIMM', query: '64GB DDR4 3200 ECC RDIMM kaufen eBay.de Server RAM', detail: 'Fast 3200 RDIMM variant if available (rare) — is the premium worth it?' },
  { key: '32GB-DDR4-2933-RDIMM', part: '32GB DDR4-2933 ECC RDIMM', query: '32GB DDR4 2933 ECC RDIMM 288-pin kaufen eBay.de Server RAM', detail: 'Cheaper per-module density — 8x of these = 256GB on 8-channel.' },
  { key: '64GB-LRDIMM', part: '64GB DDR4 LRDIMM (the TRAP — load-reduced, cheaper, not the same as RDIMM)', query: '64GB DDR4 LRDIMM kaufen eBay.de', detail: 'Confirm the LRDIMM bait: cheaper €/GB but NOT what the server needs / load-reduced.' },
  { key: '128GB-3DS-RDIMM', part: '128GB DDR4-2933 3DS RDIMM (the 2TB path module)', query: '128GB DDR4 2933 3DS RDIMM kaufen eBay.de', detail: '16x of these = 2TB. The real 2TB program cost.' },
  { key: '64GB-DDR4-RDIMM-AUCTION-CHECK', part: 'eBay.de auction vs BIN reality check for 64GB RDIMM', query: '64GB ECC RDIMM DDR4 versteigern eBay.de aktueller Preis', detail: 'Are the low "eBay prices" auction-start bids that never close low? What do they actually close at?' },
]

const rdimm = await parallel(RDIMM_TARGETS.map((t) => () =>
  agent(
    'Find the REAL, OBTAINABLE price of this server RAM module on the German used market RIGHT NOW, adversarially trap-aware.\n\n' +
    'Target: ' + t.part + '\nQuery: ' + t.query + '\nDetail: ' + t.detail + '\n\n' +
    'User: Germany (Bonn/NRW), DE used market, buying 4-16 modules. Use DuckDuckGo search + content-fetch and ACTUALLY open listings (eBay.de, Kleinanzeigen.de, Geizhals.de, idealo.de, DE server retailers like servermarket.de / renewtech.de / Future-X.de / ServerShop24).\n\n' +
    'CRITICAL — VERIFY THE TRAPS, do not trust snippets:\n' +
    '  1. LRDIMM bait: a 64GB LRDIMM is NOT a 64GB RDIMM (load-reduced, different rank/electrical profile). Mark which listings are LRDIMM even if cheap.\n' +
    '  2. Kit-vs-single: "2x32GB" or "4x16GB" sold as if it were 1 larger module. Report moduleCount per offer.\n' +
    '  3. Wrong speed: DDR4-2666 or DDR4-2400 listed as 2933/3200.\n' +
    '  4. China-import / Greater China sellers: 3-6 wk ship, no EU warranty, wrong density on arrival.\n' +
    '  5. Auction-start bids: low snippet = starting bid, real close price is higher. Prefer buy-it-now.\n\n' +
    'For EACH offer: priceEUR (as listed), seller, origin (de/eu/china-import/unknown), moduleCount, url, note. Then bestRealEUR = the single best real obtainable per-module price (BIN, correct part, DE/EU-stock), and realPriceRangeEUR = [low, high] you would actually pay per module. If only auctions or import exist, say obtainable:no/maybe.\n\n' +
    'Return the structured result. Be brutally honest — do not invent a price because it looks plausible.',
    { label: 'rdimm:' + t.key, phase: 'RDIMM-prices', schema: RDIMM_SCHEMA, stallMs: 2147483647 }
  )
))

const rdimmOK = rdimm.filter(Boolean)
log(`RDIMM targets audited: ${rdimmOK.length}/${RDIMM_TARGETS.length}`)

// ─── Phase 2: price the user's UDIMM sell value ───
phase('UDIMM-sell-value')
log('Pricing what the user\'s UDIMM sticks would actually sell for on the DE used market...')

const SELL_ITEMS = [
  { kit: 'Corsair CMK64GX4M2E3200C16 (2x32GB kit, DDR4-3200 CL16 1.35V) x1', query: 'Corsair CMK64GX4M2E3200C16 64GB 2x32GB verkaufen Preis eBay Kleinanzeigen' },
  { kit: 'Crucial CP32G4DFRA32A (single 32GB, DDR4-3200 CL22 1.2V) x2', query: 'Crucial 32GB DDR4 3200 PC4-25600 UDIMM verkaufen Preis eBay Kleinanzeigen' },
  { kit: 'Generic 16GB DDR4-3200 UDIMM sticks x2 (user says "some 16gb")', query: '16GB DDR4 3200 UDIMM Desktop RAM verkaufen Preis eBay Kleinanzeigen' },
  { kit: 'The whole 160-192GB UDIMM lot as one sale', query: '128GB DDR4 4x32GB Kit UDIMM Desktop RAM verkaufen eBay Kleinanzeigen Preis' },
]

const sellValues = await parallel(SELL_ITEMS.map((s) => () =>
  agent(
    'What does this used DDR4 UDIMM actually SELL for on the German second-hand market right now?\n\n' +
    'Kit: ' + s.kit + '\nQuery: ' + s.query + '\n\n' +
    'Use DuckDuckGo search + content-fetch on eBay.de SOLD/completed listings, Kleinanzeigen.de current listings, and Geizhals used. The USER is SELLING, so use the price a DE buyer actually pays (completed auctions / realistic Kleinanzeigen asking price, haggling down).\n\n' +
    'Return: kit, whatItIs (exact part + spec), sellLowEUR (realistic low / fast-sale price), sellHighEUR (realistic high / patient price), bestSellChannel, why.\n\n' +
    'Be honest — used DDR4 sells well below new. A 32GB DDR4-3200 UDIMM new is ~€216-320; used is typically 40-60% of that.',
    { label: 'sell:' + s.kit.slice(0, 28), phase: 'UDIMM-sell-value', schema: SELL_SCHEMA, stallMs: 2147483647 }
  )
))

const sellOK = sellValues.filter(Boolean)
log(`UDIMM sell-value items priced: ${sellOK.length}/${SELL_ITEMS.length}`)

// ─── Phase 3: cost the alternative server builds (sell UDIMM, buy RDIMM) ───
phase('Server-builds')
log('Costing the alternative best-rig builds assuming UDIMM is sold and RDIMM is bought...')

const BUILD_OPTS = [
  { build: 'Dual EPYC server (AS-4124GS-TNR barebone or H11DSi board + 2x EPYC 7302/7402 + RDIMM)', query: 'EPYC 7302 7402 Dual Socket Mainboard H11DSi AS-4124GS-TNR gebraucht kaufen Deutschland Preis', detail: 'The best 2TB + 6-GPU x16 server platform. Real obtainable board+CPU cost.' },
  { build: 'Xeon Scalable (X11SPA-T + Gold 6248 + RDIMM)', query: 'X11SPA-T Supermicro Mainboard Xeon Gold 6248 gebraucht kaufen Deutschland Preis', detail: 'The older doc path — 6 GPUs at x8 via bifurcation, 768GB-3TB. Real cost.' },
  { build: 'WRX80 + RDIMM (same SAGE board, but 256GB RDIMM instead of reused UDIMM)', query: 'WRX80E-SAGE SE RDIMM 256GB Threadripper PRO RAM kaufen Deutschland Preis', detail: 'Interesting: WRX80 accepts RDIMM to 2TB too. So even keeping the WRX80 board, sell UDIMM + buy RDIMM is an option. Cost 4x64GB RDIMM on it.' },
]

const builds = await parallel(BUILD_OPTS.map((b) => () =>
  agent(
    'Cost this alternative best-rig build on the German used market, using the REAL verified RDIMM prices (from the audit) not optimistic figures.\n\n' +
    'Build: ' + b.build + '\nQuery: ' + b.query + '\nDetail: ' + b.detail + '\n\n' +
    'User: ' + USER + '\n\n' +
    'Reference verified RAM prices from the RDIMM audit (use these, don\'t re-derive): ' + JSON.stringify(rdimmOK.map((r) => ({ target: r.target, bestRealEUR: r.bestRealEUR, range: r.realPriceRangeEUR }))) + '\n\n' +
    'Use DuckDuckGo search + content-fetch for REAL current DE listings (eBay.de BIN, Kleinanzeigen, DE server retailers). Price EVERY part: board, CPUs, cooler(s), RAM to the stated config. Mark origin + China-import traps.\n\n' +
    'Return: build, summary, parts (part, priceEUR, source, url, origin), totalEUR (platform + RAM, before GPUs/PSU/chassis), gpuX16Count, maxRam, verdict.\n\n' +
    'Be honest about what is obtainable in DE. If the platform is not obtainable at a sane price, say so in verdict.',
    { label: 'build:' + b.build.slice(0, 26), phase: 'Server-builds', schema: BUILD_SCHEMA, stallMs: 2147483647 }
  )
))

const buildOK = builds.filter(Boolean)
log(`Alternative builds costed: ${buildOK.length}/${BUILD_OPTS.length}`)

// ─── Phase 4: compare the two paths ───
phase('Compare')
const compare = await agent(
  'Head-to-head comparison + final call for the user\'s rig decision.\n\n' +
  'User: ' + USER + '\n\n' +
  'RDIMM audit (real, trap-checked eBay.de prices): ' + JSON.stringify(rdimmOK) + '\n\n' +
  'UDIMM sell-value research: ' + JSON.stringify(sellOK) + '\n\n' +
  'Alternative builds (sell UDIMM, buy RDIMM): ' + JSON.stringify(buildOK.map((b) => ({ build: b.build, summary: b.summary, parts: b.parts, totalEUR: b.totalEUR, gpuX16Count: b.gpuX16Count, maxRam: b.maxRam, verdict: b.verdict }))) + '\n\n' +
  'Write the decision:\n' +
  '  1. PATH A (keep UDIMM): WRX80 + 3955WX + reuse UDIMM = ~€956, 8ch ~170GB/s, 6x x16, RDIMM-to-2TB later. What 256GB of UDIMM-top-up would cost if the user wants more than 160-192GB now.\n' +
  '  2. PATH B (sell UDIMM, buy server RDIMM): the best alternative build from the Server-builds results — its REAL total, NET cash after selling the UDIMM, and what it buys (2TB today? more GPUs? training?)\n' +
  '  3. COMPARISON TABLE: platform, RAM config/GB, real cash needed, net cash after UDIMM sale, 2TB path, 6x x16, MoE inference fit, training fit.\n' +
  '  4. FINAL CALL: which path wins for this user (Bonn, €1000, 6-GPU + MoE + training ambition + 2TB goal)? Give a concrete recommendation and the exact cash numbers.\n' +
  '  5. WHEN PATH B WINS: the conditions (e.g. if RAM >256GB needed today, if dual-socket + 2TB matters more than UDIMM reuse, if training needs the extra channels) under which selling the UDIMM is clearly better.\n\n' +
  'Be specific, sourced, and honest. Do not pad. This is the user\'s final buy decision.\n\n' +
  'Return the structured comparison.',
  { label: 'compare', phase: 'Compare', schema: COMPARE_SCHEMA, stallMs: 2147483647 }
)

return {
  rdimmAudit: rdimmOK,
  udimmSellValue: sellOK,
  alternativeBuilds: buildOK,
  comparison: compare,
}
