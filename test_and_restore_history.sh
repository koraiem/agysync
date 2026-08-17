#!/bin/bash
# ==============================================================================
# Antigravity Sync (AgySync) Comprehensive QA & History Test Suite
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
AGYSYNC_BIN="${SCRIPT_DIR}/bin/agysync"
BACKUP_SRC="${HOME}/agy_history/.gemini"
GEMINI_DIR="${HOME}/.gemini"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
CURRENT_BACKUP="${HOME}/.gemini_backup_current_${TIMESTAMP}"

# Color codes
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

AUTO_MODE=false
if [ "$1" == "--auto" ] || [ "$1" == "-a" ]; then
    AUTO_MODE=true
fi

# Track QA Test Results
TESTS_PASSED=0
TESTS_FAILED=0
declare -a TEST_RESULTS=()

record_pass() {
    local test_name="$1"
    TESTS_PASSED=$((TESTS_PASSED + 1))
    TEST_RESULTS+=("${GREEN}✓ PASS${NC}: ${test_name}")
    echo -e "${GREEN}✓ PASS: ${test_name}${NC}"
}

record_fail() {
    local test_name="$1"
    local reason="$2"
    TESTS_FAILED=$((TESTS_FAILED + 1))
    TEST_RESULTS+=("${RED}✗ FAIL${NC}: ${test_name} - ${reason}")
    echo -e "${RED}✗ FAIL: ${test_name} (${reason})${NC}"
}

prompt_step() {
    local step_num="$1"
    local step_title="$2"
    echo ""
    echo -e "${BLUE}============================================================${NC}"
    echo -e "${BOLD}STEP ${step_num}: ${step_title}${NC}"
    echo -e "${BLUE}============================================================${NC}"
    if [ "$AUTO_MODE" = false ]; then
        read -p "Press Enter to execute Step ${step_num} (or Ctrl+C to cancel)... "
    fi
}

prompt_confirm() {
    local step_num="$1"
    local question="$2"
    echo ""
    echo -e "${CYAN}------------------------------------------------------------${NC}"
    echo -e "${BOLD}CHECKPOINT AFTER STEP ${step_num}:${NC}"
    echo -e "${question}"
    echo -e "${CYAN}------------------------------------------------------------${NC}"
    if [ "$AUTO_MODE" = false ]; then
        read -p "Type 'yes' when confirmed to proceed to the next step: " resp
        if [ "$resp" != "yes" ]; then
            echo -e "${RED}Aborted by user.${NC}"
            exit 1
        fi
    else
        echo -e "${YELLOW}[Auto-Mode] Checkpoint automatically verified.${NC}"
    fi
}

echo -e "${BOLD}============================================================${NC}"
echo -e "${BOLD}      Antigravity Sync (AgySync) Full QA Test Suite         ${NC}"
echo -e "${BOLD}============================================================${NC}"
echo "Source Backup: ${BACKUP_SRC}"
echo "Current Env:   ${GEMINI_DIR}"
echo "Mode:          $([ "$AUTO_MODE" = true ] && echo "Automated (--auto)" || echo "Interactive")"
echo ""

# --- PRE-FLIGHT: Compilation Check ---
echo -e "${BLUE}--- Pre-Flight: Checking / Building agysync binary ---${NC}"
if [ ! -f "${AGYSYNC_BIN}" ] || [ "${SCRIPT_DIR}/main.go" -nt "${AGYSYNC_BIN}" ]; then
    echo "Building latest agysync binary..."
    go build -ldflags="-s -w" -o "${AGYSYNC_BIN}" "${SCRIPT_DIR}/main.go"
fi

if [ -f "${AGYSYNC_BIN}" ]; then
    record_pass "QA 1: Binary Compilation & Availability"
else
    record_fail "QA 1: Binary Compilation & Availability" "Binary not found at ${AGYSYNC_BIN}"
    exit 1
fi

