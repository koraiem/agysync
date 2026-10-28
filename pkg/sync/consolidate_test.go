package sync

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestLocalizeWorkspacePathDisambiguation(t *testing.T) {
	localHome := "/Users/alice"
	localProjects := []string{
		"/Users/alice/dev/agysync",
		"/Users/alice/dev/routemap",
		"/Users/alice/dev/other/routemap",
		"/Users/alice/Downloads/EG Digital Signature",
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Already localized nested routemap stays nested",
			input:    "file:///Users/alice/dev/other/routemap",
			expected: "/Users/alice/dev/other/routemap",
		},
		{
			name:     "Already localized top routemap stays top",
			input:    "file:///Users/alice/dev/routemap",
			expected: "/Users/alice/dev/routemap",
		},
		{
			name:     "Cross-machine nested routemap maps to nested routemap",
			input:    "file:///home/krusty/dev/other/routemap",
			expected: "/Users/alice/dev/other/routemap",
		},
		{
			name:     "Cross-machine top routemap maps to top routemap",
			input:    "file:///home/krusty/dev/routemap",
			expected: "/Users/alice/dev/routemap",
		},
		{
			name:     "URL-encoded space path unescapes cleanly",
			input:    "file:///Users/alice/Downloads/EG%20Digital%20Signature",
			expected: "/Users/alice/Downloads/EG Digital Signature",
		},
		{
			name:     "Uncloned Linux repo falls back to localHome/dev/<repo>",
			input:    "file:///home/krusty/dev/statoolsnet",
			expected: "/Users/alice/dev/statoolsnet",
		},
		{
			name:     "Antigravity worktree localizes home prefix without matching leaf branch",
			input:    "file:///home/krusty/.gemini/antigravity/worktrees/tvui/routemap",
			expected: "/Users/alice/.gemini/antigravity/worktrees/tvui/routemap",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := LocalizeWorkspacePath(tc.input, localProjects, localHome)
			if got != tc.expected {
				t.Errorf("LocalizeWorkspacePath(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestDeterministicProjectIDAndContainerFilter(t *testing.T) {
	macURI := CanonicalizeWorkspaceURI("file:///Users/alice/dev/statoolsnet", "/Users/alice")
	linuxURI := CanonicalizeWorkspaceURI("file:///home/krusty/dev/statoolsnet", "/home/krusty")

	if macURI != "file://~/dev/statoolsnet" || linuxURI != "file://~/dev/statoolsnet" {
		t.Fatalf("expected canonical URI file://~/dev/statoolsnet, got mac=%q linux=%q", macURI, linuxURI)
	}

	idMac := DeterministicProjectID(macURI)
	idLinux := DeterministicProjectID(linuxURI)
	if idMac == "" || idMac != idLinux {
		t.Fatalf("expected identical deterministic UUIDv5 across OSes, got %q vs %q", idMac, idLinux)
	}

	containers := []string{
		"",
		"/",
		"/tmp",
		"/Users/alice",
		"/Users/alice/dev",
		"/Users/alice/Downloads",
		"/Users/alice/Desktop",
	}
	for _, c := range containers {
		if !IsGenericContainerPath(c, "/Users/alice") {
			t.Errorf("expected %q to be detected as generic container path", c)
		}
	}

	realProjects := []string{
		"/Users/alice/dev/agysync",
		"/Users/alice/Downloads/EG Digital Signature",
		"/Users/alice/dev/other/routemap",
	}
	for _, p := range realProjects {
		if IsGenericContainerPath(p, "/Users/alice") {
			t.Errorf("expected %q to be treated as a real project workspace", p)
		}
	}
}

func TestProtobufWirePreservingProjectID(t *testing.T) {
	// Construct a CortexTrajectoryMetadata blob with unknown Field 3 (session UUID) and Field 15 (internal stats bytes)
	var rawMeta []byte
	var wsSub []byte
	wsSub = protowire.AppendTag(wsSub, 1, protowire.BytesType)
	wsSub = protowire.AppendString(wsSub, "file:///home/krusty/dev/agysync")
	wsSub = protowire.AppendTag(wsSub, 4, protowire.BytesType)
	wsSub = protowire.AppendString(wsSub, "main")

	rawMeta = protowire.AppendTag(rawMeta, 1, protowire.BytesType)
	rawMeta = protowire.AppendBytes(rawMeta, wsSub)
	rawMeta = protowire.AppendTag(rawMeta, 3, protowire.BytesType)
	rawMeta = protowire.AppendString(rawMeta, "session-uuid-1234")
	rawMeta = protowire.AppendTag(rawMeta, 7, protowire.BytesType)
	rawMeta = protowire.AppendString(rawMeta, "file:///home/krusty/dev/agysync")
	secretStats := []byte{0x08, 0x2a, 0x12, 0x04, 0xde, 0xad, 0xbe, 0xef}
	rawMeta = protowire.AppendTag(rawMeta, 15, protowire.BytesType)
	rawMeta = protowire.AppendBytes(rawMeta, secretStats)

	updated, changed := UpdateTrajectoryMetadataProto(rawMeta, "file:///Users/alice/dev/agysync", "proj-uuid-999")
	if !changed {
		t.Fatalf("expected UpdateTrajectoryMetadataProto to report changed=true")
	}

	info := &IdeConversationInfo{}
	parseTrajectoryMetadataBlob(updated, info)
	if info.WorkspaceURI != "file:///Users/alice/dev/agysync" {
		t.Errorf("expected WorkspaceURI updated to file:///Users/alice/dev/agysync, got %q", info.WorkspaceURI)
	}
	if info.ProjectID != "proj-uuid-999" {
		t.Errorf("expected ProjectID updated to proj-uuid-999, got %q", info.ProjectID)
	}
	if info.GitBranch != "main" {
		t.Errorf("expected GitBranch 'main' preserved, got %q", info.GitBranch)
	}
	if !bytes.Contains(updated, []byte("session-uuid-1234")) || !bytes.Contains(updated, secretStats) {
		t.Errorf("expected unknown wire fields (Field 3 & Field 15) preserved byte-for-byte")
	}
}

func createMockConversationDb(t *testing.T, dbPath, cascadeID, workspaceURI, projectID, promptText string, stepCount int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatal(err)
	}

	var meta []byte
	if workspaceURI != "" {
		var ws []byte
		ws = protowire.AppendTag(ws, 1, protowire.BytesType)
		ws = protowire.AppendString(ws, workspaceURI)
		meta = protowire.AppendTag(meta, 1, protowire.BytesType)
		meta = protowire.AppendBytes(meta, ws)
		meta = protowire.AppendTag(meta, 7, protowire.BytesType)
		meta = protowire.AppendString(meta, workspaceURI)
	}
	meta = protowire.AppendTag(meta, 6, protowire.BytesType)
	meta = protowire.AppendString(meta, cascadeID)
	if projectID != "" {
		meta = protowire.AppendTag(meta, 18, protowire.BytesType)
		meta = protowire.AppendString(meta, projectID)
	}
	metaHex := hex.EncodeToString(meta)

	var stepPayload []byte
	stepPayload = protowire.AppendTag(stepPayload, 4, protowire.BytesType)
	stepPayload = protowire.AppendString(stepPayload, promptText)
	stepHex := hex.EncodeToString(stepPayload)

	var sb strings.Builder
	sb.WriteString(`
CREATE TABLE trajectory_meta (trajectory_id TEXT, cascade_id TEXT);
CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER, step_payload BLOB);
CREATE TABLE trajectory_metadata_blob (id TEXT PRIMARY KEY, data BLOB);
`)
	sb.WriteString(fmt.Sprintf("INSERT INTO trajectory_meta VALUES ('%s', '%s');\n", cascadeID, cascadeID))
	sb.WriteString(fmt.Sprintf("INSERT INTO trajectory_metadata_blob VALUES ('main', x'%s');\n", metaHex))
	for i := 0; i < stepCount; i++ {
		sb.WriteString(fmt.Sprintf("INSERT INTO steps VALUES (%d, 14, x'%s');\n", i, stepHex))
	}

	cmd := exec.Command("sqlite3", dbPath)
	cmd.Stdin = strings.NewReader(sb.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed creating mock db %s: %v (%s)", dbPath, err, string(out))
	}
}

func TestMultiMachineOrphanConsolidationAndConvergence(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 CLI not available")
	}

	tmpRoot := t.TempDir()
	m1Home := filepath.Join(tmpRoot, "Users", "alice")
	m2Home := filepath.Join(tmpRoot, "home", "bob")
	cloudDir := filepath.Join(tmpRoot, "cloud_drive")

	m1Paths := DetectPathsFromBase(filepath.Join(m1Home, ".gemini"), m1Home)
	m2Paths := DetectPathsFromBase(filepath.Join(m2Home, ".gemini"), m2Home)

	// Machine 1 setup:
	// 1. Existing project for agysync with ID "m1-agysync-proj-id"
	m1AgyProj := &ProjectConfig{
		ID:   "m1-agysync-proj-id",
		Name: "agysync",
		ProjectResources: ProjectResources{
			Resources: []ProjectResource{{FolderURI: "file://" + m1Home + "/dev/agysync"}},
		},
		PermissionGrants: map[string]interface{}{"v2Migrated": true},
	}
	if err := writeProjectConfigFile(m1Paths.ProjectsConfigDir, m1AgyProj); err != nil {
		t.Fatal(err)
	}

	// 2. Orphaned IDE conversation already copied by .ide_migration into CoreConversations with empty project_id=""
	conv1ID := "11111111-1111-1111-1111-111111111111"
	createMockConversationDb(t,
		filepath.Join(m1Paths.CoreConversations, conv1ID+".db"),
		conv1ID, "file://"+m1Home+"/dev/agysync", "", "Orphaned IDE AgySync Chat", 12,
	)
	// Stranded brain in antigravity-ide/brain/<conv1ID>
	strandedBrainFile := filepath.Join(m1Paths.IdeBrain, conv1ID, "implementation_plan.md")
	_ = os.MkdirAll(filepath.Dir(strandedBrainFile), 0755)
	_ = os.WriteFile(strandedBrainFile, []byte("# Plan"), 0644)

	// 3. CLI conversation in antigravity-cli/conversations for todoagent with default-cli-project
	convCliID := "22222222-2222-2222-2222-222222222222"
	createMockConversationDb(t,
		filepath.Join(m1Paths.CliConversations, convCliID+".db"),
		convCliID, "file://"+m1Home+"/dev/todoagent", DefaultCliProjectID, "CLI TodoAgent Chat", 8,
	)

	// 4. Legacy .pb conversation with Linux path in agyhub_summaries_proto.pb and project_id="outside-of-project"
	convPbID := "33333333-3333-3333-3333-333333333333"
	_ = os.MkdirAll(m1Paths.CoreConversations, 0755)
	_ = os.WriteFile(filepath.Join(m1Paths.CoreConversations, convPbID+".pb"), []byte("encrypted-pb-payload"), 0644)
	pbSummary := BuildCascadeTrajectorySummaryProto(&IdeConversationInfo{
		CascadeID:    convPbID,
		TrajectoryID: convPbID,
		Title:        "Legacy Linux Statools Chat",
		WorkspaceURI: "file://" + m2Home + "/dev/statoolsnet",
		ProjectID:    OutsideOfProjectID,
		StepCount:    45,
		CreatedSec:   1700000000,
		LastModSec:   1700001000,
	})
	_ = os.WriteFile(m1Paths.CoreSummariesPb, EncodeSummaryProtoMap(map[string][]byte{convPbID: pbSummary}), 0644)

	// Machine 2 setup:
	// 1. Conflicting local project UUID "m2-local-duplicate-id" for the same agysync workspace
	m2AgyProj := &ProjectConfig{
		ID:   "m2-local-duplicate-id",
		Name: "agysync",
		ProjectResources: ProjectResources{
			Resources: []ProjectResource{{FolderURI: "file://" + m2Home + "/dev/agysync"}},
		},
		PermissionGrants: map[string]interface{}{"v2Migrated": true, "m2CustomGrant": true},
	}
	if err := writeProjectConfigFile(m2Paths.ProjectsConfigDir, m2AgyProj); err != nil {
		t.Fatal(err)
	}

	// 2. Unmigrated IDE conversation in Machine 2's antigravity-ide/conversations for agysync
	convM2IdeID := "44444444-4444-4444-4444-444444444444"
	createMockConversationDb(t,
		filepath.Join(m2Paths.IdeConversations, convM2IdeID+".db"),
		convM2IdeID, "file://"+m2Home+"/dev/agysync", "", "Machine 2 IDE AgySync Chat", 20,
	)

	// --- Step 1: Machine 1 Consolidates & Uploads to Cloud ---
	stats1, err := ConsolidateAndReconcile(m1Paths, ConsolidateOptions{DryRun: false})
	if err != nil {
		t.Fatalf("Machine 1 ConsolidateAndReconcile failed: %v", err)
	}
	if stats1.HarvestedCliConversations != 1 || stats1.HarvestedIdeBrains != 1 {
		t.Errorf("unexpected M1 harvest stats: %+v", stats1)
	}
	if _, err := os.Stat(filepath.Join(m1Paths.CoreBrain, conv1ID, "implementation_plan.md")); err != nil {
		t.Errorf("expected stranded IDE brain to be harvested into CoreBrain: %v", err)
	}

	// Simulate pushing M1 state to cloudDir
	syncToCloud := func(srcPaths *Paths) {
		home := InferHomeFromPaths(srcPaths)
		if entries, err := os.ReadDir(srcPaths.ProjectsConfigDir); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					_ = CanonicalizeProjectConfigFile(
						filepath.Join(srcPaths.ProjectsConfigDir, e.Name()),
						filepath.Join(cloudDir, "config", "projects", e.Name()),
						home,
					)
				}
			}
		}
		_ = MergeDirectories(srcPaths.CoreConversations, filepath.Join(cloudDir, "antigravity", "conversations"), &SyncStats{}, 0, srcPaths)
		_ = MergeDirectories(srcPaths.CoreBrain, filepath.Join(cloudDir, "antigravity", "brain"), &SyncStats{}, 0, srcPaths)
		if _, err := os.Stat(srcPaths.CoreSummariesPb); err == nil {
			_ = CanonicalizeSummaryProtoFile(srcPaths.CoreSummariesPb, filepath.Join(cloudDir, "antigravity", "agyhub_summaries_proto.pb"), srcPaths)
		}
	}

	pullFromCloud := func(dstPaths *Paths) map[string]bool {
		prefer := make(map[string]bool)
		cloudProjDir := filepath.Join(cloudDir, "config", "projects")
		if entries, err := os.ReadDir(cloudProjDir); err == nil {
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					prefer[strings.TrimSuffix(e.Name(), ".json")] = true
				}
			}
		}
		_ = MergeDirectories(cloudProjDir, dstPaths.ProjectsConfigDir, &SyncStats{}, 0, dstPaths)
		_ = MergeDirectories(filepath.Join(cloudDir, "antigravity", "conversations"), dstPaths.CoreConversations, &SyncStats{}, 0, dstPaths)
		_ = MergeDirectories(filepath.Join(cloudDir, "antigravity", "brain"), dstPaths.CoreBrain, &SyncStats{}, 0, dstPaths)
		cloudPb := filepath.Join(cloudDir, "antigravity", "agyhub_summaries_proto.pb")
		if _, err := os.Stat(cloudPb); err == nil {
			_ = MergeSummaryProtoFiles(cloudPb, dstPaths.CoreSummariesPb)
		}
		return prefer
	}

	syncToCloud(m1Paths)

	// --- Step 2: Machine 2 Pulls from Cloud, Consolidates Local Orphans & Converges Projects, Pushes back ---
	preferM2 := pullFromCloud(m2Paths)
	stats2, err := ConsolidateAndReconcile(m2Paths, ConsolidateOptions{
		DryRun:           false,
		PreferProjectIDs: preferM2,
	})
	if err != nil {
		t.Fatalf("Machine 2 ConsolidateAndReconcile failed: %v", err)
	}
	if stats2.ProjectsConverged != 1 {
		t.Errorf("expected Machine 2 to converge 1 duplicate project (m2-local-duplicate-id -> m1-agysync-proj-id), got %d", stats2.ProjectsConverged)
	}
	if _, err := os.Stat(filepath.Join(m2Paths.ProjectsConfigDir, "m2-local-duplicate-id.json")); !os.IsNotExist(err) {
		t.Errorf("expected duplicate project file m2-local-duplicate-id.json to be removed on Machine 2")
	}
	syncToCloud(m2Paths)

	// --- Step 3: Machine 1 Pulls Machine 2's Delta & Reconciles ---
	preferM1 := pullFromCloud(m1Paths)
	_, err = ConsolidateAndReconcile(m1Paths, ConsolidateOptions{
		DryRun:           false,
		PreferProjectIDs: preferM1,
	})
	if err != nil {
		t.Fatalf("Machine 1 final ConsolidateAndReconcile failed: %v", err)
	}

	// Verify both machines have all 4 conversations in conversation_summaries.db with identical converged project_ids!
	expectedStatoolsProjID := DeterministicProjectID("file:///~/dev/statoolsnet")
	expectedTodoProjID := DeterministicProjectID("file:///~/dev/todoagent")

	expectedProjects := map[string]string{
		conv1ID:     "m1-agysync-proj-id",
		convCliID:   expectedTodoProjID,
		convPbID:    expectedStatoolsProjID,
		convM2IdeID: "m1-agysync-proj-id",
	}

	for machineName, p := range map[string]*Paths{"Machine1": m1Paths, "Machine2": m2Paths} {
		for cid, wantProj := range expectedProjects {
			cmd := exec.Command("sqlite3", p.CoreSummariesDb,
				fmt.Sprintf("SELECT project_id, workspace_uris FROM conversation_summaries WHERE conversation_id = '%s';", cid))
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s failed querying conversation_summaries.db for %s: %v", machineName, cid, err)
			}
			parts := strings.Split(strings.TrimSpace(string(out)), "|")
			if len(parts) < 2 {
				t.Fatalf("%s missing summary row for %s: got %q", machineName, cid, string(out))
			}
			if parts[0] != wantProj {
				t.Errorf("%s conversation %s project_id = %q, want %q", machineName, cid, parts[0], wantProj)
			}

			var uris []string
			if err := json.Unmarshal([]byte(parts[1]), &uris); err != nil || len(uris) == 0 {
				t.Errorf("%s conversation %s invalid workspace_uris JSON: %q", machineName, cid, parts[1])
			} else {
				wantHome := InferHomeFromPaths(p)
				if !strings.HasPrefix(uris[0], "file://"+wantHome+"/") {
					t.Errorf("%s conversation %s workspace_uri %q not localized to %s", machineName, cid, uris[0], wantHome)
				}
			}

			// Also verify conversations_fts header row (chunk_index = -1)
			cmdFts := exec.Command("sqlite3", p.CoreSummariesDb,
				fmt.Sprintf("SELECT project FROM conversations_fts WHERE conversation_id = '%s' AND chunk_index = -1;", cid))
			outFts, err := cmdFts.Output()
			if err != nil || strings.TrimSpace(string(outFts)) != wantProj {
				t.Errorf("%s FTS header row for %s project = %q, want %q", machineName, cid, strings.TrimSpace(string(outFts)), wantProj)
			}
		}
	}
}
