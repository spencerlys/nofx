#!/bin/bash

# ═══════════════════════════════════════════════════════════════
# NOFX Update Script
# Safely updates the NOFX system with selective rebuild options
# Usage: ./update.sh [options]
#   -f, --frontend    Rebuild frontend only
#   -b, --backend     Rebuild backend only
#   -a, --all         Rebuild both frontend and backend (default)
#   -h, --help        Show this help message
# ═══════════════════════════════════════════════════════════════

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Default options
REBUILD_FRONTEND=false
REBUILD_BACKEND=false
REBUILD_ALL=false

print_header() {
    echo -e "\n${BLUE}═══════════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════════${NC}\n"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠ $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ $1${NC}"
}

show_help() {
    echo "NOFX Update Script"
    echo ""
    echo "Usage: $0 [options]"
    echo ""
    echo "Options:"
    echo "  -f, --frontend    Rebuild frontend only"
    echo "  -b, --backend     Rebuild backend only"
    echo "  -a, --all         Rebuild both frontend and backend (default)"
    echo "  -h, --help        Show this help message"
    echo ""
    echo "Examples:"
    echo "  $0                # Rebuild both (default)"
    echo "  $0 -f             # Rebuild frontend only"
    echo "  $0 -b             # Rebuild backend only"
    echo "  $0 --all          # Rebuild both explicitly"
    exit 0
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -f|--frontend)
            REBUILD_FRONTEND=true
            shift
            ;;
        -b|--backend)
            REBUILD_BACKEND=true
            shift
            ;;
        -a|--all)
            REBUILD_ALL=true
            shift
            ;;
        -h|--help)
            show_help
            ;;
        *)
            print_error "Unknown option: $1"
            echo "Use -h or --help for usage information"
            exit 1
            ;;
    esac
done

# If no options specified, rebuild all
if [ "$REBUILD_FRONTEND" = false ] && [ "$REBUILD_BACKEND" = false ] && [ "$REBUILD_ALL" = false ]; then
    REBUILD_ALL=true
fi

# If --all is specified, enable both
if [ "$REBUILD_ALL" = true ]; then
    REBUILD_FRONTEND=true
    REBUILD_BACKEND=true
fi

print_header "NOFX Update Script"

# Show what will be rebuilt
echo -e "${BLUE}Rebuild Plan:${NC}"
if [ "$REBUILD_FRONTEND" = true ]; then
    echo -e "  ${GREEN}✓${NC} Frontend"
else
    echo -e "  ${YELLOW}○${NC} Frontend (skipped)"
fi
if [ "$REBUILD_BACKEND" = true ]; then
    echo -e "  ${GREEN}✓${NC} Backend"
else
    echo -e "  ${YELLOW}○${NC} Backend (skipped)"
fi
echo ""

# Step 1: Backup current state
print_info "Creating backup before update..."
BACKUP_DIR="backups"
mkdir -p "$BACKUP_DIR"
BACKUP_FILE="$BACKUP_DIR/pre_update_backup_$(date +%Y%m%d_%H%M%S).tar.gz"

tar -czf "$BACKUP_FILE" \
    config.json \
    decision_logs/ \
    coin_pool_cache/ \
    .env 2>/dev/null || true

print_success "Backup created: $BACKUP_FILE"

# Step 2: Pull latest code from Git
if [ -d ".git" ]; then
    print_info "Pulling latest code from repository..."

    # Get current branch
    CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
    print_info "Current branch: $CURRENT_BRANCH"

    # Show current commit
    CURRENT_COMMIT=$(git rev-parse --short HEAD)
    print_info "Current commit: $CURRENT_COMMIT"

    # Stash any local changes (except config.json and .env)
    print_info "Stashing local changes (preserving config.json and .env)..."
    git stash push -u -m "Auto-stash before update $(date +%Y%m%d_%H%M%S)" -- ':!config.json' ':!.env'

    # Pull latest changes
    print_info "Fetching latest changes..."
    git fetch origin

    # Pull with rebase to avoid merge commits
    if git pull --rebase origin "$CURRENT_BRANCH"; then
        NEW_COMMIT=$(git rev-parse --short HEAD)
        if [ "$CURRENT_COMMIT" != "$NEW_COMMIT" ]; then
            print_success "Code updated successfully: $CURRENT_COMMIT → $NEW_COMMIT"

            # Show what changed
            print_info "Recent changes:"
            git log --oneline --decorate --graph -5
        else
            print_success "Already up to date (no new commits)"
        fi
    else
        print_error "Git pull failed. Please resolve conflicts manually."
        exit 1
    fi
else
    print_warning "Not a git repository. Skipping code update."
    print_warning "Make sure you have the latest code before proceeding."
    read -p "Continue anyway? (y/n) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Step 3: Check for config changes
if [ -f "config.json.example" ]; then
    print_info "Checking for new configuration options..."

    # Compare config files (just notify, don't auto-update)
    if ! diff -q config.json config.json.example > /dev/null 2>&1; then
        print_warning "config.json.example has changed. Please review for new options."
        print_info "Run: diff config.json config.json.example"
        echo ""
    fi
