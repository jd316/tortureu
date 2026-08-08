#!/usr/bin/env python3
"""Cross-check the design docs against each other. Run before trusting any count.

    python3 check.py     # exits 1 on any failure

Exists because a hand-maintained count in RESEARCH.md was wrong by 14 tools and a
YAML example didn't parse. registry.yaml is the source of truth; everything else
is checked against it.
"""
import json, os, re, sys, yaml

reg = yaml.safe_load(open('registry.yaml'))
res, ver = open('RESEARCH.md').read(), open('VERDICT.md').read()
tor_raw = open('torture.example.yaml').read()
fails = []


def ok(cond, msg):
    print(("OK   " if cond else "FAIL ") + msg)
    if not cond:
        fails.append(msg)


tor = yaml.safe_load(tor_raw)
ok(True, "torture.example.yaml parses")
ok(len(reg['domains']) == 19, "19 domains in registry")

ids = [t['id'] for d in reg['domains'] for t in d['tools']]
ok(len(ids) == len(set(ids)), f"{len(ids)} tool ids, all unique")
ok(all(all(k in t for k in ('tier', 'when', 'how')) for d in reg['domains'] for t in d['tools']),
   "every tool has tier/when/how")

tiers = {t: sum(1 for d in reg['domains'] for x in d['tools'] if x['tier'] == t)
         for t in ('drive', 'delegate', 'know')}
ok(all(f"| {v} |" in res for v in tiers.values()), f"RESEARCH tier counts match registry {tiers}")

# D-3: every predicate must be derivable from compose + lockfile, and unambiguously parsed
preds = {p for d in reg['domains'] for t in d['tools'] for p in str(t['when']).split('|')}
bad = {p for p in preds if p not in ('always', 'never') and ':' not in p}
ok(not bad, f"all {len(preds)} predicates prefixed" + (f" — bare: {bad}" if bad else ""))
ns = {p.split(':')[0] for p in preds if ':' in p}
ok(ns <= {'dep', 'lang', 'spec', 'platform', 'has', 'lacks'}, f"predicate namespaces legal: {sorted(ns)}")

verbs = {m.group(1) for d in reg['domains'] for x in d['tools']
         for m in [re.match(r'tortureu (\S+)', str(x['how']))] if m}
listed = set(re.findall(r'^\d+\s+(\w+)', re.search(r'```\n(1  init.*?)```', res, re.S).group(1), re.M))
ok(verbs <= listed, f"all {len(verbs)} CLI verbs appear in build order")

defined = set(re.findall(r'^### (D-\d+|DC-\d+)', res, re.M))
used = set(re.findall(r'\b(D-\d+|DC-\d+)\b', res + ver + tor_raw + open('registry.yaml').read()))
ok(used <= defined, f"all D-/DC- refs resolve" + (f" — dangling: {sorted(used - defined)}" if used - defined else ""))

# DC-1: the MCP surface must be identical in both docs, and must not borrow k6's nouns
mcp_res = set(re.findall(r'^  (\w+)\(', re.search(r'tortureu mcp exposes exactly:\n(.*?)```', res, re.S).group(1), re.M))
mcp_ver = set(re.findall(r'^### `(\w+)\(', ver, re.M))
ok(mcp_res == mcp_ver, f"MCP tool list agrees across docs: {sorted(mcp_ver)}")
leak = {t for t in mcp_ver if any(n in t for n in ('script', 'test', 'threshold')) and t != 'emit_k6_script'}
ok(not leak, "DC-1 noun rule holds" + (f" — leaked: {leak}" if leak else ""))

ok({'egress', 'load', 'faults', 'assert', 'reset', 'target'} <= set(tor), "schema blocks present")
ok(tor['egress']['default'] == 'deny', "DC-2: egress defaults to deny")

json.loads(re.sub(r'\s*//.*', '', re.search(r'```jsonc\n(.*?)\n```', ver, re.S).group(1)))
ok(True, "verdict example is valid JSON")

# ---- SDD traceability: SPEC.md is normative, tests must cite real requirements ----
import glob
spec = open('SPEC.md').read()
reqs = set(re.findall(r'\*\*(R-[A-Z0-9]+-\d+)\*\*', spec))
ok(len(reqs) > 0, f"SPEC.md declares {len(reqs)} requirements")

