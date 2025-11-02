# 🔄 NOFX Update Script Guide

## Overview

The `scripts/update.sh` script provides a safe, automated way to update your NOFX trading system running on Docker with selective rebuild options for frontend and/or backend.

---

## Features

✅ **Selective Rebuild** - Choose to rebuild frontend, backend, or both  
✅ **Git Integration** - Automatically pulls latest code from repository  
✅ **Smart Stashing** - Preserves `config.json` and `.env` during updates  
✅ **Automatic Backup** - Creates backup before updating  
✅ **Health Checks** - Verifies services are healthy after update  
✅ **Zero Downtime** - Restarts only affected services  
✅ **Rollback Support** - Automatic rollback on health check failure  
✅ **Detailed Logging** - Shows git changes, resource usage, and status  

---

## Usage

### Basic Syntax

```bash
./scripts/update.sh [options]
```

### Options

| Option | Short | Description |
|--------|-------|-------------|
| `--frontend` | `-f` | Rebuild frontend only |
| `--backend` | `-b` | Rebuild backend only |
| `--all` | `-a` | Rebuild both (default) |
| `--help` | `-h` | Show help message |

---

## Examples

### 1. Update Everything (Default)

```bash
# Pulls latest code and rebuilds both frontend and backend
./scripts/update.sh

# Or explicitly:
./scripts/update.sh --all
./scripts/update.sh -a
```

**Use when:**
- Major updates affecting both components
- First time running the script
- Unsure what changed

---

### 2. Update Frontend Only

```bash
./scripts/update.sh --frontend
# Or:
./scripts/update.sh -f
```

**Use when:**
- Only UI/UX changes were made
- React component updates
- CSS/styling changes
- Frontend bug fixes
- Faster rebuild (~2-3 minutes vs 5-10 minutes)

**Example scenarios:**
- Mobile responsiveness fixes
- Dashboard layout changes
- Chart component updates
- Translation updates

---

### 3. Update Backend Only

```bash
./scripts/update.sh --backend
# Or:
./scripts/update.sh -b
```

**Use when:**
- Trading logic changes
- AI prompt modifications
- API endpoint updates
- Database schema changes
- Backend bug fixes

**Example scenarios:**
- Decision engine prompt updates
- Risk management rule changes
- Exchange integration updates
- Performance optimizations

---

## What the Script Does

### Step-by-Step Process

#### 1. **Shows Rebuild Plan**
```
Rebuild Plan:
  ✓ Frontend
  ○ Backend (skipped)
```

#### 2. **Creates Backup**
- Backs up `config.json`, `decision_logs/`, `coin_pool_cache/`, `.env`
- Saved to `backups/pre_update_backup_YYYYMMDD_HHMMSS.tar.gz`

#### 3. **Pulls Latest Code from Git**
- Shows current branch and commit
- Stashes local changes (config.json and .env are already in .gitignore)
- Fetches and pulls latest changes with rebase
- Shows recent commit history

**Example output:**
```
Current branch: main
Current commit: a1b2c3d
Stashing local changes...
Fetching latest changes...
Code updated successfully: a1b2c3d → e4f5g6h

Recent changes:
* e4f5g6h (HEAD -> main) Fix mobile layout
* d3e4f5g Update AI prompts
* c2d3e4f Add new metrics
```

#### 4. **Checks for Config Changes**
- Compares `config.json` with `config.json.example`
- Warns if new options are available

#### 5. **Rebuilds Docker Images**
- Rebuilds only selected services (frontend/backend/both)
- Uses `--no-cache` for clean build
- Shows build progress

#### 6. **Restarts Services**
- Restarts only rebuilt services
- Uses `--no-deps` to avoid restarting dependencies
- Zero downtime deployment

#### 7. **Health Checks**
- **Backend**: Checks `http://localhost:8080/health`
- **Frontend**: Checks `http://localhost:3000/health`
- Retries up to 30 times (60 seconds)
- Automatic rollback on failure

#### 8. **Cleanup**
- Removes old Docker images
- Shows reclaimed disk space

#### 9. **Status Report**
- Shows service status
- Shows resource usage (CPU, memory)
- Shows git version info
- Lists updated components

---

## Advanced Usage

### Combining with Other Commands

```bash
# Update backend and view logs
./scripts/update.sh -b && docker compose logs -f nofx

# Update frontend and check status
./scripts/update.sh -f && docker compose ps

# Update all and monitor
./scripts/update.sh && docker stats
```

### Scheduling Automatic Updates

```bash
# Add to crontab for weekly updates (Sundays at 3 AM)
crontab -e

# Add this line:
0 3 * * 0 cd /path/to/nofx && ./scripts/update.sh -a >> /var/log/nofx_update.log 2>&1
```

