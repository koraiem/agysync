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
	syncFlag := flag.Bool("sync", false, "Perform the bidirectional sync or migration (with backup, propagation, and logs)")
	migrateFlag := flag.Bool("migrate", false, "Consolidate local orphaned IDE/CLI conversations & join projects without Google Drive (dry-run unless -sync is passed)")
	autocleanFlag := flag.Bool("autoclean", false, "Prune ignored/deprecated files (.git, .DS_Store, stale locks, migrated antigravity-ide__*) from Google Drive AppData or destination")
	verboseSyncedFlag := flag.Bool("v", false, "Show only synced (copied) files during execution")
	verboseAllFlag := flag.Bool("vv", false, "Show all files checked (copied and skipped) during execution")
	translateFlag := flag.Bool("translate", false, "Force translate/localize all local SQLite database conversation paths and reconcile project indexes")
	logoutFlag := flag.Bool("logout", false, "Log out of Google Drive OAuth session by clearing stored tokens")

	// Custom Usage
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Println("\nAntigravity Sync (AgySync) CLI consolidates IDE/CLI/Desktop histories, joins projects, and synchronizes across machines.")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		fmt.Println("\nExamples:")
		fmt.Println("  1. Dry-run Google Drive Sync + Local Orphan Consolidation Preview:")
		fmt.Println("     agysync")
		fmt.Println("\n  2. Perform Unified Consolidation & Google Drive Sync (Requires OAuth2):")
		fmt.Println("     agysync -sync")
		fmt.Println("\n  3. Perform Google Drive Sync & Prune Ignored/Deprecated Files:")
		fmt.Println("     agysync -sync -autoclean")
		fmt.Println("\n  4. Local-Only Orphan Consolidation & Project Joining (Preview vs Execute):")
		fmt.Println("     agysync -migrate")
		fmt.Println("     agysync -migrate -sync")
		fmt.Println("\n  5. Local Folder Sync Mode (Fallback / Offline sync):")
		fmt.Println("     agysync -src /path/to/backup/.gemini -sync")
		fmt.Println("\n  6. Log out of Google Drive OAuth session:")
		fmt.Println("     agysync -logout")
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

	if *logoutFlag {
		fmt.Println("\nAction: Logging out of Google Drive OAuth session...")
		cleared, err := drive.Logout(dstPaths)
		if err != nil {
			fmt.Printf("Error during logout: %v\n", err)
			os.Exit(1)
		}
		if cleared {
			fmt.Println("Successfully logged out. OAuth session token cleared.")
			fmt.Printf("Removed: %s\n", dstPaths.TokenFile)
		} else {
			fmt.Println("No active OAuth session found (already logged out).")
		}
		return
	}

	// Determine verbosity level
	verbosity := 0
	if *verboseSyncedFlag {
		verbosity = 1
	}
	if *verboseAllFlag {
		verbosity = 2
	}

	if *migrateFlag {
		runLocalMigrateMode(dstPaths, *syncFlag, verbosity)
		return
	}

	if *translateFlag {
		fmt.Println("\nAction: Force translating local SQLite conversation paths & reconciling project indexes...")
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

		cStats, err := sync.ConsolidateAndReconcile(dstPaths, sync.ConsolidateOptions{
			DryRun:    false,
			Verbosity: verbosity,
		})
		if err != nil {
			fmt.Printf("Warning: error during unified history reconciliation: %v\n", err)
		}
		sync.PrintConsolidateReport(cStats, false)
		return
	}

	// 1. Differentiate execution paths: Local Sync vs Google Drive Sync
	if *srcBaseFlag != "" {
		runLocalFolderSync(*srcBaseFlag, dstPaths, *syncFlag, verbosity, *autocleanFlag)
		return
	}

	runGoogleDriveSync(dstPaths, *syncFlag, verbosity, *autocleanFlag)
}

