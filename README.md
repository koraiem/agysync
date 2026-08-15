# AgySync: Antigravity Cross-Platform History Synchronizer

**AgySync** is a lightweight, cross-platform synchronization tool that keeps your Google Antigravity chat conversations, task artifacts, and CLI history in sync across macOS, Linux, and Windows.

---

## Key Features

- 🔄 **Bidirectional History Sync**: Syncs conversations, brain logs, workspace tasks, and CLI command history.
- ⚡ **Antigravity IDE Past Conversations Detection**: Integrates with Antigravity IDE's SQLite state store (`state.vscdb`) and Unified State Sync (USS) protobuf topics so all synced chats appear in the **"Past Conversations"** menu.
- 🗺️ **Cross-Platform Path Localization**: Automatically translates Linux (`/home/<user>/...`), macOS (`/Users/<user>/...`), and Windows (`C:/Users/<user>/...`) workspace paths and maps project directories dynamically via `projects.json`.
- ☁️ **Zero-Trust Google Drive AppData**: Safely backs up to your private, hidden Google Drive application space (`drive.appdata`) with Google SSO. No external servers or third-party databases.
- 🛡️ **Automated Safety Backups**: Takes an isolated snapshot of active histories in `~/.gemini/agysync_backups/` before any sync modifications occur.
- 🧪 **Safe Dry-Run Comparison**: Run without `-sync` to inspect what files and commands would change before writing anything.
- 🔒 **Lock & Permission Sanitization**: Automatically resolves orphaned `.db-shm` / `.db-wal` locks and ensures correct read/write permissions.

---

## Directory Mappings

AgySync synchronizes the following components inside `~/.gemini`:

| Component | Directory | Description |
| :--- | :--- | :--- |
| **CLI Conversations** | `antigravity-cli/conversations/` | CLI SQLite conversation databases |
| **CLI Brain** | `antigravity-cli/brain/` | CLI session transcripts and artifacts |
| **Core Conversations** | `antigravity/conversations/` | Core Antigravity conversation databases |
| **Core Brain** | `antigravity/brain/` | Core Antigravity session transcripts |
| **IDE Conversations** | `antigravity-ide/conversations/` | Antigravity IDE conversation databases |
| **IDE Brain** | `antigravity-ide/brain/` | Antigravity IDE session transcripts |
| **Workspace Tasks** | `history/` | Workspace task checkpoints |
| **CLI Command History** | `antigravity-cli/history.jsonl` | Timestamped command history |
| **IDE Global State** | `state.vscdb` | Unified State Sync index |

---

## Installation & Compilation

Ensure you have **Go 1.21+** installed:

```bash
# Build native binary
go build -ldflags="-s -w" -o bin/agysync main.go

# Cross-compile for other platforms
GOOS=darwin  GOARCH=arm64 go build -ldflags="-s -w" -o bin/agysync-darwin-arm64 main.go
GOOS=darwin  GOARCH=amd64 go build -ldflags="-s -w" -o bin/agysync-darwin-amd64 main.go
GOOS=linux   GOARCH=amd64 go build -ldflags="-s -w" -o bin/agysync-linux-amd64 main.go
GOOS=linux   GOARCH=arm64 go build -ldflags="-s -w" -o bin/agysync-linux-arm64 main.go
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o bin/agysync-windows-amd64.exe main.go
```

---

## Usage Guide

### 1. Local Folder Sync (Offline / Backup Migration)

```bash
# 1. Dry Run Mode (Preview changes without writing)
./bin/agysync -src /path/to/backup/.gemini

# 2. Execute Bidirectional Sync with verbose logging
./bin/agysync -src /path/to/backup/.gemini -sync -v
```

### 2. Standalone Path Translation & IDE Re-Indexing

To force-localize workspace paths inside SQLite conversation databases and refresh Antigravity IDE's past conversations index:

```bash
./bin/agysync -translate -v
```

### 3. Google Drive Cloud Sync

AgySync connects to your private, hidden Google Drive application folder (`drive.appdata`) using Google OAuth2.

#### Option A: Quick Interactive Browser Login (Default)
Simply run `agysync`:
```bash
# 1. Preview changes
./bin/agysync

# 2. Execute cloud sync
./bin/agysync -sync -v
```

On first launch:
1. AgySync generates an OAuth authorization link in your terminal and starts a temporary local loopback server on `http://localhost:8989`.
2. Click or open the link in your browser and sign in with your Google account.
3. Upon approving permissions, Google automatically redirects back to `http://localhost:8989`, which **auto-captures the code and completes authentication**.
4. *(Fallback)*: If your browser is on a remote or headless machine, you can also paste the authorization code directly into the terminal prompt.
5. The token is securely cached in `~/.gemini/agysync_config/oauth_token.json` (`0600` permissions).

#### Option B: Custom OAuth Credentials (Optional)
You can configure your own Google Cloud OAuth2 Client credentials via `settings.json` or environment variables:

- **Via `settings.json` (`~/.gemini/agysync_config/settings.json`)**:
  ```json
  {
    "client_id": "your_google_oauth_client_id.apps.googleusercontent.com",
    "client_secret": "GOCSPX-your_client_secret"
  }
  ```
- **Via Environment Variables**:
  ```bash
  export AGYSYNC_CLIENT_ID="your_google_oauth_client_id.apps.googleusercontent.com"
  export AGYSYNC_CLIENT_SECRET="GOCSPX-your_client_secret"
  ```

---

## CLI Flags Reference

- `-src <path>`: Source backup folder path.
- `-sync`: Performs bidirectional sync (Phase 1 download/merge, Phase 2 upload/back-propagation). Omitting `-sync` runs Dry-Run simulation.
- `-translate`: Translates SQLite database workspace URIs and synchronizes `state.vscdb`.
- `-v`: Verbose output (logs synced files and commands).
- `-vv`: Full verbose output (logs all checked and skipped items).
- `-h`: Display help and usage examples.

---

## Quality Assurance & Testing

Run the automated QA test suite:

```bash
# Automated non-interactive QA run
./test_and_restore_history.sh --auto

# Interactive step-by-step testing
./test_and_restore_history.sh
```

---

## Troubleshooting

### Past conversations not appearing in Antigravity IDE?
1. Quit Antigravity IDE (`Cmd + Q` on macOS).
2. Run:
   ```bash
   ./bin/agysync -translate -v
   ```
3. Reopen Antigravity IDE. All conversations will appear under **"Past Conversations"**.

### Stale database locks after unclean shutdown?
AgySync automatically cleans up orphaned `.db-shm` and `.db-wal` locks during `-sync` and `-translate`.
