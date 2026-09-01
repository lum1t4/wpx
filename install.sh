#!/bin/sh
# The bootstrap intentionally does only three things: choose an architecture,
# download an immutable release, and verify its checksum. Versioned installation
# logic lives in the WPX binary where it can be tested with the rest of the code.
set -eu

repository=${WPX_REPOSITORY:-lum1t4/wpx}
release_base=${WPX_RELEASE_BASE:-"https://github.com/${repository}/releases/latest/download"}

case "$(uname -s)" in
  Linux) ;;
  *) echo "WPX currently supports Linux only." >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  aarch64|arm64) architecture=arm64 ;;
  *) echo "WPX supports amd64 and arm64; detected $(uname -m)." >&2; exit 1 ;;
esac

if [ "$(id -u)" -ne 0 ]; then
  echo "Run the installer as root, for example: curl -fsSL https://github.com/${repository}/releases/latest/download/install.sh | sudo sh" >&2
  exit 1
fi

temporary_directory=$(mktemp -d)
trap 'rm -rf "$temporary_directory"' EXIT HUP INT TERM
binary="wpx-linux-${architecture}"

curl -fsSL --proto '=https' --tlsv1.2 "${release_base}/${binary}" -o "${temporary_directory}/${binary}"
curl -fsSL --proto '=https' --tlsv1.2 "${release_base}/checksums.txt" -o "${temporary_directory}/checksums.txt"

expected=$(awk -v name="$binary" '$2 == name { print $1 }' "${temporary_directory}/checksums.txt")
if [ -z "$expected" ]; then
  echo "The release manifest does not contain ${binary}." >&2
  exit 1
fi
actual=$(sha256sum "${temporary_directory}/${binary}" | awk '{ print $1 }')
if [ "$actual" != "$expected" ]; then
  echo "The WPX binary checksum does not match the release manifest." >&2
  exit 1
fi

chmod 0755 "${temporary_directory}/${binary}"
operation=install
if [ -f /etc/wpx/config.json ] && [ -x /usr/local/bin/wpx ] && [ ! -f /var/lib/wpx/.installing ]; then
  operation=upgrade
fi
"${temporary_directory}/${binary}" "$operation" "$@"
