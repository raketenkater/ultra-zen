// RE-VERIFICATION of the Bonn buy-list. User reports the board listing is no longer
// available. Re-fetch EVERY link live today, classify status (live / gone / changed),
// and for the board — the critical part — do a FRESH exhaustive search for any WRX80
// board obtainable in DE right now (all qualifying variants). Return verified current
// status per part + replacement links.
export const meta = {
  name: 'reverify-offers',
  description: 'Re-verify the WRX80 build buy-list links live; fresh exhaustive search for a DE-available WRX80 board.',
  phases: [
    { title: 'Recheck', detail: 'Re-fetch each buy-list + fallback link; classify live/gone/changed' },
    { title: 'Board-hunt', detail: 'Fresh exhaustive search: any WRX80 board obtainable in DE today' },
    { title: 'Synthesize', detail: 'Verified current buy-list with replacement links + status table' },
  ],
}

const USER = `User is in BONN, NRW, Germany. Building AMD Threadripper PRO 3955WX + ASUS WRX80E-SAGE SE rig for MoE inference (DeepSeek-V4 119GB etc.) + training. 6 GPUs at full x16, 8-channel DDR4, reuses free UDIMM (max to 256GB). Budget €1000 platform. The previously-verified board listing (Kleinanzeigen Bielefeld, €650, id 3452717091) is reported NO LONGER AVAILABLE. Need a fresh, obtainable WRX80 board in DE.`

