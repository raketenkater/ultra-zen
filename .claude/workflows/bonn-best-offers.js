// BEST OFFERS HUNT for the WRX80 build, user in Bonn (NRW) Germany.
// Find the best CURRENTLY-obtainable DE offers for each part of the verified Path A build,
// prioritizing NRW/Bonn pickup + fast DE shipping. Adversarially verify each best offer.
// The verified baseline build (2026-08-07) is the anchor to BEAT, not re-derive:
//   Board  ASUS WRX80E-SAGE SE  €650 (Kleinanzeigen Bielefeld, NRW)
//   CPU    Threadripper PRO 3955WX €249 (Kleinanzeigen Dortmund, NRW)
//   Cooler Noctua NH-U14S TR4-SP3 €57 (Amazon.de refurb)
//   RAM    reusing free UDIMM (160GB now; top-up with 32GB DDR4-3200 UDIMM to 224-256GB)
// User wants: keep + maximize UDIMM. Bonn/NRW. Budget €1000 cash for platform, RAM top-up on top.
export const meta = {
  name: 'bonn-best-offers',
  description: 'Hunt best current DE offers for the WRX80 build (board/CPU/cooler/RAM top-up/chassis), Bonn/NRW-first, adversarially verified.',
  phases: [
    { title: 'Find', detail: 'Find best offers per part — NRW/Bonn pickup + fast DE shipping, buy-now, real stock' },
    { title: 'Verify', detail: 'Adversarially verify each best offer is real/obtainable (not China-import/auction/trap)' },
    { title: 'Synthesize', detail: 'Ranked buy-list with clickable links + total for the max-UDIMM build' },
  ],
}

const USER = `User is based in BONN, NRW, Germany. Building an AMD Threadripper PRO 3955WX + ASUS WRX80E-SAGE SE rig for local MoE inference (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. 6 GPUs at full x16, 8-channel DDR4. Reuses ~160GB DDR4 UDIMM now, wants to maximize to 224-256GB (8x32GB UDIMM cap — there is NO 64GB UDIMM). Budget €1000 cash for platform (board+CPU+cooler), RAM top-up on top. Germany (Bonn/NRW). DE used market + new retailers.`

