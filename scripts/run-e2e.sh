#!/usr/bin/env bash
#
# MonStack-Go End-to-End Test Suite Runner
# Usage: ./scripts/run-e2e.sh [tier] [options]
#   tier: harness | tier1 | tier2 | tier3 | tier4 | all (default: all)
#

set -eo pipefail

# ANSI Color Codes
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

# Base directory
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${REPO_ROOT}"

TARGET_TIER="${1:-all}"
GO_TEST_FLAGS="-v -count=1 -timeout=10m"

print_header() {
    echo -e "\n${BLUE}======================================================================${NC}"
    echo -e "${BOLD}${CYAN}   MonStack-Go E2E Test Runner: ${YELLOW}${1}${NC}"
    echo -e "${BLUE}======================================================================${NC}\n"
}

run_tier() {
    local name="$1"
    local path="$2"

    if [ ! -d "${path}" ]; then
        echo -e "${YELLOW}Notice: Directory ${path} does not exist yet (skipping tier ${name})${NC}"
        return 0
    fi

    # Check if there are any test files
    local count
    count=$(find "${path}" -name "*_test.go" | wc -l | tr -d ' ')
    if [ "${count}" -eq 0 ]; then
        echo -e "${YELLOW}Notice: No test files found in ${path} (skipping tier ${name})${NC}"
        return 0
    fi

    echo -e "${CYAN}--> Running [${BOLD}${name}${NC}${CYAN}] tests (${path})...${NC}"
    local start_time
    start_time=$(date +%s)

    if go test ${GO_TEST_FLAGS} "${path}/..."; then
        local end_time
        end_time=$(date +%s)
        local elapsed=$(( end_time - start_time ))
        echo -e "${GREEN}✓ [${name}] PASSED (${elapsed}s)${NC}\n"
        return 0
    else
        local end_time
        end_time=$(date +%s)
        local elapsed=$(( end_time - start_time ))
        echo -e "${RED}✗ [${name}] FAILED (${elapsed}s)${NC}\n"
        return 1
    fi
}

print_header "${TARGET_TIER}"

TOTAL_FAILED=0

case "${TARGET_TIER}" in
    harness)
        run_tier "Harness Self-Tests" "./tests/e2e/harness" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        ;;
    tier1)
        run_tier "Tier 1: Feature Coverage" "./tests/e2e/tier1_features" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        ;;
    tier2)
        run_tier "Tier 2: Boundary & Corner Cases" "./tests/e2e/tier2_boundaries" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        ;;
    tier3)
        run_tier "Tier 3: Cross-Feature Combinations" "./tests/e2e/tier3_combinations" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        ;;
    tier4)
        run_tier "Tier 4: Real-World Scenarios" "./tests/e2e/tier4_workloads" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        ;;
    all)
        run_tier "Harness Self-Tests" "./tests/e2e/harness" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        run_tier "Tier 1: Feature Coverage" "./tests/e2e/tier1_features" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        run_tier "Tier 2: Boundary & Corner Cases" "./tests/e2e/tier2_boundaries" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        run_tier "Tier 3: Cross-Feature Combinations" "./tests/e2e/tier3_combinations" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        run_tier "Tier 4: Real-World Scenarios" "./tests/e2e/tier4_workloads" || TOTAL_FAILED=$((TOTAL_FAILED + 1))
        ;;
    *)
        echo -e "${RED}Unknown tier: ${TARGET_TIER}${NC}"
        echo "Valid tiers: harness, tier1, tier2, tier3, tier4, all"
        exit 2
        ;;
esac

echo -e "${BLUE}======================================================================${NC}"
if [ "${TOTAL_FAILED}" -eq 0 ]; then
    echo -e "${GREEN}${BOLD}★ ALL TARGETED E2E TEST TIERS PASSED SUCCESSFULLY! ★${NC}"
    echo -e "${BLUE}======================================================================${NC}\n"
    exit 0
else
    echo -e "${RED}${BOLD}✖ ${TOTAL_FAILED} E2E TEST TIER(S) FAILED ✖${NC}"
    echo -e "${BLUE}======================================================================${NC}\n"
    exit 1
fi
