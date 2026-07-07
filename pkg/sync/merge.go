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
func MergeDirectories(srcDir, dstDir string) error {
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
			if err := MergeDirectories(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			// Check if file exists in destination
			if _, err := os.Stat(dstPath); os.IsNotExist(err) {
				if err := CopyFile(srcPath, dstPath); err != nil {
					fmt.Printf("Error copying file %s -> %s: %v\n", srcPath, dstPath, err)
				} else {
					fmt.Printf("Copied file: %s -> %s\n", srcPath, dstPath)
				}
			} else {
				// File already exists on destination; skip to preserve local session
				fmt.Printf("File already exists, skipping: %s\n", dstPath)
			}
		}
	}
	return nil
}

// MergeHistoryJsonl merges the CLI command history JSONL files.
func MergeHistoryJsonl(srcFile, dstFile string) error {
	if _, err := os.Stat(srcFile); os.IsNotExist(err) {
		return nil
	}

	historyEntries := make(map[string]HistoryEntry)

	// Read existing entries from destination if it exists
	if _, err := os.Stat(dstFile); err == nil {
		if err := readEntries(dstFile, historyEntries); err != nil {
			return fmt.Errorf("error reading destination history: %w", err)
		}
	}

	// Read entries from source
	if err := readEntries(srcFile, historyEntries); err != nil {
		return fmt.Errorf("error reading source history: %w", err)
	}

	// Convert map to slice
	var sortedList []HistoryEntry
	for _, entry := range historyEntries {
		sortedList = append(sortedList, entry)
	}

	// Sort entries by timestamp ascending
	sort.Slice(sortedList, func(i, j int) bool {
		return sortedList[i].Timestamp < sortedList[j].Timestamp
	})

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(dstFile), 0755); err != nil {
		return err
	}

	// Write back to destination
	f, err := os.Create(dstFile)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := bufio.NewWriter(f)
	for _, entry := range sortedList {
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		if _, err := writer.Write(append(data, '\n')); err != nil {
			return err
		}
	}
	return writer.Flush()
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
