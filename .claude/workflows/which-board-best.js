// WHICH MOTHERBOARD IS ACTUALLY BEST? — Adjudicate among the 5 double-verified live boards
// (4x Bielefeld SAGE SE + 1x Hamburg SAGE SE WIFI) on real merits, not just price.
// User wants the BEST board, not necessarily the cheapest. Bonn/NRW, MoE server (DeepSeek-V4 etc.)
// 6 GPUs at full x16, reuse non-ECC UDIMM, 2TB RDIMM path later. All 5 are already double-verified
// live (2026-08-08, cheapest-board-double-verify wf_0b973600-51c) — this workflow adjudicates.
export const meta = {
  name: 'which-board-best',
  description: 'Adjudicate which of the 5 confirmed-live WRX80 boards is actually BEST (spec, condition, warranty, seller, fit) for the Bonn MoE server.',
  phases: [
    { title: 'Board-facts', detail: 'Each confirmed-live board: exact listing facts + technical spec verification' },
    { title: 'Judge', detail: 'Independent judges score each board on weighted criteria (price, condition, warranty, seller, fit)' },
    { title: 'Synthesize', detail: 'Ranked best-board verdict with the concrete pick + why + fallback' },
  ],
}

const USER = `User in BONN, NRW. MoE inference server (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. 6 GPUs at full x16 (7x TRUE PCIe4 x16 required), reuses ~160→256GB non-ECC DDR4 UDIMM, 8-channel, 2TB RDIMM path later. Budget €1000 platform. Wants the BEST board among the confirmed-live options — not necessarily the cheapest.
CONFIRMED-LIVE CANDIDATES (all double-verified 2026-08-08 by wf_0b973600-51c, A+B agreement):
  1. SAGE SE €650 — Kleinanzeigen 3452717091 (Bielefeld, used, TOP-rated seller, Versand ab 1,49€)
  2. SAGE SE €690 — Kleinanzeigen 3441121680 (Bielefeld, used, same seller)
  3. SAGE SE €700 — Kleinanzeigen 3434065584 (Bielefeld, used, same seller)
  4. SAGE SE €700 — Kleinanzeigen 3439028475 (Bielefeld, used, same seller)
  5. SAGE SE WIFI €888 — Kleinanzeigen 3477914853 (Hamburg, seller claims "neu und originalverpackt" = NOS new-in-box)
All 5 are ASUS WRX80E-SAGE SE family = 7x TRUE PCIe4 x16 + 8x DDR4 8ch + non-ECC UDIMM + RDIMM/LRDIMM 2TB. The WIFI variant adds on-board WLAN/BT (relevant? for a rack server likely NOT needed).`

