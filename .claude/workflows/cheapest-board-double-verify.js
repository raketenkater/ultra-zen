// FIND THE CHEAPEST board that meets the user's hard requirements, with DOUBLE verification
// that the offers are still present. The user explicitly wants: cheapest qualifying board
// (7x TRUE PCIe4 x16 + non-ECC UDIMM support + 8x DDR4 DIMM + 2TB RDIMM/LRDIMM path), and
// every candidate double-verified live (two independent agents confirm each offer).
// Context: Bonn/NRW. Board market is scarce. Known candidates (2026-08-08): Bielefeld SAGE SE
// €650/€690/€700/€700, Hamburg WIFI €888. Gigabyte WRX80-SU8-IPMI = TRAP (no non-ECC UDIMM).
// ASRock WRX80D8-2T discontinued/OOS. Need the CHEAPEST qualifying board, confirmed twice.
export const meta = {
  name: 'cheapest-board-double-verify',
  description: 'Find the cheapest WRX80 board meeting all requirements, double-verified live by two independent agents per offer.',
  phases: [
    { title: 'Find-cheapest', detail: 'Enumerate ALL qualifying boards cheapest-first (7x x16 + non-ECC UDIMM + 8 DIMM + 2TB)' },
    { title: 'Verify-A', detail: 'Independent verifier A re-fetches each candidate live' },
    { title: 'Verify-B', detail: 'Independent verifier B re-fetches each candidate live (double-check)' },
    { title: 'Synthesize', detail: 'Cheapest confirmed board + ranked list with both verifications' },
  ],
}

const USER = `User in BONN, NRW. Needs the CHEAPEST motherboard that meets ALL hard requirements:
  (1) 6 GPUs at FULL x16 → needs 7x TRUE PCIe4 x16 slots (6 GPUs + 1 spare; server/rack boards only)
  (2) Runs the user's non-ECC DDR4 UDIMM (~160GB→256GB) → MUST support non-ECC UDIMM (NOT UDIMM-ECC-only, NOT RDIMM-only)
  (3) 8x DDR4 DIMM slots, 8-channel
  (4) 2TB RDIMM/LRDIMM expansion path later on the same board
Platform is WRX80/sWRX8. Budget €1000 for platform (board+CPU+cooler). Cheapest board wins — price is the #1 constraint. DE used market.
TRAPS to reject regardless of price: Gigabyte WRX80-SU8-IPMI (7x x16 BUT UDIMM-ECC/RDIMM/LRDIMM only — NO non-ECC UDIMM), Supermicro M12SWA-TF (only 6x x16), Gigabyte MC62-G41 (6x x16 + 1x x8), ASRock WRX80 Creator (slots 4/6 x8), China-import WIFI II (€1300+), sub-€900 3975WX.`

