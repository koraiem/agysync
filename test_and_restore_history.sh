#!/bin/bash
set -e

AGYSYNC_BIN="/Users/ahmed.koraiem/dev/agysync/bin/agysync"
BACKUP_SRC="/Users/ahmed.koraiem/agy_history/.gemini"
GEMINI_DIR="/Users/ahmed.koraiem/.gemini"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
CURRENT_BACKUP="/Users/ahmed.koraiem/.gemini_backup_current_${TIMESTAMP}"

echo "============================================================"
echo "      Antigravity Chat History Test & Restore Utility       "
echo "============================================================"
echo ""
echo "Source Backup: ${BACKUP_SRC}"
echo "Current Env:   ${GEMINI_DIR}"
echo ""

prompt_step() {
    local step_num="$1"
    local step_title="$2"
    echo ""
    echo "============================================================"
    echo "STEP ${step_num}: ${step_title}"
    echo "============================================================"
    read -p "Press Enter to execute Step ${step_num} (or Ctrl+C to cancel)... "
}

prompt_confirm() {
    local step_num="$1"
    local question="$2"
    echo ""
    echo "------------------------------------------------------------"
    echo "CHECKPOINT AFTER STEP ${step_num}:"
    echo "${question}"
    echo "------------------------------------------------------------"
    read -p "Type 'yes' when confirmed to proceed to the next step: " resp
    if [ "$resp" != "yes" ]; then
        echo "Aborted by user."
        exit 1
    fi
}

# --- STEP 1: Backup Current Environment ---
prompt_step 1 "Create Backup of Current Gemini Environment"
echo "Creating backup of ${GEMINI_DIR} at ${CURRENT_BACKUP}..."
cp -R "${GEMINI_DIR}" "${CURRENT_BACKUP}"
echo "✓ Backup created successfully at: ${CURRENT_BACKUP}"

prompt_confirm 1 "Please verify that the backup folder ${CURRENT_BACKUP} exists and is populated."

# --- STEP 2: Reset to Clean Environment ---
prompt_step 2 "Reset Gemini Environment to a Clean State"
echo "Clearing active chat conversations, brain data, and CLI history..."

# Preserve configuration/token files while clearing conversation & brain state
rm -rf "${GEMINI_DIR}/antigravity/conversations"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity/brain"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-ide/conversations"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-ide/brain"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-cli/conversations"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-cli/brain"/* 2>/dev/null || true
rm -rf "${GEMINI_DIR}/antigravity-cli/history.jsonl" 2>/dev/null || true

echo "✓ Environment reset complete."

prompt_confirm 2 "Clean state initialized. Press Enter / type 'yes' to proceed with merging agy_history."

# --- STEP 3: Merge agy_history into Clean Environment & Translate ---
prompt_step 3 "Merge agy_history into Clean Environment & Translate Paths"
echo "Merging from ${BACKUP_SRC}..."
"${AGYSYNC_BIN}" -src "${BACKUP_SRC}" -sync -v
echo "Running path translation pass..."
"${AGYSYNC_BIN}" -translate -v

echo "✓ Merge and path translation completed."

prompt_confirm 3 "Please open your Antigravity IDE / CLI now. Verify if your older chats from agy_history are accessible. Have you confirmed the data is visible?"

# --- STEP 4: Restore Current Backup & Clean Merge ---
prompt_step 4 "Restore Original Environment & Perform Combined Clean Merge"
echo "Restoring original environment from ${CURRENT_BACKUP}..."
rm -rf "${GEMINI_DIR}"
cp -R "${CURRENT_BACKUP}" "${GEMINI_DIR}"

echo "Merging older history from ${BACKUP_SRC} into restored environment..."
"${AGYSYNC_BIN}" -src "${BACKUP_SRC}" -sync -v
echo "Force-translating all local conversation database paths..."
"${AGYSYNC_BIN}" -translate -v

echo "✓ Full combined restore and clean merge complete."

prompt_confirm 4 "Please verify in Antigravity IDE / CLI that BOTH your current chats and older chats from agy_history are now fully accessible."

echo ""
echo "🎉 Process finished successfully! You can safely keep or delete the backup at ${CURRENT_BACKUP}."
