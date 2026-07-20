export const meta = {
  name: 'ggrun-full-audit',
  description: 'Full multi-dimensional audit of the ggrun Go codebase (correctness, security, performance, tests/coverage, dependency hygiene, code quality) with adversarial verification of findings.',
  phases: [
    { title: 'Build', detail: 'Compile, vet, and run existing tests to establish a baseline.' },
    { title: 'Audit', detail: 'Fan out independent audit dimensions in parallel.' },
    { title: 'Verify', detail: 'Adversarially verify each finding before reporting.' },
    { title: 'Synthesize', detail: 'Aggregate and rank findings into a final report.' },
  ],
};

const ROOT = '/home/mik/ggrun-project/ggrun';
const GO = ROOT + '/go';

// ---- Phase 1: Build / vet / test baseline ----
phase('Build');
const baseline = await agent(
  `Establish an objective build/test baseline for the ggrun Go project at ${GO}.
   Steps:
   1. cd ${GO} and run 'go version', then 'go build ./...' (report success/failure + first errors verbatim),
      'go vet ./...' (report findings), and 'go test ./...' (report which packages pass/fail and any panic).
   2. Try 'go test -race ./pkg/placement/... ./pkg/recovery/... ./pkg/server/...' — if too slow, skip and note it.
   3. Report: total Go LOC, number of packages, number of test files, whether build is green,
      count of vet warnings, count of failing tests, count of passing tests.
   Return a concise structured baseline report. Do NOT audit behavior — only establish the machine-checkable baseline.`,
  { label: 'build-baseline', phase: 'Build' }
);

// ---- Phase 2: Parallel audit dimensions ----
phase('Audit');

const correctness = await agent(
  `You are auditing CORRECTNESS of the ggrun Go codebase at ${GO} (launcher around llama.cpp/ik_llama.cpp that builds GPU/CPU placement plans for large MoE models).
   Focus areas (read the actual code, do not guess):
   - ${GO}/pkg/placement/placement.go (4716 LOC) and draft.go (1831 LOC): MoE tensor split math, VRAM budget estimation, offload layer counting, KV cache sizing. Look for integer overflow, off-by-one in layer ranges, wrong unit conversions (bytes/MB/GB), mismatched VRAM accounting between estimate and actual launch, silent truncation.
   - ${GO}/pkg/gguf/gguf.go: GGUF header/tensor parsing — bounds checks, integer parsing, endianness, malformed-input handling.
   - ${GO}/pkg/recommend/recommend.go + speed.go: model catalog matching, speed estimation formulas, division-by-zero, NaN/Inf propagation.
   - ${GO}/pkg/probe/fit.go: hardware fit scoring math.
   - ${GO}/pkg/tune/engine.go (1104 LOC): tuning search — termination conditions, numerical stability.
   For EVERY issue: cite file:line, show the actual code snippet, explain the concrete incorrect result and a triggering input. Distinguish real bugs from style.
   At the END, also emit a fenced JSON block like:
   \`\`\`json
   [{"file":"...","line":123,"severity":"high","category":"overflow","description":"...","trigger":"...","evidence":"..."}]
   \`\`\`
   where severity is one of critical|high|medium|low. Return your full narrative plus that JSON block.`,
  { label: 'audit-correctness', phase: 'Audit' }
);

