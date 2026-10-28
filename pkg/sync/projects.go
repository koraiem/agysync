package sync

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	OutsideOfProjectID  = "outside-of-project"
	DefaultCliProjectID = "default-cli-project"
)

var agysyncProjectNamespace = uuid.MustParse("6ba7b811-9dad-11d1-80b4-00c04fd430c8")

// ProjectGitFolder represents the gitFolder sub-object in a project resource
type ProjectGitFolder struct {
	FolderURI     string `json:"folderUri,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	AllowWrite    bool   `json:"allowWrite,omitempty"`
}

// ProjectResource represents an entry in projectResources.resources
type ProjectResource struct {
	FolderURI string            `json:"folderUri,omitempty"`
	GitFolder *ProjectGitFolder `json:"gitFolder,omitempty"`
}

// ProjectResources wraps the list of project resources
type ProjectResources struct {
	Resources []ProjectResource `json:"resources"`
}

// ProjectConfig represents ~/.gemini/config/projects/<project_id>.json
type ProjectConfig struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	ProjectResources ProjectResources       `json:"projectResources"`
	PermissionGrants map[string]interface{} `json:"permissionGrants,omitempty"`
	Settings         map[string]interface{} `json:"settings,omitempty"`
	IsWorkspaceOnly  bool                   `json:"isWorkspaceOnly"`
}

// PrimaryFolderURI returns the first folderUri defined on the project
func (p *ProjectConfig) PrimaryFolderURI() string {
	for _, r := range p.ProjectResources.Resources {
		if r.FolderURI != "" {
			return r.FolderURI
		}
		if r.GitFolder != nil && r.GitFolder.FolderURI != "" {
			return r.GitFolder.FolderURI
		}
	}
	return ""
}

// TransformFolderURIs applies fn to every folderUri in the project config
func (p *ProjectConfig) TransformFolderURIs(fn func(string) string) bool {
	changed := false
	for i := range p.ProjectResources.Resources {
		r := &p.ProjectResources.Resources[i]
		if r.FolderURI != "" {
			updated := fn(r.FolderURI)
			if updated != r.FolderURI {
				r.FolderURI = updated
				changed = true
			}
		}
		if r.GitFolder != nil && r.GitFolder.FolderURI != "" {
			updated := fn(r.GitFolder.FolderURI)
			if updated != r.GitFolder.FolderURI {
				r.GitFolder.FolderURI = updated
				changed = true
			}
		}
	}
	return changed
}

// ProjectRegistry manages the unified project definitions in ~/.gemini/config/projects/
type ProjectRegistry struct {
	ByID         map[string]*ProjectConfig
	ByNormPath   map[string]*ProjectConfig
	ByName       map[string][]*ProjectConfig
	AliasToNewID map[string]string
	NewOrderIDs  []string
	CreatedIDs   []string
	UpdatedIDs   []string
	RemovedFiles []string
}

// UnescapeWorkspacePath decodes percent-encoded characters (such as %20) and normalizes slashes
func UnescapeWorkspacePath(raw string) string {
	s := filepath.ToSlash(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "file://")
	if unescaped, err := url.PathUnescape(s); err == nil {
		s = unescaped
	} else {
		s = strings.ReplaceAll(s, "%20", " ")
	}
	if len(s) > 1 {
		s = strings.TrimRight(s, "/")
	}
	return s
}

// NormalizeWorkspaceURI returns a clean "file:///..." URI with normalized slashes
func NormalizeWorkspaceURI(raw string) string {
	p := UnescapeWorkspacePath(raw)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") && !strings.Contains(p, ":/") {
		p = "/" + p
	}
	return "file://" + p
}

// CanonicalizeWorkspaceURI converts a local workspace path or URI into canonical "file:///~/..." form
func CanonicalizeWorkspaceURI(raw, localHome string) string {
	p := UnescapeWorkspacePath(raw)
	if p == "" {
		return ""
	}
	homeSlash := filepath.ToSlash(strings.TrimRight(localHome, "/"))
	if homeSlash != "" {
		if p == homeSlash {
			p = "~"
		} else if strings.HasPrefix(p, homeSlash+"/") {
			p = "~" + strings.TrimPrefix(p, homeSlash)
		}
	}
	// Also canonicalize foreign user home prefixes (/Users/<user>/... or /home/<user>/...)
	if !strings.HasPrefix(p, "~") {
		if remoteHome, remoteRel := splitHomePrefix(p); remoteHome != "" {
			if remoteRel == "" {
				p = "~"
			} else {
				p = "~" + remoteRel
			}
		}
	}
	return "file://" + p
}

// DeterministicProjectID computes a deterministic UUIDv5 for a canonical workspace URI (e.g. "file://~/dev/foo")
func DeterministicProjectID(canonicalURI string) string {
	norm := strings.TrimRight(strings.TrimSpace(canonicalURI), "/")
	norm = strings.Replace(norm, "file:///~/", "file://~/", 1)
	return uuid.NewSHA1(agysyncProjectNamespace, []byte(norm)).String()
}

// IsGenericContainerPath returns true for non-project root/container directories ($HOME, ~/dev, ~/Downloads, /, /tmp, etc.)
func IsGenericContainerPath(rawPath, localHome string) bool {
	p := UnescapeWorkspacePath(rawPath)
	if p == "" || p == "/" || p == "~" {
		return true
	}

	low := strings.ToLower(p)
	switch low {
	case "/tmp", "/var/tmp", "/private/tmp", "/users", "/home", "/var", "/opt", "/etc":
		return true
	}

	homeSlash := filepath.ToSlash(strings.TrimRight(localHome, "/"))
	rel := ""
	if homeSlash != "" && (p == homeSlash || strings.HasPrefix(p, homeSlash+"/")) {
		rel = strings.TrimPrefix(p, homeSlash)
	} else if strings.HasPrefix(p, "~/") {
		rel = strings.TrimPrefix(p, "~")
	} else if remoteHome, remoteRel := splitHomePrefix(p); remoteHome != "" {
		rel = remoteRel
	} else {
		return false
	}

	relClean := strings.ToLower(strings.Trim(rel, "/"))
	if relClean == "" {
		return true
	}
	switch relClean {
	case "dev", "projects", "workspace", "workspaces", "repos", "src", "code", "llm",
		"downloads", "desktop", "documents", "pictures", "music", "movies", "public", "library", ".gemini":
		return true
	}
	return false
}

// ExtractWorktreeProjectName checks if rawPath is an Antigravity git worktree
// (e.g. ~/.gemini/antigravity/worktrees/<project>/<worktree_name>) and returns <project>.
func ExtractWorktreeProjectName(rawPath string) string {
	p := UnescapeWorkspacePath(rawPath)
	marker := "/.gemini/antigravity/worktrees/"
	idx := strings.Index(p, marker)
	if idx == -1 {
		return ""
	}
	rest := strings.TrimPrefix(p[idx+len(marker):], "/")
	parts := strings.Split(rest, "/")
	if len(parts) >= 1 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

func splitHomePrefix(cleanSlashPath string) (string, string) {
	if cleanSlashPath == "~" {
		return "~", ""
	}
	if strings.HasPrefix(cleanSlashPath, "~/") {
		return "~", strings.TrimPrefix(cleanSlashPath, "~")
	}
	trimmed := strings.TrimPrefix(cleanSlashPath, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) >= 2 && (parts[0] == "home" || parts[0] == "Users") {
		home := "/" + parts[0] + "/" + parts[1]
		return home, strings.TrimPrefix(cleanSlashPath, home)
	}
	if strings.HasPrefix(cleanSlashPath, "C:/Users/") || strings.HasPrefix(cleanSlashPath, "c:/Users/") {
		wParts := strings.Split(cleanSlashPath, "/")
		if len(wParts) >= 3 {
			home := strings.Join(wParts[:3], "/")
			return home, strings.TrimPrefix(cleanSlashPath, home)
		}
	}
	// Support nested home roots (e.g. /data/home/<user>/... or TempDir/Users/<user>/...)
	for i := 1; i+1 < len(parts); i++ {
		if parts[i] == "home" || parts[i] == "Users" {
			home := "/" + strings.Join(parts[:i+2], "/")
			return home, strings.TrimPrefix(cleanSlashPath, home)
		}
	}
	return "", ""
}

// LoadLocalProjectPaths retrieves all known local project folder paths from both
// ~/.gemini/config/projects/*.json and ~/.gemini/projects.json in a deterministic order.
func LoadLocalProjectPaths(paths *Paths) []string {
	seen := make(map[string]bool)
	var list []string

	localHome := InferHomeFromPaths(paths)

	addPath := func(raw string) {
		p := UnescapeWorkspacePath(raw)
		if p == "" || p == "/" {
			return
		}
		if strings.HasPrefix(p, "~/") && localHome != "" {
			p = filepath.ToSlash(localHome) + strings.TrimPrefix(p, "~")
		}
		if !seen[p] {
			seen[p] = true
			list = append(list, p)
		}
	}

	// 1. Read unified project configs from ~/.gemini/config/projects/*.json
	if paths != nil && paths.ProjectsConfigDir != "" {
		if entries, err := os.ReadDir(paths.ProjectsConfigDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(paths.ProjectsConfigDir, entry.Name()))
				if err != nil {
					continue
				}
				var cfg ProjectConfig
				if err := json.Unmarshal(data, &cfg); err == nil {
					if uri := cfg.PrimaryFolderURI(); uri != "" {
						addPath(uri)
					}
				}
			}
		}
	}

	// 2. Read secondary ~/.gemini/projects.json
	if paths != nil && paths.BaseDir != "" {
		var projects struct {
			Projects map[string]string `json:"projects"`
		}
		if data, err := os.ReadFile(filepath.Join(paths.BaseDir, "projects.json")); err == nil {
			if err := json.Unmarshal(data, &projects); err == nil {
				for k := range projects.Projects {
					addPath(k)
				}
			}
		}
	}

	sort.Strings(list)
	return list
}

// InferHomeFromPaths returns the home directory corresponding to paths.BaseDir (or os.UserHomeDir fallback)
func InferHomeFromPaths(paths *Paths) string {
	if paths != nil && paths.BaseDir != "" {
		cleanBase := filepath.ToSlash(filepath.Clean(paths.BaseDir))
		if filepath.Base(cleanBase) == ".gemini" {
			parent := filepath.Dir(cleanBase)
			if parent != "" && parent != "." && parent != "/" {
				return filepath.ToSlash(parent)
			}
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.ToSlash(home)
}

// LocalizeWorkspacePath translates a remote or canonical path to match the local machine's workspace path
// without colliding across similarly named nested folders (e.g. ~/dev/routemap vs ~/dev/other/routemap).
func LocalizeWorkspacePath(remotePath string, localProjects []string, localHome string) string {
	if strings.TrimSpace(remotePath) == "" {
		return ""
	}
	clean := UnescapeWorkspacePath(remotePath)
	if clean == "" {
		return ""
	}

	localHomeSlash := filepath.ToSlash(strings.TrimRight(localHome, "/"))

	// Prepare normalized local projects
	var normProjects []string
	projectSet := make(map[string]bool)
	for _, lp := range localProjects {
		np := UnescapeWorkspacePath(lp)
		if np != "" && np != "/" {
			if !projectSet[np] {
				projectSet[np] = true
				normProjects = append(normProjects, np)
			}
		}
	}
	sort.Strings(normProjects)

	// 1. If the path already starts with localHome, it is already localized to this machine!
	if localHomeSlash != "" && (clean == localHomeSlash || strings.HasPrefix(clean, localHomeSlash+"/")) {
		return clean
	}

	// 2. Extract home-relative path if present (~, /Users/<user>, /home/<user>, C:/Users/<user>)
	remoteHome, remoteRel := splitHomePrefix(clean)

	// 3. Special case: Antigravity worktree paths (~/.gemini/antigravity/worktrees/...)
	if strings.Contains(clean, "/.gemini/antigravity/worktrees/") && remoteHome != "" && localHomeSlash != "" {
		return localHomeSlash + remoteRel
	}

	// 4. Tier 1: Exact home-relative match against known local projects or existing local disk path
	var homeMapped string
	if remoteHome != "" && localHomeSlash != "" {
		if remoteRel == "" {
			return localHomeSlash
		}
		homeMapped = localHomeSlash + remoteRel
		if projectSet[homeMapped] {
			return homeMapped
		}
		if _, err := os.Stat(homeMapped); err == nil {
			return homeMapped
		}
	}

	// 5. Tier 2: Multi-segment suffix match (last 2 segments, e.g. "other/routemap")
	cleanParts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	if len(cleanParts) >= 2 {
		suffix2 := cleanParts[len(cleanParts)-2] + "/" + cleanParts[len(cleanParts)-1]
		var suffixMatches []string
		for _, lp := range normProjects {
			if strings.HasSuffix(lp, "/"+suffix2) {
				suffixMatches = append(suffixMatches, lp)
			}
		}
		if len(suffixMatches) == 1 {
			return suffixMatches[0]
		}
	}

	// 6. Tier 3: Unambiguous single-segment basename match, ONLY when the path is not a
	// distinct nested path under the same relative parent structure (e.g. ~/dev/other/routemap vs ~/dev/routemap).
	remoteBase := filepath.Base(clean)
	var baseMatches []string
	for _, lp := range normProjects {
		if filepath.Base(lp) == remoteBase {
			baseMatches = append(baseMatches, lp)
		}
	}
	if len(baseMatches) == 1 {
		match := baseMatches[0]
		// Guard against collapsing ~/dev/other/routemap into ~/dev/routemap:
		// If both homeMapped and match live under localHome/dev/ (or same top-level home subfolder)
		// but at different relative depths, preserve homeMapped!
		if homeMapped != "" && localHomeSlash != "" {
			relMapped := strings.TrimPrefix(homeMapped, localHomeSlash+"/")
			relMatch := strings.TrimPrefix(match, localHomeSlash+"/")
			mappedSegs := strings.Split(relMapped, "/")
			matchSegs := strings.Split(relMatch, "/")
			if len(mappedSegs) > 0 && len(matchSegs) > 0 && mappedSegs[0] == matchSegs[0] && relMapped != relMatch {
				return homeMapped
			}
		}
		return match
	}

	// 7. Tier 4: Fallback to home-prefix replacement
	if homeMapped != "" {
		return homeMapped
	}

	return clean
}

// LoadProjectRegistry loads all project definitions from ~/.gemini/config/projects/*.json,
// localizes their folder URIs, and deduplicates any projects that resolve to the same workspace path.
// If preferIDs contains a project ID (e.g. from Google Drive), that ID wins during deduplication.
func LoadProjectRegistry(paths *Paths, preferIDs map[string]bool, dryRun bool) (*ProjectRegistry, error) {
	reg := &ProjectRegistry{
		ByID:         make(map[string]*ProjectConfig),
		ByNormPath:   make(map[string]*ProjectConfig),
		ByName:       make(map[string][]*ProjectConfig),
		AliasToNewID: make(map[string]string),
	}

	if paths == nil || paths.ProjectsConfigDir == "" {
		return reg, nil
	}

	localHome := InferHomeFromPaths(paths)
	localProjects := LoadLocalProjectPaths(paths)

	entries, err := os.ReadDir(paths.ProjectsConfigDir)
	if err != nil && !os.IsNotExist(err) {
		return reg, err
	}

	var loaded []*ProjectConfig
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		fullPath := filepath.Join(paths.ProjectsConfigDir, entry.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		var cfg ProjectConfig
		if err := json.Unmarshal(data, &cfg); err != nil || cfg.ID == "" {
			continue
		}

		// Localize any folderUri inside the project config
		changed := cfg.TransformFolderURIs(func(rawURI string) string {
			locPath := LocalizeWorkspacePath(rawURI, localProjects, localHome)
			return NormalizeWorkspaceURI(locPath)
		})
		if changed {
			reg.UpdatedIDs = appendUniqueString(reg.UpdatedIDs, cfg.ID)
			if !dryRun {
				_ = writeProjectConfigFile(paths.ProjectsConfigDir, &cfg)
			}
		}
		loaded = append(loaded, &cfg)
	}

	// Sort deterministically: preferred (remote Drive) IDs first, then lexicographically by ID
	sort.Slice(loaded, func(i, j int) bool {
		pi := preferIDs != nil && preferIDs[loaded[i].ID]
		pj := preferIDs != nil && preferIDs[loaded[j].ID]
		if pi != pj {
			return pi
		}
		return loaded[i].ID < loaded[j].ID
	})

	for _, cfg := range loaded {
		if cfg.ID == OutsideOfProjectID || cfg.ID == DefaultCliProjectID {
			reg.ByID[cfg.ID] = cfg
			continue
		}

		primaryURI := cfg.PrimaryFolderURI()
		normPath := UnescapeWorkspacePath(primaryURI)
		if normPath != "" {
			if existing, exists := reg.ByNormPath[normPath]; exists && existing.ID != cfg.ID {
				// Duplicate project for the same workspace path! Converge onto `existing` (preferred/remote ID).
				mergeProjectConfigs(existing, cfg)
				reg.AliasToNewID[cfg.ID] = existing.ID
				reg.UpdatedIDs = appendUniqueString(reg.UpdatedIDs, existing.ID)
				dupFile := filepath.Join(paths.ProjectsConfigDir, cfg.ID+".json")
				reg.RemovedFiles = appendUniqueString(reg.RemovedFiles, dupFile)
				if !dryRun {
					_ = writeProjectConfigFile(paths.ProjectsConfigDir, existing)
					_ = os.Remove(dupFile)
				}
				continue
			}
			reg.ByNormPath[normPath] = cfg
		}

		reg.ByID[cfg.ID] = cfg
		reg.ByName[strings.ToLower(cfg.Name)] = append(reg.ByName[strings.ToLower(cfg.Name)], cfg)
	}

	// Ensure built-in pseudo-projects exist in registry
	ensureBuiltinProject(reg, paths.ProjectsConfigDir, OutsideOfProjectID, OutsideOfProjectID, dryRun)
	ensureBuiltinProject(reg, paths.ProjectsConfigDir, DefaultCliProjectID, DefaultCliProjectID, dryRun)

	return reg, nil
}

func ensureBuiltinProject(reg *ProjectRegistry, configDir, id, name string, dryRun bool) {
	if _, ok := reg.ByID[id]; ok {
		return
	}
	cfg := &ProjectConfig{
		ID:   id,
		Name: name,
		ProjectResources: ProjectResources{
			Resources: []ProjectResource{},
		},
		PermissionGrants: map[string]interface{}{
			"v2Migrated": true,
		},
		Settings:        map[string]interface{}{},
		IsWorkspaceOnly: false,
	}
	reg.ByID[id] = cfg
	if !dryRun && configDir != "" {
		_ = writeProjectConfigFile(configDir, cfg)
	}
}

func mergeProjectConfigs(dst, src *ProjectConfig) {
	if dst.PermissionGrants == nil {
		dst.PermissionGrants = make(map[string]interface{})
	}
	for k, v := range src.PermissionGrants {
		if _, exists := dst.PermissionGrants[k]; !exists {
			dst.PermissionGrants[k] = v
		}
	}
	if dst.Settings == nil {
		dst.Settings = make(map[string]interface{})
	}
	for k, v := range src.Settings {
		if _, exists := dst.Settings[k]; !exists {
			dst.Settings[k] = v
		}
	}
}

// ResolveOrCreateProjectForWorkspace finds or creates the ProjectConfig for a localized workspace path.
// Returns (projectID, createdNew).
func (reg *ProjectRegistry) ResolveOrCreateProjectForWorkspace(
	localizedPath string,
	currentProjectID string,
	isCliSource bool,
	paths *Paths,
	dryRun bool,
) (string, bool) {
	// 1. Remap currentProjectID if it was merged as a duplicate
	if mapped, ok := reg.AliasToNewID[currentProjectID]; ok {
		currentProjectID = mapped
	}

	localHome := InferHomeFromPaths(paths)
	normPath := UnescapeWorkspacePath(localizedPath)

	// 2. Check if this is an Antigravity git worktree path (~/.gemini/antigravity/worktrees/<project>/<branch>)
	if wtProj := ExtractWorktreeProjectName(normPath); wtProj != "" {
		if matches := reg.ByName[strings.ToLower(wtProj)]; len(matches) == 1 {
			return matches[0].ID, false
		}
		for _, cfg := range reg.ByNormPath {
			if strings.EqualFold(filepath.Base(UnescapeWorkspacePath(cfg.PrimaryFolderURI())), wtProj) {
				return cfg.ID, false
			}
		}
	}

	// 3. Check if normPath is a real project workspace (not empty and not a generic container folder)
	isRealWorkspace := normPath != "" && !IsGenericContainerPath(normPath, localHome)

	if isRealWorkspace {
		// Find or create the project corresponding to normPath
		wsCfg, exists := reg.ByNormPath[normPath]
		created := false
		if !exists {
			canonicalURI := CanonicalizeWorkspaceURI(normPath, localHome)
			projID := DeterministicProjectID(canonicalURI)
			if existingByID, idExists := reg.ByID[projID]; idExists {
				wsCfg = existingByID
			} else {
				projName := chooseDisambiguatedProjectName(reg, normPath)
				wsCfg = &ProjectConfig{
					ID:   projID,
					Name: projName,
					ProjectResources: ProjectResources{
						Resources: []ProjectResource{
							{
								FolderURI: NormalizeWorkspaceURI(normPath),
							},
						},
					},
					PermissionGrants: map[string]interface{}{
						"v2Migrated": true,
					},
					Settings:        map[string]interface{}{},
					IsWorkspaceOnly: false,
				}
				reg.ByID[projID] = wsCfg
				reg.ByNormPath[normPath] = wsCfg
				reg.ByName[strings.ToLower(projName)] = append(reg.ByName[strings.ToLower(projName)], wsCfg)
				reg.CreatedIDs = appendUniqueString(reg.CreatedIDs, projID)
				reg.NewOrderIDs = appendUniqueString(reg.NewOrderIDs, projID)
				created = true
				if !dryRun && paths != nil && paths.ProjectsConfigDir != "" {
					_ = writeProjectConfigFile(paths.ProjectsConfigDir, wsCfg)
				}
			}
		}

		// Should we join this conversation into wsCfg.ID?
		// Join if:
		// - currentProjectID is empty ("")
		// - currentProjectID is "outside-of-project" or "default-cli-project"
		// - currentProjectID is a dangling foreign UUID not present in reg.ByID
		if currentProjectID == "" ||
			currentProjectID == OutsideOfProjectID ||
			currentProjectID == DefaultCliProjectID ||
			reg.ByID[currentProjectID] == nil {
			reg.NewOrderIDs = appendUniqueString(reg.NewOrderIDs, wsCfg.ID)
			return wsCfg.ID, created
		}

		// Valid existing project ID is preserved
		return currentProjectID, created
	}

	// 4. Generic container path or empty workspace
	if currentProjectID == DefaultCliProjectID || isCliSource {
		return DefaultCliProjectID, false
	}
	if currentProjectID != "" && reg.ByID[currentProjectID] != nil {
		return currentProjectID, false
	}
	return OutsideOfProjectID, false
}

func chooseDisambiguatedProjectName(reg *ProjectRegistry, normPath string) string {
	base := filepath.Base(normPath)
	if len(reg.ByName[strings.ToLower(base)]) == 0 {
		return base
	}
	parent := filepath.Base(filepath.Dir(normPath))
	if parent != "" && parent != "." && parent != "/" {
		candidate := parent + "/" + base
		if len(reg.ByName[strings.ToLower(candidate)]) == 0 {
			return candidate
		}
	}
	return base
}

func writeProjectConfigFile(configDir string, cfg *ProjectConfig) error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(configDir, cfg.ID+".json")
	return os.WriteFile(target, append(data, '\n'), 0644)
}

// CanonicalizeProjectConfigFile writes a canonicalized copy (with file:///~/... folderUris) to dstFile
func CanonicalizeProjectConfigFile(srcFile, dstFile, localHome string) error {
	data, err := os.ReadFile(srcFile)
	if err != nil {
		return err
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return CopyFileOverwrite(srcFile, dstFile)
	}
	cfg.TransformFolderURIs(func(rawURI string) string {
		return CanonicalizeWorkspaceURI(rawURI, localHome)
	})
	out, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstFile), 0755); err != nil {
		return err
	}
	return os.WriteFile(dstFile, append(out, '\n'), 0644)
}

// UpdateProjectsOrder updates the sidebar projectsOrder list in both Antigravity Desktop
// (app_storage.json) and Antigravity IDE v2.6+ (state.vscdb -> google.google-antigravity).
func UpdateProjectsOrder(paths *Paths, reg *ProjectRegistry, dryRun bool) error {
	if paths == nil || reg == nil || dryRun {
		return nil
	}

	// Collect all real (non-builtin) project IDs that should be present in projectsOrder
	var allProjectIDs []string
	for id := range reg.ByID {
		if id == OutsideOfProjectID || id == DefaultCliProjectID {
			continue
		}
		allProjectIDs = append(allProjectIDs, id)
	}
	sort.Strings(allProjectIDs)

	// 1. Update Desktop app_storage.json files
	for _, appStoragePath := range paths.DesktopAppStorageFiles {
		_ = updateDesktopAppStorageProjectsOrder(appStoragePath, allProjectIDs, reg.AliasToNewID)
	}

	// 2. Update IDE state.vscdb files (key: google.google-antigravity)
	for _, vscdbPath := range paths.IdeStateVscdbFiles {
		_ = updateIdeVscdbProjectsOrder(vscdbPath, allProjectIDs, reg.AliasToNewID)
	}

	return nil
}

func mergeProjectsOrderList(currentOrder []string, allProjectIDs []string, aliasMap map[string]string) ([]string, bool) {
	seen := make(map[string]bool)
	var updated []string
	changed := false

	for _, id := range currentOrder {
		mapped := id
		if newID, ok := aliasMap[id]; ok {
			mapped = newID
			changed = true
		}
		if !seen[mapped] {
			seen[mapped] = true
			updated = append(updated, mapped)
		} else {
			changed = true
		}
	}

	for _, id := range allProjectIDs {
		if !seen[id] {
			seen[id] = true
			updated = append(updated, id)
			changed = true
		}
	}

	return updated, changed
}

func updateDesktopAppStorageProjectsOrder(appStoragePath string, allProjectIDs []string, aliasMap map[string]string) error {
	data, err := os.ReadFile(appStoragePath)
	if err != nil {
		return err
	}

	var topMap map[string]interface{}
	if err := json.Unmarshal(data, &topMap); err != nil {
		return err
	}

	var currentOrder []string
	if rawOrder, ok := topMap["projectsOrder"].(string); ok && rawOrder != "" {
		_ = json.Unmarshal([]byte(rawOrder), &currentOrder)
	}

	merged, changed := mergeProjectsOrderList(currentOrder, allProjectIDs, aliasMap)
	if !changed {
		return nil
	}

	orderJSON, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	topMap["projectsOrder"] = string(orderJSON)

	out, err := json.MarshalIndent(topMap, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(appStoragePath, out, 0644)
}

func updateIdeVscdbProjectsOrder(vscdbPath string, allProjectIDs []string, aliasMap map[string]string) error {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	cmd := exec.Command("sqlite3", vscdbPath, "SELECT value FROM ItemTable WHERE key = 'google.google-antigravity' LIMIT 1;")
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	valStr := strings.TrimSpace(string(out))
	if valStr == "" {
		return nil
	}

	var rootMap map[string]interface{}
	if err := json.Unmarshal([]byte(valStr), &rootMap); err != nil {
		return err
	}

	storageObj, ok := rootMap["antigravity-storage"].(map[string]interface{})
	if !ok {
		return nil
	}

	var currentOrder []string
	if rawOrder, ok := storageObj["projectsOrder"].(string); ok && rawOrder != "" {
		_ = json.Unmarshal([]byte(rawOrder), &currentOrder)
	}

	merged, changed := mergeProjectsOrderList(currentOrder, allProjectIDs, aliasMap)
	if !changed {
		return nil
	}

	orderJSON, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	storageObj["projectsOrder"] = string(orderJSON)
	rootMap["antigravity-storage"] = storageObj

	updatedBytes, err := json.Marshal(rootMap)
	if err != nil {
		return err
	}

	escaped := strings.ReplaceAll(string(updatedBytes), "'", "''")
	sql := fmt.Sprintf("PRAGMA busy_timeout = 5000; UPDATE ItemTable SET value = '%s' WHERE key = 'google.google-antigravity';", escaped)
	return exec.Command("sqlite3", vscdbPath, sql).Run()
}

func appendUniqueString(slice []string, val string) []string {
	for _, item := range slice {
		if item == val {
			return slice
		}
	}
	return append(slice, val)
}
