// Best-rig price verification workflow — ultracode
// Designs the best rig, then adversarially verifies EVERY component price against
// real obtainable German offers (Bonn/NRW, eBay.de BIN + Kleinanzeigen + DE retailers).
// The critical trap: search snippets show "listed" prices; we must mark which are
// actually PURCHASABLE (DE stock / DE seller / low-shipping) vs CHINA-IMPORT listings
// that won't land at the snippet price.
export const meta = {
  name: 'best-rig-verify',
  description: 'Design best rig + verify every component price in real German offers (Bonn/NRW, obtainable, not China-import traps).',
  phases: [
    { title: 'Verify-specs', detail: 'Adversarially verify board/CPU/RAM/chassis/PSU fit for the 6-GPU + 1-2TB goal' },
    { title: 'Price-fetch', detail: 'Fetch real German offers for each component (eBay.de BIN + Kleinanzeigen + DE retailers), distinguish obtainable vs import-only' },
    { title: 'Verify-prices', detail: 'Adversarially verify each price is real/obtainable and flag listing-vs-buyable gaps' },
    { title: 'Synthesize', detail: 'Merge into one verified best-rig build with sourcing plan, Bonn/NRW availability, budget over/under' },
  ],
}

// ─── Config ───
const BUDGET = 1000
const region = 'Germany, Bonn/NRW, used parts. GPUs are REUSED (not bought).'

