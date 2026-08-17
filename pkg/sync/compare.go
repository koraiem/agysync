package sync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FolderMap defines the relation between relative source paths and absolute local paths
type FolderMap struct {
	RelPath string
	DstPath string
}

// CompareSyncState runs a read-only comparison between source and destination
func CompareSyncState(srcBase string, folders []FolderMap, srcHistory, dstHistory string) error {
	fmt.Println("\n=== Dry Run: Sync Comparison Report ===")

	// 1. Check directories
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		fmt.Printf("\nComparing Folder: %s\n", f.RelPath)

		toDownload, err := getFilesMissingInDst(srcFolder, f.DstPath)
		if err != nil {
			fmt.Printf("  Error reading source folder: %v\n", err)
			continue
		}

		toUpload, err := getFilesMissingInDst(f.DstPath, srcFolder)
		if err != nil {
			fmt.Printf("  Error reading local folder: %v\n", err)
			continue
		}

		if len(toDownload) == 0 && len(toUpload) == 0 {
			fmt.Println("  ✓ Folder is perfectly in sync (no files to copy).")
		} else {
			if len(toDownload) > 0 {
				fmt.Printf("  ⬇ Would download %d missing file(s) to local machine:\n", len(toDownload))
				for _, path := range toDownload {
					fmt.Printf("    + %s\n", path)
				}
			}
			if len(toUpload) > 0 {
				fmt.Printf("  ⬆ Would upload %d missing file(s) to backup:\n", len(toUpload))
				for _, path := range toUpload {
					fmt.Printf("    + %s\n", path)
				}
			}
		}
	}

	// 2. Compare history.jsonl
	fmt.Println("\nComparing CLI History: history.jsonl")
	if err := compareHistoryJsonl(srcHistory, dstHistory); err != nil {
		fmt.Printf("  Error comparing history: %v\n", err)
	}

	fmt.Println("\n=======================================")
	return nil
}

func getFilesMissingInDst(srcDir, dstDir string) ([]string, error) {
	var missing []string

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == srcDir {
				return nil // Source directory doesn't exist, which is fine (nothing to download)
			}
			return err
		}
		if ShouldIgnoreSyncPath(path) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}

		// Get path relative to srcDir
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dstDir, rel)
		if _, err := os.Stat(dstPath); os.IsNotExist(err) {
			missing = append(missing, rel)
		}
		return nil
	})

	return missing, err
}

func compareHistoryJsonl(srcHistory, dstHistory string) error {
	srcEntries := make(map[string]bool)
	dstEntries := make(map[string]bool)

	// Read source entries
	if _, err := os.Stat(srcHistory); err == nil {
		if err := readEntryKeys(srcHistory, srcEntries); err != nil {
			return err
		}
	}

	// Read destination entries
	if _, err := os.Stat(dstHistory); err == nil {
		if err := readEntryKeys(dstHistory, dstEntries); err != nil {
			return err
		}
	}

	toDownloadCount := 0
	for key := range srcEntries {
		if !dstEntries[key] {
			toDownloadCount++
		}
	}

	toUploadCount := 0
	for key := range dstEntries {
		if !srcEntries[key] {
			toUploadCount++
		}
	}

	fmt.Printf("  Source history size: %d entries\n", len(srcEntries))
	fmt.Printf("  Local history size:  %d entries\n", len(dstEntries))

	if toDownloadCount == 0 && toUploadCount == 0 {
		fmt.Println("  ✓ CLI history is perfectly in sync.")
	} else {
		if toDownloadCount > 0 {
			fmt.Printf("  ⬇ Would import %d new command(s) to local history.\n", toDownloadCount)
		}
		if toUploadCount > 0 {
			fmt.Printf("  ⬆ Would export %d new command(s) to backup history.\n", toUploadCount)
		}
	}

	return nil
}

func readEntryKeys(filePath string, keys map[string]bool) error {
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
			continue
		}

		key := fmt.Sprintf("%d:%s", entry.Timestamp, entry.Display)
		keys[key] = true
	}
	return scanner.Err()
}
