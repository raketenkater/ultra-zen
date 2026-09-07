// DEEP-RESEARCH: What is the BEST WRX80 board out there?
// From prior research: only 3 WRX80 boards truly give 7x TRUE PCIe4 x16 + non-ECC UDIMM support:
//   - ASUS Pro WS WRX80E-SAGE SE / SE WIFI / SE WIFI II
//   - ASRock Rack WRX80D8-2T
//   - MSI WS WRX80
// The others fail: Supermicro M12SWA-TF (6x x16), Gigabyte MC62-G41 (6x+1x8), Gigabyte WRX80-SU8-IPMI
// (7x x16 but UDIMM-ECC-only, no non-ECC), ASRock WRX80 Creator (consumer, x8 slots).
// The user wants to know which of the qualifying WRX80 boards is genuinely BEST — on the axes that
// matter for their MoE server (6 GPUs full x16, non-ECC UDIMM now, 1TB RDIMM path, BIOS/AGESA for
// the 1TB fix, server features like IPMI/10GbE, real-world reliability, and obtainability in DE).
export const meta = {
  name: 'best-wrx80-board',
  description: 'Deep-research: which WRX80 board is genuinely BEST (SAGE SE vs WRX80D8-2T vs MSI WS WRX80) for the 6-GPU + UDIMM + 1TB-path MoE server?',
  phases: [
    { title: 'Spec-details', detail: 'Full spec compare of the 3 qualifying WRX80 boards (slots, UDIMM, RDIMM path, BIOS, features, form factor)' },
    { title: 'Real-world', detail: 'Reliability + real-world: 1TB builds, BIOS bugs, thermal, IPMI/BMC quality, known failures' },
    { title: 'Availability', detail: 'Obtainability in DE right now: which can you actually buy, at what real price' },
    { title: 'Synthesize', detail: 'The single BEST WRX80 board for this user + buy action' },
  ],
}

const USER = `User in BONN, Germany. MoE inference server (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. Requirements: 6 GPUs at FULL x16 (7x TRUE PCIe4 x16), runs NON-ECC DDR4 UDIMM now (~160→256GB), 8-channel, grows to 512GB-1TB RDIMM later on the SAME board (2TB proven impossible/marketing on all WRX80), budget ~€1000-1400 platform. Only user-verified-available board in DE: ASUS SAGE SE WIFI €888 (Kleinanzeigen 3477914853). The 1TB RDIMM path needs BIOS >= AGESA 1.0.0.5/1.0.0.7 (ASUS 1106+/1401).`

