# -*- makefile -*-
# -----------------------------------------------------------------------
# Copyright 2016-2024 Open Networking Foundation (ONF) and the ONF Contributors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# -----------------------------------------------------------------------

$(if $(DEBUG),$(warning ENTER))

.DEFAULT_GOAL := help

$(if $(findstring disabled-joey,$(USER)),\
   $(eval USE_LF_MK := 1)) # special snowflake

##--------------------##
##---]  INCLUDES  [---##
##--------------------##
ifdef USE_LF_MK
  include lf/include.mk
else
  include lf/transition.mk
endif # ifdef USE_LF_MK


# TOP         ?= .
# MAKEDIR     ?= $(TOP)/makefiles
#
# $(if $(VERBOSE),$(eval export VERBOSE=$(VERBOSE))) # visible to include(s)

##--------------------##
##---]  INCLUDES  [---##
##--------------------##
# include $(MAKEDIR)/include.mk
# ifdef LOCAL_LINT
#   include $(MAKEDIR)/lint/golang/sca.mk
# endif

# set default shell
# SHELL = bash -e -o pipefail

# Variables
VERSION                  ?= $(shell cat ./VERSION)

DOCKER_LABEL_VCS_DIRTY     = false
ifneq ($(shell git ls-files --others --modified --exclude-standard 2>/dev/null | wc -l | sed -e 's/ //g'),0)
    DOCKER_LABEL_VCS_DIRTY = true
endif

## [TODO] Refactor with repo:onf-make:makefiles/docker/include.mk

## Docker related
DOCKER_EXTRA_ARGS        ?=
DOCKER_REGISTRY          ?=
DOCKER_REPOSITORY        ?=
DOCKER_TAG               ?= ${VERSION}$(shell [[ ${DOCKER_LABEL_VCS_DIRTY} == "true" ]] && echo "-dirty" || true)
DOCKER_TARGET            ?= prod
ADAPTER_IMAGENAME        := ${DOCKER_REGISTRY}${DOCKER_REPOSITORY}voltha-openonu-adapter-go:${DOCKER_TAG}
TYPE                     ?= minimal

## Docker labels. Only set ref and commit date if committed
DOCKER_LABEL_VCS_URL       ?= $(shell git remote get-url $(shell git remote))
DOCKER_LABEL_VCS_REF       = $(shell git rev-parse HEAD)
DOCKER_LABEL_BUILD_DATE    ?= $(shell date -u "+%Y-%m-%dT%H:%M:%SZ")
DOCKER_LABEL_COMMIT_DATE   = $(shell git show -s --format=%cd --date=iso-strict HEAD)

DOCKER_BUILD_ARGS ?= \
	${DOCKER_EXTRA_ARGS} \
	--build-arg org_label_schema_version="${VERSION}" \
	--build-arg org_label_schema_vcs_url="${DOCKER_LABEL_VCS_URL}" \
	--build-arg org_label_schema_vcs_ref="${DOCKER_LABEL_VCS_REF}" \
	--build-arg org_label_schema_build_date="${DOCKER_LABEL_BUILD_DATE}" \
	--build-arg org_opencord_vcs_commit_date="${DOCKER_LABEL_COMMIT_DATE}" \
	--build-arg org_opencord_vcs_dirty="${DOCKER_LABEL_VCS_DIRTY}"

# tool containers
VOLTHA_TOOLS_VERSION ?= 3.1.1

GO                = docker run --rm --user $$(id -u):$$(id -g) -v ${CURDIR}:/app -v gocache:/.cache -v gocache-${VOLTHA_TOOLS_VERSION}:/go/pkg voltha/voltha-ci-tools:${VOLTHA_TOOLS_VERSION}-golang go
GO_JUNIT_REPORT   = docker run --rm --user $$(id -u):$$(id -g) -v ${CURDIR}:/app -i voltha/voltha-ci-tools:${VOLTHA_TOOLS_VERSION}-go-junit-report go-junit-report
GOCOVER_COBERTURA = docker run --rm --user $$(id -u):$$(id -g) -v ${CURDIR}:/app/src/github.com/opencord/voltha-openonu-adapter-go -i voltha/voltha-ci-tools:${VOLTHA_TOOLS_VERSION}-gocover-cobertura gocover-cobertura
GOFMT             = docker run --rm --user $$(id -u):$$(id -g) -v ${CURDIR}:/app voltha/voltha-ci-tools:${VOLTHA_TOOLS_VERSION}-golang gofmt
GOLANGCI_LINT     = docker run --rm --user $$(id -u):$$(id -g) -v ${CURDIR}:/app -v gocache:/.cache -v gocache-${VOLTHA_TOOLS_VERSION}:/go/pkg voltha/voltha-ci-tools:${VOLTHA_TOOLS_VERSION}-golangci-lint golangci-lint
HADOLINT          = docker run --rm --user $$(id -u):$$(id -g) -v ${CURDIR}:/app voltha/voltha-ci-tools:${VOLTHA_TOOLS_VERSION}-hadolint hadolint

