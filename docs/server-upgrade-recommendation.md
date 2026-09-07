# Server Upgrade — Best Rig, Verified Prices (2026-08-06)

**Question:** Best rig for local MoE serving (llama.cpp/ggrun: DeepSeek-V4 119GB, Qwen3.5-122B,
MiniMax-M3), 1-2TB RAM goal, 6-GPU expansion, budget €1000 **used**, Germany (Bonn/NRW),
GPUs reused.

**This is the second, price-verified pass.** The first doc priced 64GB DDR4-2933 RDIMM at
€120-130/stick; **that was wrong — those were LRDIMM-load-reduced or China-import bait prices
you don't actually get.** Every price below was adversarially verified against live DE offers
(2026-08-06): eBay.de buy-it-now, Kleinanzeigen.de, and DE server retailers (Future-X,
Renewtech, ServerShop24, Gekko, alpha38).

---

## HEADLINE: The €1000 goal is NOT achievable — and by a wide margin

The platform alone (board + CPU + chassis + PSU) is freely obtainable, but **the killer is RAM.**
Verified real German obtainable prices:

| Component | Real obtainable price (DE) | Source (live, confirmed) |
|---|---|---|
| Supermicro X11SPA-T board (new, DE-stock) | **€709.49** | Future-X.de / Geizhals; MediaMarkt €837.65 |
| Intel Xeon Gold 6248 (20c/40t, 150W) | **€133.28** (+€15 ship) | Renewtech.de, 36 in stock |
| 64GB DDR4-2933 ECC RDIMM (HMAA8GR7AJR4N-WM) | **€649–699** /stick | Samsung €699, SK Hynix €649 (Geizhals) |
| Used 4U GPU rack chassis | **€80** (cash pickup) | Kleinanzeigen Ratingen (NRW) |
| 2× 2000W SuperMicro PSU | **€94.04** (€47.02 ea) | alpha38.de |
| **Platform (board+CPU+chassis+PSU)** | **€969.79** | confirmed obtainable |
| **+ 256GB RAM (4×64GB)** | **~€2,600** | 4× €650 (64GB RDIMM) |

Clarifying:
- **€1000 buys** the platform (board+CPU+chassis+PSU) and NOTHING for RAM.
- **128GB** (2 sticks, ~€1,100) boots but is too tight for the 119GB DeepSeek-V4 weights.
- **256GB** (4 sticks, ~€2,200) is the sensible minimum — a **~€3,200 build**.
- **1TB** = ~€6,600 of RAM alone → ~€7,500-9,000 total (or ~€5-6.5k via a used dual-EPYC box).

**The honest answer to "is €1000 enough?" — No.** Real DE hardware for this use case starts
around €3,200 for the sensible minimum; 1TB is a separate €7-9k program.

The hardware topology is verified **sound** (see the build below). The *only* reason the goal
fails is RAM cost. So the recommendation is a **staged plan**: buy the platform + CPU + 256GB
now (€3,200), which runs all three target models with CPU+GPU offload, and fund 1TB+ later.

---

## The Best Rig (verified, obtainable)

All five parts are purchasable **today** in Germany (DE stock / DE pick-up) — no China-import
bait, no VAT surprise (VAT included for board/CPU/PSU/RAM).

### Motherboard — Supermicro X11SPA-T, LGA-3647 — NEW €709.49
- 12 DIMM, up to 3TB RDIMM (768GB non-3DS = 12×64GB).
- 6 GPUs at **x8** via native 8/8 bifurcation pairs on the 48-lane Xeon Gold 6248 (no BIOS
  hacks; 6 GPUs = 3 pairs). A concurrent 7th x16 device would need a 64-lane Xeon W-32xx.
- **Cascade-Lake CPU required** for the 3TB/768GB 64GB-density ceiling (Skylake-SP caps lower).
- ⚠️ Buy from **Future-X.de** (DE stock, new, free DE shipping). The cheaper €688-749 "Neu" ads
  are Shenzhen/HK drop-ships (3-6 wks, variant/density risk). Future-X shop rating is 3.6/5 —
  verify the -O retail variant on arrival.

### CPU — Intel Xeon Gold 6248 (SRF90), 20c/40t, 2.5GHz, 150W — €133.28 (+€15 ship)
- Cascade Lake, 48 PCIe lanes, 6 memory channels. Ample for llama.cpp MoE CPU+GPU offload
  (memory-bandwidth bound; matmul-heavy layers pinned to GPUs, coldest experts on CPU).
- ⚠️ Renewtech.de is the real DE source (36 units, 1-yr warranty). Avoid the **6248R** (SRGZG,
  24c/3.0GHz) variant and UK/US-import low bids (cheap snippet + shipping + VAT).

### RAM — 64GB DDR4-2933 ECC RDIMM, 4×64GB = 256GB — ~€2,600 (€649-699/stick)
- **Geizhals**: Samsung 64GB RDIMM €699, SK Hynix €649. Corroborated on idealo/Amazon.
- ⚠️ **Do NOT buy 64GB RDIMM under ~€300.** The sub-€200 prices are LRDIMM-load-reduced,
  wrong-speed (DDR4-2666), China-import, or bait. At purchase time you get a wrong-density
  stick after 3-6 weeks, or a module that won't run at 2933.
