package drive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agysync/pkg/sync"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	googleDrive "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

var (
	// DefaultClientID and DefaultClientSecret can be injected at build-time:
	//   go build -ldflags="-X 'agysync/pkg/drive.DefaultClientID=...' -X 'agysync/pkg/drive.DefaultClientSecret=...'"
	DefaultClientID     = ""
	DefaultClientSecret = ""

	// ErrCredentialsNotConfigured indicates that no OAuth client_id / client_secret were found.
	ErrCredentialsNotConfigured = errors.New("google oauth client credentials are not configured")
)

// DriveService wraps the googleDrive.Service and settings
type DriveService struct {
	Srv *googleDrive.Service
}

func sanitizeCredential(val string) string {
	val = strings.TrimSpace(val)
	val = strings.Trim(val, "\"'`")
	return strings.TrimSpace(val)
}

func maskSecret(val string) string {
	if len(val) <= 8 {
		return "****"
	}
	return val[:6] + "..." + val[len(val)-4:]
}

func isValidCredential(val string) bool {
	return val != "" && !strings.Contains(strings.ToLower(val), "dummy") && !strings.Contains(strings.ToLower(val), "your_client_")
}

// extractFromGoogleCredentialsJSON parses a standard Google Cloud Console client_secret_*.json file
func extractFromGoogleCredentialsJSON(filePath string) (string, string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", "", err
	}
	var raw struct {
		Installed *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
		Web *struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", "", err
	}
	if raw.Installed != nil && isValidCredential(raw.Installed.ClientID) && isValidCredential(raw.Installed.ClientSecret) {
		return sanitizeCredential(raw.Installed.ClientID), sanitizeCredential(raw.Installed.ClientSecret), nil
	}
	if raw.Web != nil && isValidCredential(raw.Web.ClientID) && isValidCredential(raw.Web.ClientSecret) {
		return sanitizeCredential(raw.Web.ClientID), sanitizeCredential(raw.Web.ClientSecret), nil
	}
	return "", "", fmt.Errorf("no valid installed/web client_id and client_secret found in %s", filePath)
}

// findCredentialsJSONInConfigDir checks ~/.gemini/agysync_config/ for a downloaded client_secret*.json file
func findCredentialsJSONInConfigDir(configDir string) (string, string, string) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return "", "", ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "client_secret") && strings.HasSuffix(name, ".json") {
			fullPath := filepath.Join(configDir, name)
			if id, secret, err := extractFromGoogleCredentialsJSON(fullPath); err == nil {
				return id, secret, fullPath
			}
		}
	}
	return "", "", ""
}