.PHONY: docker-build local-protos local-lib-go help

## -----------------------------------------------------------------------
## Local Development Helpers
## -----------------------------------------------------------------------
local-protos: ## Copies a local version of the voltha-protos dependency into the vendor directory
ifdef LOCAL_PROTOS
	rm -rf vendor/github.com/opencord/voltha-protos/v5/go
	mkdir -p vendor/github.com/opencord/voltha-protos/v5/go
	cp -r ${LOCAL_PROTOS}/go/* vendor/github.com/opencord/voltha-protos/v5/go
	rm -rf vendor/github.com/opencord/voltha-protos/v5/go/vendor
endif

## -----------------------------------------------------------------------
## Local Development Helpers
## -----------------------------------------------------------------------
local-lib-go: ## Copies a local version of the voltha-lib-go dependency into the vendor directory
ifdef LOCAL_LIB_GO
	rm -rf vendor/github.com/opencord/voltha-lib-go/v7/pkg
	mkdir -p vendor/github.com/opencord/voltha-lib-go/v7/pkg
	cp -r ${LOCAL_LIB_GO}/pkg/* vendor/github.com/opencord/voltha-lib-go/v7/pkg/
endif

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
build: docker-build ## Alias for 'docker build'

## -----------------------------------------------------------------------
## Docker targets
## -----------------------------------------------------------------------
docker-build: local-protos local-lib-go ## Build openonu adapter docker image (set BUILD_PROFILED=true to also build the profiled image)
	docker build $(DOCKER_BUILD_ARGS) --target=${DOCKER_TARGET} --build-arg CGO_PARAMETER=0 -t ${ADAPTER_IMAGENAME} -f docker/Dockerfile.openonu .
ifdef BUILD_PROFILED
	docker build $(DOCKER_BUILD_ARGS) --target=dev --build-arg CGO_PARAMETER=1 --build-arg EXTRA_GO_BUILD_TAGS="-tags profile" -t ${ADAPTER_IMAGENAME}-profile -f docker/Dockerfile.openonu .
endif
ifdef BUILD_RACE
	docker build $(DOCKER_BUILD_ARGS) --target=dev --build-arg CGO_PARAMETER=1 --build-arg EXTRA_GO_BUILD_TAGS="-race" -t ${ADAPTER_IMAGENAME}-rd -f docker/Dockerfile.openonu .
endif

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
docker-push: ## Push the docker images to an external repository
	docker push ${ADAPTER_IMAGENAME}
ifdef BUILD_PROFILED
	docker push ${ADAPTER_IMAGENAME}-profile
endif
ifdef BUILD_RACE
	docker push ${ADAPTER_IMAGENAME}-rd
endif

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
docker-kind-load: ## Load docker images into a KinD cluster
	@if [ "`kind get clusters | grep voltha-$(TYPE)`" = '' ]; then echo "no voltha-$(TYPE) cluster found" && exit 1; fi
	kind load docker-image ${ADAPTER_IMAGENAME} --name=voltha-$(TYPE) --nodes $(shell kubectl get nodes --template='{{range .items}}{{.metadata.name}},{{end}}' | sed 's/,$$//')

## -----------------------------------------------------------------------
## lint and unit tests
## -----------------------------------------------------------------------
lint-dockerfile: ## Perform static analysis on Dockerfile
	@echo "Running Dockerfile lint check ..."
	@${HADOLINT} $$(find . -name "Dockerfile.*")
	@echo "Dockerfile lint check OK"

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
lint-style: ## Perform lint style checks on source code
	@echo "Running style check..."
	@gofmt_out="$$(${GOFMT} -l $$(find . -name '*.go' -not -path './vendor/*'))" ;\
	if [ ! -z "$$gofmt_out" ]; then \
	  echo "$$gofmt_out" ;\
	  echo "Style check failed on one or more files ^, run 'go fmt -s -e -w' to fix." ;\
	  exit 1 ;\
	fi
	@echo "Style check OK"

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
lint-sanity: ## Perform basic code checks on source
	@echo "Running sanity check..."
	@${GO} vet -mod=vendor ./...
	@echo "Sanity check OK"

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
lint-mod: ## Verify the Go dependencies
	$(HIDE)echo "Running dependency check..."
	$(HIDE)$(GO) mod verify
	$(HIDE)echo "Dependency check OK. Running vendor check..."
	$(HIDE)git status > /dev/null
	$(HIDE)git diff-index --quiet HEAD -- go.mod go.sum vendor || (echo "ERROR: Staged or modified files must be committed before running this test" && echo "`git status`" && exit 1)

	$(HIDE)$(MAKE) --no-print-directory detect-local-edits

	$(HIDE)$(MAKE) --no-print-directory mod-update

	$(HIDE)git status > /dev/null
	$(HIDE)git diff-index --quiet HEAD -- go.mod go.sum vendor || (echo "ERROR: Modified files detected after running go mod tidy / go mod vendor" && echo "`git status`" && exit 1)

	$(MAKE) --no-print-directory detect-local-edits
	$(HIDE)echo "Vendor check OK."

## ---------------------------------------------------------------------
## Intent: Determine if sandbox contains locally modified files
## ---------------------------------------------------------------------
clean-tree := git status --porcelain
detect-local-edits:
	$(HIDE)[[ `$(clean-tree)` == "" ]] || (echo "ERROR: Untracked files detected, commit or remove to continue" && echo "`git status`" && exit 1)

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
lint: local-lib-go lint-style lint-sanity lint-mod lint-dockerfile

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
results-dir   := ./tests/results
coverage-stem := $(results-dir)/go-test-coverage
results-stem  := $(results-dir)/go-test-results

test: lint ## Run unit tests

	$(call banner-enter,Target $@)
	@mkdir -p $(results-dir)
	$(HIDE)$(if $(LOCAL_FIX_PERMS),chmod o+w "$(results-dir)")

    # Running remotely
	${GO} test -mod=vendor -v -coverprofile $(coverage-stem).out -covermode count ./... 2>&1 \
        | tee $(results-stem).out ;\
	RETURN=$$? ;\
	${GO_JUNIT_REPORT}   < $(results-stem).out  > $(results-stem).xml ;\
	${GOCOVER_COBERTURA} < $(coverage-stem).out > $(coverage-stem).xml ;\
	exit $$RETURN

	$(HIDE)$(if $(LOCAL_FIX_PERMS),chmod o-w "$(results-dir)")
	$(call banner-enter,Target $@)

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
sca: ## Runs static code analysis with the golangci-lint tool
	@mkdir -p ./sca-report
	@echo "Running static code analysis..."
	@${GOLANGCI_LINT} run  --out-format junit-xml ./... | tee ./sca-report/sca-report.xml
	@echo "Static code analysis OK"

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
clean :: distclean ## Removes any local filesystem artifacts generated by a build

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
distclean: ## Removes any local filesystem artifacts generated by a build or test run
	$(RM) -r ./sca-report

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
.PHONY: mod-update
mod-update: mod-tidy mod-vendor

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
.PHONY: mod-tidy
mod-tidy :
	$(call banner-enter,Target $@)
	${GO} mod tidy
	$(call banner-leave,Target $@)

## -----------------------------------------------------------------------
## -----------------------------------------------------------------------
.PHONY: mod-vendor
mod-vendor : mod-tidy
mod-vendor :
	$(call banner-enter,Target $@)
	$(if $(LOCAL_FIX_PERMS),chmod o+w $(CURDIR))
	${GO} mod vendor
	$(if $(LOCAL_FIX_PERMS),chmod o-w $(CURDIR))
	$(call banner-leave,Target $@)

## -----------------------------------------------------------------------
## For each makefile target, add ## <description> on the target line and it will be listed by 'make help'
## -----------------------------------------------------------------------
## [TODO] Replace with simple printf(s), awk logic confused by double-colons
## -----------------------------------------------------------------------
help :: ## Print help for each Makefile target
	@echo
	@grep --no-filename '^[[:alpha:]_-]*:.* ##' $(MAKEFILE_LIST) \
	    | sort \
	    | awk 'BEGIN {FS=":.* ## "}; {printf "%-25s : %s\n", $$1, $$2};'
	@printf '\n'
	@printf '  %-33.33s %s\n' 'mod-update' \
	  'Alias for makefile targets mod-tidy + mod-vendor'
	@printf '  %-33.33s %s\n' 'mod-tidy' \
	  'Refresh packages, update go.mod and go.sum'
	@printf '  %-33.33s %s\n' 'mod-vendor' \
	  'Update go package dependencies beneath vendor'.

# [EOF]
