package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"agysync/pkg/sync"
)

func main() {
	srcBaseFlag := flag.String("src", "", "Path to the backup source .gemini folder (e.g., /Users/ahmed.koraiem/agy_history/.gemini)")
	syncFlag := flag.Bool("sync", false, "Perform the local history merge (with backup and back-propagation)")
	verboseSyncedFlag := flag.Bool("v", false, "Show only synced (copied) files during full run")
	verboseAllFlag := flag.Bool("vv", false, "Show all files and their sync state (copied and skipped) during full run")

	// Define detailed help instructions
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Println("\nAntigravity Sync (AgySync) CLI synchronizes conversations, logs, and command histories across platforms.")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		fmt.Println("\nExamples:")
		fmt.Println("  1. Dry-run (check paths and simulate sync without making changes):")
		fmt.Println("     agysync -src /path/to/backup/.gemini")
		fmt.Println("\n  2. Perform Sync (Quiet / Summary-only output by default):")
		fmt.Println("     agysync -src /path/to/backup/.gemini -sync")
		fmt.Println("\n  3. Perform Sync with Verbose Syncing (Lists only files that are actually copied):")
		fmt.Println("     agysync -src /path/to/backup/.gemini -sync -v")
		fmt.Println("\n  4. Perform Sync with Verbose All (Lists all files checked, including skipped ones):")
		fmt.Println("     agysync -src /path/to/backup/.gemini -sync -vv")
		fmt.Println("\nSafety Features:")
		fmt.Println("  - Pre-sync Backup: AgySync automatically archives your active history to ~/.gemini/agysync_backups/")
		fmt.Println("    before any merging begins to prevent data loss.")
		fmt.Println("  - Non-Destructive: Sync never overwrites active local session databases; it only adds new database files.")
		fmt.Println("  - Command Deduplication: history.jsonl files are merged chronologically by timestamp and deduplicated.")
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

	if *srcBaseFlag == "" {
		fmt.Println("\nUsage: agysync -src <path_to_source_gemini_folder> -sync")
		fmt.Println("Run with -h or --help for detailed documentation and examples.")
		os.Exit(0)
	}

	srcBase := *srcBaseFlag

	// Folders to merge
	folders := []sync.FolderMap{
		{RelPath: "antigravity/conversations", DstPath: dstPaths.CoreConversations},
		{RelPath: "antigravity/brain", DstPath: dstPaths.CoreBrain},
		{RelPath: "antigravity-cli/conversations", DstPath: dstPaths.CliConversations},
		{RelPath: "antigravity-cli/brain", DstPath: dstPaths.CliBrain},
		{RelPath: "antigravity-ide/conversations", DstPath: dstPaths.IdeConversations},
		{RelPath: "antigravity-ide/brain", DstPath: dstPaths.IdeBrain},
		{RelPath: "history", DstPath: dstPaths.WorkspaceHistory},
	}

	if !*syncFlag {
		fmt.Println("\n[Dry Run Mode] No changes will be written.")
		fmt.Printf("Source Base: %s\n", srcBase)
		fmt.Printf("Destination Base: %s\n", dstPaths.BaseDir)

		srcHistory := filepath.Join(srcBase, "antigravity-cli", "history.jsonl")
		if err := sync.CompareSyncState(srcBase, folders, srcHistory, dstPaths.CliHistoryFile); err != nil {
			fmt.Printf("Error generating comparison report: %v\n", err)
		}
		os.Exit(0)
	}

	// 1. Create a safety backup of the current active state before starting modifications
	backupPath, err := sync.BackupActiveState(dstPaths)
	if err != nil {
		fmt.Printf("Fatal Error: pre-sync backup failed: %v. Aborting sync.\n", err)
		os.Exit(1)
	}
	fmt.Printf("Pre-sync backup created at: %s\n", backupPath)

	// Determine verbosity level
	verbosity := 0
	if *verboseSyncedFlag {
		verbosity = 1
	}
	if *verboseAllFlag {
		verbosity = 2
	}

	// Track stats
	downloadStats := &sync.SyncStats{}
	uploadStats := &sync.SyncStats{}

	// 2. Phase 1: Merge remote/source to local destination (Download & Merge)
	if verbosity > 0 {
		fmt.Println("\n--- Phase 1: Merging remote history into local active paths ---")
	}
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		if verbosity >= 2 {
			fmt.Printf("Checking folder %s -> %s...\n", srcFolder, f.DstPath)
		}
		if err := sync.MergeDirectories(srcFolder, f.DstPath, downloadStats, verbosity); err != nil {
			fmt.Printf("Warning: error merging directory %s: %v\n", srcFolder, err)
		}
	}

	// Merge history.jsonl
	srcHistory := filepath.Join(srcBase, "antigravity-cli", "history.jsonl")
	if verbosity >= 2 {
		fmt.Printf("Merging history.jsonl: %s -> %s...\n", srcHistory, dstPaths.CliHistoryFile)
	}
	historyResult, err := sync.MergeHistoryJsonl(srcHistory, dstPaths.CliHistoryFile, verbosity)
	if err != nil {
		fmt.Printf("Warning: error merging history.jsonl: %v\n", err)
	}

	// 3. Phase 2: Back-propagate local to remote/source (Upload & Back-propagate)
	if verbosity > 0 {
		fmt.Println("\n--- Phase 2: Back-propagating changes to source ---")
	}
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.RelPath)
		if verbosity >= 2 {
			fmt.Printf("Back-propagating check %s -> %s...\n", f.DstPath, srcFolder)
		}
		if err := sync.MergeDirectories(f.DstPath, srcFolder, uploadStats, verbosity); err != nil {
			fmt.Printf("Warning: error back-propagating directory %s: %v\n", srcFolder, err)
		}
	}

	// Back-propagate merged history.jsonl (overwrite source history with merged version)
	if verbosity >= 2 {
		fmt.Printf("Overwriting history.jsonl: %s -> %s...\n", dstPaths.CliHistoryFile, srcHistory)
	}
	if err := sync.CopyFileOverwrite(dstPaths.CliHistoryFile, srcHistory); err != nil {
		fmt.Printf("Warning: error back-propagating history.jsonl: %v\n", err)
	}

	// Print final stats summary
	fmt.Println("\n=== Sync Summary ===")
	fmt.Printf("Kept %d files unchanged, downloaded %d new files from backup, uploaded %d files to backup.\n",
		downloadStats.UnchangedCount, downloadStats.SyncedCount, uploadStats.SyncedCount)
	if historyResult != nil {
		fmt.Printf("CLI History: imported %d new commands, exported %d new commands.\n",
			historyResult.ImportedCount, historyResult.ExportedCount)
	}
	fmt.Println("====================")
}