---

## Troubleshooting

### Issue 1: Git Pull Fails

**Error:**
```
✗ Git pull failed. Please resolve conflicts manually.
```

**Solution:**
```bash
# Check git status
git status

# Resolve conflicts manually
git stash
git pull --rebase origin main
git stash pop

# Then run update script again
./scripts/update.sh
```

---

### Issue 2: Health Check Fails

**Error:**
```
✗ Backend health check failed!
⚠ Rolling back backend...
```

**Solution:**
```bash
# Check logs
docker compose logs nofx

# Check if service is running
docker compose ps

# Manually restart
docker compose restart nofx

# Check health endpoint
curl http://localhost:8080/health
```

---

### Issue 3: Docker Build Fails

**Error:**
```
ERROR: failed to solve: process "/bin/sh -c go build" did not complete successfully
```

**Solution:**
```bash
# Clean Docker cache
docker system prune -a

# Rebuild with verbose output
docker compose build --no-cache --progress=plain nofx

# Check disk space
df -h
```

---

### Issue 4: Port Already in Use

**Error:**
```
Error: bind: address already in use
```

**Solution:**
```bash
# Find process using port
sudo lsof -i :8080

# Kill process
sudo kill -9 <PID>

# Or restart Docker
sudo systemctl restart docker
```

---

## Best Practices

### 1. **Test Before Production**
```bash
# On staging/test server first
./scripts/update.sh -f

# Verify everything works
curl http://localhost:8080/health
curl http://localhost:3000/health

# Then deploy to production
```

### 2. **Monitor After Updates**
```bash
# Watch logs for 5 minutes after update
docker compose logs -f --tail=100

# Check for errors
docker compose logs | grep -i error

# Monitor resource usage
docker stats
```

### 3. **Keep Backups**
```bash
# Backups are created automatically, but verify
ls -lh backups/

# Keep at least 7 days of backups
find backups/ -mtime +7 -delete
```

### 4. **Update During Low Traffic**
- Schedule updates during off-peak hours
- Avoid updating during active trading sessions
- Consider market hours and volatility

---

## Rollback Procedure

If update causes issues, rollback to previous version:

```bash
# 1. Stop services
docker compose down

# 2. Restore from backup
tar -xzf backups/pre_update_backup_YYYYMMDD_HHMMSS.tar.gz

# 3. Checkout previous git commit
git log --oneline  # Find previous commit hash
git checkout <previous-commit-hash>

# 4. Rebuild and restart
docker compose build --no-cache
docker compose up -d

# 5. Verify
docker compose ps
curl http://localhost:8080/health
```

---

## Performance Comparison

| Update Type | Build Time | Downtime | Use Case |
|-------------|------------|----------|----------|
| Frontend only | ~2-3 min | ~5 sec | UI changes |
| Backend only | ~5-7 min | ~10 sec | Logic changes |
| Both (full) | ~8-10 min | ~15 sec | Major updates |

---

## Security Considerations

### Protected Files

The script automatically preserves:
- `config.json` (API keys, secrets)
- `.env` (environment variables)

These files are **never** overwritten by git pull.

### Backup Security

Backups contain sensitive data:
```bash
# Set proper permissions
chmod 600 backups/*.tar.gz

# Encrypt backups (optional)
gpg -c backups/pre_update_backup_*.tar.gz
```

---

## Integration with CI/CD

### GitHub Actions Example

```yaml
name: Deploy to Production

on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Deploy to server
        uses: appleboy/ssh-action@master
        with:
          host: ${{ secrets.SERVER_HOST }}
          username: ${{ secrets.SERVER_USER }}
          key: ${{ secrets.SSH_KEY }}
          script: |
            cd /path/to/nofx
            ./scripts/update.sh --all
```

---

## Monitoring & Alerts

### Post-Update Checks

```bash
# 1. Service health
curl http://localhost:8080/health
curl http://localhost:3000/health

# 2. Trading status
curl http://localhost:8080/api/status

# 3. Recent decisions
ls -lt decision_logs/ | head -5

# 4. Error logs
docker compose logs --since 10m | grep -i error
```

---

## Related Scripts

- **`scripts/deploy.sh`** - Initial deployment
- **`scripts/backup.sh`** - Manual backup
- **`scripts/health_check.sh`** - Health monitoring

---

## Support

For issues or questions:
1. Check logs: `docker compose logs -f`
2. Review this guide
3. Check main documentation: `DOCKER_PRODUCTION_GUIDE.md`
4. Open GitHub issue

---

**✅ Happy Updating!**

