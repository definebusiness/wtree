#!/usr/bin/env sh
# Create bare origins and a portable Acme Shop manifest for the tutorial.
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
fixture_dir=${1:-$script_dir}
origins_dir=$fixture_dir/origins
worktrees_dir=$fixture_dir/worktrees
manifest_path=$fixture_dir/project.wtree.yml

die() {
	printf 'setup-fixture: %s\n' "$*" >&2
	exit 1
}

command -v git >/dev/null 2>&1 || die "git is required"

for path in "$origins_dir" "$worktrees_dir" "$manifest_path"; do
	if [ -e "$path" ]; then
		die "$path already exists; move or remove it before recreating the fixture"
	fi
done

mkdir -p "$fixture_dir"
fixture_dir=$(CDPATH='' cd -- "$fixture_dir" && pwd)
origins_dir=$fixture_dir/origins
worktrees_dir=$fixture_dir/worktrees
manifest_path=$fixture_dir/project.wtree.yml
mkdir -p "$origins_dir" "$worktrees_dir"
seed_dir=$(mktemp -d "${TMPDIR:-/tmp}/wtree-tutorial.XXXXXX")
complete=false

cleanup() {
	rm -rf "$seed_dir"
	if [ "$complete" = false ]; then
		rm -rf "$origins_dir" "$worktrees_dir"
		rm -f "$manifest_path"
	fi
}

trap cleanup EXIT HUP INT TERM

init_seed() {
	repository=$1
	mkdir -p "$seed_dir/$repository"
	git -C "$seed_dir/$repository" init -q
	git -C "$seed_dir/$repository" checkout -q -b main
	git -C "$seed_dir/$repository" config user.name "Wtree Tutorial"
	git -C "$seed_dir/$repository" config user.email "tutorial@wtree.invalid"
}

commit_all() {
	repository=$1
	message=$2
	git -C "$seed_dir/$repository" add .
	git -C "$seed_dir/$repository" commit -q -m "$message"
}

make_branch() {
	repository=$1
	branch=$2
	git -C "$seed_dir/$repository" checkout -q main
	git -C "$seed_dir/$repository" checkout -q -b "$branch"
}

publish_seed() {
	repository=$1
	origin_name=$2
	origin=$origins_dir/$origin_name.git
	git init -q --bare "$origin"
	git -C "$origin" symbolic-ref HEAD refs/heads/main
	git -C "$seed_dir/$repository" remote add origin "$origin"
	git -C "$seed_dir/$repository" push -q --all origin
}

# Parent repository: project documentation and local orchestration.
# Write this before initializing the root repository, so nested repositories are
# ignored from the first interaction with the root checkout.
mkdir -p "$seed_dir/acme-shop/docs"
cat > "$seed_dir/acme-shop/.gitignore" <<'EOF'
/.wtree.yml
/.wtree.yml.lock
/backend/
/frontend/
/api/
/server/
/web/
EOF
init_seed acme-shop
cat > "$seed_dir/acme-shop/README.md" <<'EOF'
# Acme Shop

Acme Shop is composed of an independent Java API and web frontend.

The backend listens on port 8080 and the frontend listens on port 3000.
EOF
cat > "$seed_dir/acme-shop/compose.yaml" <<'EOF'
services:
  backend:
    image: acme/java-backend:main
    ports:
      - "8080:8080"
  frontend:
    image: acme/web-frontend:main
    ports:
      - "3000:3000"
EOF
cat > "$seed_dir/acme-shop/docs/architecture.md" <<'EOF'
# Architecture

The browser application calls the Java API over HTTP. The repositories are
developed independently but released together as Acme Shop.
EOF
commit_all acme-shop "Initialize Acme Shop project"

make_branch acme-shop feature/customer-search
cat >> "$seed_dir/acme-shop/README.md" <<'EOF'

Customer search is available at `/customers?query=<name>`.
EOF
cat >> "$seed_dir/acme-shop/compose.yaml" <<'EOF'
    environment:
      CUSTOMER_SEARCH_LIMIT: "20"
EOF
cat >> "$seed_dir/acme-shop/docs/architecture.md" <<'EOF'

Customer searches flow from the frontend search form to the backend search
endpoint, which limits each response to 20 matches.
EOF
commit_all acme-shop "Document customer search"

make_branch acme-shop release/2026-q3
mkdir -p "$seed_dir/acme-shop/docs/releases"
cat > "$seed_dir/acme-shop/docs/releases/2026-q3.md" <<'EOF'
# 2026 Q3 release

This release promotes the customer API as version 2026.3.
EOF
sed 's|acme/java-backend:main|acme/java-backend:2026.3|' \
	"$seed_dir/acme-shop/compose.yaml" > "$seed_dir/acme-shop/compose.yaml.new"
mv "$seed_dir/acme-shop/compose.yaml.new" "$seed_dir/acme-shop/compose.yaml"
commit_all acme-shop "Prepare 2026 Q3 release"

make_branch acme-shop chore/structured-logging
cat >> "$seed_dir/acme-shop/README.md" <<'EOF'