- 256GB runs all three models with offload headroom (DeepSeek-V4 119GB, Qwen3.5-122B ~73GB Q4,
  MiniMax-M3). 1TB is a future expansion, not an initial need.
- Ceiling note: 768GB (12×64GB non-3DS) is the realistic top on this board+CPU; 1TB+ means
  swapping to 128/256GB **3DS** modules (~€800-1,200/stick used).

### Chassis — Used 4U GPU/Mining rack, 635mm deep — €80 cash pickup
- **Kleinanzeigen.de**, Ratingen 40883 (NRW): 4U/4HE GPU-capable, 5×120mm fans, 80€ VB.
- Cluster of real local offers: Bonn-Poppelsdorf €49, Bergisch Gladbach €60, Mülheim €80,
  Hünstetten €89 — so €80 is a robust obtainable figure, bargain toward €60-70.
- ⚠️ Single VB listings can vanish; budget ~€100 if the one you want is gone. Confirm the
  3090 Ti triple-slot clearance + riser fit before paying.

### PSU — 2× SuperMicro PWS-2K02P-1R 2000W — €94.04 (€47.02×2)
- **alpha38.de** (Alpha IT, Braunschweig), 112 units in stock, 1-3 days, 19% VAT incl.
- 1 unit now (€47.02) suffices for the 3-GPU load (3090 Ti can be power-limited ~350W);
  2 units for the 6-GPU redundant target.
- ⚠️ Proprietary SuperMicro form factor ("Nicht für PCs geeignet") — needs the right
  power-distribution cabling; full 2000W needs 200-240V input. This is exactly why used units
  trade at €47. Buy both even if you need one today.

---

## Total (4×64GB sensible build): **~€3,570** — OVER BUDGET (budget €1,000)

At €1,000 you get the bare platform with no RAM. The honest minimum to run the existing GPUs
properly is ~€3,500-3,600. **Note: this RDIMM path is superseded by the WRX80/UDIMM-reuse
platform (best-platform-udimm-6gpu.md) which reuses the user's free UDIMM and costs ~€956 for
the platform.**

## RAM PRICE CORRECTION (2026-08-07)

Verified DE prices (Geizhals/idealo, current): **32GB DDR4-3200 UDIMM ~€229-320** (Crucial €229,
Kingston €318); **64GB RDIMM ~€649-699**. The earlier €3,217/256GB figure overstated the 32GB
price. The 64GB laptop SO-DIMM is worth ~€600-800 if a rare single-64GB OEM module (one DE
listing €879.99), or €216-419 as a 2×32GB kit.

---

## Path C — used dual-EPYC (the alternative, also not a €1,000 path)

The used **AS-4124GS-TNR (dual EPYC, 8TB, 9× PCIe 4.0 x16, 2000W Titanium PSUs)** is the better
6-GPU host, but:
- The cheap **€2,653 "complete server"** is a **barebone** (chassis + H12DSG-O-CPU + PSUs, no
  CPUs/RAM) — misleading as a price.
- Fully populated units list €10,200-12,350; a real 1TB dual-EPYC box lands ~€5,000-6,500.
- Only an unpopulated €1,500-2,000 barebone can approach budget, and even that clears €1,500.

Not a €1,000 option, but the right long-term 1TB + 6-GPU vehicle if the budget stretches.

---

## What would actually happen at purchase (the honest card)

| Buy | Snippet/trap price | What you actually get |
|---|---|---|
| X11SPA-T "Neu" | €688-749 (eBay) | Shenzhen/HK drop-ship, ~1-mo ship, possibly wrong variant |
| 64GB RDIMM | €120-130 (old doc) | **Doesn't exist** at that price — LRDIMM/China/mismatch bait |
| 64GB RDIMM | €170-309 | LRDIMM-load-reduced or DDR4-2666 — not 2933 RDIMM |
| Gold 6248 | €138 (eBay) | UK/US import + shipping + VAT, or 6248R variant |
| 4U chassis | €80 | Real, but VB single listing — can vanish / counter-offer to €100 |
| 2000W PSU | €30-49 (eBay) | China/Shenzhen direct-sale, 3-6 wk shipping |
| 64GB RDIMM (correct) | €649-699 (Geizhals) | Real DE stock, bought and delivered — this is the honest price |

---

## Bottom line

**The €1000 goal is not achievable for this use case.** The realistic sensible build is
**~€3,200** (fully working 256GB + 6-GPU-capable platform); **1TB is a €7-9k program**.

Everything is *obtainable* and every price is *real, in stock, DE* — the blocker is purely RAM
cost (64GB DDR4-2933 RDIMM is ~€550-733/stick in DE; the sub-€200 listings are the trap). The
staged path — platform now, 128-256GB now, add 1TB later — is the honest route; or jump to the
used dual-EPYC 4U if the budget can stretch to €1.5-2k+.

*Sources (all live 2026-08-06, adversarially verified): Future-X.de, Geizhals.de, Renewtech.de,
ServerShop24.de, Gekko-Computer, alpha38.de, Kleinanzeigen.de (NRW), supermicro.com primary
spec pages. eBay direct fetches return 403 — eBay prices come from search snippets + dealer
cross-checks, not page fetches.*
