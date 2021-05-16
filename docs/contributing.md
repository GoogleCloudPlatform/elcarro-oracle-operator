# Contributing Guidelines

Thanks in advance for your contribution to the El Carro project!

We'd love to accept your patches and contributions to this project. There are
just a few small guidelines you need to follow.

Our contributors try to follow good software development practices to help
ensure that the product we provide to our customers is stable and reliable.

We've proposed some guidelines below (and welcome more suggestions!)

## Contributor License Agreement

Contributions to this project must be accompanied by a Contributor License
Agreement (CLA). You (or your employer) retain the copyright to your
contribution; this simply gives us permission to use and redistribute your
contributions as part of the project. Head over to
<https://cla.developers.google.com/> to see your current agreements on file or
to sign a new one.

You generally only need to submit a CLA once, so if you've already submitted one
(even if it was for a different project), you probably don't need to do it
again.

## Community Guidelines

This project follows
[Google's Open Source Community Guidelines](https://opensource.google/conduct/).

## Developer Guidance

### Pull Requests

Once you are ready to submit a Pull Request, please ensure you do the following:

* Please be as descriptive in your pull request as possible. If you are
referencing an issue, please be sure to include the issue in your pull request.

* Please ensure you have added testing where appropriate.

### Testing

Ensure the following passes:

```
cd oracle
make check
make unit-test
```
and commit any resultant changes to `go.mod` and `go.sum`.

### Code Reviews

All submissions, including submissions by project members, require review. We
use GitHub pull requests for this purpose. Consult
[GitHub Help](https://help.github.com/articles/about-pull-requests/) for more
information on using pull requests.
