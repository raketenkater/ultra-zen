// EXHAUSTIVE: can the user's 64GB DDR4 laptop SO-DIMM be reused in any way
// for the new build? The prior workflow gave a one-line "dead end" — this digs
// each possible path independently and adversarially verifies any positive claim.
export const meta = {
  name: 'sodimm-reuse-deep',
  description: 'Exhaustively determine if the 64GB DDR4 SO-DIMM can be reused (adapter, native board, companion node) — adversarially verified.',
  phases: [
    { title: 'Investigate', detail: 'Independent agents for each reuse path (adapter, native SO-DIMM boards, part identity, alternative roles)' },
    { title: 'Verify', detail: 'Adversarially verify any positive "it works" claim against real evidence' },
    { title: 'Listings', detail: 'Find REAL clickable product links for the WRX80 board + 3955WX CPU (the make-or-break hunt)' },
    { title: 'Synthesize', detail: 'Decision: reuse in main build / companion node / dead end, with reasons' },
  ],
}

const LIST_SCHEMA = {
  type: 'object', required: ['part', 'links'],
  properties: {
    part: { type: 'string' },
    links: { type: 'array', items: { type: 'object', required: ['seller', 'url', 'priceEUR', 'obtainable', 'origin'], properties: { seller: { type: 'string' }, url: { type: 'string' }, priceEUR: { type: 'number' }, obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, availableNow: { type: 'boolean' }, note: { type: 'string' } } } },
    bestPick: { type: 'string', description: 'The single best obtainable DE link (seller + price + why).' },
  },
}

const USER = `The stick in question: a 64GB DDR4 laptop memory module (SO-DIMM, ~260-pin, ~69mm) taken from a laptop. Unknown make/model/ECC-status to start — the workflow should determine what such a part likely is. It would be a bonus if reusable, NOT a requirement. The main build is AMD Threadripper PRO on WRX80 (sWRX8, DDR4) reusing ~160-192GB desktop UDIMM, for big MoE inference (llama.cpp/ggrun: DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. Germany (Bonn/NRW).`