cited = {}
for f in glob.glob('**/*_test.go', recursive=True):
    for r in re.findall(r'//\s*spec:\s*(R-[A-Z0-9]+-\d+)', open(f).read()):
        cited.setdefault(r, []).append(f)

dangling = set(cited) - reqs
ok(not dangling, "no test cites a non-existent requirement"
   + (f" — dangling: {sorted(dangling)}" if dangling else ""))

# R-DET-9 / R-COV-4: every dep: predicate must be in the spec's closed vocabulary
vocab_tbl = re.search(r'\*\*R-DET-9\*\*.*?\n\n(\| Type.*?)\n\n', spec, re.S).group(1)
vocab = set()
for row in re.findall(r'^\| (.+?) \|', vocab_tbl, re.M)[1:]:
    vocab |= {x for x in re.findall(r'`([a-z0-9]+)`', row)}
deps = {p.split(':', 1)[1] for d in reg['domains'] for t in d['tools']
        for p in str(t['when']).split('|') if p.startswith('dep:')}
ok(deps <= vocab, f"R-DET-9: {len(deps)} dep: predicates all in spec vocabulary ({len(vocab)} types)"
   + (f" — orphaned: {sorted(deps - vocab)}" if deps - vocab else ""))

# R-CLI-1 verbs must match registry `how:` (R-CLI-2)
cli_tbl = re.search(r'\*\*R-CLI-1\*\*.*?\n\n(\|.*?)\n\n', spec, re.S).group(1)
spec_verbs = set(re.findall(r'^\| `(\w+)` \|', cli_tbl, re.M))
ok(spec_verbs == verbs, f"SPEC §7 verbs == registry how: verbs ({len(spec_verbs)})"
   + (f" — differ: {spec_verbs ^ verbs}" if spec_verbs != verbs else ""))

# R-COV-1: SPEC must not restate counts that could drift
ok('registry.yaml` is the source of truth' in spec, "R-COV-1 present: registry is source of truth")

# ---- torture.example.yaml must satisfy the §4 schema rules it illustrates ----
allowed = set(re.search(r'Top-level blocks: (.*?)\.\n', spec).group(1).replace('`', '').split(', '))
ok(set(tor) <= allowed, f"R-CFG-2: example top-level keys ⊆ spec")
ok(tor['egress']['default'] == 'deny', "R-CFG-4: example egress denies by default")
ok(not [h for h, v in tor['egress']['hosts'].items()
        if (v['class'] == 'mock' and 'from' not in v) or (v['class'] == 'real' and 'max_rps' not in v)],
   "R-CFG-5: example mock/real hosts carry required fields")
ok(tor['load']['model'] == 'arrival_rate', "R-CFG-6: example uses the open model")
phases = [x['phase'] for x in tor['load']['stages']]
ok(len(phases) == len(set(phases)), "R-CFG-7: example phase names unique")
ok(not [x for sc in tor['load']['scenarios'] for x in sc['flow']
        if not isinstance(x, dict) or not {'method', 'path'} <= set(x)],
   "R-CFG-9: example flow entries are method/path mappings")

vtbl = re.search(r'\*\*R-CFG-14\*\*.*?\n\n(\| Verb.*?)\n\n', spec, re.S).group(1)
iverbs, imods = set(), set()
for row in re.findall(r'^\| (.+?) \| (.*?) \|', vtbl, re.M)[1:]:
    iverbs |= set(re.findall(r'`([a-z_]+)`', row[0]))
    imods |= set(re.findall(r'`([a-z_]+)`', row[1]))
badv = {f['name']: sorted(set(f['inject']) & iverbs) for f in tor['faults']
        if len(set(f['inject']) & iverbs) != 1}
ok(not badv, "R-CFG-14: exactly one inject verb per fault" + (f" — {badv}" if badv else ""))
badk = {f['name']: sorted(set(f['inject']) - iverbs - imods) for f in tor['faults']
        if set(f['inject']) - iverbs - imods}
ok(not badk, "R-CFG-14: no unknown inject keys" + (f" — {badk}" if badk else ""))

anchors = set(phases)
bada = [f['name'] for f in tor['faults']
        if not (str(f['at']).startswith('t=') or str(f['at']).split('+')[0] in anchors)]
ok(not bada, "R-CFG-12: example faults anchor to declared phases" + (f" — {bada}" if bada else ""))
ok(bool(tor.get('assert')), "R-CFG-19: example assert block is non-empty")

