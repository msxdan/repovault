// main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// Color codes for terminal output
const (
	ColorReset  = "\033[0m"
	ColorGreen  = "\033[32m"
	ColorYellow = "\033[33m"
	ColorRed    = "\033[31m"
	ColorBlue   = "\033[34m"
	ColorGray   = "\033[37m"
)

type Config struct {
	Defaults     Defaults     `yaml:"defaults,omitempty"`
	Repositories []Repository `yaml:"repositories"`
}

type Defaults struct {
	Period             string `yaml:"period,omitempty"`
	BackupRetention    int    `yaml:"backup_retention,omitempty"`     // Number of backups to keep per branch
	LogLevel           string `yaml:"log_level,omitempty"`            // "verbose", "normal", "quiet"
	MaxConcurrentRepos int    `yaml:"max_concurrent_repos,omitempty"` // Max number of repos to sync concurrently (default: 3)
}

type Repository struct {
	Name            string   `yaml:"name"`
	URL             string   `yaml:"url"`
	Branches        []string `yaml:"branches"`
	Path            string   `yaml:"path,omitempty"`             // Optional, defaults to name
	Period          string   `yaml:"period,omitempty"`           // Optional, defaults to global period
	BackupRetention int      `yaml:"backup_retention,omitempty"` // Number of backups to keep per branch (overrides default)
	LogLevel        string   `yaml:"log_level,omitempty"`        // Override log level for this repo
	Auth            Auth     `yaml:"auth"`
}

type Auth struct {
	Type           string `yaml:"type"` // "ssh", "http", or "none"
	Username       string `yaml:"username,omitempty"`
	Password       string `yaml:"password,omitempty"`
	SSHKeyPath     string `yaml:"ssh_key_path,omitempty"`
	SSHKeyPassword string `yaml:"ssh_key_password,omitempty"` // Passphrase for SSH key
}

type RepoVault struct {
	config     Config
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	baseDir    string
	logger     *log.Logger
	useColors  bool
	state      *SyncState
	stateMutex sync.Mutex
	semaphore  chan struct{} // Semaphore to limit concurrent repo operations
}

// SyncState tracks the last sync time for each repository
type SyncState struct {
	LastSyncs map[string]time.Time `json:"last_syncs"`
}

// newSyncState creates a new SyncState
func newSyncState() *SyncState {
	return &SyncState{
		LastSyncs: make(map[string]time.Time),
	}
}

type branchSyncResult struct {
	err           error
	isNewBranch   bool
	backupCreated bool
	backupRef     string
}

func NewRepoVault(configPath, baseDir string) (*RepoVault, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Check if colors are supported (simple check for TTY)
	useColors := isatty()

	// Load or create sync state
	state, err := loadSyncState(baseDir)
	if err != nil {
		// If state file doesn't exist, create a new one
		state = newSyncState()
	}

	// Set default max concurrent repos if not specified
	maxConcurrent := config.Defaults.MaxConcurrentRepos
	if maxConcurrent <= 0 {
		maxConcurrent = 3 // Default to 3 concurrent repos
	}

	return &RepoVault{
		config:    config,
		ctx:       ctx,
		cancel:    cancel,
		baseDir:   baseDir,
		logger:    log.New(os.Stdout, "", 0), // No prefix, we'll handle our own
		useColors: useColors,
		state:     state,
		semaphore: make(chan struct{}, maxConcurrent), // Buffered channel as semaphore
	}, nil
}

func loadConfig(configPath string) (Config, error) {
	var config Config

	data, err := os.ReadFile(configPath)
	if err != nil {
		return config, err
	}

	err = yaml.Unmarshal(data, &config)
	return config, err
}

// getStateFilePath returns the path to the state file
func getStateFilePath(baseDir string) string {
	return filepath.Join(baseDir, ".repovault-state.json")
}

// loadSyncState loads the sync state from disk
func loadSyncState(baseDir string) (*SyncState, error) {
	stateFile := getStateFilePath(baseDir)

	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}

	var state SyncState
	err = json.Unmarshal(data, &state)
	if err != nil {
		return nil, err
	}

	return &state, nil
}