const STATUS_SCHEMA = {
  type: 'object', required: ['part', 'url', 'status', 'evidence', 'currentPrice', 'replacement'],
  properties: {
    part: { type: 'string' },
    url: { type: 'string' },
    status: { type: 'string', enum: ['live', 'gone', 'sold', 'changed', 'blocked-unverifiable', 'trap'] },
    evidence: { type: 'string', description: 'What the fetch actually returned (title/error/404/listing text).' },
    currentPrice: { type: 'string' },
    replacement: { type: 'string', description: 'If gone: a verified live replacement URL + price, if one exists.' },
  },
}
const BOARD_SCHEMA = {
  type: 'object', required: ['found', 'bestPick', 'allCandidates'],
  properties: {
    found: { type: 'boolean', description: 'Is ANY WRX80 board (SAGE SE/WIFI, M12SWA-TF, MC62-G41, WRX80D8-2T, or other 7x x16) obtainable in DE now?' },
    bestPick: { type: 'object', required: ['board', 'seller', 'priceEUR', 'url', 'origin', 'shippingToBonn', 'why'], properties: { board: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string', enum: ['de', 'eu', 'china-import', 'unknown'] }, shippingToBonn: { type: 'string', enum: ['pickup', 'fast', 'slow', 'unknown'] }, why: { type: 'string' } } },
    allCandidates: { type: 'array', items: { type: 'object', required: ['board', 'seller', 'priceEUR', 'url', 'origin', 'status'], properties: { board: { type: 'string' }, seller: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' }, origin: { type: 'string' }, status: { type: 'string', enum: ['available', 'listed-unknown', 'gone', 'oos', 'trap'] } } } },
    note: { type: 'string', description: 'Market reality: is this a scarce/vanishing market right now?' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['statusTable', 'boardVerdict', 'updatedBuyList', 'totalEUR', 'action'],
  properties: {
    statusTable: { type: 'string', description: 'Markdown table: part, previous link, status now, current price, replacement.' },
    boardVerdict: { type: 'string', description: 'Is the board obtainable in DE today, at what price, from where. The user\'s #1 question.' },
    updatedBuyList: { type: 'array', items: { type: 'object', required: ['part', 'pick', 'priceEUR', 'url'], properties: { part: { type: 'string' }, pick: { type: 'string' }, priceEUR: { type: 'number' }, url: { type: 'string' } } } },
    totalEUR: { type: 'number' },
    action: { type: 'string', description: 'The concrete next step for a Bonn buyer today.' },
  },
}

const LINKS = [
  { part: 'Board (primary)', url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-wrx80-mainboard-amd-threadripper/3452717091-225-1059', prev: '€650 Bielefeld' },
  { part: 'Board (2nd, same seller)', url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-wrx80-mainboard-amd-threadripper/3441121680-225-1059', prev: '€690 Bielefeld' },
  { part: 'Board (Hamburg WIFI)', url: 'https://www.kleinanzeigen.de/s-anzeige/asus-pro-ws-wrx80e-sage-se-wifi-wrx80-mainboard/3411144431-225-10384', prev: '€888-999 Hamburg' },
  { part: 'Board (NEW asus-eshop)', url: 'https://www.ebay.de/itm/363649001294', prev: '€878.90 new SAGE SE WIFI' },
  { part: 'CPU (Dortmund)', url: 'https://www.kleinanzeigen.de/s-anzeige/amd-threadripper-3955wx-pro-fuer-128-lanes/3429670569-225-26172', prev: '€249 Dortmund pickup' },
  { part: 'CPU (Hamburg)', url: 'https://www.kleinanzeigen.de/s-anzeige/amd-ryzen-threadripper-pro-3955wx-fast-neu-ovp-kein-lock-/3470227341-225-9454', prev: '€240 Hamburg' },
  { part: 'Cooler (nbb)', url: 'https://www.notebooksbilliger.de/arctic+freezer+4u+m+rev+2+882388', prev: '€45.05 new' },
  { part: 'RAM (Kassel)', url: 'https://www.kleinanzeigen.de/s-anzeige/crucial-pro-64gb-ram-kit-2-32gb-ddr4-3200-cl22-cp32g4dfra32a/3470973644-225-4926', prev: '€150/stick exact-match' },
  { part: 'RAM (Petershagen)', url: 'https://www.kleinanzeigen.de/s-anzeige/crucial-pro-ddr4-3200-64gb-2x32gb-udimm-memtest-fehlerfrei/3472543417-225-10513', prev: '€300 2x exact-match' },
  { part: 'Chassis (Mönchengladbach)', url: 'https://www.kleinanzeigen.de/s-anzeige/inter-tech-4f28-mining-rack-server-gehaeuse/3456522262-225-1965', prev: '€60 4F28' },
  { part: 'Chassis (Monheim)', url: 'https://www.kleinanzeigen.de/s-anzeige/mining-case-4w2-mindfactory/3297416736-225-1116', prev: '€50 4W2' },
]

// ─── Phase 1: recheck every link ───
phase('Recheck')
log('Re-fetching each buy-list + fallback link live...')

const checked = await parallel(LINKS.map((l) => () =>
  agent(
    'Re-verify this URL is LIVE and still obtainable for a German buyer today.\n\n' +
    'Part: ' + l.part + '\nURL: ' + l.url + '\nPreviously: ' + l.prev + '\n\nUser: ' + USER + '\n\n' +
    'Use DuckDuckGo search + content-fetch to actually open/check this URL. Kleinanzeigen.de blocks scrapers (may need a logged-in browser) — if the direct fetch 403s/JS-walls, cross-check via DDG search of the listing title + the seller\'s other listings to infer live/gone. eBay.de item pages may 403 — use the search index. For retailer URLs (notebooksbilliger), fetch the product page.\n\n' +
    'CRITICAL: classify status honestly:\n' +
    '  live = confirmed active/available today\n' +
    '  gone = confirmed removed/dead/404 (listing deleted)\n' +
    '  sold = confirmed sold/ended\n' +
    '  changed = URL works but price/details differ\n' +
    '  blocked-unverifiable = cannot confirm either way (scraper wall) — say what you actually saw\n' +
    '  trap = China-import / wrong variant / bait\n' +
    'Give evidence: what the fetch returned. If gone/sold, try to find a live replacement via DDG search.\n\n' +
    'Return the structured status.',
    { label: 'recheck:' + l.part.slice(0, 22), phase: 'Recheck', schema: STATUS_SCHEMA, stallMs: 2147483647 }
  )
))

const checkedOK = checked.filter(Boolean)
const goneCount = checkedOK.filter((c) => ['gone', 'sold'].includes(c.status)).length
log(`Rechecked ${checkedOK.length}/${LINKS.length} links; gone/sold: ${goneCount}`)

// ─── Phase 2: fresh exhaustive board hunt ───
phase('Board-hunt')
log('Fresh exhaustive search for ANY WRX80 board obtainable in DE today...')

const boardHunt = await agent(
  'EXHAUSTIVELY find ANY WRX80 / sWRX8 board (AMD Threadripper PRO DDR4) obtainable in Germany RIGHT NOW.\n\n' +
  'User: ' + USER + '\n\n' +
  'The qualifying board families (must deliver 6 GPUs at FULL x16 — hard requirement):\n' +
  '  - ASUS Pro WS WRX80E-SAGE SE / SE WIFI (7x TRUE PCIe4 x16)\n' +
  '  - Supermicro M12SWA-TF (6x PCIe4 x16 — REJECT for 7x requirement but note as fallback)\n' +
  '  - Gigabyte MC62-G41 (6x x16 + 1x x8 — REJECT for 7x)\n' +
  '  - ASRock Rack WRX80D8-2T (7x x16)\n\n' +
  'Search EVERYWHERE live: Kleinanzeigen.de (search "WRX80", "WRX80E", "SAGE", "Threadripper PRO Mainboard", "sWRX8"), eBay.de (WRX80E-SAGE SE / M12SWA-TF / MC62-G41 / WRX80D8-2T, buy-now, DE-origin), DE retailers (Geizhals.de offers list, idealo, Mindfactory, Jacob, OCTO24, Future-X, servermarket, renewtech, servershop24, green-memory), eBay-Kleinanzeigen alternatives. Open the listings.\n\n' +
  'The user reported the €650 Bielefeld board is GONE — verify that + find any replacement. Prefer NRW/Bonn + fast DE shipping + buy-now. Flag China-import (3-6wk, no EU warranty) and wrong-variant traps explicitly.\n\n' +
  'Return: found (bool), bestPick (the single best obtainable board for a Bonn buyer: board, seller, priceEUR, url, origin, shippingToBonn, why), allCandidates (each: board, seller, priceEUR, url, origin, status), note (market reality).\n\n' +
  'Be brutally honest — if the board market is empty/vanishing in DE right now, say so plainly and give the best obtainable option (even if it is the new-asus-shop at €878.90 or an EU seller).',
  { label: 'board-hunt', phase: 'Board-hunt', schema: BOARD_SCHEMA, stallMs: 2147483647 }
)
log(`Board hunt: ${boardHunt && boardHunt.found ? 'FOUND' : 'NONE found'}`)

// ─── Phase 3: synthesize ───
phase('Synthesize')
const synth = await agent(
  'Synthesize the RE-VERIFIED buy-list for the user (Bonn, NRW).\n\n' +
  'User: ' + USER + '\n\n' +
  'Per-link recheck results: ' + JSON.stringify(checkedOK) + '\n\n' +
  'Fresh board hunt: ' + JSON.stringify(boardHunt) + '\n\n' +
  'Write:\n' +
  '  1. STATUS TABLE: markdown table — part | previous link/price | status now | current price | replacement.\n' +
  '  2. BOARD VERDICT (the #1 question): is a WRX80 board obtainable in DE today? From where, at what price? If the €650 is gone, what replaces it?\n' +
  '  3. UPDATED BUY-LIST: the verified-current set of parts with live replacement URLs + prices.\n' +
  '  4. TOTAL: platform total in EUR.\n' +
  '  5. ACTION: the concrete next step for a Bonn buyer today (what to message/buy first).\n\n' +
  'Give REAL URLs. Be honest about anything still unverifiable.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  recheck: checkedOK,
  boardHunt: boardHunt,
  synthesis: synth,
}