func runLocalMigrateMode(dstPaths *sync.Paths, performSync bool, verbosity int) {
	fmt.Println("\nMode: Local History Consolidation & Project Migration")

	if !performSync {
		fmt.Println("\n[Dry Run Mode] No changes will be written.")
		cStats, err := sync.ConsolidateAndReconcile(dstPaths, sync.ConsolidateOptions{
			DryRun:    true,
			Verbosity: verbosity,
		})
		if err != nil {
			fmt.Printf("Error analyzing local history: %v\n", err)
			return
		}
		sync.PrintConsolidateReport(cStats, true)
		fmt.Println("\nTip: Run 'agysync -migrate -sync' to execute local migration, or 'agysync -sync' to migrate and sync with Google Drive.")
		return
	}

	backupPath, err := sync.BackupActiveState(dstPaths)
	if err != nil {
		fmt.Printf("Fatal Error: pre-migration backup failed: %v. Aborting.\n", err)
		os.Exit(1)
	}
	fmt.Printf("Pre-migration backup created at: %s\n", backupPath)

	cStats, err := sync.ConsolidateAndReconcile(dstPaths, sync.ConsolidateOptions{
		DryRun:    false,
		Verbosity: verbosity,
	})
	if err != nil {
		fmt.Printf("Warning: error during local consolidation: %v\n", err)
	}
	sync.PrintConsolidateReport(cStats, false)
}