// saveSyncState saves the sync state to disk
func (rv *RepoVault) saveSyncState() error {
	rv.stateMutex.Lock()
	defer rv.stateMutex.Unlock()

	stateFile := getStateFilePath(rv.baseDir)

	data, err := json.MarshalIndent(rv.state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(stateFile, data, 0644)
}

// updateLastSync updates the last sync time for a repository
func (rv *RepoVault) updateLastSync(repoID string) {
	rv.stateMutex.Lock()
	rv.state.LastSyncs[repoID] = time.Now()
	rv.stateMutex.Unlock()

	// Save state synchronously to avoid goroutine pile-up
	// This is fast enough (just writing a small JSON file)
	if err := rv.saveSyncState(); err != nil {
		rv.logWarn("Failed to save sync state: %v", err)
	}
}

// shouldPerformCatchupSync checks if a catchup sync is needed
func (rv *RepoVault) shouldPerformCatchupSync(repoID string, period time.Duration) bool {
	rv.stateMutex.Lock()
	defer rv.stateMutex.Unlock()

	lastSync, exists := rv.state.LastSyncs[repoID]
	if !exists {
		// Never synced before, no catchup needed (will do initial sync)
		return false
	}

	// Check if we're past the next scheduled sync time
	nextScheduledSync := lastSync.Add(period)
	return time.Now().After(nextScheduledSync)
}

func (rv *RepoVault) colorize(color, text string) string {
	if !rv.useColors {
		return text
	}
	return color + text + ColorReset
}

func (rv *RepoVault) logInfo(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorBlue, "INFO "),
		message)
}

func (rv *RepoVault) logWarn(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorYellow, "WARN "),
		message)
}

func (rv *RepoVault) logError(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorRed, "ERROR"),
		message)
}

func (rv *RepoVault) logSuccess(format string, args ...interface{}) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorBlue, "INFO "),
		message)
}

func (rv *RepoVault) Start() {
	maxConcurrent := cap(rv.semaphore)
	rv.logInfo("Starting RepoVault with %d repositories (max %d concurrent)",
		len(rv.config.Repositories), maxConcurrent)

	for _, repo := range rv.config.Repositories {
		rv.wg.Add(1)
		go rv.syncRepository(repo)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	rv.logInfo("Shutting down...")
	rv.cancel()
	rv.wg.Wait()

	// Final state save before exit
	if err := rv.saveSyncState(); err != nil {
		rv.logWarn("Failed to save final state: %v", err)
	}

	// Force GC to clean up before exit
	runtime.GC()

	rv.logInfo("RepoVault stopped")
}

func (rv *RepoVault) syncRepository(repo Repository) {
	defer rv.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			rv.logError("%s: Goroutine panic recovered: %v", repo.Name, r)
		}
	}()

	// Get period (repo-specific or default)
	periodStr := repo.Period
	if periodStr == "" {
		periodStr = rv.config.Defaults.Period
	}
	if periodStr == "" {
		rv.logError("%s: No period configured (set in defaults or per-repository)", repo.Name)
		return
	}

	period, err := time.ParseDuration(periodStr)
	if err != nil {
		rv.logError("%s: Invalid period '%s': %v", repo.Name, periodStr, err)
		return
	}

	repoID := rv.extractRepoIdentifier(repo.URL)

	// Acquire semaphore for initial sync to limit concurrent operations
	rv.semaphore <- struct{}{}

	// Check if a catch-up sync is needed
	if rv.shouldPerformCatchupSync(repoID, period) {
		rv.stateMutex.Lock()
		lastSync := rv.state.LastSyncs[repoID]
		rv.stateMutex.Unlock()

		missedDuration := time.Since(lastSync)
		rv.logInfo("%s: Missed sync detected (last sync: %s ago), performing catch-up sync",
			repoID, missedDuration.Round(time.Minute))
		rv.performSync(repo, period, repoID)
	} else {
		// Initial sync (or regular startup if already up-to-date)
		rv.performSync(repo, period, repoID)
	}

	// Release semaphore after initial sync completes, before entering ticker loop
	<-rv.semaphore

	ticker := time.NewTicker(period)
	defer ticker.Stop()

	for {
		select {
		case <-rv.ctx.Done():
			rv.logInfo("%s: Received shutdown signal, stopping sync loop", repoID)
			return
		case t := <-ticker.C:
			rv.logInfo("%s: Ticker fired at %s (period: %s)", repoID, t.Format("15:04:05"), period)
			rv.performSync(repo, period, repoID)
		}
	}
}

