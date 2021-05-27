.PHONY: build
build:
	bazel build //...

.PHONY: update verify
update:
	./hack/update-all.sh
verify:
	./hack/verify-all.sh

.PHONY: test
test:
	bazel test --test_tag_filters=-integration //...

# The entrypoint of Prow presubmit
.PHONY: prow-presubmit
prow-presubmit:
	bazel test \
		--remote_cache=https://storage.googleapis.com/graybox-bazel-cache \
		--google_default_credentials \
		--test_output=errors \
		--test_tag_filters=-integration \
		//...
