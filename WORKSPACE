load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive", "http_file")

# Golang
http_archive(
    name = "io_bazel_rules_go",
    sha256 = "69de5c704a05ff37862f7e0f5534d4f479418afc21806c887db544a316f3cb6b",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/rules_go/releases/download/v0.27.0/rules_go-v0.27.0.tar.gz",
        "https://github.com/bazelbuild/rules_go/releases/download/v0.27.0/rules_go-v0.27.0.tar.gz",
    ],
)

# Gazelle
http_archive(
    name = "bazel_gazelle",
    sha256 = "62ca106be173579c0a167deb23358fdfe71ffa1e4cfdddf5582af26520f1c66f",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/bazel-gazelle/releases/download/v0.23.0/bazel-gazelle-v0.23.0.tar.gz",
        "https://github.com/bazelbuild/bazel-gazelle/releases/download/v0.23.0/bazel-gazelle-v0.23.0.tar.gz",
    ],
)

load("@io_bazel_rules_go//go:deps.bzl", "go_register_toolchains", "go_rules_dependencies")
load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies")
load("//:deps.bzl", "go_dependencies")

# gazelle:repository_macro deps.bzl%go_dependencies
go_dependencies()

# Initialize after loading everything
go_rules_dependencies()

go_register_toolchains(
    nogo = "@//hack:nogo",
    version = "1.16",
)

gazelle_dependencies()

# Docker
http_archive(
    name = "io_bazel_rules_docker",
    sha256 = "59d5b42ac315e7eadffa944e86e90c2990110a1c8075f1cd145f487e999d22b3",
    strip_prefix = "rules_docker-0.17.0",
    urls = ["https://github.com/bazelbuild/rules_docker/releases/download/v0.17.0/rules_docker-v0.17.0.tar.gz"],
)

load(
    "@io_bazel_rules_docker//repositories:repositories.bzl",
    container_repositories = "repositories",
)

container_repositories()

load("@io_bazel_rules_docker//repositories:deps.bzl", container_deps = "deps")
load("@io_bazel_rules_docker//go:image.bzl", go_image_repos = "repositories")

container_deps()

go_image_repos()

# Protobuf
http_archive(
    name = "com_google_protobuf",
    sha256 = "985bb1ca491f0815daad825ef1857b684e0844dc68123626a08351686e8d30c9",
    strip_prefix = "protobuf-3.15.6",
    urls = ["https://github.com/protocolbuffers/protobuf/archive/v3.15.6.zip"],
)

load("@com_google_protobuf//:protobuf_deps.bzl", "protobuf_deps")

protobuf_deps()

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

http_archive(
    name = "shellcheck",
    build_file_content = """exports_files(["shellcheck"])""",
    sha256 = "64f17152d96d7ec261ad3086ed42d18232fcb65148b44571b564d688269d36c8",
    strip_prefix = "shellcheck-v0.7.1",
    urls = ["https://github.com/koalaman/shellcheck/releases/download/v0.7.1/shellcheck-v0.7.1.linux.x86_64.tar.xz"],
)

# Code lint binaries.
http_file(
    name = "clang-format",
    executable = True,
    sha256 = "974b20a021fe1a9758b525eace834325ad50aa828f842dbbc620a516ae33fb9e",
    urls = ["https://github.com/muttleyxd/clang-tools-static-binaries/releases/download/master-22538c65/clang-format-10_linux-amd64"],
)