const BOARD_CAND_SCHEMA = {
  type: 'object', required: ['found', 'cheapest', 'ranked', 'note'],
  properties: {
    found: { type: 'boolean' },
    cheapest: { type: 'object', required: ['board', 'seller', 'priceEUR', 'url', 'origin', 'shippingToBonn', 'meetsAllReqs'], properties: { board: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string' }, shippingToBonn: { type: 'string' }, meetsAllReqs: { type: 'string' } } },
    ranked: { type: 'array', items: { type: 'object', required: ['board', 'seller', 'priceEUR', 'url', 'origin', 'status'], properties: { board: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string' }, status: { type: 'string', enum: ['available', 'listed-unknown', 'gone', 'oos', 'trap'] } } } },
    note: { type: 'string' },
  },
}
const VERIFY_SCHEMA = {
  type: 'object', required: ['board', 'priceEUR', 'url', 'verified', 'verdict', 'evidence'],
  properties: {
    board: { type: 'string' },
    priceEUR: { type: 'number' },
    url: { type: 'string' },
    verified: { type: 'boolean', description: 'Confirmed LIVE and obtainable for a Bonn buyer.' },
    verdict: { type: 'string', enum: ['confirmed-live', 'gone', 'sold', 'blocked-unverifiable', 'trap', 'price-changed'] },
    evidence: { type: 'string', description: 'What the fetch actually returned (title/price/live markers/dead-listing).' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['cheapestConfirmed', 'doubleVerifyResult', 'fullRankedList', 'action'],
  properties: {
    cheapestConfirmed: { type: 'string', description: 'The cheapest board confirmed live by BOTH verifiers: name, seller, price, URL.' },
    doubleVerifyResult: { type: 'string', description: 'A/B verification summary — which offers both verifiers confirmed live.' },
    fullRankedList: { type: 'array', items: { type: 'object', required: ['board', 'priceEUR', 'url', 'verifyA', 'verifyB'], properties: { board: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, verifyA: { type: 'string' }, verifyB: { type: 'string' } } } },
    action: { type: 'string', description: 'The concrete next step for the Bonn buyer.' },
  },
}

// ─── Phase 1: enumerate qualifying boards cheapest-first ───
phase('Find-cheapest')
log('Enumerating ALL qualifying WRX80 boards cheapest-first...')

const cand = await agent(
  'Find the CHEAPEST WRX80 / sWRX8 motherboard obtainable in Germany that meets ALL the user\'s hard requirements, then rank all qualifying candidates by price.\n\n' +
  'User: ' + USER + '\n\n' +
  'Qualifying board families (must be 7x TRUE PCIe4 x16 AND support non-ECC UDIMM AND 8x DIMM AND 2TB RDIMM/LRDIMM path):\n' +
  '  - ASUS Pro WS WRX80E-SAGE SE / SE WIFI / SE WIFI II (7x x16, non-ECC UDIMM + RDIMM/LRDIMM) ✓\n' +
  '  - ASRock Rack WRX80D8-2T (7x x16, UDIMM+RDIMM) ✓ but discontinued/OOS — check if any DE stock remains\n' +
  '  - REJECT regardless of price: Gigabyte WRX80-SU8-IPMI (UDIMM-ECC only, no non-ECC), M12SWA-TF (6x x16), MC62-G41 (6x+1x8), ASRock WRX80 Creator (x8 slots), China-import WIFI II (€1300+)\n\n' +
  'Search EVERYWHERE live and OPEN listings: Kleinanzeigen.de (search "WRX80E", "WRX80", "SAGE SE", "sWRX8 Mainboard"), eBay.de (WRX80 board buy-now, DE-origin), Geizhals/idealo offers (incl. any remaining ASRock WRX80D8-2T stock), DE retailers (Mindfactory, jacob, OCTO24, mmcomputer, Future-X, Conrad).\n\n' +
  'Known candidates (2026-08-08) to VERIFY: Bielefeld SAGE SE €650 (ID 3452717091), €690 (ID 3441121680), €700 (ID 3434065584), €700 (another); Hamburg SAGE SE WIFI €888 VB. Verify each is still live, and search for ANY cheaper qualifying board (e.g. a used SAGE SE under €650, an ASRock WRX80D8-2T with DE stock, a rare M12SWA-TF discounted).\n\n' +
  'Return: found (bool), cheapest (the single cheapest qualifying board: board, seller, priceEUR, url, origin, shippingToBonn, meetsAllReqs), ranked (ALL qualifying candidates cheapest-first: board, seller, priceEUR, url, origin, status), note (market reality).\n\n' +
  'Be brutally honest — if €650 Bielefeld is the cheapest, say so. Search hard for anything under it before concluding.',
  { label: 'find-cheapest', phase: 'Find-cheapest', schema: BOARD_CAND_SCHEMA, stallMs: 2147483647 }
)
const candidates = (cand && cand.ranked) || []
log(`Cheapest: ${cand && cand.cheapest ? cand.cheapest.board + ' @ €' + cand.cheapest.priceEUR : 'NONE'}; candidates: ${candidates.length}`)

// ─── Phase 2: double-verify every candidate (two independent agents) ───
phase('Verify-A')
log('Independent verifier A re-fetching each candidate live...')
const verifyA = await parallel(candidates.map((c) => () =>
  agent(
    'DOUBLE-VERIFY this board listing is STILL LIVE and obtainable for a Bonn buyer RIGHT NOW (verifier A).\n\n' +
    'Board: ' + c.board + ' @ €' + c.priceEUR + '\nSeller: ' + c.seller + '\nURL: ' + c.url + '\nOrigin: ' + c.origin + '\n\nUser: ' + USER + '\n\n' +
    'Actually fetch/open the URL (DDG search + content-fetch). Kleinanzeigen.de may JS-wall or intermittently 403 — try multiple approaches (DDG cache, listing title search, seller search). Report exactly what you saw: title, price, live markers ("Nachricht senden", "Zur Merkliste", Ad-ID), or dead-listing message ("nicht mehr verfügbar").\n\n' +
    'Return: board, priceEUR, url, verified (bool — confirmed live today), verdict, evidence.',
    { label: 'verifyA:' + String(c.priceEUR), phase: 'Verify-A', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
  )
))
const verifyAOK = verifyA.filter(Boolean)
log(`Verifier A: ${verifyAOK.filter((v) => v.verified).length}/${verifyAOK.length} confirmed live`)

phase('Verify-B')
log('Independent verifier B re-fetching each candidate live (double-check)...')
const verifyB = await parallel(candidates.map((c) => () =>
  agent(
    'DOUBLE-VERIFY this board listing is STILL LIVE and obtainable for a Bonn buyer RIGHT NOW — INDEPENDENT SECOND CHECK (verifier B). Do NOT rely on any prior verification; fetch fresh yourself.\n\n' +
    'Board: ' + c.board + ' @ €' + c.priceEUR + '\nSeller: ' + c.seller + '\nURL: ' + c.url + '\nOrigin: ' + c.origin + '\n\nUser: ' + USER + '\n\n' +
    'Fetch/open the URL yourself (DDG search + content-fetch). Report what you independently saw: title, price, live markers, or dead-listing. Be adversarial — try to catch a listing that LOOKS live but is actually sold/gone.\n\n' +
    'Return: board, priceEUR, url, verified (bool), verdict, evidence.',
    { label: 'verifyB:' + String(c.priceEUR), phase: 'Verify-B', schema: VERIFY_SCHEMA, stallMs: 2147483647 }
  )
))
const verifyBOK = verifyB.filter(Boolean)
log(`Verifier B: ${verifyBOK.filter((v) => v.verified).length}/${verifyBOK.length} confirmed live`)

// ─── Phase 3: synthesize ───
phase('Synthesize')
const synth = await agent(
  'Synthesize the CHEAPEST board confirmed by DOUBLE verification, for the Bonn buyer.\n\n' +
  'User: ' + USER + '\n\n' +
  'Candidate list (cheapest-first): ' + JSON.stringify(candidates) + '\n\n' +
  'Verifier A results: ' + JSON.stringify(verifyAOK) + '\n\n' +
  'Verifier B results (independent double-check): ' + JSON.stringify(verifyBOK) + '\n\n' +
  'Write:\n' +
  '  1. CHEAPEST CONFIRMED: the cheapest board that BOTH verifiers confirmed live today — name, seller, price, URL.\n' +
  '  2. DOUBLE-VERIFY RESULT: which candidates passed BOTH verifications, and any A/B disagreement (that is the risk signal).\n' +
  '  3. FULL RANKED LIST: all qualifying boards with verifyA + verifyB status per offer.\n' +
  '  4. ACTION: the concrete next step (message which seller, buy which board).\n\n' +
  'A board must pass BOTH verifiers to be "confirmed". If the cheapest only passed one, flag it and prefer the next one that passed both.\n\n' +
  'Be specific and honest.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  candidates: candidates,
  verifyA: verifyAOK,
  verifyB: verifyBOK,
  synthesis: synth,
}
