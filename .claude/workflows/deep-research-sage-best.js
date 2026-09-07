// DEEP-RESEARCH: (1) Is the ASUS Pro WS WRX80E-SAGE SE the BEST board for our goal?
//                (2) What CPU goes with it?
// The user's goal: 6 GPUs at full x16 + run non-ECC UDIMM now (160→256GB) + grow to RDIMM 2TB
// later ON THE SAME BOARD + good for big MoE inference (DeepSeek-V4 119GB etc.) + training.
// This is a full deep-research pass structured like /deep-research:
//  - Verify SAGE SE spec against primary sources (ASUS) — esp. non-ECC UDIMM + 2TB RDIMM on same board
//  - Real-world: does non-ECC UDIMM actually boot? UDIMM→RDIMM swap reality? known issues?
//  - CPU: which Threadripper PRO (3945/3955/3975/3995) is best with this board for MoE + UDIMM budget?
//  - Compare every alternative platform (other WRX80s, TRX40, Xeon-W, dual-EPYC, DDR5)
//  - Adversarial review: any reason SAGE SE is NOT best, and any reason the CPU pick is wrong
//  - Decisive verdict: best board + best CPU
// User: Bonn, MoE server. Budget ~€1000-1200 platform. Only user-verified-available board in DE:
// SAGE SE WIFI €888 (Kleinanzeigen 3477914853). Current rig i7-10700K 2ch.
export const meta = {
  name: 'deep-research-sage-best',
  description: 'Deep-research verdict: is the ASUS WRX80E-SAGE SE the best board for the 6-GPU + UDIMM-now + RDIMM-2TB-later MoE build, and which CPU with it?',
  phases: [
    { title: 'Spec-verify', detail: 'ASUS primary-source: 7x TRUE x16 + non-ECC UDIMM + 8ch + 2TB RDIMM on same board' },
    { title: 'Real-world', detail: 'Does non-ECC UDIMM boot? UDIMM→RDIMM swap reality on same board? known issues? DOCP?' },
    { title: 'CPU-pick', detail: 'Best Threadripper PRO for this board + MoE + UDIMM budget (3945/3955/3975/3995), real DE prices' },
    { title: 'Alternatives', detail: 'Every competing platform: other WRX80s, TRX40, Xeon-W, dual-EPYC, DDR5 — vs SAGE SE' },
    { title: 'Adversarial', detail: 'Adversarially test: any reason SAGE SE is NOT best, any reason the CPU pick is wrong' },
    { title: 'Synthesize', detail: 'Decisive: best board + best CPU with scorecard + buy action' },
  ],
}

const USER = `User in BONN, Germany. Local MoE inference server (llama.cpp/ggrun: DeepSeek-V4 119GB, Qwen3.5-122B, MiniMax-M3) + training ambition. HARD GOALS:
  1. 6 GPUs at FULL x16 (7x TRUE PCIe4 x16 desired)
  2. Run NON-ECC DDR4 UDIMM now (~160GB → max 256GB = 8x32GB) — the user OWNS these sticks, free RAM is the point
  3. Grow to 2TB via RDIMM/LRDIMM LATER ON THE SAME BOARD (no platform swap) — this is the "2TB path"
  4. 8x DDR4, 8-channel (memory bandwidth = the MoE lever: i7 2ch ~38GB/s → 8ch ~204GB/s)
  5. Good for big MoE inference + training ambition
  Budget ~€1000-1200 platform (board + CPU + cooler). Only user-verified-available board in DE: ASUS WRX80E-SAGE SE WIFI €888 (Kleinanzeigen 3477914853).
  Candidate board: ASUS Pro WS WRX80E-SAGE SE / SE WIFI (sWRX8 / WRX80 chipset).
  Candidate CPUs: Threadripper PRO 3945WX/3955WX/3975WX/3995WX (all sWRX8). Known DE prices: 3955WX €240-249 (16c), 3975WX €499 OEM tray (32c), 3945WX €143 (12c).`

