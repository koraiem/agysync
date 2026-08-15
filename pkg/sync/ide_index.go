package sync

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// DetectIdeVscdbPaths locates the Antigravity IDE globalStorage state.vscdb database files
func DetectIdeVscdbPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			filepath.Join(home, "Library", "Application Support", "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
			filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage", "state.vscdb"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		candidates = []string{
			filepath.Join(appData, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
			filepath.Join(appData, "Antigravity", "User", "globalStorage", "state.vscdb"),
		}
	default: // linux and others
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		candidates = []string{
			filepath.Join(configHome, "Antigravity IDE", "User", "globalStorage", "state.vscdb"),
			filepath.Join(configHome, "Antigravity", "User", "globalStorage", "state.vscdb"),
		}
	}

	var existing []string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			existing = append(existing, c)
		}
	}
	return existing
}

// IdeConversationInfo holds extracted metadata for a single conversation
type IdeConversationInfo struct {
	CascadeID        string
	TrajectoryID     string
	Title            string
	WorkspaceURI     string
	GitRepo          string
	GitURL           string
	GitBranch        string
	StepCount        int64
	CreatedSec       int64
	CreatedNano      int64
	LastModSec       int64
	LastModNano      int64
	LastUserInputSec int64
	LastUserInputNano int64
}

// ParseConversationDb extracts conversation metadata from a local conversation .db file
func ParseConversationDb(dbPath string, paths *Paths) (*IdeConversationInfo, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("sqlite3 CLI not found on system")
	}

	cascadeID := strings.TrimSuffix(filepath.Base(dbPath), ".db")
	info := &IdeConversationInfo{
		CascadeID:   cascadeID,
		GitBranch:   "main",
		CreatedSec:  time.Now().Unix(),
		LastModSec:  time.Now().Unix(),
	}

	// 1. Query trajectory_meta
	cmdMeta := exec.Command("sqlite3", dbPath, "SELECT trajectory_id, cascade_id FROM trajectory_meta LIMIT 1;")
	outMeta, err := cmdMeta.Output()
	if err == nil {
		lines := strings.TrimSpace(string(outMeta))
		if lines != "" {
			parts := strings.Split(lines, "|")
			if len(parts) >= 1 && parts[0] != "" {
				info.TrajectoryID = parts[0]
			}
			if len(parts) >= 2 && parts[1] != "" {
				info.CascadeID = parts[1]
			}
		}
	}
	if info.TrajectoryID == "" {
		info.TrajectoryID = info.CascadeID
	}

	// 2. Query step count and latest step timestamp
	cmdCount := exec.Command("sqlite3", dbPath, "SELECT COUNT(*), MAX(idx) FROM steps;")
	outCount, err := cmdCount.Output()
	if err == nil {
		parts := strings.Split(strings.TrimSpace(string(outCount)), "|")
		if len(parts) >= 1 {
			var count int64
			fmt.Sscanf(parts[0], "%d", &count)
			info.StepCount = count
		}
	}
	if info.StepCount == 0 {
		info.StepCount = 1
	}

	// File mtime as fallback
	if fi, err := os.Stat(dbPath); err == nil {
		info.LastModSec = fi.ModTime().Unix()
		info.LastModNano = int64(fi.ModTime().Nanosecond())
		info.CreatedSec = fi.ModTime().Unix()
	}

	// 3. Query trajectory_metadata_blob for workspace and creation timestamp
	cmdBlob := exec.Command("sqlite3", dbPath, "SELECT hex(data) FROM trajectory_metadata_blob WHERE id = 'main';")
	outBlob, err := cmdBlob.Output()
	if err == nil {
		hexStr := strings.TrimSpace(string(outBlob))
		if hexStr != "" {
			blobBytes, err := hex.DecodeString(hexStr)
			if err == nil {
				parseTrajectoryMetadataBlob(blobBytes, info)
			}
		}
	}

	// 4. Extract conversation title from transcript.jsonl or steps
	info.Title = extractConversationTitle(info.CascadeID, dbPath, paths)

	// 5. Localize Workspace URI
	localProjects := LoadLocalProjectPaths(paths)
	localHome, _ := os.UserHomeDir()
	if info.WorkspaceURI != "" {
		rawPath := strings.TrimPrefix(info.WorkspaceURI, "file://")
		localizedPath := LocalizeWorkspacePath(rawPath, localProjects, localHome)
		info.WorkspaceURI = "file://" + localizedPath
	}

	return info, nil
}