const FACTS_SCHEMA = {
  type: 'object', required: ['board', 'priceEUR', 'url', 'verifiedFacts', 'conditionEvidence', 'sellerProfile', 'specFit'],
  properties: {
    board: { type: 'string' },
    priceEUR: { type: 'number' },
    url: { type: 'string' },
    verifiedFacts: { type: 'string', description: 'What the listing actually says (condition, included accessories, age, usage).' },
    conditionEvidence: { type: 'string', description: 'Concrete evidence of condition (e.g. "neu originalverpackt", "bis zuletzt in Verwendung", photos, description text).' },
    sellerProfile: { type: 'string', description: 'Seller reliability: age, rating, ad count, responsiveness signals.' },
    specFit: { type: 'string', description: 'Does it meet 7x x16 + non-ECC UDIMM + 8ch + 2TB? Any variant differences (WIFI).' },
  },
}
const JUDGE_SCHEMA = {
  type: 'object', required: ['board', 'priceEUR', 'totalScore', 'rank', 'priceScore', 'conditionScore', 'warrantyScore', 'sellerScore', 'fitScore', 'why'],
  properties: {
    board: { type: 'string' },
    priceEUR: { type: 'number' },
    totalScore: { type: 'number', description: '0-100 weighted composite.' },
    rank: { type: 'integer' },
    priceScore: { type: 'number', description: '0-100 (lower price = higher score; relative among the 5).' },
    conditionScore: { type: 'number', description: '0-100 (NOS/new-in-box > lightly used > used).' },
    warrantyScore: { type: 'number', description: '0-100 (new warranty > seller warranty > none).' },
    sellerScore: { type: 'number', description: '0-100 (seller age/rating/ad count).' },
    fitScore: { type: 'number', description: '0-100 (spec fit for MoE server: x16, UDIMM, channels, WIFI relevance, chassis fit).' },
    why: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['bestBoard', 'ranking', 'verdict', 'fallback', 'action'],
  properties: {
    bestBoard: { type: 'string', description: 'The single BEST board: name, price, URL, one-line why.' },
    ranking: { type: 'array', items: { type: 'object', required: ['board', 'priceEUR', 'rank', 'totalScore', 'url'], properties: { board: { type: 'string' }, priceEUR: { type: 'number' }, rank: { type: 'integer' }, totalScore: { type: 'number' }, url: { type: 'string' } } } },
    verdict: { type: 'string', description: 'Why this is the best board for the user — the judgment, not just price.' },
    fallback: { type: 'string', description: 'If the best board is gone/unreachable, the next-best pick.' },
    action: { type: 'string', description: 'Concrete next step.' },
  },
}

const BOARDS = [
  { name: 'SAGE SE €650', priceEUR: 650, url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-wrx80-mainboard-amd-threadripper/3452717091-225-1059' },
  { name: 'SAGE SE €690', priceEUR: 690, url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-wrx80-mainboard-amd-threadripper/3441121680-225-1059' },
  { name: 'SAGE SE €700 (3434065584)', priceEUR: 700, url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-mainboard-amd-threadripper/3434065584-225-1059' },
  { name: 'SAGE SE €700 (3439028475)', priceEUR: 700, url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-wrx80-mainboard-amd-threadripper/3439028475-225-1059' },
  { name: 'SAGE SE WIFI €888 (NOS)', priceEUR: 888, url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-wifi-workstation-mainboard/3477914853-225-9489' },
]

// ─── Phase 1: verify listing facts per board ───
phase('Board-facts')
log('Verifying exact listing facts per board (condition, seller, spec)...')

const facts = await parallel(BOARDS.map((b) => () =>
  agent(
    'Verify the EXACT listing facts for this confirmed-live board, for a best-board adjudication.\n\n' +
    'Board: ' + b.name + ' @ €' + b.priceEUR + '\nURL: ' + b.url + '\n\nUser: ' + USER + '\n\n' +
    'Fetch the listing (DDG content-fetch; Kleinanzeigen may need retries). Report the concrete facts:\n' +
    '  1. CONDITION — exact wording: "neu und originalverpackt" (new-in-box) vs "gebraucht" / "bis zuletzt in Verwendung" / "defekt"? Age, usage, what is included (I/O shield, cables, box, accessories).\n' +
    '  2. SELLER — private vs commercial, account age, rating badges, number of active ads, any warning signs.\n' +
    '  3. SPEC — confirm 7x PCIe4 x16, 8x DDR4 8ch, non-ECC UDIMM + RDIMM/LRDIMM 2TB. Is it the non-WIFI or WIFI variant? Any photos of the board (visible condition)?\n' +
    '  4. PRICE/LOGISTICS — exact price, shipping cost to Bonn, pickup option.\n\n' +
    'Return the structured facts. Be concrete and specific — quote the listing text where possible.',
    { label: 'facts:' + b.name.slice(0, 16), phase: 'Board-facts', schema: FACTS_SCHEMA, stallMs: 2147483647 }
  )
))
const factsOK = facts.filter(Boolean)
log(`Board facts verified: ${factsOK.length}/${BOARDS.length}`)

// ─── Phase 2: independent judges score each board ───
phase('Judge')
log('Independent judges scoring each board on weighted criteria...')

const judge = await agent(
  'Act as an independent judge. Score the BEST board among the confirmed-live candidates for this user, using the verified facts.\n\n' +
  'User: ' + USER + '\n\n' +
  'Verified facts per board: ' + JSON.stringify(factsOK) + '\n\n' +
  'Score each board 0-100 on five weighted criteria (weighted: price 30%, condition 25%, warranty 15%, seller 15%, fit 15%):\n' +
  '  - PRICE: lower price = higher score, RELATIVE among the 5.\n' +
  '  - CONDITION: new-in-box/NOS > lightly used > used. Evidence matters.\n' +
  '  - WARRANTY: new warranty > seller warranty > none. (Note: private Kleinanzeigen sales usually have NO Gewährleistung.)\n' +
  '  - SELLER: account age, rating, ad count, responsiveness signals.\n' +
  '  - FIT: spec fit for the MoE server — 7x x16, non-ECC UDIMM, 8ch, 2TB path. Is WIFI on the €888 variant a real benefit or irrelevant for a rack server? Chassis/fit.\n\n' +
  'Return per board: board, priceEUR, totalScore (0-100 weighted), rank (1=best), priceScore, conditionScore, warrantyScore, sellerScore, fitScore, why.\n\n' +
  'Be a FAIR judge — a board can be more expensive yet score higher if condition/warranty justify it, but a €238 premium for a WIFI chip you do not need on a rack server is a weak argument.',
  { label: 'judge', phase: 'Judge', schema: JUDGE_SCHEMA, stallMs: 2147483647 }
)

// ─── Phase 3: synthesize the best-board verdict ───
phase('Synthesize')
const synth = await agent(
  'Synthesize the FINAL "which board is actually BEST" verdict.\n\n' +
  'User: ' + USER + '\n\n' +
  'Verified facts: ' + JSON.stringify(factsOK) + '\n\n' +
  'Judge scores: ' + JSON.stringify(judge) + '\n\n' +
  'Write:\n' +
  '  1. BEST BOARD: the single best board — name, price, URL, one-line why. State it as a clear winner.\n' +
  '  2. RANKING: all 5 boards ranked with totalScore + URL.\n' +
  '  3. VERDICT: the reasoning — is the cheapest (€650) also the best? Does the €888 WIFI NOS justify its premium? For a rack MoE server, is WIFI a con (antenna clutter) or irrelevant?\n' +
  '  4. FALLBACK: if the best is gone/unreachable, the next-best.\n' +
  '  5. ACTION: concrete next step.\n\n' +
  'Be decisive and honest — give the user a clear winner, not a menu.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  facts: factsOK,
  judge: judge,
  synthesis: synth,
}
