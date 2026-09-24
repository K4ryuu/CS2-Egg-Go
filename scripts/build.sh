#!/bin/bash
# KitsuneLab CS2 Docker Image Builder

set -euo pipefail

# repo root, regardless of cwd or symlinks (this script lives in scripts/)
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ---------------------------------------------
# Styling / Colors (auto-disabled for non-TTY)
# ---------------------------------------------
if [[ -t 1 ]] && [[ -z "${NO_COLOR:-}" ]]; then
    BOLD="\033[1m"; DIM="\033[2m"; UNDER="\033[4m"
    RED="\033[31m"; GREEN="\033[32m"; YELLOW="\033[33m"; BLUE="\033[34m"; MAGENTA="\033[35m"; CYAN="\033[36m"; GRAY="\033[90m"
    RESET="\033[0m"
else
    BOLD=""; DIM=""; UNDER=""; RED=""; GREEN=""; YELLOW=""; BLUE=""; MAGENTA=""; CYAN=""; GRAY=""; RESET=""
fi

log_info()    { echo -e "ℹ ${BOLD}${CYAN}INFO${RESET}  $*" >&2; }
log_ok()      { echo -e "✓ ${BOLD}${GREEN}DONE${RESET}  $*" >&2; }
log_warn()    { echo -e "⚠ ${BOLD}${YELLOW}WARN${RESET}  $*" >&2; }
log_error()   { echo -e "✗ ${BOLD}${RED}ERROR${RESET} $*" >&2; }
section()     { echo -e "\n${BOLD}${MAGENTA}==>${RESET} ${BOLD}$*${RESET}\n" >&2; }
headline()    {
    local title="$1"; shift || true
    echo -e "${BOLD}${BLUE}──────────────────────────────────────────────────────${RESET}"
    echo -e "${BOLD}${BLUE} ${title}${RESET}"
    echo -e "${BOLD}${BLUE}──────────────────────────────────────────────────────${RESET}"
    [[ $# -gt 0 ]] && echo -e "$*\n"
}

usage() {
    echo -e "${BOLD}KitsuneLab CS2 Docker Image Builder${RESET}"
    echo -e ""
    echo -e "${BOLD}Usage:${RESET}"
    echo -e "    ./scripts/build.sh [TAG] [options]"
    echo -e ""
    echo -e "${BOLD}Positional:${RESET}"
    echo -e "    TAG                 Docker tag to use (default: dev)"
    echo -e ""
    echo -e "${BOLD}Options:${RESET}"
    echo -e "    -t, --tag TAG       Explicitly set the tag (overrides positional)"
    echo -e "    -g, --ghcr          Push the image to GitHub Container Registry (ghcr.io)"
    echo -e "    -h, --help          Show this help and exit"
    echo -e ""
    echo -e "${BOLD}Examples:${RESET}"
    echo -e "    ./scripts/build.sh                    # build :dev"
    echo -e "    ./scripts/build.sh release            # build :release"
    echo -e "    ./scripts/build.sh -t 1.2.3 -g        # build :1.2.3 and push to GHCR"
}

# ---------------------------------------------
# Parse arguments
# ---------------------------------------------
TAG="dev"
PUBLISH_GHCR=false

POSITIONAL_TAG=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            usage; exit 0 ;;
        -t|--tag)
            [[ $# -lt 2 ]] && { log_error "Missing value for $1"; exit 1; }
            TAG="$2"; shift 2 ;;
        -g|--ghcr)
            PUBLISH_GHCR=true; shift ;;
        --)
            shift; break ;;
        -*)
            log_error "Unknown option: $1"; echo; usage; exit 1 ;;
        *)
            # First non-flag arg treated as positional TAG
            if [[ -z "$POSITIONAL_TAG" ]]; then
                POSITIONAL_TAG="$1"
            else
                log_warn "Ignoring extra positional argument: $1"
            fi
            shift ;;
    esac
done

if [[ -n "$POSITIONAL_TAG" ]]; then
    TAG="$POSITIONAL_TAG"
fi

# GitHub Container Registry configuration
# Note: GHCR requires lowercase repository names
GITHUB_REPO="k4ryuu/cs2-egg-go"
GHCR_IMAGE="ghcr.io/${GITHUB_REPO}"
GHCR_FULL="${GHCR_IMAGE}:${TAG}"