// ─── Schemas ───
const SPEC_VERDICT_SCHEMA = {
  type: 'object', required: ['component', 'fits', 'confidence', 'why'],
  properties: {
    component: { type: 'string' },
    fits: { type: 'boolean' },
    confidence: { type: 'string', enum: ['high', 'medium', 'low'] },
    why: { type: 'string' },
    note: { type: 'string' },
  },
}
const PRICE_SCHEMA = {
  type: 'object', required: ['component', 'bestPriceEUR', 'obtainable', 'source', 'offers'],
  properties: {
    component: { type: 'string' },
    bestPriceEUR: { type: 'number' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    source: { type: 'string' },
    offers: {
      type: 'array', items: {
        type: 'object', required: ['priceEUR', 'seller', 'origin', 'availableNow'],
        properties: {
          priceEUR: { type: 'number' },
          seller: { type: 'string' },
          origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] },
          availableNow: { type: 'boolean' },
          note: { type: 'string' },
        },
      },
    },
    chinaImportTrap: { type: 'string', description: 'Flag if the cheap snippet price is actually a China-import / wrong-variant listing that will not land at that price' },
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
  type: 'object', required: ['summary', 'build', 'totalEUR', 'overBudget', 'sourcingPlan', 'risks'],
  properties: {
    summary: { type: 'string' },
    build: {
      type: 'array', items: {
        type: 'object', required: ['component', 'part', 'priceEUR', 'obtainable', 'source'],
        properties: {
          component: { type: 'string' },
          part: { type: 'string' },
          priceEUR: { type: 'number' },
          obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
          source: { type: 'string' },
        },
      },
    },
    totalEUR: { type: 'number' },
    overBudget: { type: 'boolean' },
    sourcingPlan: { type: 'string' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: Verify specs ───
phase('Verify-specs')
log('Verifying board/CPU/RAM/chassis/PSU fit for the 6-GPU + 1-2TB goal...')

// The design to verify (each is a falsifiable claim about hardware fit)
const SPEC_CLAIMS = [
  'X11SPA-T (LGA-3647, 12 DIMM, up to 3TB RDIMM) CAN host 6 GPUs at x8 via 16/8/8/8/8/8/8 bifurcation, and x8 PCIe3 is sufficient for MoE-layer offload (activations ~0.05-0.2% of bus).',
  'A single Xeon Gold 6248 (20c/40t, Cascade Lake, 48 PCIe lanes) is required (not Skylake) for the 3TB RDIMM ceiling on X11SPA-T, and is enough CPU for ggrun/llama.cpp MoE serving.',
  '64GB DDR4-2933 ECC RDIMM is the right density: 12×64GB = 768GB in all slots; 16×64GB (needs 16 slots) is dual-socket only. RDIMM is cheaper AND higher-ceiling than LRDIMM here.',
  'A used 4U rack chassis + 2000W redundant PSU is needed for 6 GPUs (3 reused now); typical used 4U chassis with 2×2000W PSUs fits 8 GPUs.',
  'Path-complete alternative: a used Supermicro AS-4124GS-TNR (dual EPYC, 8TB, 9× PCIe 4.0 x16, 2000W Titanium PSUs) is a better 6-GPU host than any DIY X11SPA-T if it lands used < €1500.',
]

const specVerdicts = await parallel(SPEC_CLAIMS.map((c) => () =>
  agent(
    'Adversarially verify this hardware-fit claim against manufacturer primary sources and ggrun/llama.cpp docs. Search for the ground truth; do NOT accept the claim on face value. Refute it if the evidence does not fully support it.\n\nClaim: ' + c + '\n\n' +
    'Context: local MoE serving (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) via llama.cpp/ggrun, CPU+GPU offload, 6-GPU expansion target. Region: Germany, budget €1000 used, GPUs reused.\n\n' +
    'Return: component, fits (bool), confidence, why (the evidence), note (caveats).',
    { label: 'spec:' + c.slice(0, 24), phase: 'Verify-specs', schema: SPEC_VERDICT_SCHEMA }
  )
))
const specOK = specVerdicts.filter(Boolean).filter((v) => v.fits).length
const specFail = specVerdicts.filter(Boolean).filter((v) => !v.fits)
log(`Spec verification: ${specOK}/${SPEC_CLAIMS.length} fits confirmed, ${specFail.length} refuted`)
if (specFail.length) log('REFUTED: ' + specFail.map((v) => v.component).join(', '))

// ─── Phase 2: Price-fetch each component ───
phase('Price-fetch')
log('Fetching real German offers for each component (eBay.de BIN + Kleinanzeigen + DE retailers)...')

const COMPONENTS = [
  { key: 'board-x11spa-t', query: 'Supermicro X11SPA-T LGA3647 Mainboard kaufen Deutschland', detail: 'X11SPA-T board — need DE-stock buy-it-now, not China-import-only.' },
  { key: 'cpu-gold-6248', query: 'Xeon Gold 6248 LGA3647 Prozessor gebraucht kaufen Deutschland', detail: 'Xeon Gold 6248 — need real DE used price, working, not ES/QS sample.' },
  { key: 'ram-64gb-rdimm', query: '64GB DDR4-2933 ECC RDIMM Server RAM kaufen Deutschland Preis', detail: '64GB DDR4-2933 RDIMM — best per-stick price, DE stock. Need price for 12-stick 768GB and 6-stick 384GB.' },
  { key: 'chassis-4u', query: '4U Server Gehäuse GPU gebraucht kaufen Deutschland Rack', detail: '4U rack chassis with GPU slots — used, DE.' },
  { key: 'psu-2000w', query: '2000W Server Netzteil redundant gebraucht kaufen Deutschland', detail: '2000W redundant PSU — used, DE. Also check 2x1200W as cheaper alt.' },
  { key: 'complete-server', query: 'Supermicro 4U GPU Server dual EPYC gebraucht kaufen Deutschland Preis', detail: 'Complete used AS-4124GS-TNR or similar 4U dual-EPYC 8TB server — the path-complete alternative. Need its real obtainable price.' },
]

const prices = await parallel(COMPONENTS.map((c) => () =>
  agent(
    'Research the REAL, OBTAINABLE price of this component on the German used market today. Region: Germany (Bonn/NRW), budget €1000 used, GPUs reused.\n\n' +
    'Component: ' + c.key + '\nQuery: ' + c.query + '\nDetail: ' + c.detail + '\n\n' +
    'CRITICAL — the "price trap": search snippets often show a low price that is actually:\n' +
    '  - a CHINA-IMPORT listing (eBay seller in Shenzhen/HK, ships in 3-6 weeks, high shipping, wrong variant, often different RAM density/CPU)\n' +
    '  - a "from" price that is an auction starting bid, not a buy-it-now obtainable price\n' +
    '  - a single-listing outlier, not a realistic obtainable price\n' +
    'For EACH offer you find, mark origin: de/eu/china-import/unknown, and whether it is a buy-it-now available NOW.\n\n' +
    'Prefer: eBay.de buy-it-now listings, Kleinanzeigen.de (Bonn/NRW/NRW-wide), DE retailers (servershop24.de, servermarket.de, alpha38.de, gebraucht-kaufen.de).\n' +
    'Use the DuckDuckGo search tool and the content-fetch tool to actually look at real listings, not just snippets.\n\n' +
    'Return: component, bestPriceEUR (the best REAL obtainable price in EUR), obtainable (yes/maybe/no/unknown), source (URL/name of where that price is), offers (list each real offer with priceEUR, seller, origin, availableNow), chinaImportTrap (explicit note if the cheap snippet price is a trap).\n\n' +
    'If you cannot confirm a real obtainable DE offer, set obtainable: no and bestPriceEUR to the best listing price you saw with source marked. Be honest — do not invent prices.',
    { label: 'price:' + c.key, phase: 'Price-fetch', schema: PRICE_SCHEMA }
  )
))

const withPrices = prices.filter(Boolean)
log(`Price-fetch: ${withPrices.length}/${COMPONENTS.length} components got offers`)

// ─── Phase 3: Adversarially verify each price ───
phase('Verify-prices')
log('Adversarially verifying each price is real/obtainable and flagging listing-vs-buyable gaps...')

const verified = await parallel(withPrices.map((p) => () =>
  agent(
    'You are the adversarial price-verifier. A research agent returned this price for a German used-market build. Your job: TRY TO REFUTE it.\n\n' +
    'Component: ' + p.component + '\nClaimed best obtainable price: €' + p.bestPriceEUR + '\nSource: ' + p.source + '\nOffers found: ' + JSON.stringify(p.offers || []) + '\nChina-import trap note: ' + (p.chinaImportTrap || 'none') + '\n\n' +
    'Check adversarially:\n' +
    '  1. Is €' + p.bestPriceEUR + ' actually obtainable in Germany (Bonn/NRW) for cash, or is it a listing-only / China-import / auction-start price?\n' +
    '  2. Would this price survive real-world factors (shipping, VAT if applicable, no stock, seller won\'t sell at that)?\n' +
    '  3. If the price is too good, flag it. If too high, say so.\n' +
    'Adjust the price to a REALISTIC obtainable figure if the claimed one is suspect.\n\n' +
    'Return: component, realistic (bool), verdict (confirmed-obtainable / listed-only / import-trap / unverifiable / too-good / too-high), why, adjustmentEUR (0 if confirmed; +/- amount you would adjust).',
    { label: 'verify-price:' + p.component, phase: 'Verify-prices', schema: VERIFY_PRICE_SCHEMA }
  )
))

const confirmed = verified.filter(Boolean).filter((v) => v.realistic)
const flagged = verified.filter(Boolean).filter((v) => !v.realistic)
log(`Price verification: ${confirmed.length}/${verified.filter(Boolean).length} prices survived adversarial check, ${flagged.length} flagged`)

// ─── Phase 4: Synthesize ───
phase('Synthesize')
log('Synthesizing the verified best-rig build...')

const finalPrices = {}
for (const v of verified.filter(Boolean)) {
  finalPrices[v.component] = (v.adjustmentEUR || 0) ? v.bestPriceEUR + v.adjustmentEUR : v.bestPriceEUR
}

const synth = await agent(
  'Synthesize the final "best rig" recommendation for a German (Bonn/NRW) user, budget €1000 used, GPUs reused, for local MoE serving via llama.cpp/ggrun (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3), 1-2TB RAM goal, 6-GPU expansion target.\n\n' +
  'Spec verification results: ' + JSON.stringify(specVerdicts.filter(Boolean)) + '\n\n' +
  'Price-fetch results: ' + JSON.stringify(withPrices.map((p) => ({ component: p.component, bestPriceEUR: p.bestPriceEUR, obtainable: p.obtainable, source: p.source, offers: p.offers, chinaImportTrap: p.chinaImportTrap }))) + '\n\n' +
  'Price-verification (adversarial) results: ' + JSON.stringify(verified.filter(Boolean)) + '\n\n' +
  'Adjusted final prices used: ' + JSON.stringify(finalPrices) + '\n\n' +
  'Build the final recommendation:\n' +
  '  1. The specific parts list (board, CPU, RAM count/density, chassis, PSU) with the verified obtainable EUR price for each, and whether each is obtainable (yes/maybe/no).\n' +
  '  2. Total EUR, and whether it is over the €1000 budget.\n' +
  '  3. A sourcing plan (where in Germany/NRW to actually buy each part, and the realistic effort — Kleinanzeigen haggling, eBay.de BIN, DE retailer).\n' +
  '  4. Risks: availability, import-trap variants, what would actually happen at purchase time (not at listing time).\n' +
  '  5. The honest bottom line: is the €1000 goal achievable, and what is the REAL price to get 1TB + 6-GPU-capable?\n\n' +
  'Be brutally honest. If the real obtainable prices are higher than the old optimistic estimates, say so plainly. Do not pad the numbers.\n\n' +
  'Return the structured build.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA }
)

return {
  spec: { total: SPEC_CLAIMS.length, ok: specOK, refuted: specFail.map((v) => v.component) },
  prices: verified.filter(Boolean),
  synthesis: synth,
}