# README states counts; drift here is public-facing, so gate it
readme = open('README.md').read()
ok(f"{len(reqs)} numbered requirements" in readme,
   f"README requirement count matches SPEC ({len(reqs)})")
ok(f"{len(ids)} tools across {len(reg['domains'])} domains" in readme,
   f"README tool/domain count matches registry ({len(ids)}/{len(reg['domains'])})")
ok('.private' not in readme and '.private' in open('.gitignore').read(),
   "private strategy stays out of the repo")

print(f"\n     traceability (citation, not completeness):"
      f" {len(cited)}/{len(reqs)} requirements cited by a test"
      f" ({100*len(cited)//len(reqs)}%)")
for r in sorted(cited):
    print(f"       {r}  <- {', '.join(sorted(set(cited[r])))}")
untested = sorted(reqs - set(cited))
print(f"     untested ({len(untested)}): {', '.join(untested[:8])}"
      + (" ..." if len(untested) > 8 else ""))


# ── R-LIC-1 is a PROJECT-WIDE licence boundary, not a per-package one. k6 is AGPL-3 and this
# project is MIT: we generate k6 script text and shell out, and must never import or link k6.
# internal/k6 guards itself, but a `go.k6.io` import added in any other package would relicense
# the project with nothing to catch it. Surfaced by the Task 4 review.
agpl = []
for root, _dirs, files in os.walk('.'):
    if '/.git' in root or '/.superpowers' in root or '/.private' in root:
        continue
    for f in files:
        if f.endswith('.go'):
            p = os.path.join(root, f)
            # Match real import lines only. A substring occurrence is not a violation:
            # internal/k6's own guard test contains "go.k6.io" precisely because it greps for it.
            if re.search(r'^\s*(?:[\w.]+\s+)?"go\.k6\.io[^"]*"\s*$', open(p).read(), re.M):
                agpl.append(p)
ok(not agpl, "R-LIC-1: no AGPL k6 import anywhere in the tree" + (f" — found in {agpl}" if agpl else ""))

gomod = open('go.mod').read()
ok('go.k6.io' not in gomod and 'k6' not in gomod.split('require')[-1].replace('tortureu', ''),
   "R-LIC-1: go.mod declares no k6 dependency")

# ── Every internal package must be imported by something. "Built but unwired" has now
# happened three times in this project: internal/egress had no caller; ValidateErrorRate was
# written and never invoked (R-EXE-19); internal/applier was built against real Docker and
# referenced by nothing. Each time the tests passed and the capability did not exist.
pkgs, imports = {}, set()
for root, _dirs, files in os.walk('.'):
    if any(x in root for x in ('/.git', '/.superpowers', '/.private')):
        continue
    for f in files:
        if not f.endswith('.go'):
            continue
        p = os.path.join(root, f)
        rel = os.path.relpath(root, '.')
        if rel.startswith('internal'):
            pkgs.setdefault(rel.replace(os.sep, '/'), p)
        for m in re.finditer(r'"github\.com/jdb316/tortureu/(internal/[\w/]+)"', open(p).read()):
            if not root.replace(os.sep, '/').endswith(m.group(1)):
                imports.add(m.group(1))
orphans = sorted(set(pkgs) - imports)
ok(not orphans, "every internal package has a caller" + (f" — UNWIRED: {orphans}" if orphans else ""))

# ── A registry `how:` that names a tortureu verb must name one that works, or be marked
# `planned:`. The final review found ~34 entries instructing users to run verbs that exit 2,
# and a header naming `tortureu suggest`, which is not a verb at all. Telling someone to run a
# command that cannot run is the documentation form of "built but unreachable".
IMPLEMENTED = {'init', 'run', 'doctor'}
mis = []
for d in reg['domains']:
    for t in d['tools']:
        m = re.match(r'tortureu (\w+)', str(t['how']))
        if m and m.group(1) not in IMPLEMENTED and 'planned' not in t:
            mis.append(f"{d['id']}/{t['id']} -> {t['how']}")
ok(not mis, "every registry `how:` names a working verb or is marked planned"
   + (f" — unmarked: {mis[:3]}" if mis else ""))

planned = sum(1 for d in reg['domains'] for t in d['tools'] if 'planned' in t)
print(f"     registry: {planned} entries marked planned (verb not implemented in v0)")

print(f"\n{len(fails)} failure(s)" if fails else "\nall checks passed")
sys.exit(1 if fails else 0)
