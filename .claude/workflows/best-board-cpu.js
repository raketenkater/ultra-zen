// EXHAUSTIVE: the BEST motherboard + CPU combo for the user's build, obtainable in DE
// RIGHT NOW. User's requirements: 6 GPUs at FULL x16, reuse non-ECC UDIMM (~160GB→256GB),
// 8-channel DDR4 for MoE inference (DeepSeek-V4 119GB etc.) + training ambition, 2TB RDIMM
// path later, budget €1000 platform (board+CPU+cooler). Bonn/NRW. Germany.
// This is a FRESH hunt — the board market is scarce/vanishing (Bielefeld €650 possibly gone,
// Hamburg WIFI €888, new-channel EOL). Need the verified BEST board+CPU available TODAY.
export const meta = {
  name: 'best-board-cpu',
  description: 'Find the verified best WRX80 motherboard + Threadripper PRO CPU combo obtainable in DE today, adversarially verified.',
  phases: [
    { title: 'Board-hunt', detail: 'Exhaustive: every WRX80 board obtainable in DE now, per-channel, verified' },
    { title: 'CPU-hunt', detail: 'Exhaustive: every Threadripper PRO CPU obtainable in DE now (3955/3975/3995), price-perf' },
    { title: 'Verify', detail: 'Adversarially verify the best board + CPU offers are real/obtainable for Bonn' },
    { title: 'Synthesize', detail: 'Best board+CPU combo + build total + clickable buy links' },
  ],
}

const USER = `User in BONN, NRW, Germany. Build for local MoE inference (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. Requirements: 6 GPUs at FULL x16, reuse ~160GB→256GB non-ECC DDR4 UDIMM (hard cap 256GB, 8x32GB), 8-channel DDR4 for bandwidth (~204GB/s), 2TB RDIMM expansion path later on the SAME board. Budget €1000 cash for platform (board+CPU+cooler). DE used market + new retailers.
KNOWN: WRX80/sWRX8 is the platform. 3955WX (16c/32t) ~€240-249 NRW. SAGE SE boards scarce (Bielefeld €650 maybe gone, €690/€700 siblings live, Hamburg WIFI €888). New-channel EOL for SAGE. Need the verified BEST board+CPU obtainable today.`

