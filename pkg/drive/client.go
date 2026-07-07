package drive

import (
	"context"
	"encoding/json"
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

const (
	DefaultClientID     = "1090403333333-developer-id-dummy.apps.googleusercontent.com"
	DefaultClientSecret = "GOCSPX-developer-secret-dummy-code"
)

// DriveService wraps the googleDrive.Service and settings
type DriveService struct {
	Srv *googleDrive.Service
}

// GetDriveService initializes the Drive API client
func GetDriveService(paths *sync.Paths) (*DriveService, error) {
	clientID := os.Getenv("AGYSYNC_CLIENT_ID")
	clientSecret := os.Getenv("AGYSYNC_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		fmt.Println("[Info] AGYSYNC_CLIENT_ID/SECRET env variables not set. Using built-in developer credentials.")
		clientID = DefaultClientID
		clientSecret = DefaultClientSecret
	}

	ctx := context.Background()
	config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     google.Endpoint,
		Scopes:       []string{googleDrive.DriveAppdataScope},
		RedirectURL:  "http://localhost:8989/oauth/callback",
	}

	token, err := getOrPromptToken(paths, config)
	if err != nil {
		return nil, err
	}

	httpClient := config.Client(ctx, token)
	srv, err := googleDrive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive client: %w", err)
	}

	return &DriveService{Srv: srv}, nil
}

func getOrPromptToken(paths *sync.Paths, config *oauth2.Config) (*oauth2.Token, error) {
	// 1. Try reading existing token
	if _, err := os.Stat(paths.TokenFile); err == nil {
		data, err := os.ReadFile(paths.TokenFile)
		if err == nil {
			var token oauth2.Token
			if err := json.Unmarshal(data, &token); err == nil {
				return &token, nil
			}
		}
	}

	// 2. No token found, perform OAuth2 flow
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("Authorize AgySync by visiting this link in your browser:\n\n%s\n\n", authURL)
	fmt.Println("Please log in, grant access, and copy the code.")
	fmt.Println("If the local browser callback completes, this will continue automatically.")
	fmt.Println("Otherwise, paste the authorization code here:")
	fmt.Print("Enter Code: ")

	codeChan := make(chan string)
	errChan := make(chan error)
	stdinChan := make(chan string)

	// Start local loopback HTTP server
	server := &http.Server{Addr: ":8989"}
	http.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("missing code in OAuth callback")
			fmt.Fprintln(w, "Authentication failed. Missing authorization code.")
			return
		}
		fmt.Fprintln(w, "Authentication successful! You can close this tab and return to the terminal.")
		codeChan <- code
	})

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
		// Shutdown server
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	case code = <-stdinChan:
		fmt.Println("\nAuthentication code received via terminal input!")
		// Shutdown server
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
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Check if file already exists in Drive AppData
	fileID, err := d.findFileID(driveName)
	if err != nil {
		return err
	}

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

// ListAppDataFiles returns all filenames mapped to their IDs in the AppData folder
func (d *DriveService) ListAppDataFiles() (map[string]string, error) {
	fileMap := make(map[string]string)
	pageToken := ""

	for {
		q := d.Srv.Files.List().Spaces("appDataFolder").Fields("nextPageToken, files(id, name)")
		if pageToken != "" {
			q = q.PageToken(pageToken)
		}

		res, err := q.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list app data files: %w", err)
		}

		for _, f := range res.Files {
			fileMap[f.Name] = f.Id
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
	q := fmt.Sprintf("name = '%s' and q = 'spaces = appDataFolder'", name)
	// Build query
	res, err := d.Srv.Files.List().Spaces("appDataFolder").Q(q).Fields("files(id)").Do()
	if err != nil {
		// Fallback to simple matching if Q filter fails on appDataFolder syntax
		files, err2 := d.ListAppDataFiles()
		if err2 != nil {
			return "", err
		}
		return files[name], nil
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
