// main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
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
	Repositories []Repository `yaml:"repositories"`
}

type Repository struct {
	Name                   string   `yaml:"name"`
	URL                    string   `yaml:"url"`
	Branches               []string `yaml:"branches"`
	Path                   string   `yaml:"path,omitempty"` // Optional, defaults to name
	Period                 string   `yaml:"period"`
	Auth                   Auth     `yaml:"auth"`
	ForcePushProtection    string   `yaml:"force_push_protection,omitempty"`    // "skip", "backup_and_sync", "sync_anyway"
	ProtectDefaultBranch   string   `yaml:"protect_default_branch,omitempty"`   // Override for default branch only
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
}

func NewRepoVault(configPath, baseDir string) (*RepoVault, error) {
	config, err := loadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Check if colors are supported (simple check for TTY)
	useColors := isatty()

	return &RepoVault{
		config:    config,
		ctx:       ctx,
		cancel:    cancel,
		baseDir:   baseDir,
		logger:    log.New(os.Stdout, "", 0), // No prefix, we'll handle our own
		useColors: useColors,
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

func (rv *RepoVault) colorize(color, text string) string {
	if !rv.useColors {
		return text
	}
	return color + text + ColorReset
}

func (rv *RepoVault) logInfo(format string, args ...interface{}) {
	timestamp := time.Now().Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorBlue, "INFO "),
		message)
}

func (rv *RepoVault) logWarn(format string, args ...interface{}) {
	timestamp := time.Now().Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorYellow, "WARN "),
		message)
}

func (rv *RepoVault) logError(format string, args ...interface{}) {
	timestamp := time.Now().Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorRed, "ERROR"),
		message)
}

func (rv *RepoVault) logSuccess(format string, args ...interface{}) {
	timestamp := time.Now().Format("15:04:05")
	message := fmt.Sprintf(format, args...)
	rv.logger.Printf("%s %s %s",
		rv.colorize(ColorGray, timestamp),
		rv.colorize(ColorBlue, "INFO "),
		message)
}

func (rv *RepoVault) Start() {
	rv.logInfo("Starting RepoVault with %d repositories", len(rv.config.Repositories))

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
	rv.logInfo("RepoVault stopped")
}

func (rv *RepoVault) syncRepository(repo Repository) {
	defer rv.wg.Done()

	period, err := time.ParseDuration(repo.Period)
	if err != nil {
		rv.logError("%s: Invalid period '%s': %v", repo.Name, repo.Period, err)
		return
	}

	ticker := time.NewTicker(period)
	defer ticker.Stop()

	// Initial sync
	rv.performSync(repo, period)

	for {
		select {
		case <-rv.ctx.Done():
			return
		case <-ticker.C:
			rv.performSync(repo, period)
		}
	}
}

