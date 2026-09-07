// DEEP-RESEARCH: Is there a board that runs non-ECC UDIMM NOW (free RAM) AND grows to 2TB via
// RDIMM LATER on the same board AND gives 6 GPUs at FULL x16? The user wants future scalability —
// this is the hard question. We must exhaustively search for ANY such platform and adversarially
// verify whether the combination exists at all.
//
// The tension to resolve honestly: non-ECC UDIMM reuse is a workstation/HEDT feature (WRX80, TRX40),
// while 2TB RDIMM is a server feature (dual-EPYC, Xeon Scalable) that is RDIMM-only. The user's
// sticks are NON-ECC desktop UDIMM. We must find if ANY platform bridges both, and if not, map the
// honest tradeoff (which path gives the most future scalability).
export const meta = {
  name: 'find-scalable-board',
  description: 'Find ANY board that runs non-ECC UDIMM now + 2TB RDIMM later + 6 GPUs full x16. Exhaustive search + adversarial verify of the 2TB ceiling + honest scalability tradeoff.',
  phases: [
    { title: 'Hunt', detail: 'Exhaustive search: any platform meeting UDIMM-now + 2TB-later + 6x x16? (WRX80 family, EPYC, Xeon, exotic HEDT)' },
    { title: 'WRX80-2TB', detail: 'Can ANY WRX80 board exceed 512GB? BIOS workarounds, module configs, NEMIX/Kingston claims, newer BIOS' },
    { title: 'Tradeoffs', detail: 'The honest scalability map: what each viable platform gives up (UDIMM vs 2TB vs x16)' },
    { title: 'Synthesize', detail: 'Does the ideal board exist? If not: the best scalable path + the UDIMM-vs-2TB decision' },
  ],
}

const USER = `User in BONN, Germany. MoE inference server (DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. CRITICAL NEW REQUIREMENT — the user wants a board that is FUTURE-SCALABLE:
  (1) Runs NON-ECC DDR4 UDIMM NOW (user OWNS 2x Corsair Vengeance LPX 3200 CL16 1.35V + 2x Crucial 3200 CL22 1.2V + 2x16GB = ~160GB, free RAM is the point)
  (2) Grows to ~2TB via RDIMM LATER ON THE SAME BOARD (no platform swap) — this is now a HARD requirement, not "nice to have"
  (3) 6 GPUs at FULL x16 (7x TRUE PCIe4 x16 slots)
  (4) 8-channel DDR4 (memory bandwidth is the MoE lever)
  (5) Good for big MoE inference + training
  Budget ~€1000-1400 platform. Current rig i7-10700K 2ch.
  The user was told the WRX80's "2TB" path is marketing (realistic ~512GB ceiling, 1TB fails to POST). They want to re-verify whether ANY board truly does UDIMM-now + 2TB-later + 6x x16.`