func parseTrajectoryMetadataBlob(data []byte, info *IdeConversationInfo) {
	remaining := data
	for len(remaining) > 0 {
		num, typ, length := protowire.ConsumeTag(remaining)
		if length < 0 {
			break
		}
		remaining = remaining[length:]

		switch typ {
		case protowire.VarintType:
			_, l := protowire.ConsumeVarint(remaining)
			if l < 0 {
				return
			}
			remaining = remaining[l:]
		case protowire.Fixed64Type:
			_, l := protowire.ConsumeFixed64(remaining)
			if l < 0 {
				return
			}
			remaining = remaining[l:]
		case protowire.BytesType:
			bytesVal, l := protowire.ConsumeBytes(remaining)
			if l < 0 {
				return
			}
			remaining = remaining[l:]

			if num == 1 { // Workspace metadata sub-message
				sub := bytesVal
				for len(sub) > 0 {
					snum, styp, slen := protowire.ConsumeTag(sub)
					if slen < 0 {
						break
					}
					sub = sub[slen:]
					if styp == protowire.BytesType {
						sval, sl := protowire.ConsumeBytes(sub)
						if sl < 0 {
							break
						}
						sub = sub[sl:]
						if snum == 1 && info.WorkspaceURI == "" {
							info.WorkspaceURI = string(sval)
						} else if snum == 3 { // Git info sub-message
							gitSub := sval
							for len(gitSub) > 0 {
								gnum, gtyp, glen := protowire.ConsumeTag(gitSub)
								if glen < 0 {
									break
								}
								gitSub = gitSub[glen:]
								if gtyp == protowire.BytesType {
									gval, gl := protowire.ConsumeBytes(gitSub)
									if gl < 0 {
										break
									}
									gitSub = gitSub[gl:]
									if gnum == 1 {
										info.GitRepo = string(gval)
									} else if gnum == 2 {
										info.GitURL = string(gval)
									}
								} else {
									break
								}
							}
						} else if snum == 4 {
							info.GitBranch = string(sval)
						}
					} else if styp == protowire.VarintType {
						_, sl := protowire.ConsumeVarint(sub)
						if sl < 0 {
							break
						}
						sub = sub[sl:]
					} else {
						break
					}
				}
			} else if num == 2 { // Created Timestamp sub-message
				tsSub := bytesVal
				for len(tsSub) > 0 {
					tnum, ttyp, tlen := protowire.ConsumeTag(tsSub)
					if tlen < 0 {
						break
					}
					tsSub = tsSub[tlen:]
					if ttyp == protowire.VarintType {
						v, tl := protowire.ConsumeVarint(tsSub)
						if tl < 0 {
							break
						}
						tsSub = tsSub[tl:]
						if tnum == 1 && v > 0 {
							info.CreatedSec = int64(v)
						} else if tnum == 2 {
							info.CreatedNano = int64(v)
						}
					} else {
						break
					}
				}
			} else if num == 7 && info.WorkspaceURI == "" {
				info.WorkspaceURI = string(bytesVal)
			}
		case protowire.Fixed32Type:
			_, l := protowire.ConsumeFixed32(remaining)
			if l < 0 {
				return
			}
			remaining = remaining[l:]
		default:
			return
		}
	}
}

