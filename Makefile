# Single definition of every check this project runs.
#
# CI calls these targets rather than restating the commands in YAML. That is
# the whole point: when the pipeline and the developer run different commands
# they drift, and "passes locally, red in CI" becomes normal. Here, `make ci`
# reproduces the pipeline exactly, so a red build can be debugged without
# pushing to find out.

GOVULNCHECK_VERSION ?= latest
IMAGE               ?= prebuilt
TAG                 ?= dev

.DEFAULT_GOAL := help
.PHONY: help run build test fmt fmt-check vet vuln smoke \
        check-docker-go docker-build docker-smoke ci clean

help: ## List targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk -F':.*?## ' '{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ─── Local development ────────────────────────────────────────────────────────

run: ## Start the dev server (:8080)
	go run .

build: ## Compile to ./prebuilt
	go build -o prebuilt .

fmt: ## Rewrite files with gofmt
	gofmt -w .

# ─── Checks ───────────────────────────────────────────────────────────────────

fmt-check: ## Fail if anything is unformatted
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt required for:"; echo "$$out"; exit 1; \
	fi; \
	echo "gofmt: clean"

vet: ## go vet
	go vet ./...

test: ## Unit tests
	go test ./...

# Reachability-based vulnerability scan. Reports a CVE only when the vulnerable
# symbol is actually callable from this code, which is why it is quiet enough
# to gate a build on. Sends module paths and versions to vuln.go.dev; no source
# leaves the machine.
vuln: ## govulncheck
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

smoke: ## End-to-end checks against a real binary (throwaway DB)
	./scripts/admin-smoke.sh

# Guards a bug this project actually shipped: `go get` silently raised the go
# directive from 1.22 to 1.25, and the Dockerfile was still building on
# golang:1.22-alpine. Nothing failed locally — the installed toolchain simply
# satisfied the higher directive — so it would have surfaced as a broken deploy.
# Compared at major.minor granularity, which is the precision the image tags
# carry.
check-docker-go: ## Dockerfile's Go must satisfy go.mod's directive
	@modv="$$(awk '/^go /{split($$2,a,"."); print a[1]"."a[2]; exit}' go.mod)"; \
	imgv="$$(sed -n 's/^FROM golang:\([0-9][0-9.]*\).*/\1/p' Dockerfile | head -1)"; \
	if [ -z "$$imgv" ]; then echo "could not parse builder image from Dockerfile"; exit 1; fi; \
	lowest="$$(printf '%s\n%s\n' "$$modv" "$$imgv" | sort -V | head -1)"; \
	if [ "$$lowest" != "$$modv" ]; then \
		echo "Dockerfile builds on golang:$$imgv but go.mod requires go >= $$modv"; \
		echo "the image build will fail — bump the FROM line in Dockerfile"; exit 1; \
	fi; \
	echo "go version: go.mod $$modv <= builder golang:$$imgv"

# ─── Docker ───────────────────────────────────────────────────────────────────

docker-build: check-docker-go ## Build the image
	DOCKER_BUILDKIT=1 docker build -t $(IMAGE):$(TAG) .

# Proves the artifact actually runs, not just that it compiles. Catches the
# class of failure a Go build never will: templates and static assets are
# resolved by RELATIVE path at runtime, so a missing COPY in the Dockerfile
# produces an image that builds cleanly and panics on startup.
docker-smoke: docker-build ## Build, run, verify, tear down
	@set -e; \
	name="prebuilt-ci-$$$$"; vol="$$name-data"; \
	trap 'docker rm -f $$name >/dev/null 2>&1 || true; docker volume rm $$vol >/dev/null 2>&1 || true' EXIT; \
	docker run -d --name $$name \
		-e SMTP_HOST= -e SMTP_USER= -e CONTACT_EMAIL= \
		-e ADMIN_USER=ci -e ADMIN_PASS=ci-smoke-pass \
		-v $$vol:/data -p 127.0.0.1:8099:8080 $(IMAGE):$(TAG) >/dev/null; \
	for i in $$(seq 1 60); do \
		curl -fsS -o /dev/null http://127.0.0.1:8099/ 2>/dev/null && break; \
		[ $$i = 60 ] && { echo "container never served"; docker logs $$name; exit 1; }; \
		sleep 1; \
	done; \
	fail=0; \
	chk() { got="$$(curl -s -o /dev/null -w '%{http_code}' $$3 http://127.0.0.1:8099$$1)"; \
		if [ "$$got" = "$$2" ]; then printf '  ok   %-26s %s\n' "$$1" "$$got"; \
		else printf '  FAIL %-26s want=%s got=%s\n' "$$1" "$$2" "$$got"; fail=1; fi; }; \
	chk / 200; \
	chk /public/css/style.css 200; \
	chk /public/js/main.js 200; \
	chk /public/js/admin.js 200; \
	chk /robots.txt 200; \
	chk /admin 401; \
	chk /admin 200 "-u ci:ci-smoke-pass"; \
	uid="$$(docker exec $$name id -u)"; \
	if [ "$$uid" = "0" ]; then echo "  FAIL container runs as root"; fail=1; \
	else printf '  ok   %-26s uid=%s\n' "non-root" "$$uid"; fi; \
	docker exec $$name test -f /data/prebuilt.db \
		&& printf '  ok   %-26s\n' "db created in volume" \
		|| { echo "  FAIL db not created"; fail=1; }; \
	exit $$fail

# ─── Aggregate ────────────────────────────────────────────────────────────────

ci: fmt-check vet test check-docker-go vuln smoke docker-smoke ## Everything CI runs

clean: ## Remove build artifacts
	rm -f prebuilt
	go clean -testcache
