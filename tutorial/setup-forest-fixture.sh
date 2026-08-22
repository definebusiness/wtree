#!/usr/bin/env sh
# Create a pushed plain-root repository forest ready for `wtree init`.
set -eu

fixture_dir=${1:?usage: setup-forest-fixture.sh FIXTURE_DIR}
origins_dir=$fixture_dir/origins
logical_root=$fixture_dir/maintainer-forest
seed_dir=$(mktemp -d "${TMPDIR:-/tmp}/wtree-forest-tutorial.XXXXXX")
complete=false

cleanup() {
	rm -rf "$seed_dir"
	if [ "$complete" = false ]; then
		rm -rf "$origins_dir" "$logical_root"
	fi
}
trap cleanup EXIT HUP INT TERM

for path in "$origins_dir" "$logical_root"; do
	[ ! -e "$path" ] || {
		printf 'setup-forest-fixture: %s already exists\n' "$path" >&2
		exit 1
	}
done
mkdir -p "$origins_dir" "$logical_root/services" "$logical_root/clients"
fixture_dir=$(CDPATH='' cd -- "$fixture_dir" && pwd -P)
origins_dir=$fixture_dir/origins
logical_root=$fixture_dir/maintainer-forest

create_origin() {
	id=$1
	seed=$seed_dir/$id
	origin=$origins_dir/$id.git
	mkdir -p "$seed"
	git -C "$seed" init -q
	git -C "$seed" checkout -q -b main
	git -C "$seed" config user.name "Wtree Tutorial"
	git -C "$seed" config user.email "tutorial@wtree.invalid"
	printf '# %s\n' "$id" > "$seed/README.md"
	git -C "$seed" add README.md
	git -C "$seed" commit -q -m "Initialize $id"
	git init -q --bare "$origin"
	git -C "$origin" symbolic-ref HEAD refs/heads/main
	git -C "$seed" remote add origin "$origin"
	git -C "$seed" push -q -u origin main
}

for id in api shared web; do
	create_origin "$id"
done

git clone -q "$origins_dir/api.git" "$logical_root/services/api"
mkdir -p "$logical_root/services/api/components"
git clone -q "$origins_dir/shared.git" "$logical_root/services/api/components/shared"
git clone -q "$origins_dir/web.git" "$logical_root/clients/web"

for checkout in \
	"$logical_root/services/api" \
	"$logical_root/services/api/components/shared" \
	"$logical_root/clients/web"; do
	git -C "$checkout" config user.name "Wtree Tutorial"
	git -C "$checkout" config user.email "tutorial@wtree.invalid"
done

complete=true
printf '%s\n' "$logical_root"