func extractConversationTitle(cascadeID, dbPath string, paths *Paths) string {
	// 1. Try transcript.jsonl
	transcriptCandidates := []string{
		filepath.Join(paths.IdeBrain, cascadeID, ".system_generated", "logs", "transcript.jsonl"),
		filepath.Join(paths.CoreBrain, cascadeID, ".system_generated", "logs", "transcript.jsonl"),
		filepath.Join(paths.CliBrain, cascadeID, ".system_generated", "logs", "transcript.jsonl"),
	}

	for _, tPath := range transcriptCandidates {
		if f, err := os.Open(tPath); err == nil {
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				var item struct {
					Type    string `json:"type"`
					Source  string `json:"source"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal([]byte(line), &item); err == nil {
					if item.Type == "USER_INPUT" || item.Source == "USER_EXPLICIT" {
						title := cleanPromptToTitle(item.Content)
						if title != "" {
							f.Close()
							return title
						}
					}
				}
			}
			f.Close()
		}
	}

	// 2. Try steps with step_type = 23 (Generated Title)
	cmdStep23 := exec.Command("sqlite3", dbPath, "SELECT hex(step_payload) FROM steps WHERE step_type = 23 ORDER BY idx DESC LIMIT 1;")
	if out, err := cmdStep23.Output(); err == nil {
		hexStr := strings.TrimSpace(string(out))
		if hexStr != "" {
			if b, err := hex.DecodeString(hexStr); err == nil {
				title := extractTitleFromStepPayload(b)
				if title != "" {
					return title
				}
			}
		}
	}

	// 3. Try first step payload text
	cmdStep0 := exec.Command("sqlite3", dbPath, "SELECT hex(step_payload) FROM steps WHERE idx = 0 LIMIT 1;")
	if out, err := cmdStep0.Output(); err == nil {
		hexStr := strings.TrimSpace(string(out))
		if hexStr != "" {
			if b, err := hex.DecodeString(hexStr); err == nil {
				title := extractTitleFromStepPayload(b)
				if title != "" {
					return title
				}
			}
		}
	}

	return fmt.Sprintf("Conversation %s", cascadeID[:8])
}

func cleanPromptToTitle(content string) string {
	raw := content
	// Match <USER_REQUEST>...</USER_REQUEST>
	reReq := regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)
	if m := reReq.FindStringSubmatch(raw); len(m) > 1 {
		raw = m[1]
	}

	// Remove mention tags like @[something]
	reMention := regexp.MustCompile(`@\[.*?\]`)
	cleaned := reMention.ReplaceAllString(raw, "")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		cleaned = strings.TrimSpace(raw)
	}

	lines := strings.Split(cleaned, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if len(l) > 0 {
			if len(l) > 100 {
				l = l[:97] + "..."
			}
			return l
		}
	}
	return ""
}

func extractTitleFromStepPayload(payload []byte) string {
	// Look for tag 0x22 (field 4 bytes)
	idx := bytes.Index(payload, []byte{0x22})
	if idx != -1 {
		sub := payload[idx+1:]
		v, l := protowire.ConsumeVarint(sub)
		if l > 0 && int(v) <= len(sub)-l && v > 0 {
			text := string(sub[l : l+int(v)])
			lines := strings.Split(strings.TrimSpace(text), "\n")
			if len(lines) > 0 && len(lines[0]) > 0 {
				return lines[0]
			}
		}
	}
	return ""
}

// BuildCascadeTrajectorySummaryProto constructs the raw protobuf bytes for CascadeTrajectorySummary
func BuildCascadeTrajectorySummaryProto(info *IdeConversationInfo) []byte {
	var b []byte

	// Field 1: Title (string)
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, info.Title)

	// Field 2: Step Count (varint)
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(info.StepCount))

	// Field 3: Last Modified Time (Timestamp)
	var tsMod []byte
	tsMod = protowire.AppendTag(tsMod, 1, protowire.VarintType)
	tsMod = protowire.AppendVarint(tsMod, uint64(info.LastModSec))
	if info.LastModNano > 0 {
		tsMod = protowire.AppendTag(tsMod, 2, protowire.VarintType)
		tsMod = protowire.AppendVarint(tsMod, uint64(info.LastModNano))
	}
	b = protowire.AppendTag(b, 3, protowire.BytesType)
	b = protowire.AppendBytes(b, tsMod)

	// Field 4: Trajectory ID (string)
	b = protowire.AppendTag(b, 4, protowire.BytesType)
	b = protowire.AppendString(b, info.TrajectoryID)

	// Field 5: Status (varint, 1 = IDLE)
	b = protowire.AppendTag(b, 5, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)

	// Field 7: Created Time (Timestamp)
	var tsCreated []byte
	tsCreated = protowire.AppendTag(tsCreated, 1, protowire.VarintType)
	tsCreated = protowire.AppendVarint(tsCreated, uint64(info.CreatedSec))
	if info.CreatedNano > 0 {
		tsCreated = protowire.AppendTag(tsCreated, 2, protowire.VarintType)
		tsCreated = protowire.AppendVarint(tsCreated, uint64(info.CreatedNano))
	}
	b = protowire.AppendTag(b, 7, protowire.BytesType)
	b = protowire.AppendBytes(b, tsCreated)

	// Field 9: Workspace Metadata (CortexWorkspaceMetadata)
	var wsBytes []byte
	wsBytes = protowire.AppendTag(wsBytes, 1, protowire.BytesType)
	wsBytes = protowire.AppendString(wsBytes, info.WorkspaceURI)
	wsBytes = protowire.AppendTag(wsBytes, 2, protowire.BytesType)
	wsBytes = protowire.AppendString(wsBytes, info.WorkspaceURI)

	if info.GitRepo != "" {
		var gitBytes []byte
		gitBytes = protowire.AppendTag(gitBytes, 1, protowire.BytesType)
		gitBytes = protowire.AppendString(gitBytes, info.GitRepo)
		gitBytes = protowire.AppendTag(gitBytes, 2, protowire.BytesType)
		gitBytes = protowire.AppendString(gitBytes, info.GitURL)

		wsBytes = protowire.AppendTag(wsBytes, 3, protowire.BytesType)
		wsBytes = protowire.AppendBytes(wsBytes, gitBytes)
	}
	if info.GitBranch != "" {
		wsBytes = protowire.AppendTag(wsBytes, 4, protowire.BytesType)
		wsBytes = protowire.AppendString(wsBytes, info.GitBranch)
	}

	b = protowire.AppendTag(b, 9, protowire.BytesType)
	b = protowire.AppendBytes(b, wsBytes)

	// Field 10: Last User Input Time (Timestamp)
	userSec := info.LastUserInputSec
	if userSec == 0 {
		userSec = info.CreatedSec
	}
	var tsUser []byte
	tsUser = protowire.AppendTag(tsUser, 1, protowire.VarintType)
	tsUser = protowire.AppendVarint(tsUser, uint64(userSec))
	b = protowire.AppendTag(b, 10, protowire.BytesType)
	b = protowire.AppendBytes(b, tsUser)

	// Field 15: Annotations (empty message)
	b = protowire.AppendTag(b, 15, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte{})

	// Field 16: Trajectory Type (varint 1)
	b = protowire.AppendTag(b, 16, protowire.VarintType)
	b = protowire.AppendVarint(b, 1)

	return b
}

// DecodeUssTable parses the unified state sync table bytes into a key -> value map
func DecodeUssTable(data []byte) map[string]string {
	entries := make(map[string]string)
	remaining := data

	for len(remaining) > 0 {
		num, typ, length := protowire.ConsumeTag(remaining)
		if length < 0 {
			break
		}
		remaining = remaining[length:]

		if typ == protowire.BytesType {
			rowBytes, l := protowire.ConsumeBytes(remaining)
			if l < 0 {
				break
			}
			remaining = remaining[l:]

			if num == 1 { // TopicRow
				var rowKey string
				var rowVal string

				rowRemaining := rowBytes
				for len(rowRemaining) > 0 {
					rnum, rtyp, rlen := protowire.ConsumeTag(rowRemaining)
					if rlen < 0 {
						break
					}
					rowRemaining = rowRemaining[rlen:]

					if rtyp == protowire.BytesType {
						cBytes, cl := protowire.ConsumeBytes(rowRemaining)
						if cl < 0 {
							break
						}
						rowRemaining = rowRemaining[cl:]

						if rnum == 1 {
							rowKey = string(cBytes)
						} else if rnum == 2 { // TopicValue message (field 1: value string)
							valRemaining := cBytes
							for len(valRemaining) > 0 {
								vnum, vtyp, vlen := protowire.ConsumeTag(valRemaining)
								if vlen < 0 {
									break
								}
								valRemaining = valRemaining[vlen:]

								if vtyp == protowire.BytesType {
									vBytes, vl := protowire.ConsumeBytes(valRemaining)
									if vl < 0 {
										break
									}
									valRemaining = valRemaining[vl:]
									if vnum == 1 {
										rowVal = string(vBytes)
									}
								} else {
									break
								}
							}
						}
					} else {
						break
					}
				}

				if rowKey != "" {
					entries[rowKey] = rowVal
				}
			}
		} else {
			break
		}
	}

	return entries
}

// EncodeUssTable serializes a key -> value map into unified state sync table protobuf bytes
func EncodeUssTable(entries map[string]string) []byte {
	var tableBytes []byte

	// Deterministic order
	var keys []string
	for k := range entries {
		keys = append(keys, k)
	}
	// Sort keys
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	for _, k := range keys {
		v := entries[k]

		// Build TopicValue message (Field 1: string value)
		var valMsg []byte
		valMsg = protowire.AppendTag(valMsg, 1, protowire.BytesType)
		valMsg = protowire.AppendString(valMsg, v)

		// Build TopicRow message (Field 1: string key, Field 2: TopicValue message)
		var rowMsg []byte
		rowMsg = protowire.AppendTag(rowMsg, 1, protowire.BytesType)
		rowMsg = protowire.AppendString(rowMsg, k)
		rowMsg = protowire.AppendTag(rowMsg, 2, protowire.BytesType)
		rowMsg = protowire.AppendBytes(rowMsg, valMsg)

		// Append Field 1 to table
		tableBytes = protowire.AppendTag(tableBytes, 1, protowire.BytesType)
		tableBytes = protowire.AppendBytes(tableBytes, rowMsg)
	}

	return tableBytes
}

// SyncIdeTrajectorySummaries synchronizes all local conversations into Antigravity IDE's state.vscdb
func SyncIdeTrajectorySummaries(paths *Paths, verbosity int) (int, error) {
	vscdbPaths := DetectIdeVscdbPaths()
	if len(vscdbPaths) == 0 {
		return 0, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return 0, fmt.Errorf("sqlite3 CLI is required to update IDE state database")
	}

	// Discover all conversation .db files
	var convDbPaths []string
	folders := []string{paths.IdeConversations, paths.CoreConversations, paths.CliConversations}
	seen := make(map[string]bool)

	for _, folder := range folders {
		if _, err := os.Stat(folder); err == nil {
			_ = filepath.Walk(folder, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && strings.HasSuffix(p, ".db") {
					convID := strings.TrimSuffix(filepath.Base(p), ".db")
					if !seen[convID] {
						seen[convID] = true
						convDbPaths = append(convDbPaths, p)
					}
				}
				return nil
			})
		}
	}

	if len(convDbPaths) == 0 {
		return 0, nil
	}

	totalUpdated := 0
	for _, vscdb := range vscdbPaths {
		// 1. Read existing trajectorySummaries from state.vscdb
		cmdRead := exec.Command("sqlite3", vscdb, "SELECT value FROM ItemTable WHERE key = 'antigravityUnifiedStateSync.trajectorySummaries' LIMIT 1;")
		outVal, _ := cmdRead.Output()
		b64Val := strings.TrimSpace(string(outVal))

		var entries map[string]string
		if b64Val != "" {
			rawBytes, err := base64.StdEncoding.DecodeString(b64Val)
			if err == nil {
				entries = DecodeUssTable(rawBytes)
			}
		}
		if entries == nil {
			entries = make(map[string]string)
		}

		addedOrUpdated := 0
		localProjects := LoadLocalProjectPaths(paths)
		localHome, _ := os.UserHomeDir()

		// 2. Translate/localize existing entries in the summary table
		for convID, encodedSummary := range entries {
			summaryBytes, err := base64.StdEncoding.DecodeString(encodedSummary)
			if err == nil {
				uris := FindRemoteURIs(summaryBytes)
				replacedAny := false
				current := summaryBytes
				for _, uri := range uris {
					remotePath := strings.TrimPrefix(uri, "file://")
					locPath := LocalizeWorkspacePath(remotePath, localProjects, localHome)
					locURI := "file://" + locPath
					if uri != locURI {
						if b, rep, err := TranslateProtobuf(current, uri, locURI); err == nil && rep {
							current = b
							replacedAny = true
						}
					}
					if remotePath != locPath {
						if b, rep, err := TranslateProtobuf(current, remotePath, locPath); err == nil && rep {
							current = b
							replacedAny = true
						}
					}
				}
				if replacedAny {
					entries[convID] = base64.StdEncoding.EncodeToString(current)
					addedOrUpdated++
				}
			}
		}

		// 3. For each conversation DB on disk, ensure it has a summary entry
		for _, dbPath := range convDbPaths {
			convID := strings.TrimSuffix(filepath.Base(dbPath), ".db")
			if _, exists := entries[convID]; !exists {
				info, err := ParseConversationDb(dbPath, paths)
				if err == nil && info != nil {
					protoBytes := BuildCascadeTrajectorySummaryProto(info)
					entries[convID] = base64.StdEncoding.EncodeToString(protoBytes)
					addedOrUpdated++
					if verbosity >= 1 {
						fmt.Printf("Indexed past conversation into IDE: %s (\"%s\")\n", convID, info.Title)
					}
				}
			}
		}

		// 4. Encode table and write back to state.vscdb
		newTableBytes := EncodeUssTable(entries)
		newB64Table := base64.StdEncoding.EncodeToString(newTableBytes)

		// Safely escape single quotes for SQLite
		escapedVal := strings.ReplaceAll(newB64Table, "'", "''")
		upsertSQL := fmt.Sprintf(
			"INSERT INTO ItemTable(key, value) VALUES('antigravityUnifiedStateSync.trajectorySummaries', '%s') ON CONFLICT(key) DO UPDATE SET value = '%s';",
			escapedVal, escapedVal,
		)

		if err := exec.Command("sqlite3", vscdb, upsertSQL).Run(); err != nil {
			if verbosity >= 1 {
				fmt.Printf("Warning: failed to update IDE database %s: %v\n", vscdb, err)
			}
		} else {
			totalUpdated += addedOrUpdated
		}
	}

	return totalUpdated, nil
}