func (rv *RepoVault) performSync(repo Repository, period time.Duration) {
	startTime := time.Now()

	// Use path if specified, otherwise default to name
	repoLocalPath := repo.Path
	if repoLocalPath == "" {
		repoLocalPath = repo.Name
	}

	repoPath := filepath.Join(rv.baseDir, repoLocalPath)

	// Extract repo identifier from URL for better logging
	repoID := rv.extractRepoIdentifier(repo.URL)

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

	// Check if repository already exists
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

	// Sync branches
	branches := repo.Branches
	var defaultBranch string

	if len(branches) == 1 && branches[0] == "*" {
		rv.logInfo("%s: Discovering remote branches...", repoID)
		branches, err = rv.getAllRemoteBranches(gitRepo, auth)
		if err != nil {
			rv.logError("%s: Failed to get remote branches: %v", repoID, err)
			return
		}
		rv.logInfo("%s: Found %d remote branches: %v", repoID, len(branches), branches)
	}

	// Detect default branch if we need it for protection
	if repo.ProtectDefaultBranch != "" {
		defaultBranch, err = rv.getDefaultBranch(gitRepo, branches)
		if err != nil {
			rv.logWarn("%s: Could not detect default branch: %v", repoID, err)
		} else {
			rv.logInfo("%s: Default branch detected: %s", repoID, defaultBranch)
		}
	}

	successCount := 0
	totalBranches := len(branches)

	for i, branch := range branches {
		branchStartTime := time.Now()

		// Determine force push protection for this branch
		forcePushProtection := repo.ForcePushProtection
		if forcePushProtection == "" {
			forcePushProtection = "sync_anyway" // Default
		}

		// Override for default branch if specified
		if repo.ProtectDefaultBranch != "" && branch == defaultBranch {
			forcePushProtection = repo.ProtectDefaultBranch
			rv.logInfo("%s/%s: Using default branch protection: %s", repoID, branch, forcePushProtection)
		}

		// Show progress for branches that might take a while
		branchDone := make(chan bool, 1)
		go rv.showBranchProgress(repoID, branch, i+1, totalBranches, branchDone)

		if err := rv.syncBranch(gitRepo, repoID, branch, auth, forcePushProtection); err != nil {
			branchDone <- true
			duration := time.Since(branchStartTime)
			rv.logError("%s/%s %s (%.1fs)",
				repoID, branch,
				rv.colorize(ColorRed, "✗"),
				duration.Seconds())
			rv.logError("%s/%s: %v", repoID, branch, err)
		} else {
			branchDone <- true
			duration := time.Since(branchStartTime)
			rv.logSuccess("%s/%s %s (%.1fs)",
				repoID, branch,
				rv.colorize(ColorGreen, "✓"),
				duration.Seconds())
			successCount++
		}
	}

	totalDuration := time.Since(startTime)
	nextSync := time.Now().Add(period).Format("15:04:05")

	// Calculate repository size
	rv.logInfo("%s: Calculating repository size...", repoID)
	repoSize := rv.calculateRepoSize(repoPath)
	sizeStr := rv.formatBytes(repoSize)

	if successCount == totalBranches {
		rv.logInfo("%s: All %d branches synced (%.1fs, %s, next: %s)",
			repoID, successCount, totalDuration.Seconds(), sizeStr, nextSync)
	} else {
		rv.logWarn("%s: %d/%d branches synced (%.1fs, %s, next: %s)",
			repoID, successCount, totalBranches, totalDuration.Seconds(), sizeStr, nextSync)
	}
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

	var branches []string
	for _, ref := range refs {
		if ref.Name().IsBranch() {
			branchName := ref.Name().Short()
			branches = append(branches, branchName)
		}
	}

	return branches, nil
}

