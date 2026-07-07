package sync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

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

// MergeDirectories recursively merges srcDir into dstDir without overwriting existing files.
func MergeDirectories(srcDir, dstDir string, stats *SyncStats, verbosity int) error {
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
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		if entry.IsDir() {
			if err := MergeDirectories(srcPath, dstPath, stats, verbosity); err != nil {
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
func MergeHistoryJsonl(srcFile, dstFile string, verbosity int) (*MergeHistoryJsonlResult, error) {
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		return &MergeHistoryJsonlResult{}, nil
	}

	srcEntries := make(map[string]HistoryEntry)
	dstEntries := make(map[string]HistoryEntry)

	// Read entries from source (remote)
	if err := readEntries(srcFile, srcEntries); err != nil {
		return nil, fmt.Errorf("error reading source history: %w", err)
	}

	// Read entries from destination (local)
	if _, err := os.Stat(dstFile); err == nil {
		if err := readEntries(dstFile, dstEntries); err != nil {
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

func readEntries(filePath string, entries map[string]HistoryEntry) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

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

		key := fmt.Sprintf("%d:%s", entry.Timestamp, entry.Display)
		entries[key] = entry
	}
	return scanner.Err()
}
