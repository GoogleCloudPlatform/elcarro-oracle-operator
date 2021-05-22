load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive")

# Golang
http_archive(
    name = "io_bazel_rules_go",
    sha256 = "69de5c704a05ff37862f7e0f5534d4f479418afc21806c887db544a316f3cb6b",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/rules_go/releases/download/v0.27.0/rules_go-v0.27.0.tar.gz",
        "https://github.com/bazelbuild/rules_go/releases/download/v0.27.0/rules_go-v0.27.0.tar.gz",
    ],
)

load("@io_bazel_rules_go//go:deps.bzl", "go_register_toolchains", "go_rules_dependencies")

# Gazelle
http_archive(
    name = "bazel_gazelle",
    sha256 = "62ca106be173579c0a167deb23358fdfe71ffa1e4cfdddf5582af26520f1c66f",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/bazel-gazelle/releases/download/v0.23.0/bazel-gazelle-v0.23.0.tar.gz",
        "https://github.com/bazelbuild/bazel-gazelle/releases/download/v0.23.0/bazel-gazelle-v0.23.0.tar.gz",
    ],
)

load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies")

# Download the rules_docker repository at release v0.14.4
http_archive(
    name = "io_bazel_rules_docker",
    sha256 = "4521794f0fba2e20f3bf15846ab5e01d5332e587e9ce81629c7f96c793bb7036",
    strip_prefix = "rules_docker-0.14.4",
    urls = ["https://github.com/bazelbuild/rules_docker/releases/download/v0.14.4/rules_docker-v0.14.4.tar.gz"],
)

# Protobuf
http_archive(
    name = "com_google_protobuf",
    sha256 = "512e5a674bf31f8b7928a64d8adf73ee67b8fe88339ad29adaa3b84dbaa570d8",
    strip_prefix = "protobuf-3.12.4",
    urls = ["https://github.com/protocolbuffers/protobuf/archive/refs/tags/v3.12.4.tar.gz"],
)

load("@com_google_protobuf//:protobuf_deps.bzl", "protobuf_deps")

# skylib for go_gencode
http_archive(
    name = "bazel_skylib",
    sha256 = "1c531376ac7e5a180e0237938a2536de0c54d93f5c278634818e0efc952dd56c",
    urls = [
        "https://github.com/bazelbuild/bazel-skylib/releases/download/1.0.3/bazel-skylib-1.0.3.tar.gz",
        "https://mirror.bazel.build/github.com/bazelbuild/bazel-skylib/releases/download/1.0.3/bazel-skylib-1.0.3.tar.gz",
    ],
)

http_archive(
    name = "com_github_godror_godror",
    patch_args = ["-p1"],
    patches = ["@//:hack/0001-Patch-to-add-bazel-support.patch"],
    sha256 = "ac45b8ea0d8bdb828b4862011ee1b7dc8384231a6ee887bcebbb97ffdb339109",
    strip_prefix = "godror-0.20.1",
    urls = [
        "https://github.com/godror/godror/archive/v0.20.1.tar.gz",
    ],
    # version = "v0.21.1"
)

load("//:deps.bzl", "go_dependencies")

# gazelle:repository_macro deps.bzl%go_dependencies
go_dependencies()

# Initialize after loading everything
go_rules_dependencies()

go_register_toolchains(version = "1.16")

gazelle_dependencies()

protobuf_deps()

load(
    "@io_bazel_rules_docker//repositories:repositories.bzl",
    container_repositories = "repositories",
)

container_repositories()

load("@io_bazel_rules_docker//repositories:deps.bzl", container_deps = "deps")

container_deps()

load("@io_bazel_rules_docker//repositories:pip_repositories.bzl", container_pip_deps = "pip_deps")

container_pip_deps()

# Containers to load from external repositories. This must go in WORKSPACE.
load("@io_bazel_rules_docker//container:container.bzl", "container_pull")

container_pull(
    name = "busybox",
    digest = "sha256:c9249fdf56138f0d929e2080ae98ee9cb2946f71498fc1484288e6a935b5e5bc",  # unclear how long these images last, it may expire and we need to grab latest again.
    registry = "docker.io",
    repository = "library/busybox",
    # tag = "latest",
)

container_pull(
    name = "distroless",
    registry = "gcr.io",
    repository = "distroless/cc",  # /base is also an option for glibc+openssl, see https://github.com/GoogleContainerTools/distroless
    tag = "nonroot",
)

# Kubebuilder binaries used in controller tests.
http_archive(
    name = "kubebuilder_tools",
    build_file_content = """
filegroup(
    name = "binaries",
    srcs = glob(["**"]),
    visibility = ["//visibility:public"],
)""",
    sha256 = "fb13a93a800389029b06fcc74ab6a3b969ff74178252709a040e4756251739d2",
    strip_prefix = "kubebuilder",
    urls = ["https://storage.googleapis.com/kubebuilder-tools/kubebuilder-tools-1.19.2-linux-amd64.tar.gz"],
)