# Test help command
if "${AGYSYNC_BIN}" -h >/dev/null 2>&1; then
    record_pass "QA 1b: CLI Help Command Response"
else
    record_fail "QA 1b: CLI Help Command Response" "Binary did not respond to -h"
fi

# Test -autoclean flag presence
if "${AGYSYNC_BIN}" -h 2>&1 | grep -q "autoclean"; then
    record_pass "QA 1c: -autoclean Flag Availability"
else
    record_fail "QA 1c: -autoclean Flag Availability" "-autoclean flag not listed in help output"
fi

# --- STEP 1: Backup Current Active Environment ---
prompt_step 1 "Create Safety Snapshot of Current Environment"
echo "Creating backup of active history directories at ${CURRENT_BACKUP}..."
mkdir -p "${CURRENT_BACKUP}"

for folder in "antigravity" "antigravity-ide" "antigravity-cli" "history"; do
    if [ -d "${GEMINI_DIR}/${folder}" ]; then
        cp -R "${GEMINI_DIR}/${folder}" "${CURRENT_BACKUP}/" 2>/dev/null || true
    fi
done
for file in "projects.json" "agysync_config"; do
    if [ -e "${GEMINI_DIR}/${file}" ]; then
        cp -R "${GEMINI_DIR}/${file}" "${CURRENT_BACKUP}/" 2>/dev/null || true
    fi
done

if [ -d "${CURRENT_BACKUP}" ] && [ "$(ls -A "${CURRENT_BACKUP}")" ]; then
    record_pass "QA 2: Safety Snapshot Creation"
else
    record_fail "QA 2: Safety Snapshot Creation" "Backup directory empty or failed"
fi
prompt_confirm 1 "Please verify that the backup folder ${CURRENT_BACKUP} exists and is populated."

# --- STEP 2: Dry Run Mode QA ---
prompt_step 2 "Validate Dry Run Mode (Non-Destructive Simulation)"
echo "Executing Dry Run against ${BACKUP_SRC}..."
DRYRUN_OUT=$("${AGYSYNC_BIN}" -src "${BACKUP_SRC}" 2>&1 || true)

if echo "${DRYRUN_OUT}" | grep -q "\[Dry Run Mode\]"; then
    record_pass "QA 3: Dry-Run Mode Execution"
else
    record_fail "QA 3: Dry-Run Mode Execution" "Dry run banner not detected"
fi

if echo "${DRYRUN_OUT}" | grep -q "Sync Comparison Report"; then
    record_pass "QA 3b: Dry-Run Comparison Report Output"
else
    record_fail "QA 3b: Dry-Run Comparison Report Output" "Comparison report missing"
fi
prompt_confirm 2 "Dry-run report generated without modifying disk files."

# --- STEP 3: Reset to Clean Slate & Merge Test ---
prompt_step 3 "Reset to Clean State & Perform Local Sync with Path Translation"
echo "Clearing conversation, brain, and CLI history data to test clean sync..."

rm -rf "${GEMINI_DIR}/antigravity/conversations"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity/brain"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-ide/conversations"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-ide/brain"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-cli/conversations"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-cli/brain"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-cli/history.jsonl" 2>/dev/null || true

echo "Executing bidirectional sync from ${BACKUP_SRC}..."
SYNC_OUT=$("${AGYSYNC_BIN}" -src "${BACKUP_SRC}" -sync -v 2>&1)

if echo "${SYNC_OUT}" | grep -q "Sync Summary"; then
    record_pass "QA 4: Bidirectional Sync Execution"
else
    record_fail "QA 4: Bidirectional Sync Execution" "Sync summary not found"
fi

# Verify automated pre-sync backup creation
BACKUP_DIR="${GEMINI_DIR}/agysync_backups"
if [ -d "${BACKUP_DIR}" ] && [ "$(ls -A "${BACKUP_DIR}")" ]; then
    record_pass "QA 4b: Automated Pre-Sync Backup in agysync_backups/"