const OFFERS_SCHEMA = {
  type: 'object', required: ['part', 'requirement', 'knownBaseline', 'bestPick', 'offers'],
  properties: {
    part: { type: 'string' },
    requirement: { type: 'string', description: 'The hard spec this part must meet (e.g. 7x true x16, sWRX8, TR4 socket).' },
    knownBaseline: { type: 'string', description: 'The verified anchor price from 2026-08-07 to beat.' },
    bestPick: { type: 'object', required: ['seller', 'priceEUR', 'url', 'origin', 'shippingToBonn', 'why'], properties: { seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, shippingToBonn: { type: 'string', enum: ['pickup', 'fast', 'slow', 'unknown'] }, why: { type: 'string' } } },
    offers: { type: 'array', items: { type: 'object', required: ['seller', 'priceEUR', 'url', 'origin', 'availableNow', 'shippingToBonn'], properties: { seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, availableNow: { type: 'boolean' }, shippingToBonn: { type: 'string', enum: ['pickup', 'fast', 'slow', 'unknown'] }, location: { type: 'string', description: 'Seller location (city/NRW vs elsewhere).' }, note: { type: 'string' } } } },
  },
}
const VERIFY_SCHEMA = {
  type: 'object', required: ['part', 'realistic', 'verdict', 'why', 'adjustedEUR'],
  properties: {
    part: { type: 'string' },
    realistic: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-obtainable', 'listed-only', 'import-trap', 'wrong-part', 'too-good', 'too-high', 'unverifiable'] },
    why: { type: 'string' },
    adjustedEUR: { type: 'number', description: 'Realistic obtainable EUR for a Bonn buyer if the claimed best is suspect.' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['buyList', 'totalEUR', 'maxUdimPlan', 'risks'],
  properties: {
    buyList: { type: 'array', items: { type: 'object', required: ['part', 'pick', 'priceEUR', 'url'], properties: { part: { type: 'string' }, pick: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, why: { type: 'string' } } } },
    totalEUR: { type: 'number', description: 'Platform total (board+CPU+cooler) in EUR, inside €1000.' },
    maxUdimPlan: { type: 'string', description: 'RAM top-up plan: how many 32GB sticks to buy, what part, total cost, resulting capacity.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: Find best offers per part ───
phase('Find')
log('Hunting best current offers per part, Bonn/NRW-first...')

const PARTS = [
  { key: 'board', part: 'WRX80 board (ASUS WRX80E-SAGE SE — 7x TRUE x16; backups: SAGE SE WIFI, Supermicro M12SWA-TF, Gigabyte MC62-G41, ASRock Rack WRX80D8-2T)', query: 'WRX80E-SAGE SE OR M12SWA-TF OR MC62-G41 OR WRX80D8-2T Mainboard kaufen gebraucht Deutschland Kleinanzeigen eBay NRW', known: '€650 Kleinanzeigen Bielefeld (NRW), 2026-08-07 verified', req: 'sWRX8, 8x DDR4 DIMM, RDIMM/UDIMM/LRDIMM to 2TB, 7x PCIe4 x16 TRUE (server/rack boards only — ASRock WRX80 Creator wires slots 4/6 x8, REJECT).' },
  { key: 'cpu', part: 'Threadripper PRO 3955WX (sWRX8, 16c/32t, 128 lanes)', query: 'Threadripper PRO 3955WX kaufen gebraucht Kleinanzeigen eBay Deutschland NRW Preis', known: '€249 Kleinanzeigen Dortmund (NRW), 2026-08-07 verified', req: 'sWRX8 socket. AVOID sub-€900 3975WX (China import — real DE 32-core is €2,127+). Prefer NRW pickup.' },
  { key: 'cooler', part: 'TR4/sWRX8 CPU cooler (Noctua NH-U14S TR4-SP3; alts: be quiet! Dark Rock Pro TR4, Arctic Freezer 4U SP3, Noctua NH-U9 TR4-SP3)', query: 'Noctua NH-U14S TR4-SP3 OR Dark Rock Pro TR4 Kuehler kaufen Deutschland Preis', known: '€56.99 Amazon.de refurb (Noctua store), 2026-08-07 verified', req: 'TR4/sWRX8 mount (SP3 works too). Must cover 280W+ TDP of the 3955WX (155W default but headroom good).' },
  { key: 'ram', part: '32GB DDR4-3200 UDIMM top-up (match Crucial CP32G4DFRA32A — the 2 user already owns; JEDEC 1.2V CL22). Need 2-4 sticks to reach 224-256GB', query: 'Crucial CP32G4DFRA32A OR 32GB DDR4-3200 UDIMM PC4-25600 kaufen gebraucht Deutschland Preis', known: '€215.90 new idealo (Crucial CT32G4DFD832A) / €229 Geizhals; used 32GB UDIMM ~€110-210 asking', req: 'DDR4-3200 UDIMM 288-pin NON-ECC single 32GB (NOT 2x16GB kit, NOT SO-DIMM, NOT RDIMM). Match 1.2V CL22 for minimal mixed-kit conflict. Bonn/NRW or fast DE ship.' },
  { key: 'chassis', part: 'Used 4U GPU/mining rack chassis (for later 6-GPU phase)', query: '4U Server Gehaeuse GPU Mining Rack gebraucht Kleinanzeigen NRW Bonn Duesseldorf Koeln', known: '€49-80 NRW (Bonn-Poppelsdorf €49, Bergisch Gladbach €60, Ratingen €80), 2026-08-06 verified', req: '4U/4HE, GPU-capable (≥500mm deep for 3090 Ti triple-slot), 120mm fans. NRW pickup preferred.' },
]

const found = await parallel(PARTS.map((p) => () =>
  agent(
    'Find the BEST CURRENT, OBTAINABLE offer(s) for this part on the German market, prioritizing Bonn/NRW + fast DE shipping.\n\n' +
    'Part: ' + p.part + '\nQuery: ' + p.query + '\nVerified baseline (2026-08-07) to BEAT: ' + p.known + '\nHard requirement: ' + p.req + '\n\n' +
    'User: ' + USER + '\n\n' +
    'Use DuckDuckGo search + content-fetch and ACTUALLY OPEN listings (Kleinanzeigen.de, eBay.de, Geizhals.de, idealo.de, DE retailers). eBay.de item pages may 403 — use the search index + aggregators.\n\n' +
    'CRITICAL — for each offer mark: origin (de/eu/china-import/unknown), availableNow (buy-now vs auction), shippingToBonn (pickup [NRW], fast [DE ship], slow [import/3-6wk], unknown), location (seller city/region).\n' +
    'Flag China-import traps (3-6wk ship, wrong variant, no EU warranty) and auction-start bids explicitly.\n\n' +
    'Return: part, requirement, knownBaseline, bestPick (seller, priceEUR, url, origin, shippingToBonn, why), offers[] (each: seller, priceEUR, url, origin, availableNow, shippingToBonn, location, note).\n' +
    'Give REAL resolveable URLs. If no better offer than the baseline exists, bestPick = the baseline with why. Be honest.',
    { label: 'find:' + p.key, phase: 'Find', schema: OFFERS_SCHEMA, stallMs: 2147483647 }
  )
))

const foundOK = found.filter(Boolean)
log(`Offers found: ${foundOK.length}/${PARTS.length}`)

// ─── Phase 2: adversarially verify the best pick per part ───
phase('Verify')
log('Adversarially verifying each best offer is real/obtainable for a Bonn buyer...')

const verified = await parallel(foundOK.map((f) => () =>
  agent(
    'Adversarially REFUTE this best offer for a Bonn (NRW) buyer, or confirm it.\n\n' +
    'Part: ' + f.part + '\nBest pick claimed: ' + JSON.stringify(f.bestPick) + '\n\n' +
    'Is €' + (f.bestPick && f.bestPick.priceEUR) + ' actually obtainable for a Bonn, NRW buyer — not listing-only, not China-import, not auction-start, not a wrong variant, and would the seller actually ship to Bonn (or be pickable in NRW)? Does it survive shipping/VAT/no-stock/seller-won\'t-sell realities?\n\n' +
    'Return: part, realistic (bool), verdict, why, adjustedEUR (realistic obtainable EUR for a Bonn buyer if the claimed price is suspect).',
    { label: 'verify:' + f.part.slice(0, 22), phase: 'Verify', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
  )
))

const verifiedOK = verified.filter(Boolean)
log(`Offers verified: ${verifiedOK.length}/${foundOK.length} survive`)

// ─── Phase 3: synthesize the buy-list ───
phase('Synthesize')
const synth = await agent(
  'Synthesize the FINAL buy-list for the user\'s WRX80 build (Bonn, NRW), keeping + maximizing their UDIMM.\n\n' +
  'User: ' + USER + '\n\n' +
  'Best offers found: ' + JSON.stringify(foundOK.map((f) => ({ part: f.part, bestPick: f.bestPick, offers: f.offers }))) + '\n\n' +
  'Adversarial verification: ' + JSON.stringify(verifiedOK) + '\n\n' +
  'Write the decision:\n' +
  '  1. BUY-LIST: for each part (board, CPU, cooler, RAM top-up, chassis), the single best pick for a Bonn buyer — seller, price, URL, why. Rank by obtainability + NRW proximity. If the verified best is the baseline, say so.\n' +
  '  2. TOTAL: platform total (board+CPU+cooler) in EUR — must be inside €1000.\n' +
  '  3. MAX-UDIMM PLAN: how many 32GB sticks to buy (2 for 224GB / 4 for 256GB), which exact part, total RAM cost, resulting capacity + channels.\n' +
  '  4. RISKS: what could be gone/overpriced at purchase time (single listings vanish, VB haggling, no-warranty private sales), and the fallback per part.\n\n' +
  'Be specific with clickable URLs and prices. This is the user\'s buy action — do not pad.\n\n' +
  'Return the structured buy-list.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  offers: foundOK,
  verified: verifiedOK,
  buyList: synth,
}