func (rv *RepoVault) performSync(repo Repository, period time.Duration, repoID string) {
	startTime := time.Now()

	// Use path if specified, otherwise default to name
	repoLocalPath := repo.Path
	if repoLocalPath == "" {
		repoLocalPath = repo.Name
	}

	repoPath := filepath.Join(rv.baseDir, repoLocalPath)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(repoPath), 0755); err != nil {
		rv.logError("%s: Failed to create directory: %v", repoID, err)
		return
	}

	auth, err := rv.getAuth(repo.Auth)
	if err != nil {
		rv.logError("%s: Authentication failed: %v", repoID, err)
		return
	}

	// Open repository fresh each time to reduce memory footprint
	// We trade some CPU for significant memory savings
	gitRepo, err := git.PlainOpen(repoPath)
	if err != nil {
		// Repository doesn't exist, clone it
		rv.logInfo("%s: Cloning repository...", repoID)

		// Start a progress ticker for cloning
		cloneDone := make(chan bool)
		go rv.showProgress(repoID, "Cloning", cloneDone)

		gitRepo, err = rv.cloneRepository(repo.URL, repoPath, auth)
		cloneDone <- true

		if err != nil {
			rv.logError("%s: Clone failed: %v", repoID, err)
			return
		}
		rv.logInfo("%s: Clone completed", repoID)
	}

	// Ensure we release the git repo object when done with this sync
	// This allows GC to free the memory
	defer func() {
		gitRepo = nil
	}()

	// Sync branches
	branches := repo.Branches

	// Get log level (repo-specific or default)
	logLevel := repo.LogLevel
	if logLevel == "" {
		logLevel = rv.config.Defaults.LogLevel
	}
	if logLevel == "" {
		logLevel = "normal" // Default to normal
	}

	if len(branches) == 1 && branches[0] == "*" {
		branches, err = rv.getAllRemoteBranches(gitRepo, auth)
		if err != nil {
			rv.logError("%s: Failed to get remote branches: %v", repoID, err)
			return
		}
	}

	successCount := 0
	totalBranches := len(branches)

	// Get backup retention setting (repo-specific or default)
	backupRetention := repo.BackupRetention
	if backupRetention == 0 {
		backupRetention = rv.config.Defaults.BackupRetention
	}

	// Start syncing message
	if logLevel != "quiet" {
		rv.logInfo("%s: Syncing %d branches...", repoID, totalBranches)
	}

	for i, branch := range branches {
		branchStartTime := time.Now()

		// Show progress for branches that might take a while (only in verbose mode)
		branchDone := make(chan bool, 1)
		if logLevel == "verbose" {
			go rv.showBranchProgress(repoID, branch, i+1, totalBranches, branchDone)
		}

		branchResult := rv.syncBranch(gitRepo, repoID, branch, auth, backupRetention, logLevel)

		if logLevel == "verbose" {
			branchDone <- true
		}

		duration := time.Since(branchStartTime)

		if branchResult.err != nil {
			// Always log errors regardless of log level
			rv.logError("%s: Branch '%s' failed %s (%.1fs)",
				repoID, branch,
				rv.colorize(ColorRed, "✗"),
				duration.Seconds())
			rv.logError("%s: Branch '%s' error: %v", repoID, branch, branchResult.err)
		} else {
			successCount++

			// Only log individual successes in verbose mode
			if logLevel == "verbose" {
				if branchResult.isNewBranch {
					rv.logInfo("%s/%s: Created local branch tracking remote", repoID, branch)
				}
				if branchResult.backupCreated {
					rv.logInfo("%s/%s: Backed up to %s", repoID, branch, branchResult.backupRef)
				}
				rv.logSuccess("%s/%s %s (%.1fs)",
					repoID, branch,
					rv.colorize(ColorGreen, "✓"),
					duration.Seconds())
			}

			// Show progress updates in normal mode (every 20% or every 10 branches)
			if logLevel == "normal" {
				progressInterval := totalBranches / 5
				if progressInterval < 10 {
					progressInterval = 10
				}
				if (i+1)%progressInterval == 0 || i+1 == totalBranches {
					rv.logInfo("%s: %d/%d", repoID, i+1, totalBranches)
				}
			}
		}
	}

	// After syncing all branches, checkout to default branch
	defaultBranch := rv.getDefaultBranch(gitRepo, branches)
	if defaultBranch != "" {
		worktree, err := gitRepo.Worktree()
		if err == nil {
			head, err := gitRepo.Head()
			if err == nil {
				currentBranch := head.Name().Short()
				if currentBranch != defaultBranch {
					localBranchRef := plumbing.NewBranchReferenceName(defaultBranch)
					if err := worktree.Checkout(&git.CheckoutOptions{Branch: localBranchRef, Force: true}); err == nil {
						if logLevel == "verbose" {
							rv.logInfo("%s: Checked out to default branch '%s'", repoID, defaultBranch)
						}
					}
				}
			}
		}
	}

	totalDuration := time.Since(startTime)
	nextSync := time.Now().Add(period).Format("15:04:05")

	// Only calculate repository size in verbose mode to save CPU and memory
	var sizeStr string
	if logLevel == "verbose" {
		rv.logInfo("%s: Calculating repository size...", repoID)
		repoSize := rv.calculateRepoSize(repoPath)
		sizeStr = rv.formatBytes(repoSize)
	}

	if logLevel != "quiet" {
		if successCount == totalBranches {
			if sizeStr != "" {
				rv.logInfo("%s: Synced %d branches (%.1fs, %s, next: %s)",
					repoID, successCount, totalDuration.Seconds(), sizeStr, nextSync)
			} else {
				rv.logInfo("%s: Synced %d branches (%.1fs, next: %s)",
					repoID, successCount, totalDuration.Seconds(), nextSync)
			}
		} else {
			if sizeStr != "" {
				rv.logWarn("%s: Synced %d/%d branches (%.1fs, %s, next: %s)",
					repoID, successCount, totalBranches, totalDuration.Seconds(), sizeStr, nextSync)
			} else {
				rv.logWarn("%s: Synced %d/%d branches (%.1fs, next: %s)",
					repoID, successCount, totalBranches, totalDuration.Seconds(), nextSync)
			}
		}
	}

	// Update last sync time for this repository
	rv.updateLastSync(repoID)

	// Suggest garbage collection after sync to free up memory from git operations
	// This is non-blocking and just gives a hint to the GC
	runtime.GC()
}

