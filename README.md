<p align="center">
  <img src=images/logov2.png />
  <h1 align="center">RepoVault 🔐</h1>
  <p align="center">Simple to use git backup solution.</p>
  <p align="center">
    <a href="https://github.com/msxdan/repovault/releases/latest"><img alt="Release" src="https://img.shields.io/github/release/msxdan/repovault.svg?logo=github&logoColor=white"></a>
  </p>
</p>

---

A simple, lightweight Git repository synchronization tool that automatically backs up multiple repositories from various Git hosting providers. Perfect for creating secure backups of your Git repositories with configurable sync intervals and multiple authentication methods.

## ✨ Features

- **🔄 Automatic Synchronization** - Configurable sync intervals (e.g., `5m`, `1h`, `30s`)
- **⏱️ Missed Sync Recovery** - Automatically detects and performs catch-up syncs after system restarts or downtime
- **🌟 Wildcard Branch Support** - Sync specific branches or use `*` to sync all branches
- **🔐 Multiple Authentication Methods** - SSH keys, HTTP (username/password), and public repositories
- **🏢 Multi-Provider Support** - GitHub, GitLab, Bitbucket, and self-hosted Git servers
- **👥 Multi-Account Support** - Handle multiple accounts/organizations per provider
- **📋 YAML Configuration** - Simple, readable configuration format
- **🎨 Colored Output** - Beautiful terminal output with automatic color detection
- **🐳 Docker Ready** - Containerized deployment with Docker Compose
- **⚡ Lightweight** - Single binary, minimal resource usage
- **🛡️ Secure** - Non-root execution, read-only configuration mounting

## 🚀 Quick Start

### Using Docker Compose (Recommended)

1. **Clone and setup:**

```bash
git clone https://github.com/msxdan/repovault.git
cd repovault
make setup
```

2. **Edit configuration:**

```bash
nano config.yaml
```

3. **Start syncing:**

```bash
make docker-run
```

4. **Check logs:**

```bash
make logs
```

### Local Installation

1. **Build from source:**

```bash
go build -o repovault main.go
```

2. **Run:**

```bash
./repovault config.yaml /path/to/backup/directory
```

## ⚙️ Configuration

Create a `config.yaml` file with your repositories:

```yaml
repositories:
  # GitHub repository with specific branches
  - name: "my-app"
    url: "https://github.com/username/my-app.git"
    branches: ["main", "develop", "staging"]
    path: "github/my-app"
    period: "5m"
    auth:
      type: "http"
      username: "your-username"
      password: "ghp_your_github_token"

  # Repository with all branches using SSH
  - name: "api-service"
    url: "git@github.com:username/api-service.git"
    branches: ["*"] # Sync all branches
    path: "github/api-service"
    period: "10m"
    auth:
      type: "ssh"
      ssh_key_path: "/ssh/id_rsa"

  # Public repository (no authentication)
  - name: "open-source-lib"
    url: "https://github.com/open-source/library.git"
    branches: ["main"]
    path: "github/library"
    period: "30m"
    auth:
      type: "none"

  # GitLab with HTTP authentication
  - name: "infrastructure"
    url: "https://gitlab.com/company/infrastructure.git"
    branches: ["master", "production"]
    path: "gitlab/infrastructure"
    period: "15m"
    auth:
      type: "http"
      username: "deploy-user"
      password: "glpat_your_gitlab_token"

  # Self-hosted Git server
  - name: "internal-tools"
    url: "https://git.company.com/tools/internal.git"
    branches: ["release"]
    path: "internal/tools"
    period: "20m"
    auth:
      type: "http"
      username: "backup-service"
      password: "your_token_here"
```

### Configuration Options

| Field                   | Description                                  | Example                              |
| ----------------------- | -------------------------------------------- | ------------------------------------ |
| `name`                  | Repository identifier (for logging)          | `"my-app"`                           |
| `url`                   | Git repository URL (HTTPS or SSH)            | `"https://github.com/user/repo.git"` |
| `branches`              | List of branches to sync, or `["*"]` for all | `["main", "develop"]`                |
| `path`                  | Local path relative to backup directory      | `"github/my-app"`                    |
| `period`                | Sync interval (Go duration format)           | `"30s"`, `"5m"`, `"1h"`, `"24h"`     |
| `auth.type`             | Authentication method                        | `"ssh"`, `"http"`, `"none"`          |
| `auth.username`         | Username for HTTP auth                       | `"your-username"`                    |
| `auth.password`         | Password/token for HTTP auth                 | `"ghp_token123"`                     |
| `auth.ssh_key_path`     | Path to SSH private key                      | `"/ssh/id_rsa"`                      |
| `auth.ssh_key_password` | Password to decrypt SSH Key (if encrypted)   | `"MySuperSecretPass"`                |

