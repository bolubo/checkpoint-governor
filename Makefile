GO ?= go

# The release the binaries report. Plain `make build` says "dev"; `make dist VERSION=v0.6.0` and
# `make release VERSION=v0.6.0` set it in the binary itself.
VERSION ?= dev
LDFLAGS = -X main.version=$(VERSION)
IMAGE ?= ghcr.io/bolubo/checkpoint-governor

# How long the real-clock smoke runs (make smoke MINUTES=2 for a quick check).
MINUTES ?= 10

.PHONY: all check fmt vet build test verify parity ci e2e exp clean dist release install install-smoke release-notes spot prepush

all: check

check: fmt vet build test

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

build:
	$(GO) build -ldflags "$(LDFLAGS)" ./...

# The observer is its own module: it carries client-go, and the core must never grow that dependency.
# It is built and tested on demand rather than on every `make check`, so a laptop without the module
# cache still passes the gate.
observer:
	cd contrib/observer && $(GO) build ./... && $(GO) test ./...

observer-against: 
	cd contrib/observer && $(GO) run . --world-dir=$(WORLD) --cluster=$(CLUSTER) --once

# The demo: three acts, fifteen seconds, ending with the receipt. See docs/product-v1.md §5.
demo:
	sh experiments/demo/demo.sh

# The three situations a user meets, asserted in one line each: the executable version of the README.
scenarios:
	sh experiments/demo/scenarios.sh

# The shortest path for someone who already has Go: the two binaries on their PATH.
install:
	$(GO) install -ldflags "$(LDFLAGS)" ./cmd/ckptctl ./cmd/ckpttrainer

# The shortest path for someone who does not: install.sh fetches a release tarball, verifies its
# checksum and installs the two binaries. This target proves the whole path offline by pointing the
# script at a locally built dist/ — the only way this line can be "verified" before a tag exists.
install-smoke: dist
	PREFIX=/tmp/ckptctl-smoke CKPTCTL_RELEASE_BASE=file://$(CURDIR)/dist VERSION=$(VERSION) \
	  sh install.sh
	/tmp/ckptctl-smoke/ckptctl version
	@echo "install-smoke: ok"

# The release notes a tag publishes: the CHANGELOG section for that version, so the notes and the
# repository say the same thing.
release-notes:
	@sh hack/release-notes.sh $(VERSION)

# The binaries a user downloads, with a checksum they can check. No Docker, so it runs on a laptop
# and on a runner: two architectures, four files, one SHA256SUMS.
dist:
	/bin/mkdir -p dist
	for arch in amd64 arm64; do \
	  GOOS=linux GOARCH=$$arch CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS) -s -w" -o dist/ckptctl_linux_$$arch ./cmd/ckptctl; \
	  GOOS=linux GOARCH=$$arch CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w" -o dist/ckpttrainer_linux_$$arch ./cmd/ckpttrainer; \
	  /bin/tar -czf dist/checkpoint-governor_$(VERSION)_linux_$$arch.tar.gz -C dist ckptctl_linux_$$arch ckpttrainer_linux_$$arch; \
	done
	cd dist && /usr/bin/sha256sum checkpoint-governor_*.tar.gz > SHA256SUMS
	@echo "dist: $$(ls dist/*.tar.gz | wc -l) archives, $$(wc -l < dist/SHA256SUMS) checksums"

# The same binaries plus a multi-arch image. Needs Docker with buildx and a login; the tag workflow
# runs exactly this (see .github/workflows/release.yml).
release: dist
	docker buildx build --platform linux/amd64,linux/arm64 \
	  --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) --push .
	@echo "release: $(IMAGE):$(VERSION) and dist/SHA256SUMS"

# The experiment rig: paired arms, one fault, one claim. It fails when
# the baseline also survives, because a fault that does not break the baseline proves nothing.
exp: exp-d exp-x exp-verify

# D: does the system DECIDE to write when the fault arrives? Milliseconds, no write at all.
exp-d:
	sh experiments/rig/decide.sh all

# X: is the write it performs safe? Seconds, real files, state-triggered faults.
exp-x:
	sh experiments/rig/run.sh l1 kill-during-write
	sh experiments/rig/run.sh l1 user-stop
	sh experiments/rig/run.sh l1 disk-full
	sh experiments/rig/run.sh l1 disk-full

# V: does the classifier name the state each fault leaves? Seconds, real processes, real files.
exp-verify:
	sh experiments/rig/verify-matrix.sh

# The real-clock smoke: MINUTES=10 by default, a 60s window, the real binaries on the wall
# clock. It is not part of `verify`: `verify` must stay fast, and a bound that only a machine with
# real time can break is checked exactly (and in 5s) by the virtual day in internal/loop.
smoke:
	MINUTES=$(MINUTES) sh experiments/rig/smoke.sh

# The one fault the product exists for: the platform takes the machine away. One write, then
# may-exit, printed as a receipt.
spot:
	sh experiments/rig/spot.sh

test:
	$(GO) test ./...

# The shipped binary, driven the way a user drives it: a real directory, a real world file, real
# processes, and the plan the job would read. It builds the binary once and takes half a second.
e2e:
	$(GO) test -tags=e2e ./test/e2e

# verify is the gate: `check` plus the pins that keep the contract and the delivery sheet on one
# field set, and the golden decisions that catch an unintended behaviour change.
# ci is exactly what .github/workflows/ci.yml runs, in the same order. verify stays the fast gate a
# person runs while working; this is the promise that a push will not go red, so run it before pushing.
# prepush runs the same steps and names the one that failed. Use it before opening a PR.
prepush:
	sh hack/prepush.sh

ci: check e2e exp demo install-smoke spot

verify: check e2e parity spot
	$(GO) test ./internal/loop ./cmd/ckptctl
	$(GO) test ./internal/simulate -run TestGoldenDecisions
	$(GO) test ./internal/policy -run TestEveryContractFieldHasAnOwner
	$(GO) test ./internal/observe -run TestTheDeliverySheetHonoursTheRequiredFields

# parity is the three edges the first edition had and this repository did not: numbers a scraper can
# read, an outward event per changed plan, and a condition an operator adds without a fork. Each
# line is the real test, and the last line is the claim (docs/first-edition-parity.md).
parity:
	$(GO) test ./internal/metrics -run TestTheExpositionIsValidAndHonest
	$(GO) test ./internal/notify -run TestOnePostCarriesTheEvent
	$(GO) test ./cmd/ckptctl -run TestAConditionFromDiskReachesTheDecision
	@echo "parity: 3/3 — metrics, notification, conditions from disk"

# /bin/rm by absolute path so a shadowing `rm` on PATH cannot change what clean deletes.
clean:
	/bin/rm -rf bin dist
