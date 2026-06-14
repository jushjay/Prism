#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEB_DIR="$ROOT_DIR/web"
NODE_MODULES_DIR="$WEB_DIR/node_modules"
STAMP_FILE="$NODE_MODULES_DIR/.vca-install-stamp"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/web-frontend.sh install
  ./scripts/web-frontend.sh dev [extra args...]
EOF
}

detect_package_manager() {
  if [[ -f "$WEB_DIR/pnpm-lock.yaml" ]]; then
    echo "pnpm"
    return
  fi

  if [[ -f "$WEB_DIR/package-lock.json" ]]; then
    echo "npm"
    return
  fi

  echo "npm"
}

resolve_pnpm() {
  if command -v pnpm >/dev/null 2>&1; then
    echo "pnpm"
    return
  fi

  if command -v corepack >/dev/null 2>&1; then
    echo "corepack pnpm"
    return
  fi

  echo "pnpm is required but was not found. Install pnpm or enable it with corepack." >&2
  exit 1
}

package_fingerprint() {
  local manager="$1"
  local lockfile=""

  case "$manager" in
    pnpm)
      lockfile="$WEB_DIR/pnpm-lock.yaml"
      ;;
    npm)
      lockfile="$WEB_DIR/package-lock.json"
      ;;
  esac

  if [[ -n "$lockfile" && -f "$lockfile" ]]; then
    sha256sum "$WEB_DIR/package.json" "$lockfile" | sha256sum | awk '{print $1 ":" "'"$manager"'"}'
    return
  fi

  sha256sum "$WEB_DIR/package.json" | sha256sum | awk '{print $1 ":" "'"$manager"'"}'
}

reset_frontend_artifacts() {
  echo "Removing stale frontend artifacts"
  rm -rf \
    "$WEB_DIR/node_modules" \
    "$WEB_DIR/dist" \
    "$WEB_DIR/.umi" \
    "$WEB_DIR/.umi-production" \
    "$WEB_DIR/.umi-test" \
    "$WEB_DIR/.mfsu" \
    "$WEB_DIR/.turbopack" \
    "$WEB_DIR/src/.umi" \
    "$WEB_DIR/src/.umi-production" \
    "$WEB_DIR/src/.umi-test"
}

ensure_dependencies() {
  local manager="$1"
  local fingerprint
  local previous=""

  fingerprint="$(package_fingerprint "$manager")"
  if [[ -f "$STAMP_FILE" ]]; then
    previous="$(<"$STAMP_FILE")"
  fi

  if [[ "$previous" == "$fingerprint" ]]; then
    return
  fi

  if [[ -d "$NODE_MODULES_DIR" ]]; then
    reset_frontend_artifacts
  fi

  echo "Installing frontend dependencies with $manager"
  case "$manager" in
    pnpm)
      (
        cd "$WEB_DIR"
        local pnpm_cmd
        pnpm_cmd="$(resolve_pnpm)"
        # shellcheck disable=SC2086
        $pnpm_cmd install --frozen-lockfile
      )
      ;;
    npm)
      (
        cd "$WEB_DIR"
        if [[ -f package-lock.json ]]; then
          npm ci
        else
          npm install
        fi
      )
      ;;
  esac

  mkdir -p "$NODE_MODULES_DIR"
  printf '%s\n' "$fingerprint" >"$STAMP_FILE"
}

run_dev() {
  local manager="$1"
  shift
  local host_value=""
  local port_value=""
  local passthrough=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --host)
        if [[ $# -lt 2 ]]; then
          echo "missing value for --host" >&2
          exit 1
        fi
        host_value="$2"
        shift 2
        ;;
      --port)
        if [[ $# -lt 2 ]]; then
          echo "missing value for --port" >&2
          exit 1
        fi
        port_value="$2"
        shift 2
        ;;
      *)
        passthrough+=("$1")
        shift
        ;;
    esac
  done

  (
    cd "$WEB_DIR"
    if [[ -n "$host_value" ]]; then
      export HOST="$host_value"
    fi
    if [[ -n "$port_value" ]]; then
      export PORT="$port_value"
    fi
    exec ./node_modules/.bin/max dev "${passthrough[@]}"
  )
}

main() {
  local command="${1:-}"
  if [[ -z "$command" ]]; then
    usage
    exit 1
  fi
  shift || true

  local manager
  manager="$(detect_package_manager)"

  case "$command" in
    install)
      ensure_dependencies "$manager"
      ;;
    dev)
      ensure_dependencies "$manager"
      run_dev "$manager" "$@"
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "$@"