const security = await agent(
  `You are auditing SECURITY of the ggrun Go codebase at ${GO}. ggrun launches subprocesses (llama-server, claude), downloads GGUF files, runs an HTTP server, and handles env vars/config.
   Focus areas (read actual code):
   - ${GO}/go/cmd/ggrun/main.go (4491 LOC) and claude_auto.go, claude_workflow.go: command construction — shell injection via model names/paths/flags, unsanitized user input passed to exec, use of sh -c with interpolation.
   - ${GO}/pkg/server/server.go (709 LOC): HTTP server — path traversal, unauthenticated endpoints, SSRF, binding to 0.0.0.0, missing timeouts, request body size limits.
   - ${GO}/pkg/download/download.go and ${GO}/pkg/libhub/libhub.go: TLS verification, redirect handling, hash verification of downloaded files, zip/tar extraction path traversal (zip-slip), temp file permissions.
   - ${GO}/pkg/config/config.go (615 LOC): secrets in env vars, config file permissions, reading untrusted config.
   - ${GO}/pkg/update/update.go (1092 LOC): update mechanism — signature verification, TOCTOU, fetching from untrusted URLs.
   - ${GO}/pkg/recovery/recovery.go (901 LOC): privilege/process handling.
   For EVERY issue: cite file:line, show the snippet, explain exploitability and impact. Distinguish real vulns from theoretical.
   At the END emit a fenced JSON block:
   \`\`\`json
   [{"file":"...","line":123,"severity":"high","category":"injection","description":"...","exploit":"...","evidence":"..."}]
   \`\`\`
   where severity is one of critical|high|medium|low. Return your full narrative plus that JSON block.`,
  { label: 'audit-security', phase: 'Audit' }
);

const performance = await agent(
  `You are auditing PERFORMANCE of the ggrun Go codebase at ${GO}.
   Focus areas (read actual code):
   - ${GO}/pkg/placement/placement.go + draft.go: redundant recomputation, O(n^2) loops over layers/tensors, repeated file reads (GGUF parsed many times?), unnecessary allocations in hot paths.
   - ${GO}/pkg/recommend/recommend.go: catalog scan patterns, repeated JSON unmarshal, no caching.
   - ${GO}/pkg/tui/tui.go (2140 LOC): render loop efficiency, full redraws, goroutine leaks, channel blocking.
   - ${GO}/pkg/server/server.go: per-request allocations, unbounded goroutines, missing context cancellation.
   - ${GO}/pkg/tune/engine.go: search space explosion, no memoization.
   - Concurrency: data races, mutex contention, goroutine leaks (check server/recovery/tui).
   For each issue: file:line, the code, the cost (big-O or concrete), and a trigger.
   At the END emit a fenced JSON block:
   \`\`\`json
   [{"file":"...","line":123,"severity":"medium","category":"alloc","description":"...","cost":"...","evidence":"..."}]
   \`\`\`
   where severity is one of critical|high|medium|low. Return your full narrative plus that JSON block.`,
  { label: 'audit-performance', phase: 'Audit' }
);

const tests = await agent(
  `You are auditing TEST COVERAGE and TEST QUALITY of the ggrun Go codebase at ${GO}.
   Steps:
   1. For each package under ${GO}/pkg and ${GO}/go/cmd/ggrun, compare source LOC to test LOC. Identify packages with NO tests or very low ratio.
   2. Read the existing tests (placement_test.go 3276 LOC, main_test.go 1704 LOC, engine_test.go, etc.). Assess: real behavioral tests or trivial? Do they assert or just exercise? Do they cover edge cases (overflow, malformed GGUF, empty catalog, OOM paths)?
   3. Identify critical untested paths: placement math edge cases, security-sensitive parsing, download verification, server endpoints.
   4. Flag flaky-test risks: time-dependent, order-dependent, shared global state, missing seeds.
   Return a markdown report with sections: FilesWithoutTests, LowCoveragePackages, WeakTestExamples, CriticalUntestedPaths (top 5), FlakyRisks. Be concrete with file:line.`,
  { label: 'audit-tests', phase: 'Audit' }
);

const hygiene = await agent(
  `You are auditing DEPENDENCY HYGIENE and CODE QUALITY of the ggrun Go codebase at ${GO}.
   Steps:
   1. Read ${GO}/go.mod and ${GO}/go.sum. Run 'cd ${GO} && go mod verify' and 'gofmt -l .'; report unformatted files. Note outdated deps if obvious.
   2. Code quality: dead code (unused funcs/vars), copy-paste duplication across placement/backends/draft, error handling patterns (swallowed errors, panic instead of error), TODO/FIXME/XXX density and whether tracked, inconsistent naming.
   3. Check for leftover debug code, fmt.Println in non-test code, hardcoded paths/URLs, magic numbers.
   4. Look at ${GO}/../TODO.md and ${GO}/../CHANGELOG.md for claimed-vs-actual gaps.
   Return a markdown report with sections: OutdatedDeps, UnformattedFiles, DeadCode, Duplication, SwallowedErrors, MagicNumbers, TodoGaps. Cite file:line for each.`,
  { label: 'audit-hygiene', phase: 'Audit' }
);

