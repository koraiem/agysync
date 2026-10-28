package sync

import (
	"os"
	"path/filepath"
	"runtime"
)

// Paths holds the absolute paths to the history files and directories
type Paths struct {
	BaseDir                string
	ProjectsConfigDir      string
	CliConversations       string
	CliBrain               string
	CliAnnotations         string
	CliSummariesDb         string
	CoreConversations      string
	CoreBrain              string
	CoreAnnotations        string
	CoreSummariesPb        string
	CoreSummariesDb        string
	IdeConversations       string
	IdeBrain               string
	WorkspaceHistory       string
	CliHistoryFile         string
	ConfigDir              string
	MachineIDFile          string
	SettingsFile           string
	TokenFile              string
	IdeStateVscdbFiles     []string
	DesktopAppStorageFiles []string
}

// DetectPaths locates the active Antigravity folders on the current system
func DetectPaths() (*Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	return DetectPathsFromBase(filepath.Join(home, ".gemini"), home), nil
}

// DetectPathsFromBase constructs Paths rooted at a specific .gemini directory and home directory.
func DetectPathsFromBase(baseDir, home string) *Paths {
	configDir := filepath.Join(baseDir, "agysync_config")

	return &Paths{
		BaseDir:                baseDir,
		ProjectsConfigDir:      filepath.Join(baseDir, "config", "projects"),
		CliConversations:       filepath.Join(baseDir, "antigravity-cli", "conversations"),
		CliBrain:               filepath.Join(baseDir, "antigravity-cli", "brain"),
		CliAnnotations:         filepath.Join(baseDir, "antigravity-cli", "annotations"),
		CliSummariesDb:         filepath.Join(baseDir, "antigravity-cli", "conversation_summaries.db"),
		CoreConversations:      filepath.Join(baseDir, "antigravity", "conversations"),
		CoreBrain:              filepath.Join(baseDir, "antigravity", "brain"),
		CoreAnnotations:        filepath.Join(baseDir, "antigravity", "annotations"),
		CoreSummariesPb:        filepath.Join(baseDir, "antigravity", "agyhub_summaries_proto.pb"),
		CoreSummariesDb:        filepath.Join(baseDir, "antigravity", "conversation_summaries.db"),
		IdeConversations:       filepath.Join(baseDir, "antigravity-ide", "conversations"),
		IdeBrain:               filepath.Join(baseDir, "antigravity-ide", "brain"),
		WorkspaceHistory:       filepath.Join(baseDir, "history"),
		CliHistoryFile:         filepath.Join(baseDir, "antigravity-cli", "history.jsonl"),
		ConfigDir:              configDir,
		MachineIDFile:          filepath.Join(configDir, "machine_id"),
		SettingsFile:           filepath.Join(configDir, "settings.json"),
		TokenFile:              filepath.Join(configDir, "oauth_token.json"),
		IdeStateVscdbFiles:     DetectIdeVscdbPathsForHome(home),
		DesktopAppStorageFiles: DetectDesktopAppStoragePathsForHome(home),
	}
}

// DetectDesktopAppStoragePaths locates Antigravity Desktop app_storage.json files on the current system
func DetectDesktopAppStoragePaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return DetectDesktopAppStoragePathsForHome(home)
}

// DetectDesktopAppStoragePathsForHome locates Antigravity Desktop app_storage.json files for a given home directory
func DetectDesktopAppStoragePathsForHome(home string) []string {
	if home == "" {
		return nil
	}

	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			filepath.Join(home, "Library", "Application Support", "Antigravity", "app_storage.json"),
		}
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		candidates = []string{
			filepath.Join(appData, "Antigravity", "app_storage.json"),
		}
	default: // linux and others
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}
		candidates = []string{
			filepath.Join(configHome, "Antigravity", "app_storage.json"),
		}
	}

	var existing []string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			existing = append(existing, c)
		}
	}
	return existing
}