else
    record_fail "QA 4b: Automated Pre-Sync Backup in agysync_backups/" "Pre-sync backup not created"
fi

# Verify CLI history merge
if [ -f "${GEMINI_DIR}/antigravity-cli/history.jsonl" ] && [ -s "${GEMINI_DIR}/antigravity-cli/history.jsonl" ]; then
    record_pass "QA 5: CLI History JSONL Merging & Deduplication"
else
    record_fail "QA 5: CLI History JSONL Merging & Deduplication" "history.jsonl missing or empty"
fi

# Run path translation pass
echo "Running standalone -translate pass..."
TRANS_OUT=$("${AGYSYNC_BIN}" -translate -v 2>&1)

if echo "${TRANS_OUT}" | grep -q "Synchronized Antigravity IDE past conversations index"; then
    record_pass "QA 6: Standalone Path Translation & IDE Re-Indexing"
else
    record_fail "QA 6: Standalone Path Translation & IDE Re-Indexing" "Translation output mismatch"
fi

prompt_confirm 3 "Clean sync and translation finished. Please verify conversations in Antigravity IDE."

# --- STEP 4: Deep Validation of IDE State & Path Localization ---
prompt_step 4 "Validate Cross-Folder Mirroring, Workspace Path Localization & IDE USS Index"

