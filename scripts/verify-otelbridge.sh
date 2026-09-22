#!/usr/bin/env bash
# Run from Linux or WSL. Never commits, tags, or publishes. Manifest changes
# are rejected by the final checksum check, not silently accepted.
set -euo pipefail

mode=${1:-}
if [[ "$mode" != local && "$mode" != published ]]; then
  echo "Usage: bash scripts/verify-otelbridge.sh {local|published} [module-dir] [evidence-dir]" >&2
  exit 2
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
module_dir=$(cd "${2:-$repo_root/otelbridge}" && pwd)
evidence_dir=${3:-$repo_root/output/otel-verification/$mode}
mkdir -p "$evidence_dir"
evidence_dir=$(cd "$evidence_dir" && pwd)
scratch=$(mktemp -d)
trap 'rm -f "$scratch/local.mod" "$scratch/local.sum" "$scratch/manifests.sha256"; rmdir "$scratch"' EXIT

# Do not inherit a developer workspace, alternate manifest, overlay or vendoring
# flag. An intentional local replacement is made only in the local mode below.
export GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local
cd "$module_dir"
sha256sum go.mod go.sum > "$scratch/manifests.sha256"
flags=(-mod=readonly)
core_module=github.com/go-gen-ecosystem/halolog

if [[ "$mode" == local ]]; then
  cp go.mod "$scratch/local.mod"
  cp go.sum "$scratch/local.sum"
  go mod edit "-modfile=$scratch/local.mod" "-replace=$core_module=$repo_root"
  flags+=("-modfile=$scratch/local.mod")
else
  # Versioned dependencies only. A green local integration result is not a
  # substitute for this check against the module graph users can download.
  replacements=$(go list "${flags[@]}" -m -f '{{if .Replace}}{{.Path}} => {{.Replace.Path}}{{end}}' all)
  if [[ -n "$replacements" ]]; then
    printf 'Published verification rejects module replacements:\n%s\n' "$replacements" >&2
    exit 1
  fi
  version=$(go list "${flags[@]}" -m -f '{{.Version}}' "$core_module")
  if [[ "$version" != v* ]]; then
    echo "Published verification requires a versioned HaloLog core dependency" >&2
    exit 1
  fi
  go mod download -json "$core_module@$version" > "$evidence_dir/core-download.json"
  cat "$evidence_dir/core-download.json"
  go mod verify
fi

go version | tee "$evidence_dir/toolchain.txt"
go list "${flags[@]}" -m -json all > "$evidence_dir/modules.json"
go list "${flags[@]}" -m -json "$core_module" | tee "$evidence_dir/core-selected.json"
printf 'Verification mode: %s\n' "$mode"

# Make missing/renamed contract tests fail rather than silently passing an empty
# selection. This guard also applies to a future contributor adapter checkout.
tests=$(go test "${flags[@]}" -list '^(TestCoreDependency_|TestAdapter_AllocationBudgets)' .)
for required in TestCoreDependency_MaskingStorage TestCoreDependency_RegexPolicy TestCoreDependency_BindMasking TestAdapter_AllocationBudgets; do
  if ! grep -Fxq "$required" <<< "$tests"; then
    printf 'Missing mandatory consumer contract: %s\n' "$required" >&2
    exit 1
  fi
done
go vet "${flags[@]}" ./...
go test "${flags[@]}" -count=1 -json ./... | tee "$evidence_dir/tests.jsonl"
if grep -q '"Action":"skip"' "$evidence_dir/tests.jsonl"; then
  echo "Uninstrumented bridge verification must not skip tests" >&2
  exit 1
fi

# Race instrumentation changes allocation behavior. The normal run above owns
# allocation budgets; the timed scaling experiment also runs there, not here.
go test "${flags[@]}" -race -skip 'PipelineScale' -count=1 -json ./... | tee "$evidence_dir/race.jsonl"
if grep -q '"Action":"skip"' "$evidence_dir/race.jsonl"; then
  echo "Unexpected runtime Skip in the selected race tests" >&2
  exit 1
fi
sha256sum --check "$scratch/manifests.sha256"