> [!WARNING]
> Use \* in branches with caution, backing up too many branches could take forever and reach update period before finish initial sync

> [!NOTE]
> I would recommend setting minimum update period at least `5m`

## 🔐 Authentication Methods

### SSH Key Authentication

```yaml
auth:
  type: "ssh"
  ssh_key_path: "/path/to/ssh/key"
  ssh_key_password: "MySuperPassword@1" # Only required if SSH Key is encrypted
```

For Docker, mount your SSH key:

```yaml
volumes:
  - ./ssh-keys/id_rsa:/ssh/id_rsa:ro
```

### HTTP Authentication (GitHub/GitLab Tokens)

```yaml
auth:
  type: "http"
  username: "your-username"
  password: "your-token"
```

**GitHub:** Use Personal Access Tokens (`ghp_...`)  
**GitLab:** Use Personal Access Tokens (`glpat_...`)  
**Bitbucket:** Use App Passwords

### Public Repositories

```yaml
auth:
  type: "none"
```

## 📊 Output Example

```sh
15:04:32 INFO  Starting RepoVault with 4 repositories
15:04:32 INFO  github.com/username/my-app: Cloning repository...
15:04:34 INFO  github.com/username/my-app/main ✓ (2.1s)
15:04:35 INFO  github.com/username/my-app/develop ✓ (1.3s)
15:04:35 INFO  github.com/username/my-app: All 2 branches synced (3.4s, next: 15:09)
15:04:36 WARN  gitlab.com/company/api/main: Reset dirty working directory
15:04:37 INFO  gitlab.com/company/api/main ✓ (1.2s)
15:04:37 INFO  gitlab.com/company/api: All 1 branches synced (1.2s, next: 15:19)
```

## 🐳 Docker Deployment

### Docker Compose (Recommended)

```yaml
version: "3.8"
services:
  repovault:
    image: repovault:latest
    container_name: repovault
    restart: unless-stopped
    volumes:
      - ./config.yaml:/config/config.yaml:ro
      - ./ssh-keys:/ssh:ro # If using SSH
      - ./backup:/backup:rw
    deploy:
      resources:
        limits:
          memory: 512M
          cpus: "0.5"
```

### Available Commands

```bash
# Setup and management
make setup       # Create example configuration
make docker-run  # Start with Docker Compose
make docker-stop # Stop containers
make logs        # View logs
make docker-build # Rebuild image

# Development
make build       # Build locally
make run         # Run locally
make clean       # Clean build artifacts
```

## 🛡️ Force Push Protection Options

1. skip - Maximum Protection

`yamlforce_push_protection: "skip"`

Behavior: Never sync force pushes, preserves all local history
Use case: Critical production repositories
Result: Old commits are never lost

2. backup_and_sync - Balanced Protection
   yamlforce_push_protection: "backup_and_sync"

Behavior: Backup old commits to refs, then sync new state
Use case: Important repositories where you want both history and current state
Result: Old commits preserved in backup refs, new state synced

3. sync_anyway - No Protection (Default)
   yamlforce_push_protection: "sync_anyway"

# or omit the field entirely

Behavior: Always sync (current RepoVault behavior)
Use case: Experimental repos, personal projects
Result: Matches remote exactly, may lose commits

🎯 How It Works

1. Default Branch Detection:

First tries to use repository's HEAD reference
Falls back to common names: main, master, develop, dev
Last resort: uses first available branch

2. Protection Logic:

Default branch: Uses protect_default_branch setting if specified
Other branches: Uses force_push_protection setting (or "sync_anyway" default)

3. Override Priority:

protect_default_branch always overrides force_push_protection for the default branch

## 📁 Directory Structure

After running, your backup directory will look like:

