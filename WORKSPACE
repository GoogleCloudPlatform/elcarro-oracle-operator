load("@bazel_tools//tools/build_defs/repo:http.bzl", "http_archive", "http_file")

# Modern pkg_rules
http_archive(
    name = "rules_pkg",
    sha256 = "8a298e832762eda1830597d64fe7db58178aa84cd5926d76d5b744d6558941c2",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/rules_pkg/releases/download/0.7.0/rules_pkg-0.7.0.tar.gz",
        "https://github.com/bazelbuild/rules_pkg/releases/download/0.7.0/rules_pkg-0.7.0.tar.gz",
    ],
)

load("@rules_pkg//:deps.bzl", "rules_pkg_dependencies")

rules_pkg_dependencies()

# Golang
http_archive(
    name = "io_bazel_rules_go",
    sha256 = "685052b498b6ddfe562ca7a97736741d87916fe536623afb7da2824c0211c369",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/rules_go/releases/download/v0.33.0/rules_go-v0.33.0.zip",
        "https://github.com/bazelbuild/rules_go/releases/download/v0.33.0/rules_go-v0.33.0.zip",
    ],
)

load("@io_bazel_rules_go//go:deps.bzl", "go_register_toolchains", "go_rules_dependencies")

# Initialize after loading everything
go_rules_dependencies()

go_register_toolchains(
    nogo = "@//hack:nogo",
    version = "1.18.5",
)

# Gazelle
http_archive(
    name = "bazel_gazelle",
    sha256 = "501deb3d5695ab658e82f6f6f549ba681ea3ca2a5fb7911154b5aa45596183fa",
    urls = [
        "https://mirror.bazel.build/github.com/bazelbuild/bazel-gazelle/releases/download/v0.26.0/bazel-gazelle-v0.26.0.tar.gz",
        "https://github.com/bazelbuild/bazel-gazelle/releases/download/v0.26.0/bazel-gazelle-v0.26.0.tar.gz",
    ],
)

load("@bazel_gazelle//:deps.bzl", "gazelle_dependencies")
load("//:deps.bzl", "go_dependencies")

gazelle_dependencies()

# gazelle:repository_macro deps.bzl%go_dependencies
go_dependencies()

# Docker
http_archive(
    name = "io_bazel_rules_docker",
    sha256 = "b1e80761a8a8243d03ebca8845e9cc1ba6c82ce7c5179ce2b295cd36f7e394bf",
    urls = ["https://github.com/bazelbuild/rules_docker/releases/download/v0.25.0/rules_docker-v0.25.0.tar.gz"],
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
    sha256 = "8242327e5df8c80ba49e4165250b8f79a76bd11765facefaaecfca7747dc8da2",
    strip_prefix = "protobuf-3.21.5",
    urls = ["https://github.com/protocolbuffers/protobuf/archive/v3.21.5.zip"],
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
    sha256 = "742b7c8e3d4b79847d08ccc4174f3156de52874168f51eba490e906f2b557151",
    strip_prefix = "godror-0.25.3",
    urls = [
        "https://github.com/godror/godror/archive/v0.25.3.tar.gz",
    ],
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
    sha256 = "6d9f0a6ab0119c5060799b4b8cbd0a030562da70b7ad4125c218eaf028c6cc28",
    strip_prefix = "kubebuilder",
    urls = ["https://storage.googleapis.com/kubebuilder-tools/kubebuilder-tools-1.24.2-linux-amd64.tar.gz"],
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

http_archive(
    name = "oracle_instantclient",
    build_file_content = """
load("@rules_pkg//pkg:pkg.bzl", "pkg_tar")

pkg_dir =  "/lib/x86_64-linux-gnu"
pkg_tar(
    name = "binaries_tar",
    srcs = [
      "libclntsh.so",
      "libclntshcore.so.19.1",
      "libociicus.so",
      "libnnz19.so",
      "libipc1.so",
      "libocci.so",
      "libmql1.so",
      "libocijdbc19.so",
      "liboramysql19.so",
      "BASIC_LITE_LICENSE",
    ],
    symlinks = {
      pkg_dir + "/libclntsh.so.10.1": "libclntsh.so",
      pkg_dir + "/libclntsh.so.11.1": "libclntsh.so",
      pkg_dir + "/libclntsh.so.12.1": "libclntsh.so",
      pkg_dir + "/libclntsh.so.18.1": "libclntsh.so",
      pkg_dir + "/libclntsh.so.19.1": "libclntsh.so",
      pkg_dir + "/libocci.so.10.1": "libocci.so",
      pkg_dir + "/libocci.so.11.1": "libocci.so",
      pkg_dir + "/libocci.so.12.1": "libocci.so",
      pkg_dir + "/libocci.so.18.1": "libocci.so",
      pkg_dir + "/libocci.so.19.1": "libocci.so",
    },
    mode = "0755",
    package_dir = pkg_dir,
    visibility = ["@//oracle/build:__pkg__"],
)""",
    sha256 = "be6538141de1575aa872efc567737cae63cad1eb95fab47185ba6cc3f3bf4000",
    strip_prefix = "instantclient_19_14",
    urls = [
        "https://download.oracle.com/otn_software/linux/instantclient/1914000/instantclient-basiclite-linux.x64-19.14.0.0.0dbru.zip",
    ],
)
