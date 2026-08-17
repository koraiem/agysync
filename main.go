package main

import (
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agysync/pkg/drive"
	"agysync/pkg/sync"
)

func main() {
	srcBaseFlag := flag.String("src", "", "Path to backup source .gemini folder (local folder sync mode fallback)")
	syncFlag := flag.Bool("sync", false, "Perform the bidirectional sync (with backup, propagation, and logs)")
	autocleanFlag := flag.Bool("autoclean", false, "Prune ignored/unnecessary files (.git, .DS_Store, stale locks) from Google Drive AppData or destination")
	verboseSyncedFlag := flag.Bool("v", false, "Show only synced (copied) files during execution")
	verboseAllFlag := flag.Bool("vv", false, "Show all files checked (copied and skipped) during execution")
	translateFlag := flag.Bool("translate", false, "Force translate/localize all local SQLite database conversation paths")

	// Custom Usage
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Println("\nAntigravity Sync (AgySync) CLI synchronizes conversations, logs, and command histories across platforms.")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		fmt.Println("\nExamples:")
		fmt.Println("  1. Dry-run Google Drive Sync (Simulate Drive sync and show comparison report):")
		fmt.Println("     agysync")
		fmt.Println("\n  2. Perform Google Drive Sync (Syncs to Google Drive AppData folder, requires OAuth2):")
		fmt.Println("     agysync -sync")
		fmt.Println("\n  3. Perform Google Drive Sync & Prune Ignored Files (.git, .DS_Store):")
		fmt.Println("     agysync -sync -autoclean")
		fmt.Println("\n  4. Local Folder Sync Mode (Fallback / Offline sync):")
		fmt.Println("     agysync -src /path/to/backup/.gemini -sync")
		fmt.Println("\nSafety Features:")
		fmt.Println("  - Pre-sync Backup: AgySync automatically archives your active configuration to ~/.gemini/agysync_backups/")
		fmt.Println("    before any merging begins to prevent data loss.")
		fmt.Println("  - Shared Node Control: Limits connection up to 2 machines for free (customizable via licenses in Drive).")
		fmt.Println("  - Unified Sync Logs: Appends sync metadata and statistics to a global audit log on Google Drive.")
	}

	flag.Parse()

	// Detect paths on current machine
	dstPaths, err := sync.DetectPaths()
	if err != nil {
		fmt.Printf("Error detecting local Antigravity paths: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Antigravity Sync (AgySync) ===")
	fmt.Printf("Active Base Directory: %s\n", dstPaths.BaseDir)

	// Determine verbosity level
	verbosity := 0
	if *verboseSyncedFlag {
		verbosity = 1
	}
	if *verboseAllFlag {
		verbosity = 2
	}

	if *translateFlag {
		fmt.Println("\nAction: Force translating local SQLite conversation paths...")
		count := 0
		for _, folder := range []string{"antigravity-ide/conversations", "antigravity/conversations", "antigravity-cli/conversations"} {
			dir := filepath.Join(dstPaths.BaseDir, folder)
			_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if sync.ShouldIgnoreSyncPath(path) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if info.IsDir() {
					return nil
				}
				if strings.HasSuffix(path, ".db") {
					if err := sync.TranslateDbFile(path, dstPaths); err != nil {
						fmt.Printf("Error translating %s: %v\n", path, err)
					} else {
						count++
						if verbosity >= 1 {
							fmt.Printf("Translated: %s\n", path)
						}
					}
				}
				return nil
			})
		}
		fmt.Printf("Finished! Checked and translated %d database file(s).\n", count)
		
		fmt.Println("\nAction: Updating Antigravity IDE past conversations index...")
		ideCount, err := sync.SyncIdeTrajectorySummaries(dstPaths, verbosity)
		if err != nil {
			fmt.Printf("Warning: error updating Antigravity IDE past conversations: %v\n", err)
		} else {
			fmt.Printf("Finished! Synchronized Antigravity IDE past conversations index (%d conversation(s) processed).\n", ideCount)
		}
		return
	}

	// 1. Differentiate execution paths: Local Sync vs Google Drive Sync
	if *srcBaseFlag != "" {
		runLocalFolderSync(*srcBaseFlag, dstPaths, *syncFlag, verbosity, *autocleanFlag)
		return
	}

	runGoogleDriveSync(dstPaths, *syncFlag, verbosity, *autocleanFlag)
}

