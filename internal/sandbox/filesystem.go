package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DirectoryConfig defines permissions and limits for a directory
type DirectoryConfig struct {
	Name         string // e.g., "workspace", "cache", "temp"
	Description  string // Optional human-readable description
	Root         string // Absolute path on disk
	ReadOnly     bool
	MaxFileSize  int64
	MaxFiles     int
	MaxTotalSize int64
}

// SandboxFileSystem manages all filesystem operations for a sandbox
type SandboxFileSystem struct {
	directories map[string]*Directory // Key: directory name (workspace, cache, etc.)
	mu          sync.RWMutex
}

// Directory represents a mounted directory with its own rules
type Directory struct {
	config DirectoryConfig
	stats  Stats
	mu     sync.RWMutex
}

// Stats tracks directory usage
type Stats struct {
	TotalBytes  int64
	FileCount   int
	MaxFileSize int64
}

// FileRequest represents a filesystem operation request
type FileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

// FileResponse represents a filesystem operation response
type FileResponse struct {
	Success bool     `json:"success"`
	Data    string   `json:"data,omitempty"`
	Files   []string `json:"files,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// NewSandboxFileSystem creates a new filesystem with multiple directories
func NewSandboxFileSystem(directories []DirectoryConfig) (*SandboxFileSystem, error) {
	sfs := &SandboxFileSystem{
		directories: make(map[string]*Directory),
	}

	for _, cfg := range directories {
		// Create directory on disk
		if err := os.MkdirAll(cfg.Root, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory %s: %w", cfg.Name, err)
		}

		dir := &Directory{
			config: cfg,
			stats:  Stats{},
		}

		// Calculate initial stats
		if err := dir.recalculateStats(); err != nil {
			return nil, fmt.Errorf("failed to calculate stats for %s: %w", cfg.Name, err)
		}

		sfs.directories[cfg.Name] = dir
	}

	return sfs, nil
}

// parsePath extracts directory name and relative path from user path
// Examples:
//
//	"./workspace/file.txt" -> ("workspace", "file.txt")
//	"workspace/file.txt"   -> ("workspace", "file.txt")
//	"./cache/data.json"    -> ("cache", "data.json")
//	"/temp/log.txt"        -> ("temp", "log.txt")
func (sfs *SandboxFileSystem) parsePath(userPath string) (dirName string, relPath string, err error) {
	// Normalize
	userPath = strings.TrimPrefix(userPath, "./")
	userPath = strings.TrimPrefix(userPath, "/")

	// Split on first /
	parts := strings.SplitN(userPath, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		return "", "", errors.New("invalid path: empty")
	}

	dirName = parts[0]
	if len(parts) == 2 {
		relPath = parts[1]
	} else {
		relPath = "" // Root of directory
	}

	// Check if directory exists
	if _, ok := sfs.directories[dirName]; !ok {
		available := make([]string, 0, len(sfs.directories))
		for name := range sfs.directories {
			available = append(available, name)
		}
		return "", "", fmt.Errorf("unknown directory '%s', available: %v", dirName, available)
	}

	return dirName, relPath, nil
}

// ReadFile reads a file from any directory
func (sfs *SandboxFileSystem) ReadFile(userPath string) (string, error) {
	dirName, relPath, err := sfs.parsePath(userPath)
	if err != nil {
		return "", err
	}

	sfs.mu.RLock()
	dir := sfs.directories[dirName]
	sfs.mu.RUnlock()

	return dir.ReadFile(relPath)
}

// WriteFile writes a file to any writable directory
func (sfs *SandboxFileSystem) WriteFile(userPath, content string) error {
	dirName, relPath, err := sfs.parsePath(userPath)
	if err != nil {
		return err
	}

	sfs.mu.RLock()
	dir := sfs.directories[dirName]
	sfs.mu.RUnlock()

	if dir.config.ReadOnly {
		return fmt.Errorf("directory '%s' is read-only", dirName)
	}

	return dir.WriteFile(relPath, content)
}

// ListFiles lists files in a directory
func (sfs *SandboxFileSystem) ListFiles(userPath string) ([]string, error) {
	dirName, relPath, err := sfs.parsePath(userPath)
	if err != nil {
		return nil, err
	}

	sfs.mu.RLock()
	dir := sfs.directories[dirName]
	sfs.mu.RUnlock()

	return dir.ListFiles(relPath)
}

// DeleteFile deletes a file from any writable directory
func (sfs *SandboxFileSystem) DeleteFile(userPath string) error {
	dirName, relPath, err := sfs.parsePath(userPath)
	if err != nil {
		return err
	}

	sfs.mu.RLock()
	dir := sfs.directories[dirName]
	sfs.mu.RUnlock()

	if dir.config.ReadOnly {
		return fmt.Errorf("directory '%s' is read-only", dirName)
	}

	return dir.DeleteFile(relPath)
}

// GetDirectories returns list of available directory names
func (sfs *SandboxFileSystem) GetDirectories() []string {
	sfs.mu.RLock()
	defer sfs.mu.RUnlock()

	names := make([]string, 0, len(sfs.directories))
	for name := range sfs.directories {
		names = append(names, name)
	}
	return names
}

// GetStats returns stats for all directories
func (sfs *SandboxFileSystem) GetStats() map[string]Stats {
	sfs.mu.RLock()
	defer sfs.mu.RUnlock()

	stats := make(map[string]Stats)
	for name, dir := range sfs.directories {
		stats[name] = dir.GetStats()
	}
	return stats
}

// DirectoryInfo contains directory metadata
type DirectoryInfo struct {
	Name        string
	Description string
	ReadOnly    bool
}

// GetDirectoryInfo returns directory info including descriptions
func (sfs *SandboxFileSystem) GetDirectoryInfo() []DirectoryInfo {
	sfs.mu.RLock()
	defer sfs.mu.RUnlock()

	info := make([]DirectoryInfo, 0, len(sfs.directories))
	for _, dir := range sfs.directories {
		info = append(info, DirectoryInfo{
			Name:        dir.config.Name,
			Description: dir.config.Description,
			ReadOnly:    dir.config.ReadOnly,
		})
	}
	return info
}

// Cleanup removes all directories
func (sfs *SandboxFileSystem) Cleanup() error {
	sfs.mu.Lock()
	defer sfs.mu.Unlock()

	var errs []error
	for name, dir := range sfs.directories {
		if err := dir.Cleanup(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %v", errs)
	}
	return nil
}

// JSON handlers for host functions

// HandleReadFile processes a read file request from WASM
func (sfs *SandboxFileSystem) HandleReadFile(requestJSON []byte) []byte {
	var req FileRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return mustMarshal(FileResponse{Success: false, Error: "invalid request"})
	}

	content, err := sfs.ReadFile(req.Path)
	if err != nil {
		return mustMarshal(FileResponse{Success: false, Error: err.Error()})
	}

	return mustMarshal(FileResponse{Success: true, Data: content})
}

// HandleWriteFile processes a write file request from WASM
func (sfs *SandboxFileSystem) HandleWriteFile(requestJSON []byte) []byte {
	var req FileRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return mustMarshal(FileResponse{Success: false, Error: "invalid request"})
	}

	if err := sfs.WriteFile(req.Path, req.Content); err != nil {
		return mustMarshal(FileResponse{Success: false, Error: err.Error()})
	}

	return mustMarshal(FileResponse{Success: true})
}

// HandleListFiles processes a list files request from WASM
func (sfs *SandboxFileSystem) HandleListFiles(requestJSON []byte) []byte {
	var req FileRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return mustMarshal(FileResponse{Success: false, Error: "invalid request"})
	}

	files, err := sfs.ListFiles(req.Path)
	if err != nil {
		return mustMarshal(FileResponse{Success: false, Error: err.Error()})
	}

	return mustMarshal(FileResponse{Success: true, Files: files})
}

// HandleDeleteFile processes a delete file request from WASM
func (sfs *SandboxFileSystem) HandleDeleteFile(requestJSON []byte) []byte {
	var req FileRequest
	if err := json.Unmarshal(requestJSON, &req); err != nil {
		return mustMarshal(FileResponse{Success: false, Error: "invalid request"})
	}

	if err := sfs.DeleteFile(req.Path); err != nil {
		return mustMarshal(FileResponse{Success: false, Error: err.Error()})
	}

	return mustMarshal(FileResponse{Success: true})
}

// Directory implementation

// validatePath validates and resolves a relative path within the directory
func (d *Directory) validatePath(relPath string) (string, error) {
	// Prevent path traversal
	if strings.Contains(relPath, "..") {
		return "", errors.New("path traversal not allowed")
	}

	// Handle root directory
	if relPath == "" || relPath == "." {
		return d.config.Root, nil
	}

	// Clean and join
	cleanPath := filepath.Clean(relPath)
	fullPath := filepath.Join(d.config.Root, cleanPath)

	// Ensure within directory root
	if !strings.HasPrefix(fullPath, d.config.Root) {
		return "", errors.New("invalid path")
	}

	return fullPath, nil
}

// ReadFile reads a file from the directory
func (d *Directory) ReadFile(relPath string) (string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	fullPath, err := d.validatePath(relPath)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", relPath)
		}
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return string(content), nil
}

// WriteFile writes a file to the directory
func (d *Directory) WriteFile(relPath, content string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	fullPath, err := d.validatePath(relPath)
	if err != nil {
		return err
	}

	contentSize := int64(len(content))

	// Check file size limit
	if contentSize > d.config.MaxFileSize {
		return fmt.Errorf("file size %d exceeds limit %d", contentSize, d.config.MaxFileSize)
	}

	// Get existing file size
	existingSize := int64(0)
	if info, err := os.Stat(fullPath); err == nil {
		existingSize = info.Size()
	}

	// Check file count limit (only for new files)
	if existingSize == 0 && d.stats.FileCount >= d.config.MaxFiles {
		return fmt.Errorf("file count limit reached: %d", d.config.MaxFiles)
	}

	// Check total size limit
	newTotal := d.stats.TotalBytes + contentSize - existingSize
	if newTotal > d.config.MaxTotalSize {
		return fmt.Errorf("total size limit would be exceeded: %d > %d", newTotal, d.config.MaxTotalSize)
	}

	// Create parent directories
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Write file
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Update stats
	if existingSize == 0 {
		d.stats.FileCount++
	}
	d.stats.TotalBytes += contentSize - existingSize
	if contentSize > d.stats.MaxFileSize {
		d.stats.MaxFileSize = contentSize
	}

	return nil
}

// ListFiles lists files in a directory
func (d *Directory) ListFiles(relPath string) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	fullPath, err := d.validatePath(relPath)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("directory not found: %s", relPath)
		}
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		files = append(files, name)
	}

	return files, nil
}

// DeleteFile deletes a file from the directory
func (d *Directory) DeleteFile(relPath string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	fullPath, err := d.validatePath(relPath)
	if err != nil {
		return err
	}

	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", relPath)
		}
		return err
	}

	if err := os.Remove(fullPath); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	// Update stats
	d.stats.FileCount--
	d.stats.TotalBytes -= info.Size()

	return nil
}

// GetStats returns current directory statistics
func (d *Directory) GetStats() Stats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stats
}

// recalculateStats walks the directory and recalculates usage statistics
func (d *Directory) recalculateStats() error {
	var totalBytes int64
	var fileCount int
	var maxFileSize int64

	err := filepath.Walk(d.config.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			fileCount++
			totalBytes += info.Size()
			if info.Size() > maxFileSize {
				maxFileSize = info.Size()
			}
		}
		return nil
	})

	if err != nil {
		return err
	}

	d.stats.TotalBytes = totalBytes
	d.stats.FileCount = fileCount
	d.stats.MaxFileSize = maxFileSize

	return nil
}

// Cleanup removes all files in the directory
func (d *Directory) Cleanup() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := os.RemoveAll(d.config.Root); err != nil {
		return err
	}

	d.stats = Stats{}
	return nil
}

// mustMarshal marshals a value to JSON, ignoring errors
func mustMarshal(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}
