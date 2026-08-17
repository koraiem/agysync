package sync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// RemoteFile holds metadata about a file stored on Google Drive or remote backup
type RemoteFile struct {
	ID           string
	Name         string
	MD5          string
	ModifiedTime time.Time
	Size         int64
}

// HistoryEntry represents a single line in history.jsonl
type HistoryEntry struct {
	Display        string `json:"display"`
	Timestamp      int64  `json:"timestamp"`
	Workspace      string `json:"workspace,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
}

// SyncStats tracks the number of files processed during synchronization
type SyncStats struct {
	UnchangedCount int
	SyncedCount    int
}

// MergeHistoryJsonlResult holds statistics about merged command histories
type MergeHistoryJsonlResult struct {
	ImportedCount int
	ExportedCount int
}

// CopyFile copies a single file from src to dst. It returns an error if dst already exists.
func CopyFile(src, dst string) error {
	return copyFileInternal(src, dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL)
}

// CopyFileOverwrite copies a single file from src to dst, overwriting the destination if it exists.
func CopyFileOverwrite(src, dst string) error {
	return copyFileInternal(src, dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
}

func copyFileInternal(src, dst string, flags int) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, flags, 0644)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// ShouldIgnoreSyncPath returns true for files and directories that should not be synced,
// such as git internal objects (.git), OS metadata (.DS_Store), and temporary SQLite locks.
func ShouldIgnoreSyncPath(path string) bool {
	base := filepath.Base(path)
	if base == ".git" || base == ".DS_Store" || base == "ehthumbs.db" || base == "Thumbs.db" ||
		strings.HasSuffix(base, ".db-shm") || strings.HasSuffix(base, ".db-wal") {
		return true
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		if part == ".git" {
			return true
		}
	}
	return false
}

// MergeDirectories recursively merges srcDir into dstDir without overwriting existing files.
func MergeDirectories(srcDir, dstDir string, stats *SyncStats, verbosity int, paths *Paths) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		// If source doesn't exist, we just skip it
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return err
	}

	for _, entry := range entries {
		if ShouldIgnoreSyncPath(entry.Name()) {
			continue
		}

		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		if entry.IsDir() {
			if err := MergeDirectories(srcPath, dstPath, stats, verbosity, paths); err != nil {
				return err
			}
		} else {
			// Check if file exists in destination
			if _, err := os.Stat(dstPath); os.IsNotExist(err) {
				if err := CopyFile(srcPath, dstPath); err != nil {
					fmt.Printf("Error copying file %s -> %s: %v\n", srcPath, dstPath, err)
				} else {
					stats.SyncedCount++
					if verbosity >= 1 {
						fmt.Printf("Copied file: %s -> %s\n", srcPath, dstPath)
					}
					// If database file, translate paths after copying
					if strings.HasSuffix(entry.Name(), ".db") {
						if err := TranslateDbFile(dstPath, paths); err != nil && verbosity >= 1 {
							fmt.Printf("Warning: failed to translate DB file %s: %v\n", dstPath, err)
						}
					}
				}
			} else {
				stats.UnchangedCount++
				if verbosity >= 2 {
					fmt.Printf("File already exists, skipping: %s\n", dstPath)
				}
			}
		}
	}
	return nil
}

// MergeHistoryJsonl merges the CLI command history JSONL files.
func MergeHistoryJsonl(srcFile, dstFile string, verbosity int, paths *Paths) (*MergeHistoryJsonlResult, error) {
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		return &MergeHistoryJsonlResult{}, nil
	}

	srcEntries := make(map[string]HistoryEntry)
	dstEntries := make(map[string]HistoryEntry)

	// Read entries from source (remote)
	if err := readEntries(srcFile, srcEntries, paths); err != nil {
		return nil, fmt.Errorf("error reading source history: %w", err)
	}

	// Read entries from destination (local)
	if _, err := os.Stat(dstFile); err == nil {
		if err := readEntries(dstFile, dstEntries, paths); err != nil {
			return nil, fmt.Errorf("error reading destination history: %w", err)
		}
	}

	// Union of all entries
	mergedEntries := make(map[string]HistoryEntry)
	for k, v := range srcEntries {
		mergedEntries[k] = v
	}
	for k, v := range dstEntries {
		mergedEntries[k] = v
	}

	importedCount := 0
	for k, v := range srcEntries {
		if _, ok := dstEntries[k]; !ok {
			importedCount++
			if verbosity >= 1 {
				fmt.Printf("Imported command: %s\n", v.Display)
			}
		}
	}

	exportedCount := 0
	for k, v := range dstEntries {
		if _, ok := srcEntries[k]; !ok {
			exportedCount++
			if verbosity >= 1 {
				fmt.Printf("Exported command: %s\n", v.Display)
			}
		}
	}

	// Convert merged map to sorted slice
	var sortedList []HistoryEntry
	for _, entry := range mergedEntries {
		sortedList = append(sortedList, entry)
	}

	sort.Slice(sortedList, func(i, j int) bool {
		return sortedList[i].Timestamp < sortedList[j].Timestamp
	})

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dstFile), 0755); err != nil {
		return nil, err
	}

	// Write back to destination
	f, err := os.Create(dstFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	for _, entry := range sortedList {
		data, err := json.Marshal(entry)
		if err != nil {
			return nil, err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return nil, err
		}
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}

	return &MergeHistoryJsonlResult{
		ImportedCount: importedCount,
		ExportedCount: exportedCount,
	}, nil
}

func readEntries(filePath string, entries map[string]HistoryEntry, paths *Paths) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	localProjects := LoadLocalProjectPaths(paths)
	localHome, _ := os.UserHomeDir()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}

		var entry HistoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip malformed lines
			continue
		}

		// Localize workspace path
		entry.Workspace = LocalizeWorkspacePath(entry.Workspace, localProjects, localHome)

		key := fmt.Sprintf("%d:%s", entry.Timestamp, entry.Display)
		entries[key] = entry
	}
	return scanner.Err()
}

// LoadLocalProjectPaths retrieves the local project folder paths from projects.json
func LoadLocalProjectPaths(paths *Paths) []string {
	var projects struct {
		Projects map[string]string `json:"projects"`
	}
	data, err := os.ReadFile(filepath.Join(paths.BaseDir, "projects.json"))
	if err == nil {
		_ = json.Unmarshal(data, &projects)
	}
	var list []string
	for k := range projects.Projects {
		if k != "/" {
			list = append(list, k)
		}
	}
	return list
}

// LocalizeWorkspacePath translates a remote path to match the local machine's workspace path
func LocalizeWorkspacePath(remotePath string, localProjects []string, localHome string) string {
	remotePath = filepath.ToSlash(remotePath)
	remoteClean := strings.TrimPrefix(remotePath, "file://")

	// 1. Try to match by project name (last segment)
	remoteLast := filepath.Base(remoteClean)
	for _, lp := range localProjects {
		lpSlash := filepath.ToSlash(lp)
		if filepath.Base(lpSlash) == remoteLast {
			if strings.HasPrefix(remotePath, "file://") {
				return lpSlash
			}
			return lpSlash
		}
	}

	// 2. Fallback: replace home directory prefix
	var remoteHome string
	parts := strings.Split(strings.TrimPrefix(remoteClean, "/"), "/")
	if len(parts) >= 2 && (parts[0] == "home" || parts[0] == "Users") {
		remoteHome = "/" + parts[0] + "/" + parts[1]
	} else if len(parts) >= 1 && strings.HasPrefix(remoteClean, "C:/Users/") {
		wParts := strings.Split(remoteClean, "/")
		if len(wParts) >= 3 {
			remoteHome = strings.Join(wParts[:3], "/")
		}
	}

	if remoteHome != "" {
		return strings.Replace(remoteClean, remoteHome, localHome, 1)
	}

	return remoteClean
}

// FindRemoteURIs scans binary data for strings starting with file://
func FindRemoteURIs(dataBytes []byte) []string {
	var uris []string
	str := string(dataBytes)
	for {
		idx := strings.Index(str, "file://")
		if idx == -1 {
			break
		}
		end := idx
		for end < len(str) && str[end] >= 32 && str[end] <= 126 {
			end++
		}
		uris = append(uris, str[idx:end])
		str = str[end:]
	}
	return uris
}

// TranslateProtobuf recursively translates matching strings inside raw protobuf message bytes
func TranslateProtobuf(data []byte, oldPath, newPath string) ([]byte, bool, error) {
	var replaced bool
	var result []byte
	remaining := data

	for len(remaining) > 0 {
		num, typ, length := protowire.ConsumeTag(remaining)
		if length < 0 {
			return nil, false, fmt.Errorf("invalid tag")
		}
		result = protowire.AppendTag(result, num, typ)
		remaining = remaining[length:]

		switch typ {
		case protowire.VarintType:
			val, length := protowire.ConsumeVarint(remaining)
			if length < 0 {
				return nil, false, fmt.Errorf("invalid varint")
			}
			result = protowire.AppendVarint(result, val)
			remaining = remaining[length:]
		case protowire.Fixed64Type:
			val, length := protowire.ConsumeFixed64(remaining)
			if length < 0 {
				return nil, false, fmt.Errorf("invalid fixed64")
			}
			result = protowire.AppendFixed64(result, val)
			remaining = remaining[length:]
		case protowire.BytesType:
			bytesVal, length := protowire.ConsumeBytes(remaining)
			if length < 0 {
				return nil, false, fmt.Errorf("invalid bytes")
			}
			remaining = remaining[length:]

			nestedResult, nestedReplaced, err := TranslateProtobuf(bytesVal, oldPath, newPath)
			if err == nil && nestedReplaced {
				result = protowire.AppendBytes(result, nestedResult)
				replaced = true
			} else {
				strVal := string(bytesVal)
				if strings.Contains(strVal, oldPath) {
					newStrVal := strings.ReplaceAll(strVal, oldPath, newPath)
					result = protowire.AppendBytes(result, []byte(newStrVal))
					replaced = true
				} else {
					result = protowire.AppendBytes(result, bytesVal)
				}
			}
		case protowire.Fixed32Type:
			val, length := protowire.ConsumeFixed32(remaining)
			if length < 0 {
				return nil, false, fmt.Errorf("invalid fixed32")
			}
			result = protowire.AppendFixed32(result, val)
			remaining = remaining[length:]
		default:
			return nil, false, fmt.Errorf("unknown wire type")
		}
	}
	return result, replaced, nil
}

// TranslateDbFile translates paths inside a local SQLite db file
func TranslateDbFile(dbPath string, paths *Paths) error {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	cmd := exec.Command("sqlite3", dbPath, "SELECT hex(data) FROM trajectory_metadata_blob WHERE id = 'main'")
	hexOut, err := cmd.Output()
	if err != nil {
		return err
	}
	hexStr := strings.TrimSpace(string(hexOut))
	if len(hexStr) == 0 {
		return nil
	}
	dataBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return err
	}

	localProjects := LoadLocalProjectPaths(paths)
	localHome, _ := os.UserHomeDir()

	uris := FindRemoteURIs(dataBytes)
	if len(uris) == 0 {
		return nil
	}

	replacedAny := false
	currentBytes := dataBytes
	for _, uri := range uris {
		remotePath := strings.TrimPrefix(uri, "file://")
		localizedPath := LocalizeWorkspacePath(remotePath, localProjects, localHome)
		localizedURI := "file://" + localizedPath

		if uri != localizedURI {
			b, replaced, err := TranslateProtobuf(currentBytes, uri, localizedURI)
			if err == nil && replaced {
				currentBytes = b
				replacedAny = true
			}
		}
		if remotePath != localizedPath {
			b, replaced, err := TranslateProtobuf(currentBytes, remotePath, localizedPath)
			if err == nil && replaced {
				currentBytes = b
				replacedAny = true
			}
		}
	}

	if !replacedAny {
		return nil
	}

	newHexStr := hex.EncodeToString(currentBytes)
	updateSQL := fmt.Sprintf("UPDATE trajectory_metadata_blob SET data = x'%s' WHERE id = 'main'", newHexStr)
	return exec.Command("sqlite3", dbPath, updateSQL).Run()
}

// CanonicalizeDbFile canonicalizes paths inside a SQLite db file before uploading
func CanonicalizeDbFile(dbPath string, paths *Paths) error {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	cmd := exec.Command("sqlite3", dbPath, "SELECT hex(data) FROM trajectory_metadata_blob WHERE id = 'main'")
	hexOut, err := cmd.Output()
	if err != nil {
		return err
	}
	hexStr := strings.TrimSpace(string(hexOut))
	if len(hexStr) == 0 {
		return nil
	}
	dataBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return err
	}

	localHome, _ := os.UserHomeDir()

	uris := FindRemoteURIs(dataBytes)
	if len(uris) == 0 {
		return nil
	}

	replacedAny := false
	currentBytes := dataBytes
	for _, uri := range uris {
		remotePath := strings.TrimPrefix(uri, "file://")
		canonicalPath := remotePath
		if strings.HasPrefix(remotePath, localHome) {
			canonicalPath = "~" + strings.TrimPrefix(remotePath, localHome)
		}
		canonicalURI := "file://" + canonicalPath

		if uri != canonicalURI {
			b, replaced, err := TranslateProtobuf(currentBytes, uri, canonicalURI)
			if err == nil && replaced {
				currentBytes = b
				replacedAny = true
			}
		}
		if remotePath != canonicalPath {
			b, replaced, err := TranslateProtobuf(currentBytes, remotePath, canonicalPath)
			if err == nil && replaced {
				currentBytes = b
				replacedAny = true
			}
		}
	}

	if !replacedAny {
		return nil
	}

	newHexStr := hex.EncodeToString(currentBytes)
	updateSQL := fmt.Sprintf("UPDATE trajectory_metadata_blob SET data = x'%s' WHERE id = 'main'", newHexStr)
	return exec.Command("sqlite3", dbPath, updateSQL).Run()
}

// CanonicalizePath replaces the user's local home directory with ~
func CanonicalizePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	fileSchema := "file://"
	if strings.HasPrefix(path, fileSchema+home) {
		return fileSchema + "~" + strings.TrimPrefix(path, fileSchema+home)
	}
	return path
}

// CanonicalizeHistoryJsonl reads local history, replaces home dir with ~, and writes to tempFile
func CanonicalizeHistoryJsonl(localFile, tempFile string) error {
	fIn, err := os.Open(localFile)
	if err != nil {
		return err
	}
	defer fIn.Close()

	fOut, err := os.Create(tempFile)
	if err != nil {
		return err
	}
	defer fOut.Close()

	scanner := bufio.NewScanner(fIn)
	writer := bufio.NewWriter(fOut)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}

		var entry HistoryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		entry.Workspace = CanonicalizePath(entry.Workspace)

		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
}


