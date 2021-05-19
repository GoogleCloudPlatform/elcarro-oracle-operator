# Contributing Guidelines

Thanks in advance for your contribution to the El Carro project!

We'd love to accept your patches and contributions to this project. There are
just a few small guidelines you need to follow.

Our contributors try to follow good software development practices to help
ensure that the product we provide to our customers is stable and reliable.

We've proposed some guidelines below (and welcome more suggestions!)

## Community Guidelines

This project follows
[Google's Open Source Community Guidelines](https://opensource.google/conduct/).

## Community Discussion

We want to hear how El Carro is working for everyone. We accept bug reports via
Github and have a public mailing list on google groups for more open ended
discussion at https://groups.google.com/g/el-carro .

## Developer Guidance

### Contributor License Agreement

Contributions to this project must be accompanied by a Contributor License
Agreement (CLA). You (or your employer) retain the copyright to your
contribution; this simply gives us permission to use and redistribute your
contributions as part of the project. Head over to
<https://cla.developers.google.com/> to see your current agreements on file or
to sign a new one.

You generally only need to submit a CLA once, so if you've already submitted one
(even if it was for a different project), you probably don't need to do it
again.

### Pull Requests

We encourage you to make an Issue or discuss changes via mailing list before
beginning work on large changes to ensure your work is aligned with the project
and for tips on implementing your proposed changes. For smaller bug fixes feel
free to send a Pull Request (PR) directly.

Once you are ready to submit a Pull Request, please ensure you do the following:

*   Review the
    [GitHub About PRs Page](https://help.github.com/articles/about-pull-requests/)
    if you are new to Github.

*   Please be as descriptive in your pull request as possible. If you are
    referencing an issue, please be sure to include the issue in your pull
    request.

*   Please add tests where appropriate. All changes should have associated unit
    tests and new functionality should include integration tests.

*   Squash any fix-up commits and ensure your commit messages have meaningful
    titles and descriptions. A small guide to writing good commit messages is
    [this post](https://chris.beams.io/posts/git-commit/)

### Testing

Our tests are split into multiple make targets. Static checks, formatting, and
code generation is handled in the `check` target. Commit any files changed or
created as part of formatting and code generation after this target fails and
rerun to ensure all generated code is part of your change.

Unit and integration tests are in the `unit-test` and `integration-test` targets
respectively. You should ensure all of these targets execute successfully for
your changes.

To run these all of these tests from the repository root:

```
make -C oracle check
make -C oracle unit-test
```

### Integration Testing

Our integration tests currently run on GKE, if you would like to run them you
should set the environment variables listed in `make -C oracle env` to target
your desired environment before running `make -C oracle test`.

Integration tests depend on you having
[kubebuilder](https://book.kubebuilder.io/quick-start.html) installed which
provides some k8s binaries for testing as well as `gcloud` and `kubectl` for
authentication and interacting with your test cluster.

### Code Reviews

All PRs will need to be code reviewed by at least one other El Carro
collaborator for approval and LGTM (Looks Good To Me) before it can be merged
into the repository.

Depending on the size of your PR the time to review can vary depending on the
size and complexity of the change. However we aim to assign reviewers to PRs
within a week, typically a few days. If you are concerned about the state of
your PR after at least a week from the last comment you can ask the assigned
reviewer for an update in the comments of your PR.