// GetDriveService initializes the Drive API client
func GetDriveService(paths *sync.Paths) (*DriveService, error) {
	localConfig, _ := sync.LoadLocalSettings(paths)

	envID := sanitizeCredential(os.Getenv("AGYSYNC_CLIENT_ID"))
	envSecret := sanitizeCredential(os.Getenv("AGYSYNC_CLIENT_SECRET"))

	var cfgID, cfgSecret string
	if localConfig != nil {
		cfgID = sanitizeCredential(localConfig.ClientID)
		cfgSecret = sanitizeCredential(localConfig.ClientSecret)
	}

	jsonID, jsonSecret, jsonPath := findCredentialsJSONInConfigDir(paths.ConfigDir)
	defID := sanitizeCredential(DefaultClientID)
	defSecret := sanitizeCredential(DefaultClientSecret)

	// Precedence:
	// 1. Environment variables (explicit override in current shell)
	// 2. client_secret*.json in ~/.gemini/agysync_config/
	// 3. settings.json (~/.gemini/agysync_config/settings.json)
	// 4. Build-time defaults (DefaultClientID / DefaultClientSecret)
	var clientID, clientSecret, idSource, secretSource string

	switch {
	case isValidCredential(envID):
		clientID = envID
		idSource = "env:AGYSYNC_CLIENT_ID"
	case isValidCredential(jsonID):
		clientID = jsonID
		idSource = jsonPath
	case isValidCredential(cfgID):
		clientID = cfgID
		idSource = paths.SettingsFile
	case isValidCredential(defID):
		clientID = defID
		idSource = "built-in default"
	}

	switch {
	case isValidCredential(envSecret):
		clientSecret = envSecret
		secretSource = "env:AGYSYNC_CLIENT_SECRET"
	case isValidCredential(jsonSecret):
		clientSecret = jsonSecret
		secretSource = jsonPath
	case isValidCredential(cfgSecret):
		clientSecret = cfgSecret
		secretSource = paths.SettingsFile
	case isValidCredential(defSecret):
		clientSecret = defSecret
		secretSource = "built-in default"
	}

	// If credentials are still missing, prompt interactively once and persist to settings.json
	if !isValidCredential(clientID) || !isValidCredential(clientSecret) {
		promptedID, promptedSecret, promptErr := promptCredentialsInteractive(paths, clientID, clientSecret)
		if promptErr != nil {
			return nil, fmt.Errorf("%w\nPlease set 'client_id' and 'client_secret' in:\n  %s\nor export AGYSYNC_CLIENT_ID and AGYSYNC_CLIENT_SECRET", ErrCredentialsNotConfigured, paths.SettingsFile)
		}
		clientID = promptedID
		clientSecret = promptedSecret
		idSource = "interactive prompt"
		secretSource = "interactive prompt"
	}

	// Persist credentials to settings.json immediately so user doesn't need to re-export env vars
	if localConfig != nil && (localConfig.ClientID != clientID || localConfig.ClientSecret != clientSecret) {
		localConfig.ClientID = clientID
		localConfig.ClientSecret = clientSecret
		if saveErr := sync.SaveLocalSettings(paths, localConfig); saveErr == nil {
			fmt.Printf("[Auth] Saved OAuth client credentials to %s\n", paths.SettingsFile)
		}
	}

	ctx := context.Background()
	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{googleDrive.DriveAppdataScope},
		RedirectURL:  "http://localhost:8989",
	}

	token, err := getOrPromptToken(paths, config)
	if err != nil {
		if strings.Contains(err.Error(), "invalid_client") {
			// Clear the rejected secret from settings.json so the next run prompts for a fresh secret
			// instead of getting stuck in a loop with the stale secret.
			if localConfig != nil && strings.Contains(err.Error(), "client secret is invalid") {
				localConfig.ClientSecret = ""
				_ = sync.SaveLocalSettings(paths, localConfig)
			}
			return nil, fmt.Errorf("%w\n\nCredential Diagnostic:\n  - Client ID (%s) loaded from: %s\n  - Client Secret (%s) loaded from: %s\n  - Why this happened: Google accepted the Client ID during browser login, but rejected the Client Secret when exchanging the authorization code for a token.\n  - Automatic recovery: Cleared the invalid 'client_secret' from %s. Re-run agysync and paste your valid Client Secret (GOCSPX-...) when prompted, and it will be saved permanently",
				err, maskSecret(clientID), idSource, maskSecret(clientSecret), secretSource, paths.SettingsFile)
		}
		return nil, err
	}

	httpClient := config.Client(ctx, token)
	srv, err := googleDrive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive client: %w", err)
	}

	return &DriveService{Srv: srv}, nil
}

// promptCredentialsInteractive prompts the user in an interactive terminal to enter Client ID / Secret once
func promptCredentialsInteractive(paths *sync.Paths, currentID, currentSecret string) (string, string, error) {
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return "", "", ErrCredentialsNotConfigured
	}

	reader := bufio.NewReader(os.Stdin)
	fmt.Println("\n[OAuth Setup] Google OAuth App credentials are required once and will be saved to:")
	fmt.Printf("  %s\n", paths.SettingsFile)

	clientID := currentID
	if !isValidCredential(clientID) {
		fmt.Print("Enter Google OAuth Client ID (e.g. xxx.apps.googleusercontent.com): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", "", ErrCredentialsNotConfigured
		}
		clientID = sanitizeCredential(line)
	}

	clientSecret := currentSecret
	if !isValidCredential(clientSecret) {
		fmt.Print("Enter Google OAuth Client Secret (e.g. GOCSPX-...): ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", "", ErrCredentialsNotConfigured
		}
		clientSecret = sanitizeCredential(line)
	}

	if !isValidCredential(clientID) || !isValidCredential(clientSecret) {
		return "", "", ErrCredentialsNotConfigured
	}
	return clientID, clientSecret, nil
}

func getOrPromptToken(paths *sync.Paths, config *oauth2.Config) (*oauth2.Token, error) {
	ctx := context.Background()

	// 1. Try reading and validating existing token
	if _, err := os.Stat(paths.TokenFile); err == nil {
		data, err := os.ReadFile(paths.TokenFile)
		if err == nil {
			var token oauth2.Token
			if err := json.Unmarshal(data, &token); err == nil {
				// Validate token and test refresh validity
				ts := config.TokenSource(ctx, &token)
				validToken, err := ts.Token()
				if err == nil && validToken != nil {
					// Update token file if token was refreshed
					if validToken.AccessToken != token.AccessToken {
						if tokenData, err := json.Marshal(validToken); err == nil {
							_ = os.WriteFile(paths.TokenFile, tokenData, 0600)
						}
					}
					return validToken, nil
				}
				fmt.Println("\n[Notice] Stored Google OAuth token has expired or is invalid.")
				fmt.Println("[Notice] Initiating browser login to re-authenticate...")
				_ = os.Remove(paths.TokenFile)
			}
		}
	}

	// 2. Prompt for a fresh token
	return promptNewToken(paths, config)
}