const BOARD_SCHEMA = {
  type: 'object', required: ['found', 'bestPick', 'allCandidates', 'note'],
  properties: {
    found: { type: 'boolean' },
    bestPick: { type: 'object', required: ['board', 'seller', 'priceEUR', 'url', 'origin', 'shippingToBonn', 'why'], properties: { board: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, shippingToBonn: { type: 'string', enum: ['pickup', 'fast', 'slow', 'unknown'] }, why: { type: 'string' } } },
    allCandidates: { type: 'array', items: { type: 'object', required: ['board', 'seller', 'priceEUR', 'url', 'origin', 'status', 'x16Slots'], properties: { board: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string' }, status: { type: 'string', enum: ['available', 'listed-unknown', 'gone', 'oos', 'trap'] }, x16Slots: { type: 'string' } } } },
    note: { type: 'string', description: 'Market reality: scarcity, EOL, trap warnings.' },
  },
}
const CPU_SCHEMA = {
  type: 'object', required: ['found', 'bestPick', 'allCandidates', 'note'],
  properties: {
    found: { type: 'boolean' },
    bestPick: { type: 'object', required: ['cpu', 'seller', 'priceEUR', 'url', 'origin', 'shippingToBonn', 'why'], properties: { cpu: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, shippingToBonn: { type: 'string', enum: ['pickup', 'fast', 'slow', 'unknown'] }, why: { type: 'string' } } },
    allCandidates: { type: 'array', items: { type: 'object', required: ['cpu', 'seller', 'priceEUR', 'url', 'origin', 'status'], properties: { cpu: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string' }, status: { type: 'string', enum: ['available', 'listed-unknown', 'gone', 'oos', 'trap'] } } } },
    note: { type: 'string', description: 'Price-perf reality, China-import trap warning, real-DE 3975WX/3995WX prices.' },
  },
}
const VERIFY_SCHEMA = {
  type: 'object', required: ['item', 'realistic', 'verdict', 'why', 'adjustedEUR'],
  properties: {
    item: { type: 'string' },
    realistic: { type: 'boolean' },
    verdict: { type: 'string', enum: ['confirmed-obtainable', 'listed-only', 'import-trap', 'wrong-part', 'too-good', 'too-high', 'unverifiable'] },
    why: { type: 'string' },
    adjustedEUR: { type: 'number' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['boardPick', 'cpuPick', 'comboWhy', 'buildTotalEUR', 'buyLinks', 'risks', 'alternatives'],
  properties: {
    boardPick: { type: 'string', description: 'The single best board for a Bonn buyer: name, seller, price, URL.' },
    cpuPick: { type: 'string', description: 'The single best CPU: name, seller, price, URL.' },
    comboWhy: { type: 'string', description: 'Why this board+CPU pair is the best for 6x x16 + UDIMM + 2TB path + MoE.' },
    buildTotalEUR: { type: 'number', description: 'Board+CPU (+cooler if priced) total.' },
    buyLinks: { type: 'array', items: { type: 'object', required: ['part', 'url', 'priceEUR'], properties: { part: { type: 'string' }, url: { type: 'string' }, priceEUR: { type: 'number' } } } },
    risks: { type: 'array', items: { type: 'string' } },
    alternatives: { type: 'string', description: 'If the best pick sells out: the next-best board/CPU and any fallback.' },
  },
}

// ─── Phase 1: exhaustive board hunt ───
phase('Board-hunt')
log('Exhaustively hunting every WRX80 board obtainable in DE today...')

const boardHunt = await agent(
  'EXHAUSTIVELY find EVERY WRX80 / sWRX8 board obtainable in Germany RIGHT NOW, and pick the single best for the user.\n\n' +
  'User: ' + USER + '\n\n' +
  'All qualifying board families (must deliver 6 GPUs at FULL x16 = 7x or more TRUE PCIe4 x16):\n' +
  '  - ASUS Pro WS WRX80E-SAGE SE / SE WIFI / SE WIFI II (7x TRUE x16) — the anchor family\n' +
  '  - ASRock Rack WRX80D8-2T (7x x16)\n' +
  '  - Supermicro M12SWA-TF / -TF WIFI (6x x16 — REJECT for 7x but note as fallback)\n' +
  '  - Gigabyte MC62-G41 (6x x16 + 1x x8 — REJECT for 7x)\n' +
  '  - ASRock WRX80 Creator (consumer — slots 4/6 are x8 — REJECT)\n\n' +
  'Search EVERYWHERE live and OPEN listings: Kleinanzeigen.de (search "WRX80", "WRX80E", "SAGE", "Threadripper PRO Mainboard", "sWRX8", "WRX80E-SAGE SE WIFI Hamburg", "M12SWA", "MC62"), eBay.de (all WRX80 boards, buy-now + auctions, DE-origin), DE retailers (Geizhals offers list — authoritative, idealo, Mindfactory, Jacob, OCTO24, Future-X, mmcomputer, servermarket, renewtech, servershop24, green-memory, hardwaresuche).\n\n' +
  'Known recent state (2026-08-08) to VERIFY, not assume: Bielefeld SAGE SE 3452717091 @€650 (intermittently blocked); same-seller siblings 3441121680 @€690, 3434065584 @€700; Hamburg SAGE SE WIFI @€888 VB (posted 06.08); asus-eshop eBay new SAGE SE WIFI @€899 (sold-out signal "9 verkauft 0 verfügbar"); ASRock WRX80D8-2T discontinued/OOS; new SAGE WIFI/WIFI II EOL.\n\n' +
  'Find ALL currently-available candidates (status available/listed-unknown), NOT just the known ones. For each: board, seller, priceEUR, url, origin, status, x16Slots. Then bestPick = the single best obtainable for a Bonn buyer (7x TRUE x16 required; prefer NRW/Bonn pickup + fast DE ship + buy-now; flag China-import traps).\n\n' +
  'Return the structured result. Be brutally honest about the scarcity.',
  { label: 'board-hunt', phase: 'Board-hunt', schema: BOARD_SCHEMA, stallMs: 2147483647 }
)
log(`Board hunt: ${boardHunt && boardHunt.found ? 'FOUND' : 'NONE'}`)

// ─── Phase 2: exhaustive CPU hunt ───
phase('CPU-hunt')
log('Exhaustively hunting every Threadripper PRO CPU obtainable in DE today...')

const cpuHunt = await agent(
  'EXHAUSTIVELY find EVERY AMD Threadripper PRO CPU (sWRX8) obtainable in Germany RIGHT NOW, and pick the single best price-perf for the user.\n\n' +
  'User: ' + USER + '\n\n' +
  'The sWRX8 PRO lineup (8-channel, 128 lanes, ECC/non-ECC UDIMM + RDIMM to 2TB):\n' +
  '  - Threadripper PRO 3945WX (12c/24t, 4.0GHz) — cheapest entry\n' +
  '  - Threadripper PRO 3955WX (16c/32t, 3.9GHz) — the known sweet spot (~€240-249 NRW)\n' +
  '  - Threadripper PRO 3975WX (32c/64t, 3.5GHz) — better CPU-expert MoE (more cores on CPU); real DE price €2,127-3,204; sub-€900 = China-import trap\n' +
  '  - Threadripper PRO 3995WX (64c/128t, 2.7GHz) — top; real DE price much higher\n' +
  'NOTE: 3960X/3970X/3990X are NON-PRO (sTRX4/TRX40) — WRONG socket, do NOT include as candidates (but note if cheap).\n\n' +
  'Search EVERYWHERE and OPEN listings: Kleinanzeigen.de (3955WX, 3975WX, 3995WX, "Threadripper PRO"), eBay.de (DE-origin buy-now; 403-wall → use search index + aggregators like picclick), Geizhals/idealo (retail, likely EOL/0 offers).\n\n' +
  'Known recent state (2026-08-08): Dortmund 3955WX 3429670569 @€249 (Patrick Dreyer, NRW pickup); Hamburg 3955WX 3470227341 @€240 VB (Oliver, NOS, no vendor lock, ~10mo warranty); Dortmund same-seller 2nd 3955WX @€249. VERIFY these + find any 3945WX/3975WX/3995WX deals.\n\n' +
  'For EACH candidate: cpu, seller, priceEUR, url, origin, status. Then bestPick = the single best obtainable price-perf for a Bonn buyer (3955WX 16c at €240-249 likely wins vs 3945WX 12c if that is ~€180-200; a 3975WX at real-DE ~€2k+ is out of €1000 budget — note as upgrade path).\n\n' +
  'Return the structured result. Be brutally honest about price-perf and the China-import trap on 3975WX/3995WX.',
  { label: 'cpu-hunt', phase: 'CPU-hunt', schema: CPU_SCHEMA, stallMs: 2147483647 }
)
log(`CPU hunt: ${cpuHunt && cpuHunt.found ? 'FOUND' : 'NONE'}`)

// ─── Phase 3: adversarially verify the best board + CPU ───
phase('Verify')
log('Adversarially verifying the best board + CPU offers are real/obtainable for Bonn...')

const toVerify = []
if (boardHunt && boardHunt.bestPick) toVerify.push({ item: 'Board: ' + boardHunt.bestPick.board + ' @ €' + boardHunt.bestPick.priceEUR + ' from ' + boardHunt.bestPick.seller, data: boardHunt.bestPick })
if (cpuHunt && cpuHunt.bestPick) toVerify.push({ item: 'CPU: ' + cpuHunt.bestPick.cpu + ' @ €' + cpuHunt.bestPick.priceEUR + ' from ' + cpuHunt.bestPick.seller, data: cpuHunt.bestPick })

const verified = await parallel(toVerify.map((v) => () =>
  agent(
    'Adversarially REFUTE this offer for a Bonn (NRW) buyer, or confirm it.\n\n' +
    'Item: ' + v.item + '\nDetails: ' + JSON.stringify(v.data) + '\n\nUser: ' + USER + '\n\n' +
    'Is €' + (v.data && v.data.priceEUR) + ' actually obtainable today for a Bonn buyer — not listing-only, not China-import, not auction-start, not wrong variant, would the seller ship to Bonn (or pickable in NRW)? Does it survive shipping/VAT/no-stock/seller-won\'t-sell realities? Cross-check via DDG + content-fetch.\n\n' +
    'Return: item, realistic (bool), verdict, why, adjustedEUR.',
    { label: 'verify:' + (v.data && v.data.board ? v.data.board.slice(0, 20) : v.data.cpu.slice(0, 20)), phase: 'Verify', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
  )
))
const verifiedOK = verified.filter(Boolean)
log(`Verified: ${verifiedOK.filter((v) => v.realistic).length}/${toVerify.length} survive`)

// ─── Phase 4: synthesize ───
phase('Synthesize')
const synth = await agent(
  'Synthesize the BEST motherboard + CPU combo for the user\'s build, obtainable in DE today.\n\n' +
  'User: ' + USER + '\n\n' +
  'Board hunt: ' + JSON.stringify(boardHunt) + '\n\n' +
  'CPU hunt: ' + JSON.stringify(cpuHunt) + '\n\n' +
  'Verification: ' + JSON.stringify(verifiedOK) + '\n\n' +
  'Write the decision:\n' +
  '  1. BOARD PICK: the single best board for a Bonn buyer — exact name, seller, price, URL, why. Must be 7x TRUE x16.\n' +
  '  2. CPU PICK: the single best CPU — exact name, seller, price, URL, why. Price-perf within €1000 budget.\n' +
  '  3. COMBO WHY: why this board+CPU pair is the best for the user (6x x16 + UDIMM reuse + 2TB path + 8ch MoE).\n' +
  '  4. BUILD TOTAL: board+CPU (+cooler if you have a price) — must be ≤ €1000.\n' +
  '  5. BUY LINKS: clickable list.\n' +
  '  6. RISKS + ALTERNATIVES: what could sell out, and the next-best board/CPU fallback.\n\n' +
  'Be specific, sourced, honest. This is the user\'s buy decision.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  boardHunt: boardHunt,
  cpuHunt: cpuHunt,
  verified: verifiedOK,
  synthesis: synth,
}
