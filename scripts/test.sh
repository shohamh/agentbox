#!/usr/bin/env bash
# Builds the stack, runs the end-to-end and console suites inside the Compose
# network, and prints a pass/fail table. See docs/testing.md.
#
#   scripts/test.sh                 everything (E2E + tamper + degraded + UI)
#   TEST=E2E-03 scripts/test.sh     one test (also: UI-05, T7, DEG, TAMPER, E2E, UI; comma separated)
#   KEEP=1 scripts/test.sh          leave the stack up afterwards
#   STACK_UP=1 scripts/test.sh      stack is already running: only run the tests
#   SKIP_BUILD=1 scripts/test.sh    do not rebuild binaries and images (scripts/release.sh)
#   PROFILES="tamper dev"           extra Compose profiles to start (default: chosen from TEST)
set -uo pipefail
cd "$(dirname "$0")/.."
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'
# Short model keys in the test bed so E2E-18 can watch a key being replaced (production default: 15m).
export AGENTBOX_KEY_TTL="${AGENTBOX_KEY_TTL:-90s}"

PROJECT="${COMPOSE_PROJECT:-agentbox}"
FILES=(-f deploy/compose/docker-compose.yml -f deploy/compose/appliance.yml)
compose() {
  local env=(); [ -f deploy/compose/.secrets/compose.env ] && env=(--env-file deploy/compose/.secrets/compose.env)
  docker compose -p "$PROJECT" "${FILES[@]}" "${env[@]}" "$@"
}
RESULTS="test/results"
mkdir -p "$RESULTS"
: > "$RESULTS/go.log"
rm -rf "$RESULTS/ui"
mkdir -p "$RESULTS/ui"

die() { echo "test.sh: $*" >&2; exit 2; }
step() { echo; echo "==> $*"; }
need_script() { [ -f "$1" ] || die "$1 is missing (expected from $2)"; }

# ---------- what to run ----------
SEL="${TEST:-}"
GO_RE=""
UI_GREP=""
RUN_GO=1
RUN_UI=1
if [ -n "$SEL" ]; then
  RUN_GO=0
  RUN_UI=0
  IFS=',' read -ra ITEMS <<< "$SEL"
  for it in "${ITEMS[@]}"; do
    it="$(echo "$it" | tr -d ' ')"
    case "$it" in
      E2E)     RUN_GO=1; GO_RE="${GO_RE:+$GO_RE|}^TestE2E_" ;;
      UI)      RUN_UI=1; UI_GREP="${UI_GREP:+$UI_GREP|}UI-" ;;
      DEG)     RUN_GO=1; GO_RE="${GO_RE:+$GO_RE|}^TestE2E_1[1-5]_" ;;
      TAMPER)  RUN_GO=1; GO_RE="${GO_RE:+$GO_RE|}^TestT[0-9]+_" ;;
      E2E-*)   RUN_GO=1; GO_RE="${GO_RE:+$GO_RE|}^TestE2E_${it#E2E-}_" ;;
      T[0-9]*) RUN_GO=1; GO_RE="${GO_RE:+$GO_RE|}^Test${it}_" ;;
      UI-*)    RUN_UI=1; UI_GREP="${UI_GREP:+$UI_GREP|}${it}( |\$)" ;;
      *) die "unknown TEST item '$it' (E2E-nn, Tn, UI-nn, E2E, DEG, TAMPER, UI)" ;;
    esac
  done
fi

# Profiles: tamper (E2E-10, T1, T2) and dev (T3) need extra appliances, which cost RAM.
if [ -z "${PROFILES+x}" ]; then
  PROFILES=""
  if [ -z "$SEL" ] || echo "$SEL" | grep -qE 'E2E-10|T[12]($|,)|TAMPER|(^|,)E2E($|,)'; then PROFILES="tamper"; fi
  if [ -z "$SEL" ] || echo "$SEL" | grep -qE 'T3($|,)|TAMPER'; then PROFILES="$PROFILES dev"; fi
fi
PROFILE_ARGS=()
for p in $PROFILES; do PROFILE_ARGS+=(--profile "$p"); done

# ---------- stack ----------
cleanup() {
  if [ "${KEEP:-0}" != "1" ] && [ "${STACK_UP:-0}" != "1" ]; then
    step "tearing the stack down"
    compose --profile tamper --profile dev down -v --remove-orphans >/dev/null 2>&1 || true
  else
    echo "stack left running; stop it with: docker compose -p $PROJECT ${FILES[*]} --profile tamper --profile dev down -v"
  fi
}
trap cleanup EXIT

if [ "${STACK_UP:-0}" != "1" ]; then
  if [ "${SKIP_BUILD:-0}" != "1" ]; then
    step "building binaries, generating keys, building the appliance image and signed release manifests"
    need_script scripts/release.sh "the appliance worker"
    # Keys first: the release signs with them and the appliance image bakes in the policy public key.
    bash scripts/build-bins.sh && bash scripts/bootstrap.sh || die "bootstrap failed"
    SKIP_BINS=1 bash scripts/release.sh || die "release build failed"
  fi
  step "bringing up the control plane"
  need_script scripts/up.sh "the control-plane worker"
  bash scripts/up.sh || die "control plane did not come up"
  step "publishing sandbox images to the internal registry"
  need_script scripts/publish-images.sh "the appliance worker"
  bash scripts/publish-images.sh || die "image publish failed"
  step "starting the appliances and PCs ${PROFILES:+(profiles: $PROFILES)}"
  compose "${PROFILE_ARGS[@]}" up -d || die "appliances did not start"
fi

CON="$(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT" --filter "label=com.docker.compose.service=console" | head -1)"
[ -n "$CON" ] || die "no console container in project $PROJECT: is the stack up?"
NET="$(docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$CON" | awk '{print $1}')"
[ -n "$NET" ] || die "cannot find the stack network"
echo "stack network: $NET"

