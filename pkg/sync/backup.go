package sync

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// BackupActiveState snapshots all active Antigravity databases and logs before starting a sync
func BackupActiveState(paths *Paths) (string, error) {
	timestamp := time.Now().Format("20060102_150405")
	backupDir := filepath.Join(paths.BaseDir, "agysync_backups", "backup_"+timestamp)

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

	// 1. First Pass: Count total files to sync
	totalFiles := 0
	for _, target := range targets {
		totalFiles += CountFilesRecursive(target.srcPath)
	}

	fmt.Printf("Preparing safety backup (%d files total)...\n", totalFiles)
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

	// 2. Second Pass: Copy files recursively with progress updates
	for _, target := range targets {
		if _, err := os.Stat(target.srcPath); os.IsNotExist(err) {
			continue
		}

		dstPath := filepath.Join(backupDir, target.relPath)
		if err := copyRecursiveWithProgress(target.srcPath, dstPath, tracker); err != nil {
			fmt.Println() // Print newline to clear progress bar
			return "", fmt.Errorf("failed to backup %s: %w", target.srcPath, err)
		}
	}

	// Final newline to clear carriage return progress bar
	tracker.Finish()
	fmt.Println("Pre-sync backup created successfully.")
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

func copyRecursiveWithProgress(src, dst string, tracker *ProgressTracker) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			srcChild := filepath.Join(src, entry.Name())
			dstChild := filepath.Join(dst, entry.Name())
			if err := copyRecursiveWithProgress(srcChild, dstChild, tracker); err != nil {
				return err
			}
		}
		return nil
	}

	// It's a file
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	tracker.Current++
	tracker.Print()
	return nil
}