func runLocalFolderSync(srcBase string, dstPaths *sync.Paths, performSync bool, verbosity int, autoClean bool) {
	fmt.Println("\nMode: Local Folder Sync (Fallback)")

	folders := []sync.FolderMap{
		{RelPath: "antigravity/conversations", DstPath: dstPaths.CoreConversations},
		{RelPath: "antigravity/brain", DstPath: dstPaths.CoreBrain},
		{RelPath: "antigravity-cli/conversations", DstPath: dstPaths.CliConversations},
		{RelPath: "antigravity-cli/brain", DstPath: dstPaths.CliBrain},
		{RelPath: "antigravity-ide/conversations", DstPath: dstPaths.IdeConversations},
		{RelPath: "antigravity-ide/brain", DstPath: dstPaths.IdeBrain},
		{RelPath: "history", DstPath: dstPaths.WorkspaceHistory},
	}

	srcHistory := filepath.Join(srcBase, "antigravity-cli", "history.jsonl")

	if !performSync {
		fmt.Println("\n[Dry Run Mode] No changes will be written.")
		fmt.Printf("Source Base: %s\n", srcBase)
		if err := sync.CompareSyncState(srcBase, folders, srcHistory, dstPaths.CliHistoryFile); err != nil {
			fmt.Printf("Error generating comparison report: %v\n", err)
		}
		return
	}

	// Create safety backup
	backupPath, err := sync.BackupActiveState(dstPaths)
	if err != nil {
		fmt.Printf("Fatal Error: pre-sync backup failed: %v. Aborting sync.\n", err)
		os.Exit(1)
	}
	fmt.Printf("Pre-sync backup created at: %s\n", backupPath)

	downloadStats := &sync.SyncStats{}
	uploadStats := &sync.SyncStats{}

	// Phase 1: Download/Merge
	fmt.Println("\n--- Phase 1: Merging remote history into local active paths ---")
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		if err := sync.MergeDirectories(srcFolder, f.DstPath, downloadStats, verbosity, dstPaths); err != nil {
			fmt.Printf("Warning: error merging directory %s: %v\n", srcFolder, err)
		}
	}

	historyResult, err := sync.MergeHistoryJsonl(srcHistory, dstPaths.CliHistoryFile, verbosity, dstPaths)
	if err != nil {
		fmt.Printf("Warning: error merging history.jsonl: %v\n", err)
	}

	// Update Antigravity IDE past conversations index
	if ideCount, err := sync.SyncIdeTrajectorySummaries(dstPaths, verbosity); err != nil {
		fmt.Printf("Warning: error updating Antigravity IDE past conversations index: %v\n", err)
	} else if verbosity >= 1 && ideCount > 0 {
		fmt.Printf("Synchronized %d past conversation(s) into Antigravity IDE index.\n", ideCount)
	}

	// Phase 2: Upload/Propagate
	fmt.Println("\n--- Phase 2: Back-propagating changes to source ---")
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		if err := sync.MergeDirectories(f.DstPath, srcFolder, uploadStats, verbosity, dstPaths); err != nil {
			fmt.Printf("Warning: error back-propagating directory %s: %v\n", srcFolder, err)
		}
	}

	if err := sync.CopyFileOverwrite(dstPaths.CliHistoryFile, srcHistory); err != nil {
		fmt.Printf("Warning: error back-propagating history.jsonl: %v\n", err)
	}

	prunedLocalCount := 0
	if autoClean {
		for _, f := range folders {
			srcFolder := filepath.Join(srcBase, f.RelPath)
			_ = filepath.Walk(srcFolder, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if sync.ShouldIgnoreSyncPath(path) {
					if info.IsDir() {
						_ = os.RemoveAll(path)
						prunedLocalCount++
						return filepath.SkipDir
					}
					_ = os.Remove(path)
					prunedLocalCount++
				}
				return nil
			})
		}
	}

	// Print final stats summary
	fmt.Println("\n=== Sync Summary ===")
	fmt.Printf("Kept %d files unchanged, downloaded %d new files from backup, uploaded %d files to backup.\n",
		downloadStats.UnchangedCount, downloadStats.SyncedCount, uploadStats.SyncedCount)
	if historyResult != nil {
		fmt.Printf("CLI History: imported %d new commands, exported %d new commands.\n",
			historyResult.ImportedCount, historyResult.ExportedCount)
	}
	if autoClean {
		fmt.Printf("Autoclean: pruned %d ignored file(s) from backup source.\n", prunedLocalCount)
	}
	fmt.Println("====================")
}