Backend request logs are emitted as newline-delimited JSON under `logs/`.
EOF
cat >> "$seed_dir/acme-shop/compose.yaml" <<'EOF'
    volumes:
      - ./logs:/var/log/acme
EOF
commit_all acme-shop "Document structured backend logs"
publish_seed acme-shop acme-shop

# Java backend repository. Its checkout is deliberately named backend later.
init_seed java-backend
mkdir -p \
	"$seed_dir/java-backend/src/main/java/com/acme/shop/customer" \
	"$seed_dir/java-backend/src/test/java/com/acme/shop/customer"
cat > "$seed_dir/java-backend/pom.xml" <<'EOF'
<project>
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.acme.shop</groupId>
  <artifactId>java-backend</artifactId>
  <version>1.0.0</version>
</project>
EOF
mkdir -p "$seed_dir/java-backend/src/main/java/com/acme/shop"
cat > "$seed_dir/java-backend/src/main/java/com/acme/shop/Application.java" <<'EOF'
package com.acme.shop;

public final class Application {
    public static final String NAME = "Acme Shop API";
}
EOF
cat > "$seed_dir/java-backend/src/main/java/com/acme/shop/customer/CustomerController.java" <<'EOF'
package com.acme.shop.customer;

public final class CustomerController {
    private final CustomerService service = new CustomerService();

    public String customer(String id) {
        return service.findById(id);
    }
}
EOF
cat > "$seed_dir/java-backend/src/main/java/com/acme/shop/customer/CustomerService.java" <<'EOF'
package com.acme.shop.customer;

public final class CustomerService {
    public String findById(String id) {
        return "customer:" + id;
    }
}
EOF
cat > "$seed_dir/java-backend/src/test/java/com/acme/shop/customer/CustomerServiceTest.java" <<'EOF'
package com.acme.shop.customer;

public final class CustomerServiceTest {
    // Tutorial placeholder: findById should prefix the supplied identifier.
}
EOF
commit_all java-backend "Initialize customer API"

make_branch java-backend feature/customer-search
cat > "$seed_dir/java-backend/src/main/java/com/acme/shop/customer/CustomerSearchController.java" <<'EOF'
package com.acme.shop.customer;

public final class CustomerSearchController {
    private final CustomerSearchService service = new CustomerSearchService();

    public String search(String query) {
        return service.searchByName(query);
    }
}
EOF
cat > "$seed_dir/java-backend/src/main/java/com/acme/shop/customer/CustomerSearchService.java" <<'EOF'
package com.acme.shop.customer;

public final class CustomerSearchService {
    public String searchByName(String query) {
        return "matches:" + query;
    }
}
EOF
cat >> "$seed_dir/java-backend/src/main/java/com/acme/shop/customer/CustomerService.java" <<'EOF'

// The customer-search branch adds name-based lookup through CustomerSearchService.
EOF
cat >> "$seed_dir/java-backend/src/test/java/com/acme/shop/customer/CustomerServiceTest.java" <<'EOF'
// Tutorial placeholder: search results should preserve the query.
EOF
commit_all java-backend "Add customer search API"

make_branch java-backend release/2026-q3
sed 's|<version>1.0.0</version>|<version>2026.3</version>|' \
	"$seed_dir/java-backend/pom.xml" > "$seed_dir/java-backend/pom.xml.new"
mv "$seed_dir/java-backend/pom.xml.new" "$seed_dir/java-backend/pom.xml"
mkdir -p "$seed_dir/java-backend/src/main/resources"
cat > "$seed_dir/java-backend/src/main/resources/release.properties" <<'EOF'
release=2026.3
EOF
cat >> "$seed_dir/java-backend/src/main/java/com/acme/shop/Application.java" <<'EOF'

// Release metadata is exposed by the 2026 Q3 build.
EOF
commit_all java-backend "Set 2026 Q3 release version"

make_branch java-backend chore/structured-logging
mkdir -p "$seed_dir/java-backend/src/main/java/com/acme/shop/logging"
cat > "$seed_dir/java-backend/src/main/java/com/acme/shop/logging/LoggingConfiguration.java" <<'EOF'
package com.acme.shop.logging;

public final class LoggingConfiguration {
    public static final String FORMAT = "json";
}
EOF
cat >> "$seed_dir/java-backend/src/main/java/com/acme/shop/customer/CustomerController.java" <<'EOF'

// Requests are logged as structured JSON on this branch.
EOF
cat >> "$seed_dir/java-backend/src/test/java/com/acme/shop/customer/CustomerServiceTest.java" <<'EOF'
// Tutorial placeholder: customer requests should include a correlation ID.
EOF
commit_all java-backend "Add structured request logging"

make_branch java-backend hotfix/customer-timeout
cat >> "$seed_dir/java-backend/src/main/java/com/acme/shop/customer/CustomerService.java" <<'EOF'

// Upstream customer calls time out after two seconds on this branch.
EOF
cat >> "$seed_dir/java-backend/src/test/java/com/acme/shop/customer/CustomerServiceTest.java" <<'EOF'
// Regression: an unresponsive upstream must time out after two seconds.
EOF
commit_all java-backend "Apply customer service timeout"
publish_seed java-backend java-backend

