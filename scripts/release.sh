#!/bin/bash
# Cuts a GitHub Release for the latest CHANGELOG.md entry.
#
# Ownership split with .github/workflows/release.yml (so the binary is never
# uploaded twice): this script only builds cs2node LOCALLY as a fail-fast
# smoke test and creates the (asset-less) release. Publishing the release
# fires release.yml's `release: published` trigger, which is the sole owner
# of the actual build + checksums + upload + download-verify round trip that
# ships to users. Do not add an asset upload here without removing it there.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHANGELOG="$REPO_ROOT/CHANGELOG.md"

if [[ -t 1 ]] && [[ -z "${NO_COLOR:-}" ]]; then
    BOLD="\033[1m"; RED="\033[31m"; GREEN="\033[32m"; YELLOW="\033[33m"; CYAN="\033[36m"; RESET="\033[0m"
else
    BOLD=""; RED=""; GREEN=""; YELLOW=""; CYAN=""; RESET=""
fi

log_info() { echo -e "ℹ ${BOLD}${CYAN}INFO${RESET}  $*" >&2; }
log_ok()   { echo -e "✓ ${BOLD}${GREEN}DONE${RESET}  $*" >&2; }
log_warn() { echo -e "⚠ ${BOLD}${YELLOW}WARN${RESET} $*" >&2; }
log_error(){ echo -e "✗ ${BOLD}${RED}ERROR${RESET} $*" >&2; }

usage() {
    echo -e "${BOLD}Usage:${RESET} ./scripts/release.sh [--prerelease] [--dry-run]"
    echo -e ""
    echo -e "Reads the top entry of CHANGELOG.md, builds cs2node locally as a smoke test,"
    echo -e "then publishes a GitHub Release for vX.Y.Z targeting HEAD, with those notes."
    echo -e "GitHub Actions then builds and attaches the real binary + checksums."
    echo -e ""
    echo -e "    --prerelease   mark the release as a pre-release (beta channel)"
    echo -e "    --dry-run      show what would be released, touch nothing (no gh needed)"
}

PRERELEASE=false
DRY_RUN=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --prerelease) PRERELEASE=true; shift ;;
        --dry-run)    DRY_RUN=true; shift ;;
        -h|--help)    usage; exit 0 ;;
        *) log_error "Unknown option: $1"; echo; usage; exit 1 ;;
    esac
done

[[ -f "$CHANGELOG" ]] || { log_error "CHANGELOG.md not found at $CHANGELOG"; exit 1; }

# ---------------------------------------------
# Extract the latest version + its notes from CHANGELOG.md
# ---------------------------------------------
VERSION=$(grep -m1 -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?\]' "$CHANGELOG" \
    | grep -oE '[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?')
[[ -n "$VERSION" ]] || { log_error "No '## [X.Y.Z]' entry found at the top of CHANGELOG.md"; exit 1; }
TAG="v${VERSION}"
NOTES=$(awk '/^## \[/ { if (found) exit; found=1; next } found { print }' "$CHANGELOG")
HEAD_SHA=$(git -C "$REPO_ROOT" rev-parse HEAD)

log_ok "Latest CHANGELOG entry: ${BOLD}${VERSION}${RESET} (tag ${TAG} @ ${HEAD_SHA:0:7})"

# a prerelease-looking version not marked as one would install itself on every
# stable node once release.yml builds it (same guard release.yml enforces)
if [[ "$PRERELEASE" == false ]] && [[ "$VERSION" == *-* ]]; then
    log_error "${VERSION} looks like a prerelease but --prerelease was not passed"
    exit 1
fi

if $DRY_RUN; then
    log_info "Dry run, notes preview:"
    echo "---"
    echo "$NOTES"
    echo "---"
    log_info "Would build dist/cs2node (smoke test only), then run:"
    log_info "  gh release create ${TAG} --title ${TAG} --notes <above> --target ${HEAD_SHA}$($PRERELEASE && echo ' --prerelease')"
    exit 0
fi

command -v gh >/dev/null 2>&1 || { log_error "gh (GitHub CLI) is not installed or not in PATH"; exit 1; }

if gh release view "$TAG" >/dev/null 2>&1; then
    log_error "Release ${TAG} already exists"
    exit 1
fi

# ---------------------------------------------
# Local smoke build (fail fast, never uploaded, release.yml owns the real one)
# ---------------------------------------------
log_info "Building cs2node locally as a pre-flight check..."
CHANNEL="stable"; $PRERELEASE && CHANNEL="beta"
(cd "$REPO_ROOT" && make node VERSION="$VERSION" CHANNEL="$CHANNEL" >/dev/null)
# cross-compiled linux/amd64, not run locally, just confirmed it exists
BIN_SIZE=$(du -h "$REPO_ROOT/dist/cs2node" | cut -f1)
log_ok "Local build OK (dist/cs2node, ${BIN_SIZE})"

# ---------------------------------------------
# Release (no assets: release.yml attaches the real ones on publish).
# No tag exists yet, so `gh release create` cuts it from --target, the exact
# commit this script ran against, not whatever the default branch happens to
# be on GitHub.
# ---------------------------------------------
RELEASE_ARGS=(release create "$TAG" --title "$TAG" --notes "$NOTES" --target "$HEAD_SHA")
$PRERELEASE && RELEASE_ARGS+=(--prerelease)

log_info "Creating GitHub Release ${TAG} at ${HEAD_SHA:0:7}..."
gh "${RELEASE_ARGS[@]}"
log_ok "Release ${TAG} published, GitHub Actions will build and attach cs2node_linux_amd64 + checksums.txt"
