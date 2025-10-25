<p align="center">
  <img src=images/logov2.png />
  <h1 align="center">RepoVault</h1>
  <p align="center">Automated git repository backup tool</p>
  <p align="center">
    <a href="https://github.com/msxdan/repovault/releases/latest"><img alt="Release" src="https://img.shields.io/github/release/msxdan/repovault.svg?logo=github&logoColor=white"></a>
  </p>
</p>

---

Backs up git repositories on a schedule. Supports GitHub, GitLab, Bitbucket, and self-hosted servers.

## Features

- Configurable sync intervals (`5m`, `1h`, `30s`, etc.)
- Automatic backups before syncing (preserves history on force pushes)
- Configurable backup retention (keep last N backups per branch)
- Catch-up syncs after downtime
- Wildcard branch support (`*` syncs all branches)
- SSH, HTTP, and token authentication
- Works with multiple accounts per provider
- Environment variable support in config
- Three log levels: verbose, normal, quiet
- Docker support
- Lightweight single binary

## Quick Start

### Docker (Recommended)

```bash
git clone https://github.com/msxdan/repovault.git
cd repovault
make setup
nano config.yaml  # Edit your config
make docker-run
make logs         # Check status
```

### Local

```bash
go build -o repovault main.go
./repovault config.yaml /path/to/backup/directory
```

## Configuration

Example `config.yaml`:

```yaml
defaults:
  period: "30m"
  backup_retention: 5 # Keep last 5 backups per branch
  log_level: "normal" # Options: "verbose", "normal", "quiet"

repositories:
  # GitHub with token auth
  - name: "my-app"
    url: "https://github.com/username/my-app.git"
    branches: ["main", "develop"]
    path: "github/my-app"
    period: "5m" # Override default period
    auth:
      type: "http"
      username: "your-username"
      password: $GITHUB_TOKEN # Use environment variable

  # SSH authentication with all branches
  - name: "api-service"
    url: "git@github.com:username/api-service.git"
    branches: ["*"]
    path: "github/api-service"
    backup_retention: 10 # Override default retention
    log_level: "verbose" # Override log level for this repo
    auth:
      type: "ssh"
      ssh_key_path: "/ssh/id_rsa"
      ssh_key_password: $SSH_KEY_PASSWORD # Use env var if key is encrypted

  # Public repository
  - name: "open-source-lib"
    url: "https://github.com/open-source/library.git"
    branches: ["main"]
    path: "github/library"
    auth:
      type: "none"
```

### Global Defaults

| Field                       | Description                          | Default      | Example                            |
| --------------------------- | ------------------------------------ | ------------ | ---------------------------------- |
| `defaults.period`           | Default sync interval for all repos  | Required     | `"30m"`, `"1h"`                    |
| `defaults.backup_retention` | Number of backups to keep per branch | 0 (disabled) | `5`                                |
| `defaults.log_level`        | Default log level                    | `"normal"`   | `"verbose"`, `"normal"`, `"quiet"` |

### Repository Options

| Field                   | Description                                                          | Example                              |
| ----------------------- | -------------------------------------------------------------------- | ------------------------------------ |
| `name`                  | Repository identifier                                                | `"my-app"`                           |
| `url`                   | Git repository URL                                                   | `"https://github.com/user/repo.git"` |
| `branches`              | Branches to sync, or `["*"]` for all                                 | `["main", "develop"]`                |
| `path`                  | Local path relative to backup directory (optional, defaults to name) | `"github/my-app"`                    |
| `period`                | Sync interval (optional, overrides default)                          | `"5m"`, `"1h"`, `"24h"`              |
| `backup_retention`      | Backups to keep (optional, overrides default)                        | `10`                                 |
| `log_level`             | Log level (optional, overrides default)                              | `"verbose"`, `"normal"`, `"quiet"`   |
| `auth.type`             | Authentication method                                                | `"ssh"`, `"http"`, `"none"`          |
| `auth.username`         | Username for HTTP auth                                               | `"your-username"`                    |
| `auth.password`         | Password/token for HTTP auth                                         | `"ghp_token123"` or `$GITHUB_TOKEN`  |
| `auth.ssh_key_path`     | Path to SSH private key                                              | `"/ssh/id_rsa"`                      |
| `auth.ssh_key_password` | SSH key password (if encrypted)                                      | `"password"` or `$SSH_PASSWORD`      |

### Environment Variables

Config values can reference environment variables by prefixing with `$`:

```yaml
auth:
  username: "myuser"
  password: $GITHUB_TOKEN # Reads from GITHUB_TOKEN env var
```

> [!WARNING]
> Using `*` for branches can take a long time if there are many branches.

> ![NOTE]
> Recommended minimum period is `5m`.

## Authentication

### SSH

```yaml
auth:
  type: "ssh"
  ssh_key_path: "/path/to/ssh/key"
  ssh_key_password: $SSH_KEY_PASSWORD # Only if key is encrypted
```

