package sync

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// MachineConfig defines the identity of a device node
type MachineConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // desktop, laptop, server
}

// SyncMetadata represents the global state stored in Google Drive AppData
type SyncMetadata struct {
	RegisteredNodes []MachineConfig `json:"registered_nodes"`
	MaxAllowedNodes int             `json:"max_allowed_nodes"`
	LicenseKey      string          `json:"license_key"`
}

// LoadOrGenerateMachineID retrieves the local hardware UUID or generates a fallback ID
func LoadOrGenerateMachineID(paths *Paths) (string, error) {
	if err := os.MkdirAll(paths.ConfigDir, 0755); err != nil {
		return "", err
	}

	if _, err := os.Stat(paths.MachineIDFile); err == nil {
		data, err := os.ReadFile(paths.MachineIDFile)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}

	// Generate a secure random 16-byte hex token as a machine ID
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		return "", err
	}
	id := hex.EncodeToString(bytes)

	if err := os.WriteFile(paths.MachineIDFile, []byte(id), 0600); err != nil {
		return "", err
	}
	return id, nil
}

// LoadLocalSettings retrieves the local machine name and type, creating defaults if missing
func LoadLocalSettings(paths *Paths) (*MachineConfig, error) {
	id, err := LoadOrGenerateMachineID(paths)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(paths.SettingsFile); err == nil {
		data, err := os.ReadFile(paths.SettingsFile)
		if err == nil {
			var config MachineConfig
			if err := json.Unmarshal(data, &config); err == nil {
				config.ID = id // Keep ID in sync with the machine_id file
				return &config, nil
			}
		}
	}

	// Defaults based on hostname/OS
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown-host"
	}

	name := fmt.Sprintf("%s-%s", hostname, runtime.GOOS)
	mType := "desktop"
	if runtime.GOOS == "darwin" {
		mType = "laptop" // Decent default for macs
	}

	config := &MachineConfig{
		ID:   id,
		Name: name,
		Type: mType,
	}

	// Save defaults
	data, err := json.MarshalIndent(config, "", "  ")
	if err == nil {
		_ = os.WriteFile(paths.SettingsFile, data, 0644)
	}

	return config, nil
}

// RegisterAndValidateNode validates if the machine can sync, enforcing free/paid tier limits
func RegisterAndValidateNode(meta *SyncMetadata, local *MachineConfig) (bool, error) {
	// Set default limit if uninitialized
	if meta.MaxAllowedNodes <= 0 {
		meta.MaxAllowedNodes = 2
	}

	// Check if this machine is already registered
	for _, node := range meta.RegisteredNodes {
		if node.ID == local.ID {
			// Update properties (name, type) if they changed
			node.Name = local.Name
			node.Type = local.Type
			return true, nil
		}
	}

	// Upgrade license checks can be added here
	// E.g., if meta.LicenseKey is valid, increase MaxAllowedNodes.
	if meta.LicenseKey != "" {
		// Mock validation: any 16+ char license key raises the limit to 5 nodes.
		if len(meta.LicenseKey) >= 16 {
			meta.MaxAllowedNodes = 5
		}
	}

	// If not registered, verify node limit
	if len(meta.RegisteredNodes) >= meta.MaxAllowedNodes {
		return false, fmt.Errorf("node limit reached (%d of %d machines). Please purchase a premium license ($5/additional machine) to connect more machines", len(meta.RegisteredNodes), meta.MaxAllowedNodes)
	}

	// Register this machine
	meta.RegisteredNodes = append(meta.RegisteredNodes, *local)
	fmt.Printf("Registered new machine node: %s (%s)\n", local.Name, local.Type)
	return true, nil
}