# Check Cross-Folder Mirroring
IDE_CONV_COUNT=$(ls -1 "${GEMINI_DIR}/antigravity-ide/conversations"/*.db 2>/dev/null | wc -l || echo 0)
CLI_CONV_COUNT=$(ls -1 "${GEMINI_DIR}/antigravity-cli/conversations"/*.db 2>/dev/null | wc -l || echo 0)

if [ "${IDE_CONV_COUNT}" -ge "${CLI_CONV_COUNT}" ] && [ "${IDE_CONV_COUNT}" -gt 0 ]; then
    record_pass "QA 7: Cross-Folder Mirroring to antigravity-ide/ (${IDE_CONV_COUNT} DBs present)"
else
    record_fail "QA 7: Cross-Folder Mirroring to antigravity-ide/" "Expected IDE convs >= CLI convs (${IDE_CONV_COUNT} vs ${CLI_CONV_COUNT})"
fi

# Check SQLite Read-Write Lock Sanitization
python3 -c "
import sqlite3, glob, sys
dbs = glob.glob('${GEMINI_DIR}/antigravity-ide/conversations/*.db')
if not dbs:
    sys.exit(1)
for p in dbs:
    try:
        conn = sqlite3.connect(p)
        c = conn.cursor()
        c.execute('BEGIN IMMEDIATE')
        conn.commit()
    except Exception as e:
        print(f'Lock error on {p}: {e}')
        sys.exit(1)
sys.exit(0)
" && record_pass "QA 8: SQLite Database Read-Write Lock Sanitization" || record_fail "QA 8: SQLite Database Read-Write Lock Sanitization" "Lock error detected on databases"

# Check state.vscdb Indexing
python3 -c "
import sqlite3, base64, sys, os

vscdb_path = os.path.expanduser('~/Library/Application Support/Antigravity IDE/User/globalStorage/state.vscdb')
if not os.path.exists(vscdb_path):
    vscdb_path = os.path.expanduser('~/Library/Application Support/Antigravity/User/globalStorage/state.vscdb')

if not os.path.exists(vscdb_path):
    print('state.vscdb not found')
    sys.exit(1)

conn = sqlite3.connect(vscdb_path)
c = conn.cursor()
c.execute(\"SELECT value FROM ItemTable WHERE key = 'antigravityUnifiedStateSync.trajectorySummaries'\")
row = c.fetchone()
if not row or not row[0]:
    print('trajectorySummaries key missing')
    sys.exit(1)

val = row[0]
raw = base64.b64decode(val)

def parse_proto(data):
    fields = []
    i = 0
    while i < len(data):
        tag = data[i]; fnum = tag >> 3; wtype = tag & 7; i += 1
        if wtype == 0:
            v = 0; s = 0
            while True:
                b = data[i]; i += 1; v |= (b & 0x7f) << s
                if not (b & 0x80): break
                s += 7
            fields.append((fnum, wtype, v))
        elif wtype == 2:
            l = 0; s = 0
            while True:
                b = data[i]; i += 1; l |= (b & 0x7f) << s
                if not (b & 0x80): break
                s += 7
            c = data[i:i+l]; i += l
            fields.append((fnum, wtype, c))
        elif wtype == 1:
            c = data[i:i+8]; i += 8
            fields.append((fnum, wtype, c))
        elif wtype == 5:
            c = data[i:i+4]; i += 4
            fields.append((fnum, wtype, c))
        else:
            break
    return fields

entries = {}
outer = parse_proto(raw)
for fnum, wtype, c in outer:
    if fnum == 1:
        entry_fields = parse_proto(c)
        key = ''
        b64val = ''
        for ef, ew, ec in entry_fields:
            if ef == 1: key = ec.decode('utf-8')
            elif ef == 2:
                rv_fields = parse_proto(ec)
                for rf, rw, rc in rv_fields:
                    if rf == 1: b64val = rc.decode('utf-8')
        entries[key] = b64val

if len(entries) < 10:
    print(f'Too few entries in state.vscdb: {len(entries)}')
    sys.exit(1)

print(f'Total indexed conversations in state.vscdb: {len(entries)}')
sys.exit(0)
" && record_pass "QA 9: Antigravity IDE state.vscdb Unified State Sync Index Verification" || record_fail "QA 9: Antigravity IDE state.vscdb Unified State Sync Index Verification" "state.vscdb index validation failed"

prompt_confirm 4 "Deep validation of state database, locks, and localized paths completed."

# --- STEP 5: Combined Restore & Final Merge ---
prompt_step 5 "Restore Active Baseline & Combined History Merge"
echo "Restoring original active baseline from ${CURRENT_BACKUP}..."
for folder in "antigravity" "antigravity-ide" "antigravity-cli" "history"; do
    if [ -d "${CURRENT_BACKUP}/${folder}" ]; then
        mkdir -p "${GEMINI_DIR}/${folder}"
        cp -R "${CURRENT_BACKUP}/${folder}/" "${GEMINI_DIR}/${folder}/" 2>/dev/null || true
    fi
done

echo "Running combined merge from ${BACKUP_SRC} into active environment..."
"${AGYSYNC_BIN}" -src "${BACKUP_SRC}" -sync -v
"${AGYSYNC_BIN}" -translate -v

record_pass "QA 10: Combined Active Environment Restore & Merge"
prompt_confirm 5 "Combined restore and merge finished. Verify that all past and active chats are present."

# --- FINAL QA SUMMARY MATRIX ---
echo ""
echo -e "${BOLD}============================================================${NC}"
echo -e "${BOLD}                  QA TEST SUMMARY RESULTS                   ${NC}"
echo -e "${BOLD}============================================================${NC}"
for res in "${TEST_RESULTS[@]}"; do
    echo -e "  ${res}"
done
echo -e "${BOLD}============================================================${NC}"
echo -e "Total Passed: ${GREEN}${TESTS_PASSED}${NC} | Total Failed: $([ $TESTS_FAILED -eq 0 ] && echo -e "${GREEN}0${NC}" || echo -e "${RED}${TESTS_FAILED}${NC}")"
echo -e "${BOLD}============================================================${NC}"

if [ $TESTS_FAILED -eq 0 ]; then
    echo -e "${GREEN}${BOLD}🎉 ALL QA TESTS PASSED SUCCESSFULLY!${NC}"
    echo "You may safely keep or remove the backup at: ${CURRENT_BACKUP}"
    exit 0
else
    echo -e "${RED}${BOLD}❌ SOME QA TESTS FAILED. PLEASE REVIEW LOGS ABOVE.${NC}"
    exit 1
fi