```
backup/
├── github/
│   ├── username/
│   │   ├── my-app/          # Synced repository
│   │   └── another-repo/
│   └── organization/
│       └── team-project/
├── gitlab/
│   └── company/
│       └── infrastructure/
└── internal/
    └── tools/
```

## ⏱️ Missed Sync Recovery

RepoVault automatically handles missed syncs when your computer is off or the service is stopped. Here's how it works:

### How It Works

1. **State Tracking**: RepoVault maintains a state file (`.repovault-state.json`) in your backup directory that tracks the last successful sync time for each repository.

2. **Startup Check**: When RepoVault starts, it checks if any repositories missed their scheduled sync:
   - Compares the last sync time + configured period with the current time
   - If the scheduled sync time has passed, performs a catch-up sync immediately

3. **Automatic Recovery**: After catch-up syncs complete, normal periodic syncing resumes.

### Example Scenario

```yaml
repositories:
  - name: "my-app"
    url: "https://github.com/username/my-app.git"
    period: "3h"  # Sync every 3 hours
```

**Timeline:**
- 16:12 - Last sync completed successfully
- 19:12 - Scheduled sync time (computer was off, sync missed)
- 22:12 - Another scheduled sync missed
- 23:40 - Computer turned back on, RepoVault starts
- 23:40 - Detects missed sync (last sync was 7h28m ago)
- 23:40 - Performs immediate catch-up sync
- 02:40 - Resumes normal 3-hour sync cycle

### Viewing Missed Sync Logs

When a missed sync is detected, you'll see:

```
23:40:29 INFO  github.com/username/my-app: Missed sync detected (last sync: 7h ago), performing catch-up sync
23:40:29 INFO  github.com/username/my-app: Syncing 2 branches...
```

### State File Location

The state file is stored at: `<backup-directory>/.repovault-state.json`

**Example:**
```json
{
  "last_syncs": {
    "github.com/username/my-app": "2025-10-25T16:12:27Z",
    "gitlab.com/company/api": "2025-10-25T16:11:53Z"
  }
}
```

> **Note**: The state file is automatically created and managed by RepoVault. No manual configuration required!

## 🔧 Advanced Usage

### Multiple GitHub Accounts

```yaml
repositories:
  - name: "personal-dotfiles"
    url: "https://github.com/personal-account/dotfiles.git"
    # ...

  - name: "work-api"
    url: "https://github.com/work-org/api-service.git"
    # ...
```

### Custom Sync Intervals

```yaml
repositories:
  - name: "critical-app"
    period: "1m" # Every minute
    # ...

  - name: "archive-repo"
    period: "24h" # Once per day
    # ...
```

### Wildcard Branch Syncing

```yaml
repositories:
  - name: "multi-branch-project"
    branches: ["*"] # Syncs ALL remote branches
    # ...
```

## 🛡️ Security Best Practices

1. **Use Personal Access Tokens** instead of passwords
2. **Mount SSH keys as read-only** in Docker
3. **Secure your config.yaml** file (contains credentials)
4. **Use least-privilege tokens** (read-only access)
5. **Regular token rotation** for enhanced security

GitHub Fine Grained Token

- Contents - read-only
- Metadata

## 🐛 Troubleshooting

### Common Issues

**Authentication Errors:**

```bash
# Check SSH key permissions
chmod 600 ./ssh-keys/id_rsa

# Verify token permissions for private repos
# Ensure token has 'repo' scope for GitHub
```

**"Working directory not clean" warnings:**

- This is normal behavior - RepoVault forces sync to match remote exactly
- Local changes are automatically discarded (this is a backup tool)

**Wildcard branches failing:**

- Ensure authentication is properly configured for private repositories
- Check that the token has sufficient permissions

### Logs and Debugging

```bash
# View real-time logs
docker compose logs -f repovault

# Check container status
docker compose ps

# Debug configuration
docker compose config
```

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## 📄 License

MIT License - see [LICENSE](LICENSE) file for details.

## 🙏 Acknowledgments

- Built with [go-git](https://github.com/go-git/go-git) library
- Inspired by the need for simple, reliable Git repository backups

---

**⭐ Star this repo if RepoVault helps you keep your repositories safe!**

# TODO

Reduce memory consuption, with a few repos, it's using 1.772 GiB and not freeing up

- Limit concurrent syncs to avoid memory peaks

Current logs for VERBOSE mode, INFO mode should how show general stats, success, pending and failed