const SPEC_SCHEMA = {
  type: 'object', required: ['board', 'verifiedSpec', 'x16True', 'nonEccUdim', 'channels', 'maxRam', 'tbPath', 'pcieVersion', 'evidence', 'confidence'],
  properties: {
    board: { type: 'string' },
    verifiedSpec: { type: 'string', description: 'The primary-source (ASUS/spec-sheet) verified spec.' },
    x16True: { type: 'boolean', description: '7x TRUE PCIe4 x16 slots (electrically, not just physically).' },
    nonEccUdim: { type: 'boolean', description: 'Supports NON-ECC UDIMM (user\'s RAM).' },
    channels: { type: 'integer' },
    maxRam: { type: 'string', description: 'Max RAM UDIMM and RDIMM.' },
    tbPath: { type: 'boolean', description: '2TB via RDIMM/LRDIMM on same board.' },
    pcieVersion: { type: 'string' },
    evidence: { type: 'string', description: 'Source URL + what it says.' },
    confidence: { type: 'string', enum: ['high', 'medium', 'low'] },
  },
}
const REALWORLD_SCHEMA = {
  type: 'object', required: ['board', 'nonEccUdimBoots', 'udimmToRdimSwap', 'mixedKit', 'knownIssues', 'wifiVariant'],
  properties: {
    board: { type: 'string' },
    nonEccUdimBoots: { type: 'string', description: 'Evidence non-ECC UDIMM actually POSTs/boots on this board (forums, not just spec).' },
    udimmToRdimSwap: { type: 'string', description: 'UDIMM→RDIMM later on the SAME board: clean swap? Any BIOS/slot/config gotchas? Can you mix UDIMM+RDIMM?' },
    mixedKit: { type: 'string', description: 'User has 2x Corsair 3200 CL16 1.35V + 2x Crucial 3200 CL22 1.2V + 2x16GB. Mixed-kit reality on WRX80.' },
    knownIssues: { type: 'string', description: 'Known problems: DOCP, RDIMM/LRDIMM detection, chipset fan, EOL BIOS, 256GB UDIMM ceiling.' },
    wifiVariant: { type: 'string', description: 'SAGE SE vs SE WIFI differences — WIFI worth it for a rack server?' },
  },
}
const CPU_SCHEMA = {
  type: 'object', required: ['bestCpu', 'why', 'candidates', 'moEFit', 'trainingFit', 'realPriceEUR', 'obtainable'],
  properties: {
    bestCpu: { type: 'string', description: 'The single best Threadripper PRO for this board + user (name, price, source).' },
    why: { type: 'string', description: 'Why this CPU — price-perf, MoE fit, budget.' },
    candidates: { type: 'array', items: { type: 'object', required: ['cpu', 'priceEUR', 'source', 'prosCons'], properties: { cpu: { type: 'string' }, priceEUR: { type: 'number' }, source: { type: 'string' }, prosCons: { type: 'string' } } } },
    moEFit: { type: 'string', description: 'CPU-core vs memory-bandwidth trade for MoE (DeepSeek-V4 119GB, CPU-expert offload). Is 16c enough, or is 32c worth it?' },
    trainingFit: { type: 'string', description: 'Is the CPU a bottleneck for training (vs the GPUs)?' },
    realPriceEUR: { type: 'number' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
  },
}
const ALT_SCHEMA = {
  type: 'object', required: ['platform', 'specFit', 'gpuX16', 'udimmSupport', 'maxRam', 'tbPath', 'realPriceEUR', 'obtainable', 'verdict'],
  properties: {
    platform: { type: 'string' },
    specFit: { type: 'string' },
    gpuX16: { type: 'string' },
    udimmSupport: { type: 'string' },
    maxRam: { type: 'string' },
    tbPath: { type: 'string' },
    realPriceEUR: { type: 'number' },
    obtainable: { type: 'string', enum: ['yes', 'maybe', 'no', 'unknown'] },
    verdict: { type: 'string', description: 'Better than SAGE SE? Worse? N/A (fails a hard requirement)?' },
  },
}
const ADV_SCHEMA = {
  type: 'object', required: ['finding', 'severity', 'valid', 'why', 'mitigation'],
  properties: {
    finding: { type: 'string' },
    severity: { type: 'string', enum: ['critical', 'major', 'minor', 'info'] },
    valid: { type: 'boolean' },
    why: { type: 'string' },
    mitigation: { type: 'string' },
  },
}
const SYNTH_SCHEMA = {
  type: 'object', required: ['boardVerdict', 'cpuPick', 'scorecard', 'buyAction', 'risks'],
  properties: {
    boardVerdict: { type: 'string', description: 'IS the SAGE SE the best board for this user\'s goals? Clear yes/no/nuanced.' },
    cpuPick: { type: 'string', description: 'The best CPU to pair: name, price, URL, why.' },
    scorecard: { type: 'string', description: 'SAGE SE vs top alternatives vs the user\'s exact goals (6x x16, UDIMM now, 2TB later, MoE, price).' },
    buyAction: { type: 'string', description: 'Concrete: buy SAGE SE WIFI €888 + which CPU, or wait, or alternative. Verify in browser first.' },
    risks: { type: 'array', items: { type: 'string' } },
  },
}

// ─── Phase 1: verify SAGE SE spec ───
phase('Spec-verify')
log('Verifying SAGE SE spec against ASUS primary sources...')

const spec = await agent(
  'Verify the ASUS Pro WS WRX80E-SAGE SE / SE WIFI spec against PRIMARY sources (asus.com official spec, ASUS QVL, technical docs).\n\n' +
  'User: ' + USER + '\n\n' +
  'This is the make-or-break spec verification. Verify each claim with evidence:\n' +
  '  1. 7x PCIe 4.0 TRUE x16 slots (electrically x16, not x8). Compare: ASRock WRX80 Creator has slots 4/6 x8; Gigabyte MC62-G41 6x x16+1x x8; Supermicro M12SWA-TF 6x x16.\n' +
  '  2. Memory: 8x DDR4 8-channel. Supports NON-ECC UDIMM (user RAM) AND ECC UDIMM AND RDIMM/LRDIMM to 2TB. This is what makes SAGE SE special vs Gigabyte WRX80-SU8-IPMI (UDIMM-ECC-only).\n' +
  '  3. PCIe 4.0, 128 lanes from TR PRO.\n' +
  '  4. Form factor (EEB).\n\n' +
  'Return the structured spec with source URLs. Rigorous, primary sources.',
  { label: 'spec-verify', phase: 'Spec-verify', schema: SPEC_SCHEMA, stallMs: 2147483647 }
)
log(`Spec: 7x x16=${spec && spec.x16True}, non-ECC UDIMM=${spec && spec.nonEccUdim}, 2TB=${spec && spec.tbPath}`)

// ─── Phase 2: real-world validation (esp UDIMM→RDIMM swap on same board) ───
phase('Real-world')
log('Real-world: does non-ECC UDIMM boot, and can you swap to RDIMM 2TB later on the SAME board?...')

const realworld = await agent(
  'Research REAL-WORLD validation of the ASUS Pro WS WRX80E-SAGE SE / SE WIFI, focused on the user\'s exact plan.\n\n' +
  'User: ' + USER + '\n\n' +
  'Find concrete, sourced evidence (forums/reddit/STH/level1techs, not spec sheets):\n' +
  '  1. Does NON-ECC UDIMM actually POST/boot on this board? (User has Corsair 3200 CL16 1.35V + Crucial 3200 CL22 1.2V.) TR-PRO + UDIMM builds.\n' +
  '  2. THE CORE QUESTION — UDIMM now → RDIMM 2TB later on the SAME board: is this a clean swap? Any gotchas (can\'t mix UDIMM+RDIMM, all-slots config, BIOS)? Does anyone actually run 1-2TB RDIMM/LRDIMM-3DS on a WRX80E-SAGE SE?\n' +
  '  3. Mixed-kit reality (1.35V CL16 + 1.2V CL22 + 2x16GB) on WRX80.\n' +
  '  4. Known issues: DOCP, RDIMM/LRDIMM detection, chipset fan, EOL BIOS, 256GB UDIMM ceiling.\n' +
  '  5. SAGE SE vs SE WIFI — for a rack server is WIFI clutter?\n\n' +
  'Return the structured findings with sources. Honest about what is NOT documented.',
  { label: 'real-world', phase: 'Real-world', schema: REALWORLD_SCHEMA, stallMs: 2147483647 }
)
log(`Real-world: non-ECC UDIMM boots=${realworld && realworld.nonEccUdimBoots}`)

// ─── Phase 3: CPU pick ───
phase('CPU-pick')
log('Determining the best Threadripper PRO CPU with this board...')

const cpuPick = await agent(
  'Determine the BEST CPU to pair with the ASUS WRX80E-SAGE SE for this user.\n\n' +
  'User: ' + USER + '\n\n' +
  'The sWRX8 Threadripper PRO lineup (8ch, 128 lanes, all work with non-ECC UDIMM + RDIMM 2TB):\n' +
  '  - 3945WX 12c/24t ~€143-180\n' +
  '  - 3955WX 16c/32t ~€240-249 (known: Hamburg €240 NOS no-lock, Dortmund €249)\n' +
  '  - 3975WX 32c/64t ~€499 OEM tray (aphextwinde eBay.de) — real DE 32c, NOT the €732+ China/US traps\n' +
  '  - 3995WX 64c/128t — €1020-1633 (US import / OOS), over budget\n\n' +
  'For the MoE workload: DeepSeek-V4 119GB runs with CPU+GPU offload; CPU-expert MoE (--n-cpu-moe) is memory-BW bound (8ch 3200). Question: is 16c (3955WX) enough, or does 32c (3975WX) materially help CPU-expert offload / training? Given the budget €1000-1200 (board €888), a €499 3975WX leaves only ~€-200 (over) vs €240 3955WX leaves €50 — trade off.\n\n' +
  'Also check: any real DE deals for 3975WX/3995WX below the known prices? Is the 3975WX OEM tray (€499) real/obtainable?\n\n' +
  'Return: bestCpu (the single best: name, price, source, URL), why, candidates[], moEFit, trainingFit, realPriceEUR, obtainable.',
  { label: 'cpu-pick', phase: 'CPU-pick', schema: CPU_SCHEMA, stallMs: 2147483647 }
)
log(`CPU pick: ${cpuPick && cpuPick.bestCpu}`)

// ─── Phase 4: alternatives ───
phase('Alternatives')
log('Comparing every alternative platform that could host 6 GPUs x16 + UDIMM now + 2TB later...')

const ALTS = [
  'Other WRX80 boards with 7x TRUE x16: ASRock Rack WRX80D8-2T (discontinued/OOS DE), Gigabyte WRX80-SU8-IPMI (7x x16 but UDIMM-ECC/RDIMM-only — REJECT for non-ECC UDIMM), Supermicro M12SWA-TF (6x x16 — REJECT 7x). Any OTHERS (rare server boards) with 7x TRUE x16 AND non-ECC UDIMM besides SAGE SE?',
  'sTRX4 Threadripper 3000 (non-PRO) / TRX40: 4-channel, 64 lanes, max 4 GPUs x16, no 2TB. If the user DROPPED the 6-GPU or 2TB goal, is a TRX40 a cheaper viable alternative? Real DE price.',
  'Intel Xeon W-3300 (LGA4189) / W-3200 (LGA3647): 8-channel, 64 lanes, 6 GPUs at x8 (bifurcation NOT full x16), ECC-only (no non-ECC UDIMM). Honest comparison.',
  'Dual-socket EPYC SP3: 2TB+, H11DSi only 2x PCIe3 x16, RDIMM-only (no non-ECC UDIMM). AS-4124GS-TNR 9x x16 but €4-7k complete-server. Real comparison.',
  'DDR5 platforms (Threadripper 7000 sTRX5, EPYC 9004): DDR5 RDIMM only, no DDR4 UDIMM reuse, far more expensive. Is dropping UDIMM reuse to jump to DDR5 worth it? Price/benefit.',
]
const alts = await parallel(ALTS.map((a) => () =>
  agent(
    'Evaluate this ALTERNATIVE platform vs the ASUS WRX80E-SAGE SE for the user\'s build.\n\n' +
    'Platform: ' + a + '\n\nUser: ' + USER + '\n\n' +
    'Does it meet the HARD goals (6 GPUs full x16 + non-ECC UDIMM now + 2TB later on same board + 8ch)? If not, which goal fails and how much does it matter for MoE? Real DE price (board + CPU)? Obtainable? Verdict: better than SAGE SE / worse / N/A?',
    { label: 'alt:' + a.slice(0, 22), phase: 'Alternatives', schema: ALT_SCHEMA, stallMs: 2147483647 }
  )
))
const altsOK = alts.filter(Boolean)
log(`Alternatives: ${altsOK.length}/${ALTS.length}`)

// ─── Phase 5: adversarial review ───
phase('Adversarial')
log('Adversarially testing the SAGE SE + CPU recommendation...')

const ADV_FINDINGS = [
  'Is the SAGE SE\'s non-ECC UDIMM support + RDIMM-2TB-later real, or does the WRX80/TR-PRO platform silently force constraints (e.g. JEDEC-only, no DOCP for the mixed 1.35V+1.2V kit, or UDIMM-only in some slots)? Does the "2TB path" actually work on this board or is it marketing?',
  'Is the 6-GPU-full-x16 goal worth it? ggrun measured x1/x4 links cost only ~0.05-0.2% for MoE inference. Is paying the SAGE SE premium for 7x x16 wasteful vs a cheaper board with x8? Is there a cheaper board that meets the OTHER goals?',
  'Is the €888 SAGE SE WIFI (user-verified available, EOL board) good value, or should the user wait for a €650-700 used SAGE SE, or is the money better spent elsewhere (more RAM, a better CPU, a second GPU)?',
  'Is the CPU pick right? For CPU-expert MoE (memory-BW bound, 8ch) does 16c/3955WX cap out, making the 32c/3975WX OEM tray (€499) a worthwhile upgrade even though it blows the €1000 budget? Or is the GPU the bottleneck, making the CPU a 16c enough?',
  'Is the whole WRX80/TR-PRO platform the right call vs just keeping the i7-10700K and adding GPUs, or vs a cheap used dual-EPYC for CPU-expert MoE? Re-examine the "8ch memory is the lever" premise against the ~€1,200 platform cost.',
]
const adv = await parallel(ADV_FINDINGS.map((f) => () =>
  agent(
    'Adversarially investigate this potential weakness in the recommendation (ASUS WRX80E-SAGE SE + Threadripper PRO for this user).\n\n' +
    'Question: ' + f + '\n\nUser: ' + USER + '\n\n' +
    'Is this a REAL problem for THIS user, or theoretical? Cite evidence. Assign severity + validity (does it disqualify/materially weaken the pick?). Give mitigation.\n\n' +
    'Return: finding, severity, valid, why, mitigation.',
    { label: 'adv:' + f.slice(0, 24), phase: 'Adversarial', schema: ADV_SCHEMA, stallMs: 2147483647 }
  )
))
const advOK = adv.filter(Boolean)
log(`Adversarial findings: ${advOK.length}/${ADV_FINDINGS.length}`)

// ─── Phase 6: synthesize decisive verdict ───
phase('Synthesize')
const synth = await agent(
  'SYNTHESIZE the deep-research verdict: (1) IS the ASUS Pro WS WRX80E-SAGE SE the best board for this user\'s goals? (2) WHAT CPU with it?\n\n' +
  'User goals: ' + USER + '\n\n' +
  'Spec verification (primary sources): ' + JSON.stringify(spec) + '\n\n' +
  'Real-world (UDIMM boot + UDIMM→RDIMM-2TB swap + mixed kit + issues): ' + JSON.stringify(realworld) + '\n\n' +
  'CPU recommendation: ' + JSON.stringify(cpuPick) + '\n\n' +
  'Alternative platforms: ' + JSON.stringify(altsOK) + '\n\n' +
  'Adversarial findings: ' + JSON.stringify(advOK) + '\n\n' +
  'Write the FINAL verdict:\n' +
  '  1. BOARD VERDICT: IS the SAGE SE the best board for THIS user\'s goals (6x x16 + UDIMM now + 2TB later on SAME board + 8ch MoE + training)? Clear decisive answer.\n' +
  '  2. CPU PICK: the best CPU to pair — name, price, URL, why. (Weigh 16c/3955WX @€240-249 vs 32c/3975WX OEM @€499 vs budget.)\n' +
  '  3. SCORECARD: SAGE SE vs top alternatives against the user\'s EXACT goals.\n' +
  '  4. BUY ACTION: concrete — buy SAGE SE WIFI €888 + which CPU, or wait, or alternative. Remind the user to verify links in a real browser (remote-fetch phantom-listings lesson).\n' +
  '  5. RISKS.\n\n' +
  'Be decisive and honest. This is a deep-research verdict.',
  { label: 'synthesize', phase: 'Synthesize', schema: SYNTH_SCHEMA, stallMs: 2147483647 }
)

return {
  spec: spec,
  realworld: realworld,
  cpuPick: cpuPick,
  alternatives: altsOK,
  adversarial: advOK,
  synthesis: synth,
}