// ---- Phase 3: Adversarial verification (plain agents; they return strings) ----
phase('Verify');

const verifyPrompt = (dim, findingsText) =>
  `You are an adversarial verifier. Another auditor claimed the following ${dim} findings in the ggrun Go codebase at ${GO}.
   For EACH finding, re-read the cited file:line in the real code, and decide:
     - CONFIRMED: the issue is real and reproducible as described.
     - PARTIAL: there is a real issue but the severity/details are overstated or partly wrong.
     - REJECTED: the finding is incorrect, a false positive, style-only, or not reproducibly triggered.
   Be skeptical. Reject theoretical-only issues that cannot actually be triggered given how the code is called.
   Return ONLY a fenced JSON block:
   \`\`\`json
   [{"file":"...","line":123,"verdict":"CONFIRMED|PARTIAL|REJECTED","reason":"...","cwe":"..."}]
   \`\`\`
   One entry per finding, in the same order. Do not add commentary outside the JSON block.

   FINDINGS TO VERIFY:
   ${findingsText}`;

const vc = await agent(verifyPrompt('correctness', correctness), { label: 'verify-correctness', phase: 'Verify' });
const vs = await agent(verifyPrompt('security', security), { label: 'verify-security', phase: 'Verify' });
const vp = await agent(verifyPrompt('performance', performance), { label: 'verify-performance', phase: 'Verify' });
const vh = await agent(verifyPrompt('hygiene', hygiene), { label: 'verify-hygiene', phase: 'Verify' });

// ---- Phase 4: Synthesize ----
phase('Synthesize');
const report = await agent(
  `Synthesize a FINAL AUDIT REPORT for ggrun (Go codebase at ${GO}) from all the audit and verification output below.
   Use the verification verdicts to filter: only include findings marked CONFIRMED or PARTIAL (drop REJECTED). For PARTIAL, apply the corrected severity from the verifier's reason.

   BUILD BASELINE:
   ${baseline}

   CORRECTNESS AUDIT:
   ${correctness}

   CORRECTNESS VERIFICATION (verdicts):
   ${vc}

   SECURITY AUDIT:
   ${security}

   SECURITY VERIFICATION (verdicts):
   ${vs}

   PERFORMANCE AUDIT:
   ${performance}

   PERFORMANCE VERIFICATION (verdicts):
   ${vp}

   HYGIENE AUDIT:
   ${hygiene}

   HYGIENE VERIFICATION (verdicts):
   ${vh}

   TEST COVERAGE ASSESSMENT:
   ${tests}

   Produce a markdown report with:
   - Executive summary (overall health, top risks, whether build is green)
   - Build & test baseline (from baseline above)
   - Findings by severity (Critical / High / Medium / Low), each with file:line, category, what's wrong, impact, fix suggestion — ONLY CONFIRMED/PARTIAL
   - Test coverage gaps (top 5 untested critical paths from tests audit)
   - Prioritized remediation roadmap (what to fix first)
   - Methodology note (how findings were verified)
   Rank by severity. Cite file:line. Do not invent findings not present above. This is the deliverable — make it complete and well-structured.`,
  { label: 'synthesize-report', phase: 'Synthesize' }
);

return {
  baseline,
  correctness_audit: correctness,
  security_audit: security,
  performance_audit: performance,
  tests_audit: tests,
  hygiene_audit: hygiene,
  verification: { correctness: vc, security: vs, performance: vp, hygiene: vh },
  report,
};