func (rv *RepoVault) cloneRepository(url, path string, auth transport.AuthMethod) (*git.Repository, error) {
	return git.PlainClone(path, false, &git.CloneOptions{
		URL:  url,
		Auth: auth,
	})
}

func (rv *RepoVault) getAllRemoteBranches(repo *git.Repository, auth transport.AuthMethod) ([]string, error) {
	remote, err := repo.Remote("origin")
	if err != nil {
		return nil, err
	}

	refs, err := remote.List(&git.ListOptions{
		Auth: auth, // Add authentication here
	})
	if err != nil {
		return nil, err
	}

	// Pre-allocate with a reasonable capacity to reduce allocations
	branches := make([]string, 0, 16)
	for _, ref := range refs {
		if ref.Name().IsBranch() {
			branchName := ref.Name().Short()
			branches = append(branches, branchName)
		}
	}

	return branches, nil
}

func (rv *RepoVault) syncBranch(repo *git.Repository, repoID, branchName string, auth transport.AuthMethod, backupRetention int, logLevel string) branchSyncResult {
	result := branchSyncResult{}
	worktree, err := repo.Worktree()
	if err != nil {
		result.err = err
		return result
	}

	// Fetch all refs first (only fetch specific branch to save bandwidth and memory)
	err = repo.Fetch(&git.FetchOptions{
		Auth: auth,
		RefSpecs: []config.RefSpec{
			config.RefSpec("+refs/heads/" + branchName + ":refs/remotes/origin/" + branchName),
		},
		Force: true,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		// Fallback to fetching all refs if specific branch fetch fails
		err = repo.Fetch(&git.FetchOptions{
			Auth: auth,
			RefSpecs: []config.RefSpec{
				config.RefSpec("+refs/heads/*:refs/remotes/origin/*"),
			},
		})
		if err != nil && err != git.NoErrAlreadyUpToDate {
			// Only log fetch errors if they're serious
			if err.Error() != "already up-to-date" {
				rv.logWarn("%s/%s: Fetch warning: %v", repoID, branchName, err)
			}
		}
	}

	// Check if local branch exists
	localBranchRef := plumbing.NewBranchReferenceName(branchName)
	localRef, err := repo.Reference(localBranchRef, true)
	if err != nil {
		// If the local branch doesn't exist yet, we'll create it below.
		localRef = nil
	}

	// Get remote branch reference
	remoteBranchRef := plumbing.NewRemoteReferenceName("origin", branchName)
	remoteRef, err := repo.Reference(remoteBranchRef, true)
	if err != nil {
		result.err = fmt.Errorf("remote branch not found")
		return result
	}

	if localRef == nil {
		// Create local branch tracking remote (first sync)
		newRef := plumbing.NewHashReference(localBranchRef, remoteRef.Hash())
		err = repo.Storer.SetReference(newRef)
		if err != nil {
			result.err = fmt.Errorf("failed to create local branch")
			return result
		}
		result.isNewBranch = true
	} else {
		// Create backup before syncing if local is different from remote
		localHash := localRef.Hash()
		remoteHash := remoteRef.Hash()

		if localHash != remoteHash && backupRetention > 0 {
			// Create backup reference before syncing
			backupRefName := fmt.Sprintf("refs/backups/%s/%s",
				time.Now().Format("2006-01-02-15-04-05"), branchName)

			backupRef := plumbing.NewHashReference(plumbing.ReferenceName(backupRefName), localHash)
			err := repo.Storer.SetReference(backupRef)
			if err != nil {
				if logLevel == "verbose" {
					rv.logWarn("%s/%s: Failed to create backup reference: %v", repoID, branchName, err)
				}
			} else {
				result.backupCreated = true
				result.backupRef = backupRefName

				// Clean up old backups
				if err := rv.cleanupOldBackups(repo, repoID, branchName, backupRetention, logLevel); err != nil {
					if logLevel == "verbose" {
						rv.logWarn("%s/%s: Failed to cleanup old backups: %v", repoID, branchName, err)
					}
				}
			}
		}
	}

	// Get current branch to avoid unnecessary checkouts
	head, err := repo.Head()
	if err != nil {
		result.err = fmt.Errorf("failed to get HEAD")
		return result
	}

	currentBranch := head.Name().Short()

	// Only checkout if we're not already on the target branch
	if currentBranch != branchName {
		// First try direct checkout with force (will overwrite unstaged changes unless they cause conflicts)
		if err := worktree.Checkout(&git.CheckoutOptions{Branch: localBranchRef, Force: true}); err != nil {
			// Evaluate status only if checkout failed
			status, sErr := worktree.Status()
			if sErr != nil {
				result.err = fmt.Errorf("failed to checkout branch (%v) and get status (%v)", err, sErr)
				return result
			}
			if hasBlockingChanges(status) {
				if logLevel == "verbose" {
					rv.logWarn("%s/%s: Reset dirty working directory (blocking checkout)", repoID, branchName)
				}
				if rErr := worktree.Reset(&git.ResetOptions{Commit: head.Hash(), Mode: git.HardReset}); rErr != nil {
					result.err = fmt.Errorf("failed to reset working directory: %v", rErr)
					return result
				}
				if cErr := worktree.Checkout(&git.CheckoutOptions{Branch: localBranchRef, Force: true}); cErr != nil {
					result.err = fmt.Errorf("failed to checkout branch after reset: %v", cErr)
					return result
				}
			} else {
				// If there are no blocking tracked changes, propagate original checkout error
				result.err = fmt.Errorf("failed to checkout branch: %v", err)
				return result
			}
		}
	}

	// Update local branch to match remote
	localRef, _ = repo.Reference(localBranchRef, true)
	if localRef != nil && localRef.Hash() == remoteRef.Hash() {
		// Already up to date
		return result
	}

	// Reset local branch to match remote (force sync)
	err = worktree.Reset(&git.ResetOptions{
		Commit: remoteRef.Hash(),
		Mode:   git.HardReset,
	})
	if err != nil {
		result.err = fmt.Errorf("failed to reset to remote")
		return result
	}

	return result
}

func expandEnvVars(s string) string {
	if strings.HasPrefix(s, "$") {
		envVar := strings.TrimPrefix(s, "$")
		if value := os.Getenv(envVar); value != "" {
			return value
		}
	}
	return s
}

func (rv *RepoVault) getAuth(authConfig Auth) (transport.AuthMethod, error) {
	switch authConfig.Type {
	case "ssh":
		sshKeyPath := expandEnvVars(authConfig.SSHKeyPath)
		if sshKeyPath == "" {
			return nil, fmt.Errorf("ssh_key_path is required for ssh auth")
		}

		sshKey, err := os.ReadFile(sshKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read SSH key: %v", err)
		}

		// Expand environment variables for SSH key password
		passphrase := expandEnvVars(authConfig.SSHKeyPassword)

		publicKey, err := ssh.NewPublicKeys("git", sshKey, passphrase)
		if err != nil {
			if strings.Contains(err.Error(), "bcrypt_pbkdf") && passphrase == "" {
				return nil, fmt.Errorf("SSH key appears to be encrypted but no passphrase provided. Add 'ssh_key_password' to your config")
			}
			return nil, fmt.Errorf("failed to parse SSH key: %v", err)
		}

		// Disable host key checking
		publicKey.HostKeyCallback = gossh.InsecureIgnoreHostKey()

		return publicKey, nil

	case "http":
		// Expand environment variables for username and password
		username := expandEnvVars(authConfig.Username)
		password := expandEnvVars(authConfig.Password)

		if username == "" || password == "" {
			return nil, fmt.Errorf("username and password are required for http auth")
		}

		return &http.BasicAuth{
			Username: username,
			Password: password,
		}, nil

	case "none":
		return nil, nil

	default:
		return nil, fmt.Errorf("unsupported auth type: %s", authConfig.Type)
	}
}

// Simple check for TTY support (basic color detection)
func isatty() bool {
	// Check if stdout is a terminal
	if fileInfo, _ := os.Stdout.Stat(); (fileInfo.Mode() & os.ModeCharDevice) != 0 {
		// Additional check for common terminals
		term := os.Getenv("TERM")
		if term != "" && term != "dumb" {
			return true
		}
	}
	return false
}

// getDefaultBranch tries to determine the default branch of the repository
func (rv *RepoVault) getDefaultBranch(repo *git.Repository, availableBranches []string) string {
	// Try common default branch names in order of preference
	commonDefaults := []string{"main", "master", "develop", "dev"}
	for _, defaultCandidate := range commonDefaults {
		for _, branch := range availableBranches {
			if branch == defaultCandidate {
				return defaultCandidate
			}
		}
	}

	// Last resort: return the first branch if any exist
	if len(availableBranches) > 0 {
		return availableBranches[0]
	}

	return ""
}

// cleanupOldBackups removes old backups keeping only the most recent N backups per branch
func (rv *RepoVault) cleanupOldBackups(repo *git.Repository, repoID, branchName string, keepCount int, logLevel string) error {
	if keepCount <= 0 {
		return nil // No cleanup needed if retention is 0 or negative
	}

	// Get all backup references for this branch
	refs, err := repo.References()
	if err != nil {
		return err
	}

	// Collect backup refs for this specific branch
	type backupRef struct {
		ref  *plumbing.Reference
		time time.Time
	}
	// Pre-allocate slice to reduce allocations
	backups := make([]backupRef, 0, keepCount+10)

	backupPrefix := "refs/backups/"
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		refName := ref.Name().String()

		// Check if this is a backup ref for our branch
		if strings.HasPrefix(refName, backupPrefix) && strings.HasSuffix(refName, "/"+branchName) {
			// Extract timestamp from ref name (format: refs/backups/2006-01-02-15-04-05/branchName)
			parts := strings.Split(refName, "/")
			if len(parts) >= 4 {
				timestampStr := parts[2]
				timestamp, err := time.Parse("2006-01-02-15-04-05", timestampStr)
				if err == nil {
					backups = append(backups, backupRef{ref: ref, time: timestamp})
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// If we have more backups than we want to keep, delete the oldest ones
	if len(backups) > keepCount {
		// Sort by timestamp (newest first)
		for i := 0; i < len(backups)-1; i++ {
			for j := i + 1; j < len(backups); j++ {
				if backups[i].time.Before(backups[j].time) {
					backups[i], backups[j] = backups[j], backups[i]
				}
			}
		}

		// Delete old backups (keep only the first keepCount)
		for i := keepCount; i < len(backups); i++ {
			err := repo.Storer.RemoveReference(backups[i].ref.Name())
			if err != nil {
				if logLevel == "verbose" {
					rv.logWarn("%s/%s: Failed to remove old backup %s: %v",
						repoID, branchName, backups[i].ref.Name().String(), err)
				}
			} else {
				if logLevel == "verbose" {
					rv.logInfo("%s/%s: Removed old backup %s",
						repoID, branchName, backups[i].ref.Name().Short())
				}
			}
		}
	}

	return nil
}

// Show progress for long-running operations
func (rv *RepoVault) showProgress(repoID, operation string, done <-chan bool) {
	ticker := time.NewTicker(15 * time.Second) // Show progress every 15 seconds
	defer ticker.Stop()

	startTime := time.Now()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			elapsed := time.Since(startTime)
			rv.logInfo("%s: %s... (%.0fs elapsed)", repoID, operation, elapsed.Seconds())
		}
	}
}

// Show progress for branch operations with context
func (rv *RepoVault) showBranchProgress(repoID, branch string, current, total int, done <-chan bool) {
	ticker := time.NewTicker(10 * time.Second) // Show progress every 10 seconds
	defer ticker.Stop()

	startTime := time.Now()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			elapsed := time.Since(startTime)
			rv.logInfo("%s/%s: Syncing branch %d/%d... (%.0fs elapsed)",
				repoID, branch, current, total, elapsed.Seconds())
		}
	}
}

