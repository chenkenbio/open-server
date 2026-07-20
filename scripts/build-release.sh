#!/bin/sh
set -eu

repository=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository"

version=${VERSION:-$(go run ./cmd/open-server -version 2>&1)}
version=${version#open-server }
case "$version" in
	""|*[!0-9A-Za-z.-]*)
		echo "invalid release version: $version" >&2
		exit 1
		;;
esac

release_dir=${RELEASE_DIR:-dist/v${version}}
update_root_links=${UPDATE_ROOT_LINKS:-1}
case "$update_root_links" in
	0|1) ;;
	*)
		echo "UPDATE_ROOT_LINKS must be 0 or 1" >&2
		exit 1
		;;
esac

prefix="open-server-v${version}"
ldflags="-s -w"
mkdir -p "$release_dir"

build() {
	goos=$1
	goarch=$2
	output=$3
	echo "Building $output"
	GOOS=$goos GOARCH=$goarch go build -trimpath -ldflags "$ldflags" -o "$output" ./cmd/open-server
}

link_latest() {
	target=$1
	link=$2
	echo "Linking $link -> $target"
	ln -sfn "$target" "$link"
}

build linux amd64 "${release_dir}/${prefix}-linux-amd64"
build linux arm64 "${release_dir}/${prefix}-linux-arm64"
build darwin amd64 "${release_dir}/${prefix}-darwin-amd64"
build darwin arm64 "${release_dir}/${prefix}-darwin-arm64"

if [ "$update_root_links" -eq 1 ]; then
	link_latest "${release_dir}/${prefix}-linux-amd64" "open-server-latest-linux-amd64"
	link_latest "${release_dir}/${prefix}-linux-arm64" "open-server-latest-linux-arm64"
	link_latest "${release_dir}/${prefix}-darwin-amd64" "open-server-latest-darwin-amd64"
	link_latest "${release_dir}/${prefix}-darwin-arm64" "open-server-latest-darwin-arm64"
else
	link_latest "${prefix}-linux-amd64" "${release_dir}/open-server-latest-linux-amd64"
	link_latest "${prefix}-linux-arm64" "${release_dir}/open-server-latest-linux-arm64"
	link_latest "${prefix}-darwin-amd64" "${release_dir}/open-server-latest-darwin-amd64"
	link_latest "${prefix}-darwin-arm64" "${release_dir}/open-server-latest-darwin-arm64"
fi

if [ -n "${CODESIGN_IDENTITY:-}" ]; then
	for artifact in "${release_dir}/${prefix}"-darwin-*; do
		echo "Signing $artifact"
		codesign --force --options runtime --timestamp --sign "$CODESIGN_IDENTITY" "$artifact"
	done
fi