# Web frontend repository.
init_seed web-frontend
mkdir -p "$seed_dir/web-frontend/src/api" "$seed_dir/web-frontend/src/components"
cat > "$seed_dir/web-frontend/package.json" <<'EOF'
{
  "name": "web-frontend",
  "version": "1.0.0",
  "private": true
}
EOF
cat > "$seed_dir/web-frontend/src/app.js" <<'EOF'
import { customer } from "./api/customers.js";
import { CustomerCard } from "./components/CustomerCard.js";

export async function renderCustomer(id) {
  return CustomerCard(await customer(id));
}
EOF
cat > "$seed_dir/web-frontend/src/api/customers.js" <<'EOF'
export async function customer(id) {
  return { id, name: `Customer ${id}` };
}
EOF
cat > "$seed_dir/web-frontend/src/components/CustomerCard.js" <<'EOF'
export function CustomerCard(customer) {
  return `<article>${customer.name}</article>`;
}
EOF
cat > "$seed_dir/web-frontend/src/styles.css" <<'EOF'
body {
  font-family: sans-serif;
}
EOF
commit_all web-frontend "Initialize customer frontend"

make_branch web-frontend feature/customer-search
cat > "$seed_dir/web-frontend/src/components/CustomerSearch.js" <<'EOF'
export function CustomerSearch(results) {
  return `<ul>${results.map(({ name }) => `<li>${name}</li>`).join("")}</ul>`;
}
EOF
cat >> "$seed_dir/web-frontend/src/api/customers.js" <<'EOF'

export async function searchCustomers(query) {
  return [{ id: "search-result", name: query }];
}
EOF
cat >> "$seed_dir/web-frontend/src/app.js" <<'EOF'

// The customer-search branch renders CustomerSearch results from the API.
EOF
cat >> "$seed_dir/web-frontend/src/styles.css" <<'EOF'

.search-results {
  display: grid;
  gap: 0.5rem;
}
EOF
commit_all web-frontend "Add customer search interface"

make_branch web-frontend experiment/dark-navigation
cat > "$seed_dir/web-frontend/src/components/DarkNavigation.js" <<'EOF'
export function DarkNavigation() {
  return '<nav class="dark-navigation">Acme Shop</nav>';
}
EOF
cat >> "$seed_dir/web-frontend/src/app.js" <<'EOF'

// The dark-navigation experiment replaces the standard page navigation.
EOF
cat >> "$seed_dir/web-frontend/src/styles.css" <<'EOF'

:root {
  --navigation-background: #171923;
  --navigation-foreground: #f7fafc;
}
EOF
commit_all web-frontend "Experiment with dark navigation"
publish_seed web-frontend web-frontend

# This represents the portable output produced by the project maintainer's
# fully configured `wtree init`. Commit it at the parent repository root, and
# also copy it beside the origins so the tutorial can exercise a local manifest
# source without creating a checkout in this fixture script.
git -C "$seed_dir/acme-shop" checkout -q main
root_initial_commit=$(git -C "$seed_dir/acme-shop" rev-list --max-parents=0 main)
backend_initial_commit=$(git -C "$seed_dir/java-backend" rev-list --max-parents=0 main)
frontend_initial_commit=$(git -C "$seed_dir/web-frontend" rev-list --max-parents=0 main)
cat > "$seed_dir/acme-shop/project.wtree.yml" <<EOF
version: 2
project:
    id: 8af7d31c-2cf3-4fc8-9e1d-f62d5d0a83c0
    name: acme-shop
    base_repository: root
repositories:
    backend:
        clone:
            remote: origin
            url: $origins_dir/java-backend.git
        upstream:
            branch: main
            remote: origin
            merge: refs/heads/main
        identity:
            initial_commits:
                - $backend_initial_commit
        parent: root
        mount: backend
        default_branch: main
    frontend:
        clone:
            remote: origin
            url: $origins_dir/web-frontend.git
        upstream:
            branch: main
            remote: origin
            merge: refs/heads/main
        identity:
            initial_commits:
                - $frontend_initial_commit
        parent: root
        mount: frontend
        default_branch: main
    root:
        clone:
            remote: origin
            url: $origins_dir/acme-shop.git
        upstream:
            branch: main
            remote: origin
            merge: refs/heads/main
        identity:
            initial_commits:
                - $root_initial_commit
        parent: ""
        mount: .
        default_branch: main
EOF
git -C "$seed_dir/acme-shop" add .gitignore project.wtree.yml
git -C "$seed_dir/acme-shop" commit -q -m "Publish portable wtree manifest"
git -C "$seed_dir/acme-shop" push -q origin main
cp "$seed_dir/acme-shop/project.wtree.yml" "$manifest_path"

complete=true
printf '%s\n' \
	"Created the Acme Shop fixture in $fixture_dir" \
	"" \
	"Portable manifest: $manifest_path" \
	"Bare origins:      $origins_dir" \
	"Worktree root:     $worktrees_dir" \
	"" \
	"No project checkout was created; use wtree clone in the tutorial."