// Calculate total size of repository directory
func (rv *RepoVault) calculateRepoSize(repoPath string) int64 {
	var totalSize int64

	err := filepath.Walk(repoPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		if !info.IsDir() {
			totalSize += info.Size()
		}

		return nil
	})

	if err != nil {
		// If we can't calculate size, return 0
		return 0
	}

	return totalSize
}

// Format bytes into human readable format
func (rv *RepoVault) formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	units := []string{"KB", "MB", "GB", "TB", "PB"}
	if exp >= len(units) {
		exp = len(units) - 1
	}

	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), units[exp])
}

// Determine if the worktree has modifications to tracked files (ignores purely untracked files)
// hasBlockingChanges returns true if there are tracked-file changes that could block a checkout
func hasBlockingChanges(status git.Status) bool {
	for _, s := range status {
		if isPureUntracked(s) {
			continue
		}
		if isChange(s) {
			return true
		}
	}
	return false
}

// isPureUntracked indicates both staging and worktree are untracked
func isPureUntracked(s *git.FileStatus) bool {
	return s.Staging == git.Untracked && s.Worktree == git.Untracked
}

// isChange indicates any modification/add/delete/rename for tracked content
func isChange(s *git.FileStatus) bool {
	return !isPureUntracked(s) && (s.Staging != git.Unmodified || s.Worktree != git.Unmodified)
}

