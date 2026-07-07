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

	// Define detailed help instructions
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
		fmt.Println("\nAntigravity Sync (AgySync) CLI synchronizes conversations, logs, and command histories across platforms.")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		fmt.Println("\nExamples:")
		fmt.Println("  1. Dry-run (check paths and simulate sync without making changes):")
		fmt.Println("     agysync -src /path/to/backup/.gemini")
		fmt.Println("\n  2. Perform Sync (Backup local configuration, merge files from source, and back-propagate changes to source):")
		fmt.Println("     agysync -src /path/to/backup/.gemini -sync")
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

	if !*syncFlag {
		fmt.Println("\n[Dry Run Mode] No changes will be written.")
		fmt.Printf("Source Base: %s\n", srcBase)
		fmt.Printf("Destination Base: %s\n", dstPaths.BaseDir)
		os.Exit(0)
	}

	// 1. Create a safety backup of the current active state before starting modifications
	backupPath, err := sync.BackupActiveState(dstPaths)
	if err != nil {
		fmt.Printf("Fatal Error: pre-sync backup failed: %v. Aborting sync.\n", err)
		os.Exit(1)
	}
	fmt.Printf("Pre-sync backup created at: %s\n", backupPath)

	// Folders to merge
	folders := []struct {
		relPath string
		dstPath string
	}{
		{"antigravity/conversations", dstPaths.CoreConversations},
		{"antigravity/brain", dstPaths.CoreBrain},
		{"antigravity-cli/conversations", dstPaths.CliConversations},
		{"antigravity-cli/brain", dstPaths.CliBrain},
		{"antigravity-ide/conversations", dstPaths.IdeConversations},
		{"antigravity-ide/brain", dstPaths.IdeBrain},
		{"history", dstPaths.WorkspaceHistory},
	}

	// 2. Phase 1: Merge remote/source to local destination (Download & Merge)
	fmt.Println("\n--- Phase 1: Merging remote history into local active paths ---")
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.relPath)
		fmt.Printf("Merging %s -> %s...\n", srcFolder, f.dstPath)
		if err := sync.MergeDirectories(srcFolder, f.dstPath); err != nil {
			fmt.Printf("Warning: error merging directory %s: %v\n", srcFolder, err)
		}
	}

	// Merge history.jsonl
	srcHistory := filepath.Join(srcBase, "antigravity-cli", "history.jsonl")
	fmt.Printf("\nMerging history.jsonl: %s -> %s...\n", srcHistory, dstPaths.CliHistoryFile)
	if err := sync.MergeHistoryJsonl(srcHistory, dstPaths.CliHistoryFile); err != nil {
		fmt.Printf("Warning: error merging history.jsonl: %v\n", err)
	} else {
		fmt.Println("Successfully merged local history.jsonl!")
	}

	// 3. Phase 2: Back-propagate local to remote/source (Upload & Back-propagate)
	fmt.Println("\n--- Phase 2: Back-propagating changes to source (simulating upload) ---")
	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.relPath)
		fmt.Printf("Back-propagating %s -> %s...\n", f.dstPath, srcFolder)
		if err := sync.MergeDirectories(f.dstPath, srcFolder); err != nil {
			fmt.Printf("Warning: error back-propagating directory %s: %v\n", srcFolder, err)
		}
	}

	// Back-propagate merged history.jsonl (overwrite source history with merged version)
	fmt.Printf("\nBack-propagating history.jsonl: %s -> %s...\n", dstPaths.CliHistoryFile, srcHistory)
	if err := sync.CopyFileOverwrite(dstPaths.CliHistoryFile, srcHistory); err != nil {
		fmt.Printf("Warning: error back-propagating history.jsonl: %v\n", err)
	} else {
		fmt.Println("Successfully back-propagated history.jsonl to source!")
	}

	fmt.Println("\nSynchronization complete and changes back-propagated!")
}
