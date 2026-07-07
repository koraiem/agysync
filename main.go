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
	syncFlag := flag.Bool("sync", false, "Perform the local history merge")
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
		fmt.Println("Please specify the source backup folder path using the -src flag.")
		os.Exit(0)
	}

	if !*syncFlag {
		fmt.Println("\nDry run mode. Run with -sync to execute changes.")
		fmt.Printf("Source Base: %s\n", *srcBaseFlag)
		fmt.Printf("Destination Base: %s\n", dstPaths.BaseDir)
		os.Exit(0)
	}

	srcBase := *srcBaseFlag

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

	for _, f := range folders {
		srcFolder := filepath.Join(srcBase, f.relPath)
		fmt.Printf("\nMerging %s -> %s...\n", srcFolder, f.dstPath)
		if err := sync.MergeDirectories(srcFolder, f.dstPath); err != nil {
			fmt.Printf("Error merging directory %s: %v\n", srcFolder, err)
		}
	}

	// Merge history.jsonl
	srcHistory := filepath.Join(srcBase, "antigravity-cli", "history.jsonl")
	fmt.Printf("\nMerging history.jsonl: %s -> %s...\n", srcHistory, dstPaths.CliHistoryFile)
	if err := sync.MergeHistoryJsonl(srcHistory, dstPaths.CliHistoryFile); err != nil {
		fmt.Printf("Error merging history.jsonl: %v\n", err)
	} else {
		fmt.Println("Successfully merged history.jsonl!")
	}

	fmt.Println("\nSynchronization complete!")
}