fi

# Step 4: Rebuild Docker images based on options
SERVICES_TO_BUILD=""

if [ "$REBUILD_BACKEND" = true ]; then
    SERVICES_TO_BUILD="$SERVICES_TO_BUILD nofx"
fi

if [ "$REBUILD_FRONTEND" = true ]; then
    SERVICES_TO_BUILD="$SERVICES_TO_BUILD nofx-frontend"
fi

if [ -n "$SERVICES_TO_BUILD" ]; then
    print_info "Rebuilding Docker images for:$SERVICES_TO_BUILD"
    docker compose build --no-cache $SERVICES_TO_BUILD
    print_success "Docker images rebuilt successfully"
else
    print_warning "No services selected for rebuild"
    exit 0
fi

# Step 5: Restart services with zero downtime
print_info "Restarting services..."

# Restart only the services that were rebuilt
for service in $SERVICES_TO_BUILD; do
    print_info "Restarting $service..."
    docker compose up -d --no-deps $service
done

# Step 6: Wait for services to be healthy
print_info "Waiting for services to start..."
sleep 5

# Check backend health (if backend was rebuilt)
if [ "$REBUILD_BACKEND" = true ]; then
    print_info "Checking backend health..."
    BACKEND_HEALTHY=false
    for i in {1..30}; do
        if curl -f -s http://localhost:8080/health > /dev/null 2>&1; then
            BACKEND_HEALTHY=true
            break
        fi
        echo -n "."
        sleep 2
    done
    echo ""

    if [ "$BACKEND_HEALTHY" = true ]; then
        print_success "Backend is healthy"
    else
        print_error "Backend health check failed!"
        print_warning "Rolling back backend..."
        docker compose restart nofx
        print_error "Update failed. Check logs: docker compose logs nofx"
        exit 1
    fi
fi

# Check frontend health (if frontend was rebuilt)
if [ "$REBUILD_FRONTEND" = true ]; then
    print_info "Checking frontend health..."
    FRONTEND_HEALTHY=false
    for i in {1..30}; do
        if curl -f -s http://localhost:3000/health > /dev/null 2>&1; then
            FRONTEND_HEALTHY=true
            break
        fi
        echo -n "."
        sleep 2
    done
    echo ""

    if [ "$FRONTEND_HEALTHY" = true ]; then
        print_success "Frontend is healthy"
    else
        print_warning "Frontend health check failed. Check logs: docker compose logs nofx-frontend"
        print_info "Frontend may still be starting up. Monitor with: docker compose logs -f nofx-frontend"
    fi
fi

# Step 7: Clean up old Docker images
print_info "Cleaning up old Docker images..."
PRUNED=$(docker image prune -f 2>&1 | grep "Total reclaimed space" || echo "No space reclaimed")
print_success "Cleanup complete: $PRUNED"

# Step 8: Display status
print_header "Update Status"

echo -e "${BLUE}Services Status:${NC}"
docker compose ps

echo ""
echo -e "${BLUE}Resource Usage:${NC}"
docker stats --no-stream --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}" $SERVICES_TO_BUILD

# Step 9: Display version info
if [ -d ".git" ]; then
    echo ""
    echo -e "${BLUE}Version Information:${NC}"
    echo -e "${GREEN}Current commit:${NC} $(git rev-parse --short HEAD)"
    echo -e "${GREEN}Branch:${NC} $(git rev-parse --abbrev-ref HEAD)"
    echo -e "${GREEN}Last commit:${NC} $(git log -1 --pretty=%B | head -1)"
    echo -e "${GREEN}Author:${NC} $(git log -1 --pretty=%an)"
    echo -e "${GREEN}Date:${NC} $(git log -1 --pretty=%ar)"
fi

print_success "Update completed successfully!"

# Step 10: Show what was updated
echo ""
echo -e "${BLUE}Updated Components:${NC}"
if [ "$REBUILD_BACKEND" = true ]; then
    echo -e "  ${GREEN}✓${NC} Backend (nofx)"
fi
if [ "$REBUILD_FRONTEND" = true ]; then
    echo -e "  ${GREEN}✓${NC} Frontend (nofx-frontend)"
fi

# Step 11: Provide monitoring commands
echo ""
print_info "Useful monitoring commands:"
echo "  ${YELLOW}View all logs:${NC}        docker compose logs -f"
if [ "$REBUILD_BACKEND" = true ]; then
    echo "  ${YELLOW}View backend logs:${NC}    docker compose logs -f nofx"
fi
if [ "$REBUILD_FRONTEND" = true ]; then
    echo "  ${YELLOW}View frontend logs:${NC}   docker compose logs -f nofx-frontend"
fi
echo "  ${YELLOW}Check status:${NC}         docker compose ps"
echo "  ${YELLOW}Check resources:${NC}      docker stats"
echo ""

# Step 12: Backup reminder
if [ -f "scripts/backup.sh" ]; then
    print_info "Reminder: Consider creating a backup after verifying the update"
    echo "  ./scripts/backup.sh"
fi