func promptNewToken(paths *sync.Paths, config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.SetAuthURLParam("prompt", "select_account consent"))
	fmt.Printf("\nAuthorize AgySync by visiting this link in your browser:\n\n%s\n\n", authURL)
	fmt.Println("Please log in, grant access, and approve permissions.")
	fmt.Println("If the local browser callback completes, authentication will finish automatically.")
	fmt.Println("Otherwise, paste the authorization code below:")
	fmt.Print("Enter Code: ")

	codeChan := make(chan string)
	errChan := make(chan error)
	stdinChan := make(chan string)

	// Start local loopback HTTP server with custom ServeMux to handle callback
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("missing code in OAuth callback")
			fmt.Fprintln(w, "Authentication failed. Missing authorization code.")
			return
		}
		fmt.Fprintln(w, "Authentication successful! You can close this tab and return to the terminal.")
		codeChan <- code
	})

	server := &http.Server{Addr: ":8989", Handler: mux}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	// Start terminal stdin reader in a goroutine
	go func() {
		var inputCode string
		if _, err := fmt.Scanln(&inputCode); err == nil {
			stdinChan <- strings.TrimSpace(inputCode)
		}
	}()

	var code string
	select {
	case code = <-codeChan:
		fmt.Println("\nAuthentication received via browser callback!")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	case code = <-stdinChan:
		fmt.Println("\nAuthentication code received via terminal input!")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	case err := <-errChan:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		return nil, fmt.Errorf("OAuth web server error: %w", err)
	case <-time.After(3 * time.Minute):
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		return nil, fmt.Errorf("OAuth login timed out after 3 minutes")
	}

	ctx := context.Background()
	token, err := config.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code for token: %w", err)
	}

	// Save token
	tokenData, err := json.Marshal(token)
	if err == nil {
		_ = os.MkdirAll(filepath.Dir(paths.TokenFile), 0755)
		_ = os.WriteFile(paths.TokenFile, tokenData, 0600)
	}

	return token, nil
}

// FlatName converts a relative local path to a safe Google Drive filename
func FlatName(relPath string) string {
	return strings.ReplaceAll(relPath, "/", "__")
}

// RelPath converts a flattened Drive filename back to a relative local path
func RelPath(flatName string) string {
	return strings.ReplaceAll(flatName, "__", "/")
}

// UploadFile uploads or updates a file in the user's hidden AppData folder
func (d *DriveService) UploadFile(localPath, driveName string) error {
	fileID, err := d.findFileID(driveName)
	if err != nil {
		return err
	}
	return d.UploadFileWithID(localPath, driveName, fileID)
}

// UploadFileWithID uploads or updates a file in the user's hidden AppData folder using an optional known fileID
func (d *DriveService) UploadFileWithID(localPath, driveName, fileID string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if fileID != "" {
		// Update existing file
		_, err = d.Srv.Files.Update(fileID, &googleDrive.File{}).Media(f).Do()
		if err != nil {
			return fmt.Errorf("failed to update file in Drive: %w", err)
		}
	} else {
		// Upload as a new file
		driveFile := &googleDrive.File{
			Name:    driveName,
			Parents: []string{"appDataFolder"},
		}
		_, err = d.Srv.Files.Create(driveFile).Media(f).Do()
		if err != nil {
			return fmt.Errorf("failed to create file in Drive: %w", err)
		}
	}
	return nil
}

// DownloadFile downloads a file from the hidden AppData folder to a local path
func (d *DriveService) DownloadFile(driveName, localPath string) error {
	fileID, err := d.findFileID(driveName)
	if err != nil {
		return err
	}
	if fileID == "" {
		return fmt.Errorf("file not found in Drive: %s", driveName)
	}
	return d.DownloadFileByID(fileID, driveName, localPath)
}

// DownloadFileByID downloads a file by its known Drive file ID to a local path
func (d *DriveService) DownloadFileByID(fileID, driveName, localPath string) error {
	if fileID == "" {
		return d.DownloadFile(driveName, localPath)
	}

	res, err := d.Srv.Files.Get(fileID).Download()
	if err != nil {
		return fmt.Errorf("failed to initiate file download: %w", err)
	}
	defer res.Body.Close()

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}

	out, err := os.Create(localPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, res.Body)
	return err
}