func runLocalFolderSync(srcBase string, dstPaths *sync.Paths, performSync bool, verbosity int, autoClean bool) {
	fmt.Println("\nMode: Local Folder Sync (Fallback)")

	folders := []sync.FolderMap{
		{RelPath: "config/projects", DstPath: dstPaths.ProjectsConfigDir},
		{RelPath: "antigravity/conversations", DstPath: dstPaths.CoreConversations},
		{RelPath: "antigravity/brain", DstPath: dstPaths.CoreBrain},
		{RelPath: "antigravity/annotations", DstPath: dstPaths.CoreAnnotations},
		{RelPath: "antigravity-cli/conversations", DstPath: dstPaths.CliConversations},
		{RelPath: "antigravity-cli/brain", DstPath: dstPaths.CliBrain},
		{RelPath: "history", DstPath: dstPaths.WorkspaceHistory},
	}

	srcHistory := filepath.Join(srcBase, "antigravity-cli", "history.jsonl")

	if !performSync {
		fmt.Println("\n[Dry Run Mode] No changes will be written.")
		fmt.Printf("Source Base: %s\n", srcBase)
		if cStats, err := sync.ConsolidateAndReconcile(dstPaths, sync.ConsolidateOptions{DryRun: true, Verbosity: verbosity}); err == nil {
			sync.PrintConsolidateReport(cStats, true)
		}
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

	// Record remote project IDs so remote projects win during deduplication
	preferProjectIDs := make(map[string]bool)
	remoteProjDir := filepath.Join(srcBase, "config", "projects")
	if entries, err := os.ReadDir(remoteProjDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				preferProjectIDs[strings.TrimSuffix(e.Name(), ".json")] = true
			}
		}
	}

	// Phase 1: Download/Merge
	fmt.Println("\n--- Phase 1: Merging remote history into local active paths ---")
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		if err := sync.MergeDirectories(srcFolder, f.DstPath, downloadStats, verbosity, dstPaths); err != nil {
			fmt.Printf("Warning: error merging directory %s: %v\n", srcFolder, err)
		}
	}

	// Also ingest legacy antigravity-ide folders from backup source directly into Core
	legacyIdeMappings := []sync.FolderMap{
		{RelPath: "antigravity-ide/conversations", DstPath: dstPaths.CoreConversations},
		{RelPath: "antigravity-ide/brain", DstPath: dstPaths.CoreBrain},
	}
	for _, f := range legacyIdeMappings {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		_ = sync.MergeDirectories(srcFolder, f.DstPath, downloadStats, verbosity, dstPaths)
	}

	// Merge remote agyhub_summaries_proto.pb if present
	srcSummariesPb := filepath.Join(srcBase, "antigravity", "agyhub_summaries_proto.pb")
	if _, err := os.Stat(srcSummariesPb); err == nil {
		_ = sync.MergeSummaryProtoFiles(srcSummariesPb, dstPaths.CoreSummariesPb)
	}

	historyResult, err := sync.MergeHistoryJsonl(srcHistory, dstPaths.CliHistoryFile, verbosity, dstPaths)
	if err != nil {
		fmt.Printf("Warning: error merging history.jsonl: %v\n", err)
	}

	// Stage 3: Consolidate local orphans, localize paths, join projects, and reconcile indexes
	cStats, err := sync.ConsolidateAndReconcile(dstPaths, sync.ConsolidateOptions{
		DryRun:           false,
		Verbosity:        verbosity,
		PreferProjectIDs: preferProjectIDs,
	})
	if err != nil {
		fmt.Printf("Warning: error consolidating history and projects: %v\n", err)
	}
	sync.PrintConsolidateReport(cStats, false)

	// Phase 2: Upload/Propagate
	fmt.Println("\n--- Phase 2: Back-propagating changes to source ---")
	localHome := sync.InferHomeFromPaths(dstPaths)
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		if f.RelPath == "config/projects" {
			// Canonicalize project configs when propagating to backup source
			if entries, err := os.ReadDir(f.DstPath); err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
						srcFile := filepath.Join(f.DstPath, e.Name())
						dstFile := filepath.Join(srcFolder, e.Name())
						if err := sync.CanonicalizeProjectConfigFile(srcFile, dstFile, localHome); err == nil {
							uploadStats.SyncedCount++
						}
					}
				}
			}
			continue
		}
		if err := sync.MergeDirectories(f.DstPath, srcFolder, uploadStats, verbosity, dstPaths); err != nil {
			fmt.Printf("Warning: error back-propagating directory %s: %v\n", srcFolder, err)
		}
	}

	if _, err := os.Stat(dstPaths.CoreSummariesPb); err == nil {
		_ = sync.CanonicalizeSummaryProtoFile(dstPaths.CoreSummariesPb, srcSummariesPb, dstPaths)
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
		fmt.Println("\nFor offline consolidation, use: agysync -migrate -sync")
		fmt.Println("For offline folder sync, use:   agysync -src <backup_folder_path>")
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

	// Active unified folders synced with Google Drive
	folders := []string{
		"config/projects",
		"antigravity/conversations",
		"antigravity/brain",
		"antigravity/annotations",
		"antigravity-cli/conversations",
		"antigravity-cli/brain",
		"history",
	}

	// 4. Retrieve cloud files metadata
	remoteFiles, err := driveSrv.ListAppDataFiles()
	if err != nil {
		fmt.Printf("Error listing files on Google Drive: %v\n", err)
		os.Exit(1)
	}

	if !performSync {
		// Dry Run Comparison report for Google Drive + Local Orphan Consolidation
		fmt.Println("\n[Dry Run Mode] Simulating local consolidation and cloud synchronization...")

		if cStats, err := sync.ConsolidateAndReconcile(dstPaths, sync.ConsolidateOptions{DryRun: true, Verbosity: verbosity}); err == nil {
			sync.PrintConsolidateReport(cStats, true)
		}

		fmt.Println("\n=== Cloud Sync Comparison Report ===")

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
					if sync.ShouldIgnoreSyncPath(rel) {
						continue
					}
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

		// Check legacy antigravity-ide__* files on Drive that would be ingested into antigravity/
		var legacyIdeToIngest []string
		for name := range remoteFiles {
			if strings.HasPrefix(name, "antigravity-ide__conversations__") || strings.HasPrefix(name, "antigravity-ide__brain__") {
				mappedRel := strings.TrimPrefix(drive.RelPath(name), "antigravity-ide/")
				mappedRel = "antigravity/" + mappedRel
				if !sync.ShouldIgnoreSyncPath(mappedRel) {
					localTarget := filepath.Join(dstPaths.BaseDir, mappedRel)
					if _, err := os.Stat(localTarget); os.IsNotExist(err) {
						legacyIdeToIngest = append(legacyIdeToIngest, mappedRel)
					}
				}
			}
		}
		if len(legacyIdeToIngest) > 0 {
			fmt.Printf("\nLegacy IDE Cloud Files: Would ingest %d file(s) from antigravity-ide__* into antigravity/\n", len(legacyIdeToIngest))
		}

		// history.jsonl comparison
		fmt.Println("\nComparing CLI History: history.jsonl")
		tempLocalHistory := filepath.Join(os.TempDir(), "agysync_temp_hist.jsonl")
		if rf, ok := remoteFiles["history.jsonl"]; ok {
			err = driveSrv.DownloadFileByID(rf.ID, "history.jsonl", tempLocalHistory)
		} else {
			err = fmt.Errorf("not found")
		}
		if err == nil {
			_ = sync.CompareSyncState(os.TempDir(), []sync.FolderMap{}, tempLocalHistory, dstPaths.CliHistoryFile)
			_ = os.Remove(tempLocalHistory)
		} else {
			fmt.Println("  No remote command history file found in Google Drive AppData.")
		}

		// Ignored / deprecated files report on Drive
		var ignoredRemoteFiles []sync.RemoteFile
		for name, file := range remoteFiles {
			rel := drive.RelPath(name)
			if sync.ShouldIgnoreSyncPath(rel) || strings.HasPrefix(name, "antigravity-ide__") {
				ignoredRemoteFiles = append(ignoredRemoteFiles, file)
			}
		}

		if len(ignoredRemoteFiles) > 0 {
			if autoClean {
				fmt.Printf("\n  🗑️ [Autoclean] Would prune %d ignored/deprecated file(s) from Google Drive (.git, .DS_Store, stale locks, legacy antigravity-ide__*):\n", len(ignoredRemoteFiles))
				if verbosity >= 1 {
					for _, f := range ignoredRemoteFiles {
						fmt.Printf("    - %s\n", drive.RelPath(f.Name))
					}
				}
			} else {
				fmt.Printf("\n  ℹ️ [Autoclean Info] Found %d ignored/deprecated file(s) on Google Drive. Run with -autoclean -sync to prune them.\n", len(ignoredRemoteFiles))
			}
		}

		fmt.Println("\n=======================================")
		return
	}

	// 5. Run Sync Execution (Stage 0: Pre-Sync Safety Backup)
	backupPath, err := sync.BackupActiveState(dstPaths)
	if err != nil {
		fmt.Printf("Fatal Error: pre-sync backup failed: %v. Aborting sync.\n", err)
		os.Exit(1)
	}
	fmt.Printf("Pre-sync backup created at: %s\n", backupPath)

	downloadStats := &sync.SyncStats{}
	uploadStats := &sync.SyncStats{}
	localHome := sync.InferHomeFromPaths(dstPaths)

	// Record remote project IDs from Drive so remote project IDs win during deduplication
	preferProjectIDs := make(map[string]bool)
	for name := range remoteFiles {
		if strings.HasPrefix(name, "config__projects__") && strings.HasSuffix(name, ".json") {
			projID := strings.TrimSuffix(strings.TrimPrefix(name, "config__projects__"), ".json")
			if projID != "" {
				preferProjectIDs[projID] = true
			}
		}
	}

	// Stage 1 & 2: Download & Merge from Google Drive
	if verbosity > 0 {
		fmt.Println("\n--- Phase 1: Downloading updates from Google Drive AppData ---")
	}

	// Build download task list (active folders + legacy antigravity-ide__* mapped into antigravity/)
	type downloadCandidate struct {
		driveName string
		localPath string
		remote    sync.RemoteFile
		isLegacy  bool
	}
	var downloadList []downloadCandidate

	for _, folder := range folders {
		prefix := drive.FlatName(folder) + "__"
		for name, rf := range remoteFiles {
			if strings.HasPrefix(name, prefix) {
				rel := drive.RelPath(name)
				if sync.ShouldIgnoreSyncPath(rel) {
					continue
				}
				downloadList = append(downloadList, downloadCandidate{
					driveName: name,
					localPath: filepath.Join(dstPaths.BaseDir, rel),
					remote:    rf,
				})
			}
		}
	}

	// Also include legacy antigravity-ide__conversations__* and antigravity-ide__brain__* mapped into antigravity/
	for name, rf := range remoteFiles {
		if strings.HasPrefix(name, "antigravity-ide__conversations__") || strings.HasPrefix(name, "antigravity-ide__brain__") {
			rel := drive.RelPath(name)
			if sync.ShouldIgnoreSyncPath(rel) {
				continue
			}
			mappedRel := "antigravity/" + strings.TrimPrefix(rel, "antigravity-ide/")
			// Only ingest if the corresponding antigravity__* file isn't already in remoteFiles
			modernFlat := drive.FlatName(mappedRel)
			if _, modernExists := remoteFiles[modernFlat]; !modernExists {
				downloadList = append(downloadList, downloadCandidate{
					driveName: name,
					localPath: filepath.Join(dstPaths.BaseDir, mappedRel),
					remote:    rf,
					isLegacy:  true,
				})
			}
		}
	}

	var pTracker *sync.ProgressTracker
	if verbosity == 0 {
		pTracker = &sync.ProgressTracker{
			Label: "Downloading from Google Drive",
			Total: len(downloadList),
		}
		pTracker.Print()
	}

	for _, item := range downloadList {
		localPath := item.localPath
		remoteFile := item.remote

		shouldDownload := false
		info, err := os.Stat(localPath)
		if os.IsNotExist(err) {
			shouldDownload = true
		} else if err == nil && !item.isLegacy {
			canonicalMD5, err := getCanonicalFileMD5(localPath, dstPaths, localHome)
			if err == nil && canonicalMD5 != remoteFile.MD5 {
				if remoteFile.ModifiedTime.After(info.ModTime()) {
					shouldDownload = true
				}
			}
		}

		if shouldDownload {
			if verbosity >= 1 {
				fmt.Printf("Downloading: %s -> %s\n", item.driveName, localPath)
			}
			if err := driveSrv.DownloadFileByID(remoteFile.ID, item.driveName, localPath); err != nil {
				fmt.Printf("\nWarning: failed to download file %s: %v\n", item.driveName, err)
			} else {
				downloadStats.SyncedCount++
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
	if pTracker != nil {
		pTracker.Finish()
	}

	// Download and merge agyhub_summaries_proto.pb if present on Drive
	if rf, ok := remoteFiles["antigravity__agyhub_summaries_proto.pb"]; ok {
		tempRemotePb := filepath.Join(os.TempDir(), "agysync_temp_remote_summaries.pb")
		if err := driveSrv.DownloadFileByID(rf.ID, "antigravity__agyhub_summaries_proto.pb", tempRemotePb); err == nil {
			_ = sync.MergeSummaryProtoFiles(tempRemotePb, dstPaths.CoreSummariesPb)
			_ = os.Remove(tempRemotePb)
		}
	}

	// Merge history.jsonl
	var historyResult *sync.MergeHistoryJsonlResult
	if rf, ok := remoteFiles["history.jsonl"]; ok {
		tempLocalHistory := filepath.Join(os.TempDir(), "agysync_temp_hist.jsonl")
		if err := driveSrv.DownloadFileByID(rf.ID, "history.jsonl", tempLocalHistory); err == nil {
			historyResult, _ = sync.MergeHistoryJsonl(tempLocalHistory, dstPaths.CliHistoryFile, verbosity, dstPaths)
			_ = os.Remove(tempLocalHistory)
		}
	}

	// Stage 3: Consolidate local IDE/CLI orphans, localize workspaces, converge/create projects, and reconcile all indexes
	cStats, err := sync.ConsolidateAndReconcile(dstPaths, sync.ConsolidateOptions{
		DryRun:           false,
		Verbosity:        verbosity,
		PreferProjectIDs: preferProjectIDs,
	})
	if err != nil {
		fmt.Printf("Warning: error consolidating history and projects: %v\n", err)
	}
	sync.PrintConsolidateReport(cStats, false)

	// Stage 4: Upload & Propagate Canonicalized State to Google Drive
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

			uploadPath, tempCleanupPath := prepareCanonicalUploadFile(path, folder, info.Name(), dstPaths, localHome)
			defer func() {
				if tempCleanupPath != "" {
					_ = os.Remove(tempCleanupPath)
				}
			}()

			remoteFile, exists := remoteFiles[flat]
			shouldUpload := false
			if !exists {
				shouldUpload = true
			} else {
				canonicalMD5, err := getLocalFileMD5(uploadPath)
				if err == nil && canonicalMD5 != remoteFile.MD5 {
					if info.ModTime().After(remoteFile.ModifiedTime) {
						shouldUpload = true
					}
				}
			}

			if shouldUpload {
				if verbosity >= 1 {
					fmt.Printf("Uploading: %s -> %s\n", rel, flat)
				}
				if err := driveSrv.UploadFileWithID(uploadPath, flat, remoteFile.ID); err != nil {
					fmt.Printf("\nWarning: failed to upload file %s: %v\n", rel, err)
				} else {
					uploadStats.SyncedCount++
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

	// Upload canonicalized antigravity__agyhub_summaries_proto.pb if modified
	if _, err := os.Stat(dstPaths.CoreSummariesPb); err == nil {
		tempPb := filepath.Join(os.TempDir(), "agysync_temp_upload_summaries.pb")
		if err := sync.CanonicalizeSummaryProtoFile(dstPaths.CoreSummariesPb, tempPb, dstPaths); err == nil {
			rf, exists := remoteFiles["antigravity__agyhub_summaries_proto.pb"]
			canMD5, err := getLocalFileMD5(tempPb)
			if !exists || (err == nil && canMD5 != rf.MD5) {
				if err := driveSrv.UploadFileWithID(tempPb, "antigravity__agyhub_summaries_proto.pb", rf.ID); err != nil {
					fmt.Printf("Warning: failed to upload agyhub_summaries_proto.pb: %v\n", err)
				} else {
					uploadStats.SyncedCount++
				}
			}
			_ = os.Remove(tempPb)
		}
	}

	// Upload merged and canonicalized history.jsonl if modified
	if _, err := os.Stat(dstPaths.CliHistoryFile); err == nil {
		tempHistFile := filepath.Join(os.TempDir(), "agysync_temp_upload_history.jsonl")
		if err := sync.CanonicalizeHistoryJsonl(dstPaths.CliHistoryFile, tempHistFile); err == nil {
			rf, exists := remoteFiles["history.jsonl"]
			canMD5, err := getLocalFileMD5(tempHistFile)
			if !exists || (err == nil && canMD5 != rf.MD5) {
				if err := driveSrv.UploadFileWithID(tempHistFile, "history.jsonl", rf.ID); err != nil {
					fmt.Printf("Warning: failed to upload merged history.jsonl: %v\n", err)
				}
			}
			_ = os.Remove(tempHistFile)
		} else {
			rf := remoteFiles["history.jsonl"]
			if err := driveSrv.UploadFileWithID(dstPaths.CliHistoryFile, "history.jsonl", rf.ID); err != nil {
				fmt.Printf("Warning: failed to upload merged history.jsonl (fallback): %v\n", err)
			}
		}
	}

	// Phase 3: Pruning ignored & deprecated files from Google Drive AppData (Only if -autoclean is passed)
	prunedDriveCount := 0
	if autoClean {
		var ignoredRemoteFiles []sync.RemoteFile
		for name, file := range remoteFiles {
			rel := drive.RelPath(name)
			if sync.ShouldIgnoreSyncPath(rel) || strings.HasPrefix(name, "antigravity-ide__") {
				ignoredRemoteFiles = append(ignoredRemoteFiles, file)
			}
		}

		if len(ignoredRemoteFiles) > 0 {
			if verbosity > 0 {
				fmt.Printf("\n--- Phase 3: Pruning %d ignored/deprecated file(s) from Google Drive AppData ---\n", len(ignoredRemoteFiles))
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
		"timestamp":          time.Now().Unix(),
		"machine_id":         localNode.ID,
		"machine_name":       localNode.Name,
		"machine_type":       localNode.Type,
		"files_unchanged":    downloadStats.UnchangedCount,
		"files_downloaded":   downloadStats.SyncedCount,
		"files_uploaded":     uploadStats.SyncedCount,
		"files_pruned":       prunedDriveCount,
		"commands_imported":  importedCount,
		"commands_exported":  exportedCount,
		"projects_created":   cStats.ProjectsCreated,
		"projects_converged": cStats.ProjectsConverged,
		"convos_joined":      cStats.ConversationsJoined,
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
		fmt.Printf("Autoclean: pruned %d ignored/deprecated file(s) from Google Drive.\n", prunedDriveCount)
	}
	fmt.Println("====================")
}

func prepareCanonicalUploadFile(path, folder, baseName string, dstPaths *sync.Paths, localHome string) (string, string) {
	if strings.HasSuffix(path, ".db") {
		tempDbPath := filepath.Join(os.TempDir(), fmt.Sprintf("agysync_temp_upload_%d_%s", os.Getpid(), baseName))
		if err := sync.CopySqliteDbSafe(path, tempDbPath); err == nil {
			if err := sync.CanonicalizeDbFile(tempDbPath, dstPaths); err == nil {
				return tempDbPath, tempDbPath
			}
			_ = os.Remove(tempDbPath)
		}
	} else if folder == "config/projects" && strings.HasSuffix(path, ".json") {
		tempJsonPath := filepath.Join(os.TempDir(), fmt.Sprintf("agysync_temp_upload_%d_%s", os.Getpid(), baseName))
		if err := sync.CanonicalizeProjectConfigFile(path, tempJsonPath, localHome); err == nil {
			return tempJsonPath, tempJsonPath
		}
	}
	return path, ""
}

func getCanonicalFileMD5(localPath string, dstPaths *sync.Paths, localHome string) (string, error) {
	folder := ""
	if strings.Contains(filepath.ToSlash(localPath), "/config/projects/") {
		folder = "config/projects"
	}
	uploadPath, cleanupPath := prepareCanonicalUploadFile(localPath, folder, filepath.Base(localPath), dstPaths, localHome)
	if cleanupPath != "" {
		defer os.Remove(cleanupPath)
	}
	return getLocalFileMD5(uploadPath)
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
