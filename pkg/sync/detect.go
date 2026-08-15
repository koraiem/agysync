package sync

import (
	"os"
	"path/filepath"
)

// Paths holds the absolute paths to the history files and directories
type Paths struct {
	BaseDir           string
	CliConversations  string
	CliBrain          string
	CoreConversations string
	CoreBrain         string
	IdeConversations  string
	IdeBrain          string
	WorkspaceHistory  string
	CliHistoryFile    string
	ConfigDir         string
	MachineIDFile     string
	SettingsFile      string
	TokenFile         string
	IdeStateVscdbFiles []string
}

// DetectPaths locates the active Antigravity folders on the current system
func DetectPaths() (*Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	baseDir := filepath.Join(home, ".gemini")
	configDir := filepath.Join(baseDir, "agysync_config")

	return &Paths{
		BaseDir:            baseDir,
		CliConversations:   filepath.Join(baseDir, "antigravity-cli", "conversations"),
		CliBrain:           filepath.Join(baseDir, "antigravity-cli", "brain"),
		CoreConversations:  filepath.Join(baseDir, "antigravity", "conversations"),
		CoreBrain:          filepath.Join(baseDir, "antigravity", "brain"),
		IdeConversations:   filepath.Join(baseDir, "antigravity-ide", "conversations"),
		IdeBrain:           filepath.Join(baseDir, "antigravity-ide", "brain"),
		WorkspaceHistory:   filepath.Join(baseDir, "history"),
		CliHistoryFile:     filepath.Join(baseDir, "antigravity-cli", "history.jsonl"),
		ConfigDir:          configDir,
		MachineIDFile:      filepath.Join(configDir, "machine_id"),
		SettingsFile:       filepath.Join(configDir, "settings.json"),
		TokenFile:          filepath.Join(configDir, "oauth_token.json"),
		IdeStateVscdbFiles: DetectIdeVscdbPaths(),
	}, nil
}