const SPEC_SCHEMA = {
  type: 'object', required: ['board', 'x16TrueCount', 'nonEccUdim', 'rdimmPath', 'biosAgesa', 'formFactor', 'serverFeatures', 'memChannels', 'evidence'],
  properties: {
    board: { type: 'string' },
    x16TrueCount: { type: 'integer', description: 'TRUE PCIe4 x16 slots.' },
    nonEccUdim: { type: 'boolean' },
    rdimPath: { type: 'string', description: 'RDIMM ceiling + which modules verified.' },
    biosAgesa: { type: 'string', description: 'Latest BIOS + AGESA, 1TB fix status.' },
    formFactor: { type: 'string', description: 'ATX/EEB/E-ATX + dimensions.' },
    serverFeatures: { type: 'string', description: 'IPMI/BMC, dual 10GbE, onboard video, etc.' },
    memChannels: { type: 'integer' },
    evidence: { type: 'string', description: 'Source URLs.' },
  },
}
const RELIABILITY_SCHEMA = {
  type: 'object', required: ['board', 'oneTbReports', 'biosQuality', 'knownFailures', 'ipmiBmc', 'thermal', 'reliabilityScore'],
  properties: {
    board: { type: 'string' },
    oneTbReports: { type: 'string', description: 'Real 1TB builds + sources.' },
    biosQuality: { type: 'string', description: 'BIOS update cadence, stability, bug history.' },
    knownFailures: { type: 'string', description: 'Known issues: bricking, PCIe corruption, slot failures, chipset fan, RMA reports.' },
    ipmiBmc: { type: 'string', description: 'IPMI/BMC quality for a headless server (ASRock/ASUS).' },
    thermal: { type: 'string', description: 'VRM/DIMM thermal, cooling needs at 6 GPU + 1TB.' },
    reliabilityScore: { type: 'number', description: '0-100.' },
  },
}
const AVAIL_SCHEMA = {
  type: 'object', required: ['board', 'obtainableDE', 'realPriceEUR', 'sources', 'note'],
  properties: {
    board: { type: 'string' },
    obtainableDE: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    realPriceEUR: { type: 'number' },
    sources: { type: 'array', items: { type: 'object', required: ['seller', 'priceEUR', 'url', 'status'], properties: { seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, status: { type: 'string' } } } },
    note: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['bestBoard', 'ranking', 'verdict', 'buyAction', 'risks'],
  properties: {
    bestBoard: { type: 'string', description: 'The single best WRX80 board: name, price, URL, why.' },
    ranking: { type: 'array', items: { type: 'object', required: ['board', 'score', 'priceEUR', 'url'], properties: { board: { type: 'string' }, score: { type: 'number' }, priceEUR: { type: 'number' }, url: { type: 'string' } } } },
    verdict: { type: 'string', description: 'The judgment: which board is best for the user\'s exact build, and why.' },
    buyAction: { type: 'string', description: 'Concrete: buy which board now, from where, what to verify.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

const BOARDS = [
  'ASUS Pro WS WRX80E-SAGE SE / SE WIFI / SE WIFI II',
  'ASRock Rack WRX80D8-2T',
  'MSI WS WRX80',
]

// ─── Phase 1: full spec compare ───
phase('Spec-details')
log('Full spec compare of the 3 qualifying WRX80 boards...')

const specs = await parallel(BOARDS.map((b) => () =>
  agent(
    'Full primary-source spec verification of this WRX80 board for a 6-GPU + UDIMM + 1TB-path MoE server.\n\n' +
    'Board: ' + b + '\n\nUser: ' + USER + '\n\n' +
    'Verify from manufacturer primary sources (vendor spec page, manual, QVL):\n' +
    '  1. TRUE PCIe4 x16 slot count (electrically, from CPU) — must be 7 for 6 GPUs + spare.\n' +
    '  2. NON-ECC UDIMM support (the user\'s free sticks must work).\n' +
    '  3. RDIMM/LRDIMM-3DS path + real ceiling (512GB? 1TB? which modules verified).\n' +
    '  4. Latest BIOS + AGESA version, the 1TB fix status (AGESA 1.0.0.5/1.0.0.7).\n' +
    '  5. Form factor + dimensions (ATX/EEB/E-ATX) for chassis fit.\n' +
    '  6. Server features: IPMI/BMC (ASRock Rack has IPMI?), dual 10GbE, onboard VGA, NVMe count.\n' +
    '  7. Memory channels (8) + max UDIMM (256GB).\n\n' +
    'Return the structured spec with evidence.',
    { label: 'spec:' + b.slice(0, 20), phase: 'Spec-details', schema: SPEC_SCHEMA, stallMs: 2147483647 }
  )
))
const specsOK = specs.filter(Boolean)
log(`Specs: ${specsOK.length}/${BOARDS.length}`)

// ─── Phase 2: real-world reliability ───
phase('Real-world')
log('Real-world reliability + 1TB builds + BIOS/IPMI quality...')

const reliability = await parallel(BOARDS.map((b) => () =>
  agent(
    'Research REAL-WORLD reliability + quality of this WRX80 board for a headless 6-GPU MoE server.\n\n' +
    'Board: ' + b + '\n\nUser: ' + USER + '\n\n' +
    'Find sourced real-world evidence (Level1Techs, reddit, STH, vendor forums):\n' +
    '  1. 1TB builds: anyone running 8x128GB RDIMM on this board? Which BIOS? Stable?\n' +
    '  2. BIOS quality: update cadence, stability, bug history (boot loops, memory bugs).\n' +
    '  3. Known failures: bricking, PCIe corruption, slot failures, chipset fan noise/failure, RMA rates.\n' +
    '  4. IPMI/BMC: quality for headless (ASRock Rack IPMI? ASUS BMC? remote console, fan control).\n' +
    '  5. Thermal: VRM + DIMM cooling needs at 6 GPU + 1TB (active memory cooling required?).\n' +
    '  6. Reliability score 0-100.\n\n' +
    'Return the structured reliability assessment.',
    { label: 'rel:' + b.slice(0, 20), phase: 'Real-world', schema: RELIABILITY_SCHEMA, stallMs: 2147483647 }
  )
))
const reliabilityOK = reliability.filter(Boolean)
log(`Reliability: ${reliabilityOK.length}/${BOARDS.length}`)

// ─── Phase 3: availability in DE ───
phase('Availability')
log('Availability of each qualifying WRX80 board in DE right now...')

const availability = await parallel(BOARDS.map((b) => () =>
  agent(
    'Find the REAL current availability + price of this WRX80 board in Germany.\n\n' +
    'Board: ' + b + '\n\nUser: ' + USER + '\n\n' +
    'Search DE: Kleinanzeigen.de, eBay.de, Geizhals/idealo, DE retailers (Mindfactory, jacob, OCTO24, Future-X, servermarket, renewtech, servershop24).\n\n' +
    'Known (2026-08-08): only ASUS SAGE SE WIFI €888 (Kleinanzeigen 3477914853) is user-verified available. WRX80D8-2T = discontinued/OOS everywhere. MSI WS WRX80 = scarce.\n\n' +
    'CRITICAL LESSON: remote Kleinanzeigen fetches lie (phantom listings) — the user MUST verify any link in a real browser. Mark each source: seller, priceEUR, url, status (available/listed-unknown/gone/trap).\n\n' +
    'Return: board, obtainableDE, realPriceEUR, sources[], note.',
    { label: 'avail:' + b.slice(0, 20), phase: 'Availability', schema: AVAIL_SCHEMA, stallMs: 2147483647 }
  )
))
const availabilityOK = availability.filter(Boolean)
log(`Availability: ${availabilityOK.length}/${BOARDS.length}`)

// ─── Phase 4: synthesize ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the verdict: which WRX80 board is genuinely BEST for this user\'s build?\n\n' +
  'User: ' + USER + '\n\n' +
  'Spec details: ' + JSON.stringify(specsOK) + '\n\n' +
  'Real-world reliability: ' + JSON.stringify(reliabilityOK) + '\n\n' +
  'DE availability: ' + JSON.stringify(availabilityOK) + '\n\n' +
  'Write the FINAL verdict:\n' +
  '  1. BEST BOARD: the single best WRX80 board for the user (SAGE SE family vs WRX80D8-2T vs MSI WS WRX80) — name, price, URL, why. Weigh: 7x TRUE x16, non-ECC UDIMM, 1TB RDIMM path (BIOS/AGESA), server features (IPMI/10GbE for a headless rack server), reliability, and — critically — OBTAINABILITY in DE (the ASUS is the only user-verified one; the ASRock/MSI are unorderable phantoms).\n' +
  '  2. RANKING: all 3 with score + price + URL.\n' +
  '  3. VERDICT: the judgment — is the best-on-paper board also the best obtainable? If the ASUS SAGE SE is the only one you can actually buy, that settles it. If the WRX80D8-2T (IPMI + dual 10GbE) is genuinely better on paper, note whether it\'s worth hunting for vs just buying the obtainable ASUS.\n' +
  '  4. BUY ACTION: concrete — buy which board now, from where, what to verify in a real browser.\n' +
  '  5. RISKS.\n\n' +
  'Be decisive and honest. The user needs a clear winner they can actually buy.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  specs: specsOK,
  reliability: reliabilityOK,
  availability: availabilityOK,
  synthesis: synth,
}