# Primary (and only) build target
FULL_IMAGE="${GHCR_FULL}"

headline "KitsuneLab CS2 Docker Image Builder" "Image: ${BOLD}${FULL_IMAGE}${RESET}"

# ---------------------------------------------
# Pre-flight checks
# ---------------------------------------------
section "Pre-flight checks"

if ! command -v docker >/dev/null 2>&1; then
    log_error "Docker is not installed or not in PATH"
    exit 1
fi
log_ok "Docker is available"

# Validate the Go tree before building (the image compiles it again, this just fails fast)
log_info "Validating Go sources..."
if ! (cd "$REPO_ROOT" && go vet ./... >/dev/null 2>&1); then
    log_error "go vet failed - fix errors before building"
    (cd "$REPO_ROOT" && go vet ./... 2>&1 | sed 's/^/  /' >&2)
    exit 1
fi

log_ok "Go sources validated"

# ---------------------------------------------
# Builder (buildx docker-container driver)
# ---------------------------------------------
# Images exported by Docker Desktop's containerd store carry hardlinks that an
# overlay2 dockerd on the node cannot register ("invalid hardlink target").
# BuildKit in a docker-container builder produces a plain OCI layer set and
# pushes straight from the builder, so the node-side pull works.
BUILDER="${BUILDX_BUILDER:-cs2-builder}"
BUILD_PLATFORM="${BUILD_PLATFORM:-linux/amd64}"

if ! docker buildx inspect "$BUILDER" >/dev/null 2>&1; then
    log_info "Creating buildx builder ${BUILDER}"
    docker buildx create --name "$BUILDER" --driver docker-container >/dev/null
fi
log_ok "Using buildx builder ${BUILDER} (${BUILD_PLATFORM})"

# ---------------------------------------------
# GHCR Login (if needed)
# ---------------------------------------------
ghcr_login() {
    log_info "Logging in to GitHub Container Registry..."

    # Check for GITHUB_TOKEN environment variable
    if [[ -n "${GITHUB_TOKEN:-}" ]] && [[ -n "${GITHUB_USER:-}" ]]; then
        log_info "Using GITHUB_TOKEN from environment"
        echo "$GITHUB_TOKEN" | docker login ghcr.io -u "$GITHUB_USER" --password-stdin >/dev/null 2>&1
        if [[ $? -eq 0 ]]; then
            log_ok "Logged in to ghcr.io"
            return 0
        else
            log_error "Failed to login with GITHUB_TOKEN"
            return 1
        fi
    fi

    # Interactive login fallback
    log_warn "GITHUB_TOKEN not found in environment"
    log_info "You need a GitHub Personal Access Token with 'packages:write' scope"
    log_info "Create one at: https://github.com/settings/tokens/new?scopes=write:packages"
    echo ""

    read -p "GitHub username: " gh_user
    read -sp "GitHub token: " gh_token
    echo ""

    echo "$gh_token" | docker login ghcr.io -u "$gh_user" --password-stdin >/dev/null 2>&1
    if [[ $? -eq 0 ]]; then
        log_ok "Logged in to ghcr.io"
        return 0
    else
        log_error "Failed to login to ghcr.io"
        return 1
    fi
}

# Auth before the build: with --push the builder uploads as part of the build
GHCR_NEW_LOGIN=false
if [[ "$PUBLISH_GHCR" == true ]]; then
    if [[ -f "${HOME}/.docker/config.json" ]] && grep -q "ghcr.io" "${HOME}/.docker/config.json" 2>/dev/null; then
        true
    else
        if ! ghcr_login; then
            log_error "Cannot push to GHCR without authentication"
            exit 1
        fi
        GHCR_NEW_LOGIN=true
    fi
fi

# ---------------------------------------------
# Build (+ push)
# ---------------------------------------------
section "Building Docker image"
# context is the repo root: the Dockerfile compiles cmd/cs2egg from source
pushd "$REPO_ROOT" >/dev/null
VERSION=$(tr -d '[:space:]' < VERSION 2>/dev/null || echo 0.0.0-dev)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo none)
CHANNEL="stable"; [[ "$TAG" != "stable" ]] && CHANNEL="$TAG"

