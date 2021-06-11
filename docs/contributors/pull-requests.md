This guide provides instructions and guidelines on how to create pull requests
for the El Carro repository.

## Before you submit a Pull Request

This guide assumes that you've already made code changes that you'd like merged
into the main repository. If you haven't yet made your changes and are looking
to set up a development environment, check out the [dev guide](dev-guide.md).

We encourage you to make an Issue or discuss changes via mailing list before
beginning work on large changes to ensure your work is aligned with the project
and for tips on implementing your proposed changes. For smaller bug fixes feel
free to send a Pull Request (PR) directly.

Once you are ready to submit a Pull Request, please ensure you do the following:

-   Review the
    [GitHub About PRs Page](https://help.github.com/articles/about-pull-requests/)
    if you are new to Github.

-   Please be as descriptive in your pull request as possible. If you are
    referencing an issue, please be sure to include the issue in your pull
    request.

-   Please add tests where appropriate. All changes should have associated unit
    tests and new functionality should include integration tests.

-   Squash any fix-up commits and ensure your commit messages have meaningful
    titles and descriptions. A small guide to writing good commit messages can
    be found [here](https://chris.beams.io/posts/git-commit/).

-   Prefer smaller, self-contained Pull Requests.

-   All contributions must be licensed Apache 2.0 and all files must have a copy
    of the boilerplate license disclosure, which can be copied from an existing
    file.

-   Most changes must be accompanied by adequate tests.

## After you open a Pull Request

Once you open a Pull Request, you should request a review from one of the
maintainers of the project. Before tests can be run for your Pull Request, a
reviewer/maintainer must signal that your PR is ok to test by replying to the PR
with the `/ok-to-test` command which will automatically trigger tests.

## Pull Request merge process

Before a Pull Request can be merged the following steps must be completed:

-   A [Google CLA](https://cla.developers.google.com) must be signed. No need to
    sign a new one if you already have one on file.

-   All tests (unit, functional & integrations) must pass.

-   Code checks (e.g: `make check`) must pass.

-   The Pull Request must be approved by at least one reviewer.