For Docker, mount your key:

```yaml
volumes:
  - ./ssh-keys/id_rsa:/ssh/id_rsa:ro
```

### HTTP (Tokens)

```yaml
auth:
  type: "http"
  username: "your-username"
  password: $GITHUB_TOKEN
```

- **GitHub:** Personal Access Token (`ghp_...`) with `repo` scope (or `Contents: read-only` for fine-grained tokens)
- **GitLab:** Personal Access Token (`glpat_...`)
- **Bitbucket:** App Password

### Public Repos

```yaml
auth:
  type: "none"
```

## Automatic Backups

RepoVault automatically creates backups before syncing when the local branch differs from remote. This protects against data loss from force pushes.

### How It Works

1. Before syncing, RepoVault compares local and remote commits
2. If they differ, it creates a backup reference: `refs/backups/YYYY-MM-DD-HH-MM-SS/branch-name`
3. Old backups are automatically cleaned up based on `backup_retention` setting
4. Backups are stored as git references (no extra disk space for commits already in the repo)

### Configuration

```yaml
defaults:
  backup_retention: 5 # Keep last 5 backups per branch

repositories:
  - name: "critical-repo"
    backup_retention: 20 # Keep more backups for critical repos
```

Set `backup_retention: 0` to disable backups for a repository.

### Viewing Backups

List all backups:

```bash
git show-ref | grep refs/backups
```

Restore from a backup:

```bash
git checkout refs/backups/2025-10-25-14-30-00/main
```

## Log Levels

Control output verbosity globally or per-repository:

- **verbose**: Shows all branch operations, progress updates, and backup details
- **normal**: Shows sync progress every 20% or 10 branches (default)
- **quiet**: Only shows errors and final summaries

```yaml
defaults:
  log_level: "normal"

repositories:
  - name: "noisy-repo"
    log_level: "quiet" # Override for specific repo
```

## Missed Sync Recovery

RepoVault tracks sync times in `<backup-dir>/.repovault-state.json`. On startup, it checks for missed syncs and catches up automatically.

Example: If your last sync was 7 hours ago and your period is 3 hours, RepoVault will sync immediately on startup, then resume the normal schedule.

```
23:40:29 INFO  github.com/username/my-app: Missed sync detected (last sync: 7h ago), performing catch-up sync
```

## Example Output

```shell
22:13:59 INFO  Starting RepoVault with 5 repositories
22:13:59 INFO  github.com/msxdan/dotfiles_private: Syncing 1 branches...
22:13:59 INFO  github.com/msxdan/marvincloud.io: Syncing 1 branches...
22:13:59 INFO  github.com/msxdan/zet: Syncing 1 branches...
22:14:00 INFO  github.com/msxdan/dotfiles: Syncing 2 branches...
22:14:00 INFO  github.com/msxdan/marvincloud.io: 1/1
22:14:00 INFO  github.com/msxdan/homelab-private: Syncing 2 branches...
22:14:00 INFO  github.com/msxdan/marvincloud.io: Synced 1 branches (0.5s, 9.2 MB, next: 22:44:00)
22:14:00 INFO  github.com/msxdan/dotfiles_private: 1/1
22:14:00 INFO  github.com/msxdan/zet: 1/1
22:14:00 INFO  github.com/msxdan/dotfiles_private: Synced 1 branches (0.5s, 1.7 MB, next: 22:44:00)
22:14:00 INFO  github.com/msxdan/zet: Synced 1 branches (0.5s, 201.3 MB, next: 22:44:00)
22:14:00 INFO  github.com/msxdan/dotfiles: 2/2
22:14:00 INFO  github.com/msxdan/dotfiles: Synced 2 branches (0.9s, 2.8 MB, next: 22:44:00)
22:14:00 INFO  github.com/msxdan/homelab-private: 2/2
22:14:00 INFO  github.com/msxdan/homelab-private: Synced 2 branches (1.0s, 1.7 MB, next: 22:44:00)
```

## Docker Deployment

Example `docker-compose.yml`:

```yaml
services:
  repovault:
    image: repovault:latest
    container_name: repovault
    restart: unless-stopped
    environment:
      - GITHUB_TOKEN=${GITHUB_TOKEN}
      - SSH_KEY_PASSWORD=${SSH_KEY_PASSWORD}
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

## Troubleshooting

**Authentication errors:**

```bash
chmod 600 ./ssh-keys/id_rsa  # Fix SSH key permissions
```

**Encrypted SSH key error:**
If you see "bcrypt_pbkdf" error, your SSH key is encrypted. Add `ssh_key_password` to your config.

**"Working directory not clean" warnings:**
This is normal - RepoVault forces sync to match the remote exactly. Local changes are backed up before being overwritten.

**Wildcard branches not working:**
Check that your token has permission to list and read all branches.

**View logs:**

```bash
docker compose logs -f repovault
```

## License

MIT - see [LICENSE](LICENSE) file.