run_with_spinner() {
    local label="$1"; shift
    local cmd=("$@")
    local spin=('⠋' '⠙' '⠹' '⠸' '⠼' '⠴' '⠦' '⠧' '⠇' '⠏')
    local i=0
    local start_ts=$(date +%s)
    local log_file="/tmp/build.$$.$RANDOM.log"
    "${cmd[@]}" >"$log_file" 2>&1 &
    local pid=$!
    printf "${BOLD}${MAGENTA}%s${RESET}\n" "$label"
    while kill -0 $pid 2>/dev/null; do
        printf "\r${CYAN}%s${RESET} ${DIM}%s${RESET}" "${spin[$i]}" "$label"
        i=$(((i+1)%${#spin[@]}))
        sleep 0.12
    done
    wait $pid
    local ec=$?
    local end_ts=$(date +%s)
    local dur=$((end_ts-start_ts))
    printf "\r" # clear spinner line
    if [ $ec -eq 0 ]; then
        log_ok "${label} finished in ${dur}s"
    else
        log_error "${label} failed after ${dur}s (exit $ec)"
        echo "${BOLD}Last 40 lines:${RESET}" >&2
        tail -n 40 "$log_file" >&2 || true
        exit $ec
    fi
    BUILD_LAST_LOG="$log_file"
}

# Every rebuild of a tag leaves the previous image behind as <none>:<none> (no repo to
# match on), so prune dangling images the standard way. Tagged images are never touched.
cleanup_untagged() {
    local out
    out=$(docker image prune -f 2>/dev/null | tail -n 1)
    [[ "$out" == *"Total reclaimed space: 0B"* ]] || log_ok "Pruned dangling images (${out#Total reclaimed space: } reclaimed)"
    return 0
}

# tags: what was asked to be published, or the hub name for a local build
TAGS=()
[[ "$PUBLISH_GHCR" == true ]] && TAGS+=(-t "$GHCR_FULL")
[[ ${#TAGS[@]} -eq 0 ]]       && TAGS=(-t "$GHCR_FULL")
TARGETS=$(printf '%s ' "${TAGS[@]}" | sed 's/-t //g')
BUILD_ARGS=(--builder "$BUILDER" --platform "$BUILD_PLATFORM" -f docker/KitsuneLab-Dockerfile
            --build-arg "VERSION=$VERSION" --build-arg "CHANNEL=$CHANNEL" --build-arg "COMMIT=$COMMIT"
            --build-arg "BUILT=$(date -u +%Y-%m-%dT%H:%MZ)" "${TAGS[@]}")

if [[ "$PUBLISH_GHCR" == true ]]; then
    run_with_spinner "Building + pushing ${TARGETS}" docker buildx build "${BUILD_ARGS[@]}" --push .
    [[ "$GHCR_NEW_LOGIN" == true ]] && log_info "Credentials saved to: ${HOME}/.docker/config.json"
else
    run_with_spinner "Building ${FULL_IMAGE}" docker buildx build "${BUILD_ARGS[@]}" --load .
    size=$(docker image inspect "$FULL_IMAGE" -f '{{.Size}}' 2>/dev/null || echo 0)
    if [[ "$size" =~ ^[0-9]+$ ]] && [ "$size" -gt 0 ]; then
        human_size=$(awk -v s="$size" 'BEGIN{u[1]="B";u[2]="KB";u[3]="MB";u[4]="GB";u[5]="TB";i=1;while(s>1024&&i<5){s/=1024;i++}printf("%.2f %s",s,u[i])}')
        log_ok "Built ${BOLD}${FULL_IMAGE}${RESET} (${human_size})"
    fi
    section "Next steps"
    echo -e "To publish, rerun with ${BOLD}-g${RESET} (push to GHCR):"
    echo -e "  ${BOLD}./scripts/build.sh ${TAG} -g${RESET}"
    echo -e "\n${DIM}The buildx builder pushes directly; a locally loaded image is not re-pushed.${RESET}"
fi

popd >/dev/null
cleanup_untagged

exit 0
