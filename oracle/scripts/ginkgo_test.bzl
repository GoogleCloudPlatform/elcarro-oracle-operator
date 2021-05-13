load("@io_bazel_rules_go//go:def.bzl", "go_test")

# split ginkgo tests into multiple targets to allow parallelism and to
# interoperate with bazel's --jobs control for parallelism
def ginkgo_test(name, srcs, deps, nodes = 1, **kwargs):
    # Only modify the name and args for parallel tests. Otherwise pass-thru to go_test.
    if nodes > 1:
        for i in range(1, int(nodes) + 1):
            go_test(
                name = "{}_node_{}".format(name, i),
                srcs = srcs,
                deps = deps,
                args = ["-ginkgo.parallel.total", str(nodes), "-ginkgo.parallel.node", str(i)],
                **kwargs
            )
    else:
        go_test(
            name = name,
            srcs = srcs,
            deps = deps,
            **kwargs
        )