func runGoogleDriveSync(dstPaths *sync.Paths, performSync bool, verbosity int, autoClean bool) {
	fmt.Println("\nMode: Google Drive Cloud Sync")

	// 1. Load Local Machine Identity
	localNode, err := sync.LoadLocalSettings(dstPaths)
	if err != nil {
		fmt.Printf("Fatal Error: failed to load local settings: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Device Node ID:   %s\n", localNode.ID[:8]+"...")
	fmt.Printf("Device Node Name: %s\n", localNode.Name)
	fmt.Printf("Device Node Type: %s\n", localNode.Type)

	// 2. Connect to Google Drive AppData space
	driveSrv, err := drive.GetDriveService(dstPaths)
	if err != nil {
		fmt.Printf("\nAuthentication Error: %v\n", err)
		fmt.Println("\nPlease set Google OAuth environment variables to run cloud sync:")
		fmt.Println("  export AGYSYNC_CLIENT_ID=\"your_client_id\"")
		fmt.Println("  export AGYSYNC_CLIENT_SECRET=\"your_client_secret\"")
		fmt.Println("\nFor offline testing, use the local mode folder flag: agysync -src <backup_folder_path>")
		os.Exit(1)
	}

	// 3. Fetch global metadata and check registration/limits
	meta, err := driveSrv.GetMetadataFile()
	if err != nil {
		fmt.Printf("Error retrieving cloud metadata: %v\n", err)
		os.Exit(1)
	}

	// Validate node limits
	if _, err := sync.RegisterAndValidateNode(meta, localNode); err != nil {
		fmt.Printf("\nLicensing Error: %v\n", err)
		fmt.Println("Upgrade your account or contact support to connect more devices.")
		os.Exit(1)
	}

	if performSync {
		// Save registration metadata back to Drive
		if err := driveSrv.SaveMetadataFile(meta); err != nil {
			fmt.Printf("Warning: failed to update node registry in Drive: %v\n", err)
		}
	}

	// Folders to process
	folders := []string{
		"antigravity/conversations",
		"antigravity/brain",
		"antigravity-cli/conversations",
		"antigravity-cli/brain",
		"antigravity-ide/conversations",
		"antigravity-ide/brain",
		"history",
	}

	// 4. Retrieve cloud files metadata
	remoteFiles, err := driveSrv.ListAppDataFiles()
	if err != nil {
		fmt.Printf("Error listing files on Google Drive: %v\n", err)
		os.Exit(1)
	}

	if !performSync {
		// Dry Run Comparison report for Google Drive
		fmt.Println("\n[Dry Run Mode] Simulating cloud synchronization...")
		fmt.Println("\n=== Sync Comparison Report ===")

		toDownloadCount := 0
		toUploadCount := 0

		for _, folder := range folders {
			localDir := filepath.Join(dstPaths.BaseDir, folder)
			fmt.Printf("\nComparing Folder: %s\n", folder)

			// Checks what files we would upload
			var missingOnDrive []string
			_ = filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if sync.ShouldIgnoreSyncPath(path) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if info.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(dstPaths.BaseDir, path)
				flat := drive.FlatName(rel)
				if _, ok := remoteFiles[flat]; !ok {
					missingOnDrive = append(missingOnDrive, rel)
				}
				return nil
			})

			// Checks what files we would download
			var missingLocally []string
			prefix := drive.FlatName(folder) + "__"
			for name := range remoteFiles {
				if strings.HasPrefix(name, prefix) {
					rel := drive.RelPath(name)
					localPath := filepath.Join(dstPaths.BaseDir, rel)
					if _, err := os.Stat(localPath); os.IsNotExist(err) {
						missingLocally = append(missingLocally, rel)
					}
				}
			}

			if len(missingOnDrive) == 0 && len(missingLocally) == 0 {
				fmt.Println("  ✓ Folder is perfectly in sync with Google Drive.")
			} else {
				if len(missingLocally) > 0 {
					fmt.Printf("  ⬇ Would download %d missing file(s) from Drive:\n", len(missingLocally))
					for _, path := range missingLocally {
						fmt.Printf("    + %s\n", path)
					}
					toDownloadCount += len(missingLocally)
				}
				if len(missingOnDrive) > 0 {
					fmt.Printf("  ⬆ Would upload %d local file(s) to Drive:\n", len(missingOnDrive))
					for _, path := range missingOnDrive {
						fmt.Printf("    + %s\n", path)
					}
					toUploadCount += len(missingOnDrive)
				}
			}
		}

		// history.jsonl comparison
		fmt.Println("\nComparing CLI History: history.jsonl")
		tempLocalHistory := filepath.Join(os.TempDir(), "agysync_temp_hist.jsonl")
		if _, ok := remoteFiles["history.jsonl"]; ok {
			err = driveSrv.DownloadFile("history.jsonl", tempLocalHistory)
		}
		if err == nil {
			_ = sync.CompareSyncState(os.TempDir(), []sync.FolderMap{}, tempLocalHistory, dstPaths.CliHistoryFile)
			_ = os.Remove(tempLocalHistory)
		} else {
			fmt.Println("  No remote command history file found in Google Drive AppData.")
		}

		// Ignored files report on Drive
		var ignoredRemoteFiles []sync.RemoteFile
		for name, file := range remoteFiles {
			rel := drive.RelPath(name)
			if sync.ShouldIgnoreSyncPath(rel) {
				ignoredRemoteFiles = append(ignoredRemoteFiles, file)
			}
		}

		if len(ignoredRemoteFiles) > 0 {
			if autoClean {
				fmt.Printf("\n  🗑️ [Autoclean] Would prune %d ignored/unnecessary file(s) from Google Drive (.git, .DS_Store, stale locks):\n", len(ignoredRemoteFiles))
				if verbosity >= 1 {
					for _, f := range ignoredRemoteFiles {
						fmt.Printf("    - %s\n", drive.RelPath(f.Name))
					}
				}
			} else {
				fmt.Printf("\n  ℹ️ [Autoclean Info] Found %d ignored/unnecessary file(s) on Google Drive (.git, .DS_Store). Run with -autoclean -sync to prune them.\n", len(ignoredRemoteFiles))
			}
		}

		fmt.Println("\n=======================================")
		return
	}

	// 5. Run Sync Execution
	backupPath, err := sync.BackupActiveState(dstPaths)
	if err != nil {
		fmt.Printf("Fatal Error: pre-sync backup failed: %v. Aborting sync.\n", err)
		os.Exit(1)
	}
	fmt.Printf("Pre-sync backup created at: %s\n", backupPath)

	downloadStats := &sync.SyncStats{}
	uploadStats := &sync.SyncStats{}

	// Phase 1: Download & Merge from Google Drive
	if verbosity > 0 {
		fmt.Println("\n--- Phase 1: Downloading updates from Google Drive AppData ---")
	}

	// Count total remote files matching our prefixes first
	totalRemoteFiles := 0
	for _, folder := range folders {
		prefix := drive.FlatName(folder) + "__"
		for name := range remoteFiles {
			if strings.HasPrefix(name, prefix) {
				totalRemoteFiles++
			}
		}
	}

	var pTracker *sync.ProgressTracker
	if verbosity == 0 {
		pTracker = &sync.ProgressTracker{
			Label: "Downloading from Google Drive",
			Total: totalRemoteFiles,
		}
		pTracker.Print()
	}

	for _, folder := range folders {
		prefix := drive.FlatName(folder) + "__"
		for name := range remoteFiles {
			if strings.HasPrefix(name, prefix) {
				rel := drive.RelPath(name)
				localPath := filepath.Join(dstPaths.BaseDir, rel)

				remoteFile := remoteFiles[name]
				shouldDownload := false
				info, err := os.Stat(localPath)
				if os.IsNotExist(err) {
					shouldDownload = true
				} else if err == nil {
					localMD5, err := getLocalFileMD5(localPath)
					if err == nil && localMD5 != remoteFile.MD5 {
						if remoteFile.ModifiedTime.After(info.ModTime()) {
							shouldDownload = true
						}
					}
				}

				if shouldDownload {
					if verbosity >= 1 {
						fmt.Printf("Downloading: %s -> %s\n", name, localPath)
					}
					if err := driveSrv.DownloadFile(name, localPath); err != nil {
						fmt.Printf("\nWarning: failed to download file %s: %v\n", name, err)
					} else {
						downloadStats.SyncedCount++
						// If database file, translate paths after downloading
						if strings.HasSuffix(localPath, ".db") {
							if err := sync.TranslateDbFile(localPath, dstPaths); err != nil && verbosity >= 1 {
								fmt.Printf("Warning: failed to translate DB file %s: %v\n", localPath, err)
							}
						}
					}
				} else {
					downloadStats.UnchangedCount++
					if verbosity >= 2 {
						fmt.Printf("File already up to date, skipping: %s\n", localPath)
					}
				}

				if pTracker != nil {
					pTracker.Current++
					pTracker.Print()
				}
			}
		}
	}
	if pTracker != nil {
		pTracker.Finish()
	}

	// Merge history.jsonl
	var historyResult *sync.MergeHistoryJsonlResult
	if _, ok := remoteFiles["history.jsonl"]; ok {
		tempLocalHistory := filepath.Join(os.TempDir(), "agysync_temp_hist.jsonl")
		if err := driveSrv.DownloadFile("history.jsonl", tempLocalHistory); err == nil {
			historyResult, _ = sync.MergeHistoryJsonl(tempLocalHistory, dstPaths.CliHistoryFile, verbosity, dstPaths)
			_ = os.Remove(tempLocalHistory)
		}
	}

	// Update Antigravity IDE past conversations index
	if ideCount, err := sync.SyncIdeTrajectorySummaries(dstPaths, verbosity); err != nil {
		fmt.Printf("Warning: error updating Antigravity IDE past conversations index: %v\n", err)
	} else if verbosity >= 1 && ideCount > 0 {
		fmt.Printf("Synchronized %d past conversation(s) into Antigravity IDE index.\n", ideCount)
	}

	// Phase 2: Upload & Propagate to Google Drive
	if verbosity > 0 {
		fmt.Println("\n--- Phase 2: Uploading local changes to Google Drive AppData ---")
	}

	// Count total local files first
	totalLocalFiles := 0
	for _, folder := range folders {
		totalLocalFiles += sync.CountFilesRecursive(filepath.Join(dstPaths.BaseDir, folder))
	}

	if verbosity == 0 {
		pTracker = &sync.ProgressTracker{
			Label: "Uploading to Google Drive",
			Total: totalLocalFiles,
		}
		pTracker.Print()
	} else {
		pTracker = nil
	}

	for _, folder := range folders {
		localDir := filepath.Join(dstPaths.BaseDir, folder)
		_ = filepath.Walk(localDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if sync.ShouldIgnoreSyncPath(path) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dstPaths.BaseDir, path)
			flat := drive.FlatName(rel)

			remoteFile, exists := remoteFiles[flat]
			shouldUpload := false
			if !exists {
				shouldUpload = true
			} else {
				localMD5, err := getLocalFileMD5(path)
				if err == nil && localMD5 != remoteFile.MD5 {
					if info.ModTime().After(remoteFile.ModifiedTime) {
						shouldUpload = true
					}
				}
			}

			if shouldUpload {
				if verbosity >= 1 {
					fmt.Printf("Uploading: %s -> %s\n", rel, flat)
				}

				uploadPath := path
				var tempDbPath string
				if strings.HasSuffix(path, ".db") {
					tempDbPath = filepath.Join(os.TempDir(), "agysync_temp_upload_"+info.Name())
					if err := sync.CopyFileOverwrite(path, tempDbPath); err == nil {
						if err := sync.CanonicalizeDbFile(tempDbPath, dstPaths); err == nil {
							uploadPath = tempDbPath
						}
					}
				}

				if err := driveSrv.UploadFile(uploadPath, flat); err != nil {
					fmt.Printf("\nWarning: failed to upload file %s: %v\n", rel, err)
				} else {
					uploadStats.SyncedCount++
				}

				if tempDbPath != "" {
					_ = os.Remove(tempDbPath)
				}
			} else {
				uploadStats.UnchangedCount++
				if verbosity >= 2 {
					fmt.Printf("File already up to date in Drive, skipping: %s\n", rel)
				}
			}

			if pTracker != nil {
				pTracker.Current++
				pTracker.Print()
			}
			return nil
		})
	}
	if pTracker != nil {
		pTracker.Finish()
	}

	// Upload merged and canonicalized history.jsonl
	if _, err := os.Stat(dstPaths.CliHistoryFile); err == nil {
		tempHistFile := filepath.Join(os.TempDir(), "agysync_temp_upload_history.jsonl")
		if err := sync.CanonicalizeHistoryJsonl(dstPaths.CliHistoryFile, tempHistFile); err == nil {
			if err := driveSrv.UploadFile(tempHistFile, "history.jsonl"); err != nil {
				fmt.Printf("Warning: failed to upload merged history.jsonl: %v\n", err)
			}
			_ = os.Remove(tempHistFile)
		} else {
			if err := driveSrv.UploadFile(dstPaths.CliHistoryFile, "history.jsonl"); err != nil {
				fmt.Printf("Warning: failed to upload merged history.jsonl (fallback): %v\n", err)
			}
		}
	}

	// Phase 3: Pruning ignored files from Google Drive AppData (Only if -autoclean is passed)
	prunedDriveCount := 0
	if autoClean {
		var ignoredRemoteFiles []sync.RemoteFile
		for name, file := range remoteFiles {
			rel := drive.RelPath(name)
			if sync.ShouldIgnoreSyncPath(rel) {
				ignoredRemoteFiles = append(ignoredRemoteFiles, file)
			}
		}

		if len(ignoredRemoteFiles) > 0 {
			if verbosity > 0 {
				fmt.Printf("\n--- Phase 3: Pruning %d ignored file(s) from Google Drive AppData ---\n", len(ignoredRemoteFiles))
			}
			var pruneTracker *sync.ProgressTracker
			if verbosity == 0 {
				pruneTracker = &sync.ProgressTracker{
					Label: "Pruning ignored files from Google Drive",
					Total: len(ignoredRemoteFiles),
				}
				pruneTracker.Print()
			}

			for _, file := range ignoredRemoteFiles {
				if verbosity >= 1 {
					fmt.Printf("Pruning from Drive: %s\n", drive.RelPath(file.Name))
				}
				if err := driveSrv.DeleteFile(file.ID); err != nil {
					fmt.Printf("\nWarning: failed to delete file %s from Drive: %v\n", file.Name, err)
				} else {
					prunedDriveCount++
				}
				if pruneTracker != nil {
					pruneTracker.Current++
					pruneTracker.Print()
				}
			}
			if pruneTracker != nil {
				pruneTracker.Finish()
			}
		}
	}

	// 6. Write and upload shared sync log
	importedCount := 0
	exportedCount := 0
	if historyResult != nil {
		importedCount = historyResult.ImportedCount
		exportedCount = historyResult.ExportedCount
	}

	logEntry := map[string]interface{}{
		"timestamp":         time.Now().Unix(),
		"machine_id":        localNode.ID,
		"machine_name":      localNode.Name,
		"machine_type":      localNode.Type,
		"files_unchanged":   downloadStats.UnchangedCount,
		"files_downloaded":  downloadStats.SyncedCount,
		"files_uploaded":    uploadStats.SyncedCount,
		"files_pruned":      prunedDriveCount,
		"commands_imported": importedCount,
		"commands_exported": exportedCount,
	}

	if err := driveSrv.AppendGlobalSyncLog(logEntry); err != nil {
		fmt.Printf("Warning: failed to write sync audit log to Google Drive: %v\n", err)
	}

	// Print final stats summary
	fmt.Println("\n=== Sync Summary ===")
	fmt.Printf("Kept %d files unchanged, downloaded %d new files from Drive, uploaded %d files to Drive.\n",
		downloadStats.UnchangedCount, downloadStats.SyncedCount, uploadStats.SyncedCount)
	if historyResult != nil {
		fmt.Printf("CLI History: imported %d new commands, exported %d new commands.\n",
			historyResult.ImportedCount, historyResult.ExportedCount)
	}
	if autoClean {
		fmt.Printf("Autoclean: pruned %d ignored file(s) from Google Drive.\n", prunedDriveCount)
	}
	fmt.Println("====================")
}

func getLocalFileMD5(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

