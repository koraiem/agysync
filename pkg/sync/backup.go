package sync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BackupActiveState snapshots all active Antigravity databases and logs before starting a sync.
// It performs an incremental backup into the "latest" directory to save disk space and time.
func BackupActiveState(paths *Paths) (string, error) {
	backupDir := filepath.Join(paths.BaseDir, "agysync_backups", "latest")

	targets := []struct {
		relPath string
		srcPath string
	}{
		{"antigravity/conversations", paths.CoreConversations},
		{"antigravity/brain", paths.CoreBrain},
		{"antigravity-cli/conversations", paths.CliConversations},
		{"antigravity-cli/brain", paths.CliBrain},
		{"antigravity-ide/conversations", paths.IdeConversations},
		{"antigravity-ide/brain", paths.IdeBrain},
		{"history", paths.WorkspaceHistory},
		{"antigravity-cli/history.jsonl", paths.CliHistoryFile},
	}

	// 1. First Pass: Count total files to check
	totalFiles := 0
	for _, target := range targets {
		totalFiles += CountFilesRecursive(target.srcPath)
	}

	fmt.Printf("Analyzing changes for safety backup (%d files total)...\n", totalFiles)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup root directory: %w", err)
	}

	tracker := &ProgressTracker{
		Label:   "Backing up files",
		Current: 0,
		Total:   totalFiles,
	}

	// Print initial progress
	tracker.Print()

	// 2. Second Pass: Copy changed/new files recursively
	copiedCount := 0
	for _, target := range targets {
		if _, err := os.Stat(target.srcPath); os.IsNotExist(err) {
			continue
		}

		dstPath := filepath.Join(backupDir, target.relPath)
		count, err := copyIncrementalRecursive(target.srcPath, dstPath, tracker)
		if err != nil {
			fmt.Println() // Print newline to clear progress bar
			return "", fmt.Errorf("failed to backup %s: %w", target.srcPath, err)
		}
		copiedCount += count
	}

	// Final newline to clear carriage return progress bar
	tracker.Finish()
	fmt.Printf("Pre-sync backup updated. Synced %d modified/new file(s).\n", copiedCount)
	return backupDir, nil
}

// CountFilesRecursive counts all files recursively under the given path
func CountFilesRecursive(src string) int {
	info, err := os.Stat(src)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return 1
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		count += CountFilesRecursive(filepath.Join(src, entry.Name()))
	}
	return count
}

func copyIncrementalRecursive(src, dst string, tracker *ProgressTracker) (int, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return 0, err
	}

	if srcInfo.IsDir() {
		if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
			return 0, err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return 0, err
		}
		totalCopied := 0
		for _, entry := range entries {
			srcChild := filepath.Join(src, entry.Name())
			dstChild := filepath.Join(dst, entry.Name())
			copied, err := copyIncrementalRecursive(srcChild, dstChild, tracker)
			if err != nil {
				return 0, err
			}
			totalCopied += copied
		}
		return totalCopied, nil
	}

	// Check if file is already identical in the backup
	dstInfo, err := os.Stat(dst)
	if err == nil {
		if srcInfo.Size() == dstInfo.Size() && srcInfo.ModTime().Equal(dstInfo.ModTime()) {
			// Skip copying
			tracker.Current++
			tracker.Print()
			return 0, nil
		}
	}

	// Copy file
	srcFile, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return 0, err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return 0, err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return 0, err
	}

	// Set original modification time on the destination file to preserve it for future checks
	_ = os.Chtimes(dst, srcInfo.ModTime(), srcInfo.ModTime())

	tracker.Current++
	tracker.Print()
	return 1, nil
}
