#!/usr/bin/env sh
# Create a pushed two-repository checkout that is ready for `wtree init`.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
fixture_dir=${1:-$script_dir/init-fixture}
origins_dir=$fixture_dir/origins
project_dir=$fixture_dir/maintainer-app

die() {
	printf 'setup-init-fixture: %s\n' "$*" >&2
	exit 1
}

command -v git >/dev/null 2>&1 || die "git is required"

for path in "$origins_dir" "$project_dir"; do
	if [ -e "$path" ]; then
		die "$path already exists; move or remove it before recreating the fixture"
	fi
done

mkdir -p "$fixture_dir"
fixture_dir=$(CDPATH='' cd -- "$fixture_dir" && pwd)
origins_dir=$fixture_dir/origins
project_dir=$fixture_dir/maintainer-app
seed_dir=$(mktemp -d "${TMPDIR:-/tmp}/wtree-init-tutorial.XXXXXX")
complete=false

cleanup() {
	rm -rf "$seed_dir"
	if [ "$complete" = false ]; then
		rm -rf "$origins_dir" "$project_dir"
	fi
}

trap cleanup EXIT HUP INT TERM

create_origin() {
	name=$1
	seed=$seed_dir/$name
	origin=$origins_dir/$name.git
	mkdir -p "$seed"
	git -C "$seed" init -q
	git -C "$seed" checkout -q -b main
	git -C "$seed" config user.name "Wtree Tutorial"
	git -C "$seed" config user.email "tutorial@wtree.invalid"
	case "$name" in
		maintainer-app)
			cat > "$seed/README.md" <<'EOF'
# Maintainer App

This checkout demonstrates publishing a portable project with `wtree init`.
EOF
			cat > "$seed/.gitignore" <<'EOF'
/build/
EOF
			;;
		shared-library)
			cat > "$seed/library.go" <<'EOF'
package library

const Name = "shared-library"
EOF
			;;
	esac
	git -C "$seed" add .
	git -C "$seed" commit -q -m "Initialize $name"
	git init -q --bare "$origin"
	git -C "$origin" symbolic-ref HEAD refs/heads/main
	git -C "$seed" remote add origin "$origin"
	git -C "$seed" push -q -u origin main
}

mkdir -p "$origins_dir"
create_origin maintainer-app
create_origin shared-library

git clone -q "$origins_dir/maintainer-app.git" "$project_dir"
git clone -q "$origins_dir/shared-library.git" "$project_dir/library"
git -C "$project_dir" config user.name "Wtree Tutorial"
git -C "$project_dir" config user.email "tutorial@wtree.invalid"
git -C "$project_dir/library" config user.name "Wtree Tutorial"
git -C "$project_dir/library" config user.email "tutorial@wtree.invalid"

complete=true
printf '%s\n' \
	"Created the init fixture in $fixture_dir" \
	"" \
	"Project checkout: $project_dir" \
	"Nested checkout:  $project_dir/library" \
	"Bare origins:     $origins_dir"