// ─── Schemas ───
const PATH_SCHEMA = {
  type: 'object', required: ['path', 'works', 'confidence', 'why', 'details'],
  properties: {
    path: { type: 'string' },
    works: { type: 'boolean', description: 'Can this path put the SO-DIMM to real use in the build or architecture?' },
    confidence: { type: 'string', enum: ['high', 'medium', 'low'] },
    why: { type: 'string' },
    details: { type: 'string' },
    caveats: { type: 'string' },
  },
}
const VERIFY_SCHEMA = {
  type: 'object', required: ['path', 'real', 'verdict', 'why'],
  properties: {
    path: { type: 'string' },
    real: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-works', 'confirmed-fails', 'plausible-but-unproven', 'marketing-hype', 'spec-fails'] },
    why: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['summary', 'mainBuild', 'companionNode', 'deadEnd', 'recommendation'],
  properties: {
    summary: { type: 'string' },
    mainBuild: { type: 'string', description: 'Can it go in the WRX80 main build? Which path, with what verdict?' },
    companionNode: { type: 'string', description: 'Could it be used in a separate cheap node (native SO-DIMM mini-PC etc.) in the ggrun multi-node/companion architecture?' },
    deadEnd: { type: 'string', description: 'If no viable path, the concrete reasons (pin count, voltage, SPD, controller, ECC) why not.' },
    recommendation: { type: 'string' },
  },
}

// ─── Phase 1: investigate each reuse path independently ───
phase('Investigate')
log('Investigating each SO-DIMM reuse path independently...')

const PATHS = [
  'Passive SO-DIMM → DIMM adapter (e.g. $13 AliExpress/Amazon DDR4 SO-DIMM-to-DIMM cards): do ANY actually work for DDR4 in a desktop/workstation board? Real failure modes (pin 260 vs 288, voltage, SPD length, physical height vs CPU cooler, board BIOS/SPD rejection)? Any confirmed boot on a consumer/workstation board? What about on a Threadripper PRO / WRX80-class board with 8-channel ECC-capable controller?',
  'The 64GB DDR4 SO-DIMM part itself: what is a 64GB DDR4 laptop module likely to be — ECC or non-ECC, which voltage (1.2V), which dies (16Gb), which SPD? Is 64GB DDR4 SO-DIMM even a real/common part, or is the user maybe mis-measuring (e.g. it is 2×32GB, or DDR4-2666, or it is a 64GB kit)? How to identify it definitively (labels, CPU-Z)?',
  'Native SO-DIMM boards: which desktop/server/mini-ITX boards natively use DDR4 SO-DIMM slots (e.g. ASRock X299E-ITX/ac, AM4 mini-ITX, NUC, embedded server boards)? Could this stick go in such a board as a SEPARATE cheap node — and would that node be useful in a llama.cpp/ggrun multi-node or companion/worker role (e.g. as a small worker/reviewer, or a draft/spec-draft node)?',
  'Electromechanical/practical reuse of the SO-DIMM on the WRX80 build: soldering/re-ball, an m.2-style SO-DIMM carrier, an external SO-DIMM box (Oculink/USB), a SO-DIMM riser into a 288-pin slot. Are ANY of these production-viable, or all hobby-experimental/unreliable?',
]

const paths = await parallel(PATHS.map((p) => () =>
  agent(
    'Investigate this specific path for reusing a 64GB DDR4 laptop SO-DIMM in a desktop/server build.\n\nPath: ' + p + '\n\nUser context: ' + USER + '\n\n' +
    'Research the REAL evidence (manufacturer specs, datasheets, forum threads, adapter product listings with real reviews). Distinguish marketing hype from confirmed working results. For each sub-question give a sourced answer.\n\n' +
    'Return: path (the verdict in one line), works (bool — can this path put the SO-DIMM to real use in the build or architecture?), confidence, why (the evidence-based reasoning), details (specifics: which adapters/boards/parts, prices if DE), caveats.\n\n' +
    'Be brutally honest. A "no" with the specific reason (pin count, voltage, SPD, controller) is a valid, valuable answer.',
    { label: 'path:' + p.slice(0, 20), phase: 'Investigate', schema: PATH_SCHEMA, stallMs: 2147483647 }
  )
))

const anyWorks = paths.filter(Boolean).filter((r) => r.works)
log(`Paths investigated: ${paths.filter(Boolean).length}; claims "works": ${anyWorks.length}`)

// ─── Phase 2: adversarially verify any positive claim ───
phase('Verify')
log('Adversarially verifying any "it works" claim...')

const verified = await parallel(paths.filter(Boolean).filter((r) => r.works).map((r) => () =>
  agent(
    'Adversarially REFUTE this claim that a 64GB DDR4 SO-DIMM can be reused via this path.\n\nPath: ' + r.path + '\nClaim: ' + JSON.stringify(r) + '\n\n' +
    'Search for counter-evidence: does this actually work in a real build, or is it a product-listing fantasy / forum anecdote that fails at purchase time? Would a German user reliably reproduce it?\n\n' +
    'Return: path, real (bool), verdict, why.',
    { label: 'verify:' + r.path.slice(0, 16), phase: 'Verify', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
  )
))

const stillWorks = verified.filter(Boolean).filter((v) => v.real)
log(`After adversarial verify: ${stillWorks.length}/${verified.length} positive claims survive`)

// ─── Phase 3: find REAL product links for board + CPU ───
phase('Listings')
log('Finding real clickable DE listings for the WRX80 board + 3955WX CPU...')

const BUY_PARTS = [
  { part: 'WRX80 board (Supermicro M12SWA-TF, or Gigabyte MC62-G41 / ASUS WRX80E-SAGE / ASRock Rack WRX80D8-2T)', query: 'M12SWA-TF OR MC62-G41 OR WRX80E-SAGE OR WRX80D8-2T kaufen gebraucht Deutschland eBay Kleinanzeigen', detail: 'The single make-or-break: a WRX80 sWRX8 board that wires 6 true x16 slots, obtainable in DE now.' },
  { part: 'Threadripper PRO 3955WX CPU (sWRX8)', query: 'Threadripper PRO 3955WX kaufen gebraucht Deutschland eBay Kleinanzeigen Preis', detail: 'The €249 Dortmund Kleinarzene offer + any other real DE buy-now for the 16-core WX CPU.' },
  { part: 'TR4/sWRX8 CPU cooler', query: 'Noctua NH-U14S TR4 SP3 Threadripper K[EFC kaufen Deutschland Preis', detail: 'The ~€90 TR4/sWRX8-specific cooler (e.g. Noctua NH-U14S TR4-3D).' },
  { part: '64GB DDR4 SO-DIMM (the user stick, for companion-node comparison)', query: '64GB DDR4 SO-DIMM 260 pin kaufen eBay Deutschland Preis', detail: 'What the user\'s 64GB laptop stick is worth / whether a companion board (NUC-style) that uses it is cheap.' },
]

const listings = await parallel(BUY_PARTS.map((p) => () =>
  agent(
    'Find REAL, currently-live, CLICKABLE German product listings for this part the user needs. Use the DuckDuckGo search + content-fetch tools and actually open the listings.\n\nPart: ' + p.part + '\nQuery: ' + p.query + '\nDetail: ' + p.detail + '\n\nUser: Germany (Bonn/Bonn). Budget €1000 cash. The build reuses ~160-192GB UDIMM on Threadripper PRO/WRX80.\n\n' +
    'CRITICAL — mark each real link with: obtainable (yes/maybe/no), origin (DE, EU, china-import), availableNow (buy-it-now-now, vs auction). Flag China-import traps (3-6 wk ship, wrong variant) explicitly. Prefer eBay.de BIN, Kleinarzeigen.de (Bonn/NRW), DE retailers (Geizbars, MindFactory, IT-2018, renewtech.de, servermarket, Future-X, etc.).\n\n' +
    'Return part + bestPick (the one best obtainable DE link: seller + price + why) + the links[] array. For each link include the URL, seller, priceEUR, obtainable, origin, availableNow, note. If a part is not relatable in DE, say so with the best listing found.\n\n' +
    'Be honest: give REAL URLs that currently resolve, not guesses.',
    { label: 'list:' + p.part.slice(0, 24), phase: 'Listings', schema: LIST_SCHEMA, stallMs: 2147483647 }
  )
))

const listed = listings.filter(Boolean)
log(`Listings found: ${listed.length}/${BUY_PARTS.length}`)

// ─── Phase 4: synthesize ───
phase('Synthesize')
const synth = await agent(
  'Synthesize the FINAL answer on whether the user can reuse their 64GB DDR4 laptop SO-DIMM.\n\n' +
  'User: ' + USER + '\n\n' +
  'Investigation results: ' + JSON.stringify(paths.filter(Boolean)) + '\n\n' +
  'Adversarial verification: ' + JSON.stringify(verified.filter(Boolean)) + '\n\n' +
  'REAL DE listings found (for the main build parts + SO-DIMM comparison): ' + JSON.stringify(listed.map((l) => ({ part: l.part, bestPick: l.bestPick, links: l.links }))) + '\n\n' +
  'Write the decision:\n' +
  '  1. MAIN BUILD: Can it go in the WRX80/Threadripper-PRO build (the main target)? Via which path, with which verdict?\n' +
  '  2. COMPANION NODE: Is there a genuine alternative that makes the stick useful (e.g. a cheap native-SO-DIMM mini-PC / NUC as a companion/worker node in the ggrun multi-node architecture)? Cost, what it would run, whether it is worth it for MoE inference.\n' +
  '  3. DEAD END: If no viable path, give the concrete technical reasons (pin count 260 vs 288, voltage, SPD length, memory-controller rejection, ECC, physical height) so the user understands WHY, definitively.\n' +
  '  4. THE VERIFIED PRODUCT LINKS (from the listings results) — for the WRX80 board and 3955WX CPU specifically, list the best obtainable DE link(s) (seller, price, URL) the user can actually click and buy, plus the TR4 cooler. Be explicit which are confirmed obtainable DE-stock vs China-import traps.\n' +
  '  5. RECOMMENDATION: the honest bottom line — is the SO-DIMM worth any effort, or just exclude it?\n\n' +
  'Be specific and sourced. Do not pad. This is the user\'s final answer on the SO-DIMM.\n\n' +
  'Return the structured synthesis.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  paths: paths.filter(Boolean),
  verified: verified.filter(Boolean),
  listings: listed,
  synthesis: synth,
}