# ---------- suites ----------
FAIL=0
if [ "$RUN_GO" = "1" ]; then
  step "building the e2e suite image"
  docker build -q -f test/Dockerfile -t agentbox-tests . >/dev/null || die "e2e image build failed (try: scripts/go.sh vet -tags e2e ./test/...)"
  step "running the Go suites ${GO_RE:+(-test.run '$GO_RE')}"
  docker run --rm --network "$NET" -v /var/run/docker.sock:/var/run/docker.sock \
    -e COMPOSE_PROJECT="$PROJECT" -e PERF_STRICT="${PERF_STRICT:-}" -e STRICT_MESSAGES="${STRICT_MESSAGES:-}" \
    -e E2E05_SOAK_SECS="${E2E05_SOAK_SECS:-45}" -e SAMPLE_REPO="${SAMPLE_REPO:-}" \
    agentbox-tests -test.v -test.timeout 120m ${GO_RE:+-test.run "$GO_RE"} 2>&1 | tee "$RESULTS/go.log"
  [ "${PIPESTATUS[0]}" = "0" ] || FAIL=1
fi

if [ "$RUN_UI" = "1" ]; then
  step "building the Playwright image"
  docker build -q -f test/ui/Dockerfile -t agentbox-ui-tests . >/dev/null || die "ui image build failed"
  step "running the console suite ${UI_GREP:+(-g '$UI_GREP')}"
  docker rm -f agentbox-ui-run >/dev/null 2>&1
  docker run --name agentbox-ui-run --network "$NET" -v /var/run/docker.sock:/var/run/docker.sock \
    -e COMPOSE_PROJECT="$PROJECT" -e SPLUNK_HEC_URL="${SPLUNK_HEC_URL:-}" -e SPLUNK_HEC_TOKEN="${SPLUNK_HEC_TOKEN:-}" -e SAMPLE_REPO="${SAMPLE_REPO:-}" \
    agentbox-ui-tests npx playwright test ${UI_GREP:+-g "$UI_GREP"} 2>&1 | tee "$RESULTS/ui.log"
  [ "${PIPESTATUS[0]}" = "0" ] || FAIL=1
  docker cp agentbox-ui-run:/ui/test-results "$RESULTS/ui/" >/dev/null 2>&1
  docker cp agentbox-ui-run:/ui/playwright-report "$RESULTS/ui/" >/dev/null 2>&1
  docker rm -f agentbox-ui-run >/dev/null 2>&1
fi

# ---------- report ----------
step "results"
PY=""; for c in python3 python py; do if command -v "$c" >/dev/null 2>&1 && "$c" -c "import sys" >/dev/null 2>&1; then PY="$c"; break; fi; done
if [ -n "$PY" ]; then
  "$PY" - "$RESULTS" <<'PYEOF'
import json, os, re, sys

res = sys.argv[1]
rows = []
try:
    for line in open(os.path.join(res, "go.log"), encoding="utf-8", errors="replace"):
        m = re.match(r"^--- (PASS|FAIL|SKIP): (Test\w+) \(([\d.]+)s\)", line)
        if m:
            st, name, secs = m.groups()
            idm = re.match(r"Test(E2E)_(\d+)_|Test(T\d+)_", name)
            if idm and idm.group(1):
                ident = "E2E-" + idm.group(2)
            elif idm:
                ident = idm.group(3)
            else:
                ident = name
            rows.append((st, ident, name, secs + "s"))
except FileNotFoundError:
    pass


def walk(s):
    for sp in s.get("specs", []):
        t = sp["tests"][0] if sp.get("tests") else {}
        st = t.get("status", "")
        r = (t.get("results") or [{}])[-1]
        status = {"expected": "PASS", "skipped": "SKIP", "unexpected": "FAIL", "flaky": "FLAKY"}.get(st, "PASS" if sp.get("ok") else "FAIL")
        m = re.match(r"(UI-\d+)\s*(.*)", sp["title"])
        rows.append((status, m.group(1) if m else sp["title"], (m.group(2) if m else "")[:70], "%.1fs" % (r.get("duration", 0) / 1000)))
    for c in s.get("suites", []):
        walk(c)


try:
    j = json.load(open(os.path.join(res, "ui", "test-results", "results.json")))
    for s in j.get("suites", []):
        walk(s)
except Exception:
    pass

print("%-6s %-8s %-9s %s" % ("RESULT", "ID", "TIME", "TEST"))
for st, i, n, d in rows:
    print("%-6s %-8s %-9s %s" % (st, i, d, n))
bad = [r for r in rows if r[0] in ("FAIL", "FLAKY")]
print("\n%d tests: %d passed, %d failed, %d skipped" % (len(rows), sum(r[0] == "PASS" for r in rows), len(bad), sum(r[0] == "SKIP" for r in rows)))
open(os.path.join(res, "summary.txt"), "w").write("\n".join("%s %s %s" % (r[0], r[1], r[3]) for r in rows) + "\n")
sys.exit(1 if bad else 0)
PYEOF
  [ $? -eq 0 ] || FAIL=1
else
  echo "(python not found: see $RESULTS/go.log and $RESULTS/ui.log)"
  grep -E '^--- (PASS|FAIL|SKIP)' "$RESULTS/go.log" 2>/dev/null
fi
if grep -h 'RECORD ' "$RESULTS/go.log" 2>/dev/null | sed 's/^ *//' > "$RESULTS/records.txt" && [ -s "$RESULTS/records.txt" ]; then
  echo
  echo "recorded findings:"
  cat "$RESULTS/records.txt"
fi
exit "$FAIL"