// Extract repository identifier from URL for logging
// Examples:
//
//	https://github.com/user/repo.git -> github.com/user/repo
//	git@gitlab.com:org/project.git -> gitlab.com/org/project
//	https://git.company.com/team/app.git -> git.company.com/team/app
func (rv *RepoVault) extractRepoIdentifier(url string) string {
	// Remove .git suffix if present (TrimSuffix is clearer)
	url = strings.TrimSuffix(url, ".git")

	// Handle SSH format: git@domain:owner/repo
	if strings.HasPrefix(url, "git@") {
		// Extract domain and path from git@domain:owner/repo
		parts := strings.Split(url[4:], ":")
		if len(parts) == 2 {
			domain := parts[0]
			path := parts[1]
			return domain + "/" + path
		}
	}

	// Handle HTTPS format: https://domain/owner/repo
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		// Remove protocol
		url = strings.TrimPrefix(url, "https://")
		url = strings.TrimPrefix(url, "http://")
		return url
	}

	// Fallback: return as-is
	return url
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: repovault <config.yaml> [base-directory]")
	}

	// Set GC to run more aggressively to reduce memory usage
	// GOGC=50 means GC triggers when heap grows 50% (default is 100%)
	// This trades some CPU for lower memory usage
	debug.SetGCPercent(50)

	configPath := os.Args[1]
	baseDir := "/backup"

	if len(os.Args) >= 3 {
		baseDir = os.Args[2]
	}

	// Create base directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		log.Fatalf("Failed to create base directory: %v", err)
	}

	repoVault, err := NewRepoVault(configPath, baseDir)
	if err != nil {
		log.Fatalf("Failed to initialize RepoVault: %v", err)
	}

	repoVault.Start()
}