// DeleteFile deletes a file from Google Drive AppData folder by its file ID
func (d *DriveService) DeleteFile(fileID string) error {
	return d.Srv.Files.Delete(fileID).Do()
}

// ListAppDataFiles returns all filenames mapped to their RemoteFile metadata in the AppData folder
func (d *DriveService) ListAppDataFiles() (map[string]sync.RemoteFile, error) {
	fileMap := make(map[string]sync.RemoteFile)
	pageToken := ""

	for {
		q := d.Srv.Files.List().Spaces("appDataFolder").Fields("nextPageToken, files(id, name, md5Checksum, modifiedTime, size)")
		if pageToken != "" {
			q = q.PageToken(pageToken)
		}

		res, err := q.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list app data files: %w", err)
		}

		for _, f := range res.Files {
			modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)
			fileMap[f.Name] = sync.RemoteFile{
				ID:           f.Id,
				Name:         f.Name,
				MD5:          f.Md5Checksum,
				ModifiedTime: modTime,
				Size:         f.Size,
			}
		}

		pageToken = res.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return fileMap, nil
}

// GetMetadataFile reads sync_metadata.json from Google Drive if it exists
func (d *DriveService) GetMetadataFile() (*sync.SyncMetadata, error) {
	fileID, err := d.findFileID("sync_metadata.json")
	if err != nil {
		return nil, err
	}
	if fileID == "" {
		// Uninitialized global metadata
		return &sync.SyncMetadata{
			RegisteredNodes: []sync.MachineConfig{},
			MaxAllowedNodes: 2,
		}, nil
	}

	res, err := d.Srv.Files.Get(fileID).Download()
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	var meta sync.SyncMetadata
	if err := json.NewDecoder(res.Body).Decode(&meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

// SaveMetadataFile uploads sync_metadata.json to Google Drive
func (d *DriveService) SaveMetadataFile(meta *sync.SyncMetadata) error {
	tempFile := filepath.Join(os.TempDir(), "agysync_temp_meta.json")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(tempFile, data, 0644); err != nil {
		return err
	}
	defer os.Remove(tempFile)

	return d.UploadFile(tempFile, "sync_metadata.json")
}

func (d *DriveService) findFileID(name string) (string, error) {
	escapedName := strings.ReplaceAll(name, "'", "\\'")
	q := fmt.Sprintf("name = '%s' and trashed = false", escapedName)
	// Build query
	res, err := d.Srv.Files.List().Spaces("appDataFolder").Q(q).Fields("files(id)").Do()
	if err != nil {
		// Fallback to simple matching if Q filter fails
		files, err2 := d.ListAppDataFiles()
		if err2 != nil {
			return "", err
		}
		return files[name].ID, nil
	}

	if len(res.Files) > 0 {
		return res.Files[0].Id, nil
	}
	return "", nil
}

// AppendGlobalSyncLog appends a log entry to global_sync_log.jsonl on Google Drive
func (d *DriveService) AppendGlobalSyncLog(entry map[string]interface{}) error {
	tempLocalLog := filepath.Join(os.TempDir(), "agysync_global_temp.log")
	fileID, err := d.findFileID("global_sync_log.jsonl")
	if err != nil {
		return err
	}

	// 1. Download existing log if present
	if fileID != "" {
		res, err := d.Srv.Files.Get(fileID).Download()
		if err == nil {
			f, _ := os.Create(tempLocalLog)
			_, _ = io.Copy(f, res.Body)
			f.Close()
			res.Body.Close()
		}
	}

	// 2. Append new entry
	f, err := os.OpenFile(tempLocalLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	entryData, err := json.Marshal(entry)
	if err == nil {
		_, _ = f.Write(append(entryData, '\n'))
	}
	f.Close()
	defer os.Remove(tempLocalLog)

	// 3. Re-upload
	return d.UploadFile(tempLocalLog, "global_sync_log.jsonl")
}

// DownloadGlobalSyncLog downloads the global_sync_log.jsonl into a local path
func (d *DriveService) DownloadGlobalSyncLog(localPath string) error {
	return d.DownloadFile("global_sync_log.jsonl", localPath)
}

// Logout removes the locally stored Google OAuth token
func Logout(paths *sync.Paths) (bool, error) {
	if _, err := os.Stat(paths.TokenFile); os.IsNotExist(err) {
		return false, nil
	}
	if err := os.Remove(paths.TokenFile); err != nil {
		return false, fmt.Errorf("failed to remove token file: %w", err)
	}
	return true, nil
}
