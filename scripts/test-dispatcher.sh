#!/bin/sh
# Tests bin/cache-ttl-tracker (the dispatcher) against a folder of fake
# releases served over file://. Needs curl. Run with `make test-dispatcher`.

set -u

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$(CDPATH= cd -- "$here/.." && pwd)

if ! command -v curl >/dev/null 2>&1; then
	echo "test-dispatcher: curl is required" >&2
	exit 1
fi

case "$(uname -s)" in
	Linux*)  os=linux ;;
	Darwin*) os=darwin ;;
	*) echo "test-dispatcher: skipped on $(uname -s)"; exit 0 ;;
esac
case "$(uname -m)" in
	x86_64|amd64)  arch=amd64 ;;
	arm64|aarch64) arch=arm64 ;;
	*) echo "test-dispatcher: skipped on $(uname -m)"; exit 0 ;;
esac
asset="cache-ttl-tracker-$os-$arch"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
plugin="$tmp/plugin"
rel="$tmp/releases"
mkdir -p "$plugin/bin" "$plugin/.claude-plugin"
cp "$root/bin/cache-ttl-tracker" "$plugin/bin/cache-ttl-tracker"
bin="$plugin/bin/$asset"

export CACHE_TTL_TRACKER_RELEASES_URL="file://$rel"

failures=0
check() { # name, expected, actual
	if [ "$2" = "$3" ]; then
		echo "ok   $1"
	else
		echo "FAIL $1: expected '$2', got '$3'"
		failures=$((failures + 1))
	fi
}

set_version() {
	printf '{\n  "name": "cache-ttl-tracker",\n  "version": "%s",\n  "description": "mentions a version in prose"\n}\n' "$1" \
		> "$plugin/.claude-plugin/plugin.json"
}

# A fake release asset is a script that prints which release it came from.
publish() { # tag
	mkdir -p "$rel/download/$1"
	printf '#!/bin/sh\necho "%s"\n' "$1" > "$rel/download/$1/$asset"
}
publish_latest() {
	mkdir -p "$rel/latest/download"
	printf '#!/bin/sh\necho "latest"\n' > "$rel/latest/download/$asset"
}
run() { sh "$plugin/bin/cache-ttl-tracker" 2>/dev/null; }

# 1. No binary: the release for the checkout's version is fetched and recorded.
set_version 1.0.1
publish v1.0.1
check "first run fetches the checkout's release" "v1.0.1" "$(run)"
check "version recorded beside the binary" "1.0.1" "$(cat "$bin.version")"

# 2. Same version: nothing is fetched (the release is gone, the binary still runs).
rm -rf "$rel"
check "same version runs the cached binary" "v1.0.1" "$(run)"

# 3. A new version moves the binary on.
set_version 1.0.2
publish v1.0.2
check "a newer version replaces the binary" "v1.0.2" "$(run)"

# 4. A release that is not published yet keeps the working binary.
set_version 1.0.3
rm -rf "$rel"
check "a missing release keeps the installed binary" "v1.0.2" "$(run)"
check "the failed fetch is recorded" "1.0.3" "$(cat "$bin.attempt")"

# 5. The failed fetch is not retried straight away, even once it exists.
publish v1.0.3
check "no retry within the hour" "v1.0.2" "$(run)"

# 6. After an hour it is retried.
touch -t 200001010000 "$bin.attempt"
check "retried once the attempt is old" "v1.0.3" "$(run)"
[ ! -e "$bin.attempt" ]; check "attempt marker cleared on success" "0" "$?"

# 7. A binary with no recorded version (an install from before this check) is replaced.
rm -f "$bin.version"
set_version 1.0.4
publish v1.0.4
check "an unversioned binary is replaced" "v1.0.4" "$(run)"

# 8. A local build records the version and is left alone.
printf '#!/bin/sh\necho "local"\n' > "$bin"
printf '1.0.4\n' > "$bin.version"
rm -rf "$rel"
check "a local build is never replaced" "local" "$(run)"

# 9. No binary and the exact release missing: any latest release is better than none.
rm -f "$bin" "$bin.version" "$bin.attempt"
publish_latest
set_version 9.9.9
check "first run falls back to the latest release" "latest" "$(run)"
check "a fallback is not recorded as the right version" "no" "$([ -e "$bin.version" ] && echo yes || echo no)"
check "the exact release is looked for later" "9.9.9" "$(cat "$bin.attempt")"

# 10. No binary and nothing to download: fail loudly.
rm -rf "$rel"
rm -f "$bin" "$bin.version" "$bin.attempt"
sh "$plugin/bin/cache-ttl-tracker" >/dev/null 2>&1
check "no binary and no release is an error" "1" "$?"

# 11. A manifest that cannot be read switches the check off.
printf '#!/bin/sh\necho "kept"\n' > "$bin"
chmod +x "$bin"
rm -f "$plugin/.claude-plugin/plugin.json"
check "no manifest, no version check" "kept" "$(run)"

if [ "$failures" -ne 0 ]; then
	echo "$failures failed"
	exit 1
fi
echo "all passed"