func (rv *RepoVault) syncBranch(repo *git.Repository, repoID, branchName string, auth transport.AuthMethod, forcePushProtection string) error {
	worktree, err := repo.Worktree()
	if err != nil {
		return err
	}

	// Fetch all refs first
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
		return fmt.Errorf("remote branch not found")
	}

	if localRef == nil {
		// Create local branch tracking remote (first sync)
		newRef := plumbing.NewHashReference(localBranchRef, remoteRef.Hash())
		err = repo.Storer.SetReference(newRef)
		if err != nil {
			return fmt.Errorf("failed to create local branch")
		}
		rv.logInfo("%s/%s: Created local branch tracking remote", repoID, branchName)
	} else {
		// Check for force push before syncing
		localHash := localRef.Hash()
		remoteHash := remoteRef.Hash()

		if localHash != remoteHash {
			forcePushDetected, err := rv.detectForcePush(repo, localHash, remoteHash)
			if err != nil {
				rv.logWarn("%s/%s: Could not detect force push: %v", repoID, branchName, err)
			} else if forcePushDetected {
				handled, err := rv.handleForcePush(repo, repoID, branchName, localRef, remoteRef, forcePushProtection)
				if err != nil {
					return fmt.Errorf("failed to handle force push: %v", err)
				}
				if !handled {
					rv.logWarn("%s/%s: Skipping sync due to force push protection", repoID, branchName)
					return nil // Skip this branch
				}
			}
		}
	}

	// Get current branch to avoid unnecessary checkouts
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD")
	}

	currentBranch := head.Name().Short()

	// Only checkout if we're not already on the target branch
	if currentBranch != branchName {
		// First try direct checkout with force (will overwrite unstaged changes unless they cause conflicts)
		if err := worktree.Checkout(&git.CheckoutOptions{Branch: localBranchRef, Force: true}); err != nil {
			// Evaluate status only if checkout failed
			status, sErr := worktree.Status()
			if sErr != nil {
				return fmt.Errorf("failed to checkout branch (%v) and get status (%v)", err, sErr)
			}
			if hasBlockingChanges(status) {
				var dirtyDetails []string
				for path, st := range status {
					if isPureUntracked(st) {
						continue
					}
					if isChange(st) {
						dirtyDetails = append(dirtyDetails, fmt.Sprintf("%s (%s/%s)", path, statusCodeString(st.Staging), statusCodeString(st.Worktree)))
					}
				}
				rv.logWarn("%s/%s: Reset dirty working directory (blocking checkout)", repoID, branchName)
				if rErr := worktree.Reset(&git.ResetOptions{Commit: head.Hash(), Mode: git.HardReset}); rErr != nil {
					return fmt.Errorf("failed to reset working directory: %v", rErr)
				}
				if cErr := worktree.Checkout(&git.CheckoutOptions{Branch: localBranchRef, Force: true}); cErr != nil {
					return fmt.Errorf("failed to checkout branch after reset: %v", cErr)
				}
			} else {
				// If there are no blocking tracked changes, propagate original checkout error
				return fmt.Errorf("failed to checkout branch: %v", err)
			}
		}
	}

	// Update local branch to match remote
	localRef, _ = repo.Reference(localBranchRef, true)
	if localRef != nil && localRef.Hash() == remoteRef.Hash() {
		// Already up to date
		return nil
	}

	// Reset local branch to match remote (force sync)
	err = worktree.Reset(&git.ResetOptions{
		Commit: remoteRef.Hash(),
		Mode:   git.HardReset,
	})
	if err != nil {
		return fmt.Errorf("failed to reset to remote")
	}

	return nil
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

		//publicKey, err := ssh.NewPublicKeys("git", sshKey, passphrase)
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

// Detect if a force push occurred by checking if local commits are reachable from remote
func (rv *RepoVault) detectForcePush(repo *git.Repository, localHash, remoteHash plumbing.Hash) (bool, error) {
	// If hashes are the same, no update needed
	if localHash == remoteHash {
		return false, nil
	}

	// Check if local commit is an ancestor of remote commit (normal forward update)
	isAncestor, err := rv.isAncestor(repo, localHash, remoteHash)
	if err != nil {
		return false, err
	}

	// If local is NOT an ancestor of remote, it's likely a force push
	return !isAncestor, nil
}

// Check if commit A is an ancestor of commit B
func (rv *RepoVault) isAncestor(repo *git.Repository, ancestorHash, descendantHash plumbing.Hash) (bool, error) {
	// Get the descendant commit
	descendantCommit, err := repo.CommitObject(descendantHash)
	if err != nil {
		return false, err
	}

	// Walk through commit history to find ancestor
	iter := descendantCommit.Parents()
	defer iter.Close()

	// Check if ancestor is in the parent chain
	visited := make(map[plumbing.Hash]bool)
	queue := []plumbing.Hash{descendantHash}

	for len(queue) > 0 {
		currentHash := queue[0]
		queue = queue[1:]

		if visited[currentHash] {
			continue
		}
		visited[currentHash] = true

		if currentHash == ancestorHash {
			return true, nil
		}

		commit, err := repo.CommitObject(currentHash)
		if err != nil {
			continue // Skip if we can't get the commit
		}

		// Add parents to queue
		iter := commit.Parents()
		err = iter.ForEach(func(parent *object.Commit) error {
			if !visited[parent.Hash] {
				queue = append(queue, parent.Hash)
			}
			return nil
		})
		if err != nil {
			continue
		}
	}

	return false, nil
}

