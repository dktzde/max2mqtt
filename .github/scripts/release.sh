#!/usr/bin/env bash
# Publishes a GitHub pre-release for the app version in max2mqtt/config.yaml,
# with its entry from max2mqtt/CHANGELOG.md as the notes, unless that release
# exists. Home Assistant does not need releases (the Supervisor builds the app
# from main); they show the version on the repository page and notify people
# who watch the repository for releases.
#
# Always a pre-release: the bridge drives real radiator valves, so the
# maintainer turns it into a full release ("latest") by hand after a test on
# the device.
#
# Usage: release.sh <commit>   tag the given commit (on main) as v<version>
# With DRY_RUN=1 it only prints what it would publish.
# Needs GH_TOKEN and GH_REPO for gh.
set -euo pipefail
target=$1

version=$(awk '/^version:/ { gsub(/"/, "", $2); print $2; exit }' max2mqtt/config.yaml)
tag="v$version"

if gh release view "$tag" > /dev/null 2>&1; then
  echo "Release $tag exists already."
  exit 0
fi

# The entry under "## <version>" up to the next "## " heading, without the
# heading itself (the release title is the version)
notes=$(awk -v heading="## $version" '
  $0 == heading { found = 1; next }
  found && /^## / { exit }
  found { print }
' max2mqtt/CHANGELOG.md | sed -e '/./,$!d')
if [ -z "$notes" ]; then
  echo "::error::No entry \"## $version\" in max2mqtt/CHANGELOG.md"
  exit 1
fi
notes=$(printf '%s\n\n---\n\nPre-release: it becomes a full release after a test on the maintainer'"'"'s device. Home Assistant builds the app on your device from this repository and offers this version as an app update. There is nothing to download here.\n' "$notes")

if [ "${DRY_RUN:-}" = 1 ]; then
  printf 'Would publish pre-release %s on %s with these notes:\n\n%s\n' "$tag" "$target" "$notes"
  exit 0
fi

gh release create "$tag" --target "$target" --title "$version" --notes "$notes" --prerelease
echo "Published pre-release $tag."