const HUNT_SCHEMA = {
  type: 'object', required: ['platform', 'meetsAll', 'udimmNow', 'tbLater', 'gpuX16', 'realPriceEUR', 'obtainable', 'verdict'],
  properties: {
    platform: { type: 'string' },
    meetsAll: { type: 'boolean', description: 'Does it truly do UDIMM-now + 2TB-later + 6x x16?' },
    udimmNow: { type: 'string', description: 'Can it run the user\'s non-ECC UDIMM now? (Real, not spec-marketing.)' },
    tbLater: { type: 'string', description: 'Can it reach 2TB via RDIMM later on the SAME board? (Real-world verified, not just spec.)' },
    gpuX16: { type: 'string', description: '6 GPUs at FULL x16? How many TRUE x16 slots?' },
    realPriceEUR: { type: 'number' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    verdict: { type: 'string', description: 'The honest answer for this platform.' },
  },
}
const WRX80_SCHEMA = {
  type: 'object', required: ['ceiling', 'oneTbWorks', 'workarounds', 'biosState', 'moduleConfigs', 'verifiedReports', 'verdict'],
  properties: {
    ceiling: { type: 'string', description: 'The REAL verified max capacity on WRX80 boards.' },
    oneTbWorks: { type: 'boolean', description: 'Does ANY config reach 1TB? (4x128+4x64? 8x128 with new BIOS? LRDIMM?)' },
    workarounds: { type: 'string', description: 'BIOS workarounds, module configs, AGESA updates that might unlock >512GB.' },
    biosState: { type: 'string', description: 'Latest BIOS, any memory-capacity fixes, EOL status.' },
    moduleConfigs: { type: 'string', description: 'Specific tested configs: 8x64=512, 7x128=896, 8x128=1TB(fail), 4x128+4x64, LRDIMM variants.' },
    verifiedReports: { type: 'string', description: 'Real-world reports (Level1Techs, reddit, STH) of >512GB on WRX80, with sources.' },
    verdict: { type: 'string', description: 'Is the 2TB path truly impossible on WRX80, or just unproven?' },
  },
}
const TRADEOFF_SCHEMA = {
  type: 'object', required: ['platform', 'udimmReuse', 'maxRamReal', 'gpuX16', 'tbPath', 'futureScalability', 'honestTradeoff'],
  properties: {
    platform: { type: 'string' },
    udimmReuse: { type: 'string', description: 'Can it use the user\'s non-ECC UDIMM? (Yes/No + capacity.)' },
    maxRamReal: { type: 'string', description: 'The REAL max RAM, verified.' },
    gpuX16: { type: 'string' },
    tbPath: { type: 'string', description: 'Is there a real 2TB+ path, and at what cost?' },
    futureScalability: { type: 'string', description: '0-100: how future-scalable is this for the user\'s goals (bigger models, training)?' },
    honestTradeoff: { type: 'string', description: 'What this platform makes you give up.' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['idealExists', 'bestScalablePath', 'decision', 'recommendation', 'risks'],
  properties: {
    idealExists: { type: 'boolean', description: 'Does ANY board truly do UDIMM-now + 2TB-later + 6x x16?' },
    bestScalablePath: { type: 'string', description: 'The best path for future scalability, given the honest constraints.' },
    decision: { type: 'string', description: 'The UDIMM-vs-2TB tradeoff decision the user must make, framed clearly.' },
    recommendation: { type: 'string', description: 'Concrete: what to buy now, what the upgrade path is.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: exhaustive hunt for ANY platform meeting all 3 ───
phase('Hunt')
log('Exhaustively hunting for ANY board: UDIMM-now + 2TB-later + 6x x16...')

const PLATFORMS = [
  'WRX80 / Threadripper PRO (SAGE SE, WRX80D8-2T, MSI WS WRX80, M12SWA-TF, MC62-G41): 7x x16 + UDIMM now, but the 2TB RDIMM path was flagged as marketing (1TB fails, ~512GB ceiling). Can ANY WRX80 board truly exceed 512GB to 1-2TB?',
  'Single-socket EPYC SP3 (e.g. Supermicro H12SSL, H12SST, Gigabyte MZ32): 8ch, big RDIMM capacity (2TB+), 128 lanes. But does ANY EPYC board accept NON-ECC UDIMM? (EPYC is ECC RDIMM/UDIMM-ECC. Non-ECC? Likely NO — verify.) If it can\'t run the user\'s non-ECC sticks, it fails requirement 1.',
  'Dual-socket EPYC (H11DSi, AS-4124GS-TNR): 2TB+, but RDIMM-only + 2x PCIe3 x16. Fails 6-GPU x16. Any variant with more x16?',
  'Intel Xeon W-3300 / W-3400 (LGA4189/4677): ECC RDIMM/UDIMM. Does it accept NON-ECC UDIMM? (LGA4189 = ECC-only, LGA4677 = DDR5.) Any Xeon that runs non-ECC UDIMM + 2TB?',
  'EXOTIC options: HEDT/consumer with >8 DIMM slots (X299, TRX50, WRX90), or any board where non-ECC UDIMM co-exists with RDIMM to big capacity. Does ANY board in existence run BOTH non-ECC UDIMM (user\'s free sticks) AND RDIMM to 1-2TB?',
]
const hunt = await parallel(PLATFORMS.map((p) => () =>
  agent(
    'Exhaustively evaluate this platform family for the user\'s hard question: does ANY board in it truly run NON-ECC UDIMM now AND grow to ~2TB via RDIMM later on the SAME board AND give 6 GPUs at full x16?\n\n' +
    'Platform: ' + p + '\n\nUser: ' + USER + '\n\n' +
    'Search manufacturer specs, manuals, QVLs, and REAL-WORLD forum reports (Level1Techs, reddit, STH). The user\'s sticks are NON-ECC desktop UDIMM (Corsair Vengeance LPX + Crucial Pro).\n\n' +
    'Answer honestly: meetsAll (does ANY board in this family do all 3?), udimmNow (real), tbLater (real-world 2TB, not spec), gpuX16 (TRUE x16 count), realPriceEUR (DE used/new), obtainable, verdict.\n\n' +
    'The truth matters more than a positive answer — if the family cannot do it, say so plainly with the specific reason.',
    { label: 'hunt:' + p.slice(0, 22), phase: 'Hunt', schema: HUNT_SCHEMA, stallMs: 2147483647 }
  )
))
const huntOK = hunt.filter(Boolean)
const anyMeets = huntOK.filter((h) => h.meetsAll)
log(`Platform families hunted: ${huntOK.length}; meet ALL 3 goals: ${anyMeets.length}`)

// ─── Phase 2: verify the WRX80 2TB ceiling deeply ───
phase('WRX80-2TB')
log('Deep-verifying: can ANY WRX80 board exceed 512GB? BIOS, configs, claims...')

const wrx80 = await agent(
  'Deep-research: can ANY WRX80 board (ASUS SAGE SE/WIFI, ASRock WRX80D8-2T, MSI WS WRX80) truly reach 1-2TB RAM? The prior finding said 1TB fails to POST (Level1Techs Q-code loop on 5995WX + 8x128GB), realistic ceiling ~512GB (8x64GB Kingston RDIMM works). VERIFY this rigorously and look for workarounds.\n\n' +
  'User: ' + USER + '\n\n' +
  'Research:\n' +
  '  1. CEILING: what is the actual real-world max on WRX80? (896GB? 512GB? 1TB?)\n' +
  '  2. Does ANY config reach 1TB: 4x128+4x64? 8x128 with a newer BIOS? LRDIMM 128GB? 2R vs 4R? Mixing densities?\n' +
  '  3. WORKAROUNDS: newer BIOS/AGESA that fixes the 1TB MMIO bug? NEMIX 2TB kit claim (real or fantasy)? Kingston "1TB w/ 128GB LRDIMM" claim? Any settings?\n' +
  '  4. BIOS STATE: latest BIOS for SAGE SE/WRX80D8-2T, EOL status, any capacity fixes.\n' +
  '  5. VERIFIED REPORTS: every real-world >512GB report you can find, with source + config.\n\n' +
  'Verdict: is the 2TB path TRULY impossible on WRX80, or just unproven/requires specific config? Be brutally honest — this determines whether the user\'s "2TB later" goal is achievable on WRX80 at all.',
  { label: 'wrx80-2tb', phase: 'WRX80-2TB', schema: WRX80_SCHEMA, stallMs: 2147483647 }
)
log(`WRX80 2TB: 1TB works=${wrx80 && wrx80.oneTbWorks}, ceiling=${wrx80 && wrx80.ceiling}`)

// ─── Phase 3: honest scalability tradeoffs ───
phase('Tradeoffs')
log('Mapping the honest scalability tradeoff per viable platform...')

const TRADEOFFS = [
  'WRX80/Threadripper PRO (SAGE SE €888): UDIMM now (160→256GB free), RDIMM later to the real ceiling (~512GB?), 7x TRUE x16. For a user who wants 2TB later — how far does this actually get them? Is 512GB "enough scalability" given models fit 256GB?',
  'Single-socket EPYC SP3 (e.g. H12SSL): RDIMM only — the user would SELL the non-ECC UDIMM (~€400-600) and buy RDIMM (~€400-800 for 256GB). Then it scales to 2TB+ natively. 6 GPUs at x16? (128 lanes, but slot wiring varies.) Real DE price.',
  'Dual-socket EPYC: 2TB+ native, but 2x PCIe3 x16 max (fails 6-GPU x16), RDIMM only. The AS-4124GS-TNR 9x x16 is €4-7k complete.',
  'Hybrid staged plan: WRX80 + UDIMM now (cheap, reuse free RAM, 512GB ceiling), then when 2TB is truly needed, move to a 2TB-capable server (dual-EPYC) and sell/repurpose the WRX80. Is this the pragmatic path?',
]
const tradeoffs = await parallel(TRADEOFFS.map((t) => () =>
  agent(
    'Map the honest scalability tradeoff for this platform/path vs the user\'s goals.\n\n' +
    'Path: ' + t + '\n\nUser: ' + USER + '\n\n' +
    'Assess: udimmReuse (can it use the free non-ECC sticks?), maxRamReal (verified real max), gpuX16, tbPath (real 2TB?), futureScalability (0-100 for the user\'s MoE + training + bigger-model future), honestTradeoff (what it makes you give up).\n\n' +
    'Be honest — a platform that reaches 2TB but makes the user sell the UDIMM and caps at 2x x16 is not "more scalable" if it fails the other goals.',
    { label: 'trade:' + t.slice(0, 22), phase: 'Tradeoffs', schema: TRADEOFF_SCHEMA, stallMs: 2147483647 }
  )
))
const tradeoffsOK = tradeoffs.filter(Boolean)
log(`Tradeoffs mapped: ${tradeoffsOK.length}/${TRADEOFFS.length}`)

// ─── Phase 4: synthesize ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the decisive answer to the user\'s hard question: is there a board that runs non-ECC UDIMM now AND grows to ~2TB later on the same board AND gives 6 GPUs at full x16?\n\n' +
  'User: ' + USER + '\n\n' +
  'Platform hunt (does ANY family do all 3?): ' + JSON.stringify(huntOK) + '\n\n' +
  'WRX80 2TB ceiling deep-verify: ' + JSON.stringify(wrx80) + '\n\n' +
  'Scalability tradeoffs: ' + JSON.stringify(tradeoffsOK) + '\n\n' +
  'Write the FINAL answer:\n' +
  '  1. IDEAL EXISTS? Does ANY board truly do UDIMM-now + 2TB-later + 6x x16? Give the definitive yes/no with the reasoning.\n' +
  '  2. BEST SCALABLE PATH: the best way to get future scalability, given the honest constraints. Is it WRX80 + UDIMM now (accept 512GB ceiling)? Or a staged plan (WRX80 now → server when 2TB needed)? Or sell UDIMM + EPYC?\n' +
  '  3. DECISION: the UDIMM-vs-2TB tradeoff, framed so the user can decide. What does "scalable for the future" realistically mean for their actual models (119-122GB, fit in 256GB; 2TB would only matter for far-future giants)?\n' +
  '  4. RECOMMENDATION: concrete — what to buy now, what the real upgrade path is, at what cost.\n' +
  '  5. RISKS.\n\n' +
  'Be decisive and honest. If the "perfect board" does not exist, say so plainly and give the best real path.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  hunt: huntOK,
  wrx80: wrx80,
  tradeoffs: tradeoffsOK,
  synthesis: synth,
}