// Handle force push according to protection policy
func (rv *RepoVault) handleForcePush(repo *git.Repository, repoID, branchName string, localRef, remoteRef *plumbing.Reference, protection string) (bool, error) {
	localHash := localRef.Hash()
	remoteHash := remoteRef.Hash()

	rv.logWarn("%s/%s: %s Force push detected! Remote history changed",
		repoID, branchName, rv.colorize(ColorRed, "⚠"))
	rv.logWarn("%s/%s: Local: %s, Remote: %s",
		repoID, branchName, localHash.String()[:8], remoteHash.String()[:8])

	switch protection {
	case "skip":
		rv.logWarn("%s/%s: Skipping sync to preserve local history", repoID, branchName)
		return false, nil

	case "backup_and_sync":
		// Create backup reference before syncing
		backupRefName := fmt.Sprintf("refs/backups/%s/%s",
			time.Now().Format("2006-01-02-15-04-05"), branchName)

		backupRef := plumbing.NewHashReference(plumbing.ReferenceName(backupRefName), localHash)
		err := repo.Storer.SetReference(backupRef)
		if err != nil {
			rv.logError("%s/%s: Failed to create backup reference: %v", repoID, branchName, err)
			return false, err
		}

		rv.logInfo("%s/%s: %s Backed up old commits to %s",
			repoID, branchName, rv.colorize(ColorGreen, "✓"), backupRefName)
		rv.logWarn("%s/%s: Proceeding with sync (old commits preserved in backup)", repoID, branchName)
		return true, nil

	case "sync_anyway":
		rv.logWarn("%s/%s: Syncing anyway (local commits will be lost)", repoID, branchName)
		return true, nil

	default:
		rv.logWarn("%s/%s: Unknown force push protection '%s', defaulting to sync_anyway",
			repoID, branchName, protection)
		return true, nil
	}
}

// Get the default branch of the repository
func (rv *RepoVault) getDefaultBranch(repo *git.Repository, availableBranches []string) (string, error) {
	// First, try to get the HEAD reference which points to the default branch
	headRef, err := repo.Head()
	if err == nil {
		if headRef.Name().IsBranch() {
			defaultBranch := headRef.Name().Short()
			// Verify this branch is in our available branches list
			for _, branch := range availableBranches {
				if branch == defaultBranch {
					return defaultBranch, nil
				}
			}
		}
	}

	// Fallback: try common default branch names in order of preference
	commonDefaults := []string{"main", "master", "develop", "dev"}
	for _, defaultCandidate := range commonDefaults {
		for _, branch := range availableBranches {
			if branch == defaultCandidate {
				return defaultCandidate, nil
			}
		}
	}

	// Last resort: return the first branch if any exist
	if len(availableBranches) > 0 {
		return availableBranches[0], nil
	}

	return "", fmt.Errorf("no branches found")
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
func isPureUntracked(s *git.FileStatus) bool { return s.Staging == git.Untracked && s.Worktree == git.Untracked }

// isChange indicates any modification/add/delete/rename for tracked content
func isChange(s *git.FileStatus) bool { return !isPureUntracked(s) && (s.Staging != git.Unmodified || s.Worktree != git.Unmodified) }

// statusCodeString maps git.StatusCode to a short human string
func statusCodeString(code git.StatusCode) string {
	switch code {
	case git.Unmodified:
		return "="
	case git.Untracked:
		return "?"
	case git.Added:
		return "+"
	case git.Modified:
		return "M"
	case git.Deleted:
		return "D"
	case git.Renamed:
		return "R"
	case git.Copied:
		return "C"
	case git.UpdatedButUnmerged:
		return "U" // merge conflict
	default:
		return fmt.Sprintf("%d", code)
	}
}

// Extract repository identifier from URL for logging
// Examples:
//   https://github.com/user/repo.git -> github.com/user/repo
//   git@gitlab.com:org/project.git -> gitlab.com/org/project
//   https://git.company.com/team/app.git -> git.company.com/team/app
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