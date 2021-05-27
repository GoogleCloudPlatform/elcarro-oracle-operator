## GitHub workflow for El Carro

This document provides an overview of the Git/GitHub workflow used by the El
Carro project. Following the instructions in this guide helps us streamline
development and evolution of the project.

To make your first contribution to El Carro, you should follow the steps below.

1.  Create a fork of the El Carro repo

    -   Visit https://github.com/GoogleCloudPlatform/elcarro-oracle-operator
    -   Click the Fork button (top right) to create a fork hosted on GitHub

2.  Clone your fork to your machine Your fork should be cloned to a directory on
    your $GOPATH as per
    [Go's workspace instructions](https://golang.org/doc/code.html#Workspaces).

    -   Authenticate yourself to GitHub. We recommend using SSH as described
        [here](https://docs.github.com/en/github/authenticating-to-github/connecting-to-github-with-ssh).

    -   [Download and install Go](https://golang.org/doc/install) if you haven't
        already

    -   Define a local working directory:

        ```sh
        export WORKING_DIR="$(go env GOPATH)/src/elcarro.anthosapis.com"
        ```

    -   Set your GitHub user:

        ```sh
        export GITHUB_USER={GitHub username}
        ```

    -   Clone your fork

        ```sh
        mkdir -p $WORKING_DIR
        cd $WORKING_DIR
        git clone git@github.com:$GITHUB_USER/elcarro-oracle-operator.git

        cd $WORKING_DIR/elcarro-oracle-operator
        git remote add upstream git@github.com:GoogleCloudPlatform/elcarro-oracle-operator.git

        # Disable pushing to upstream main branch
        git remote set-url --push upstream no_push

        # Verify remotes:
        git remote -v
        ```

3.  Branch from your clone

    -   Before creating a new local branch, always get your local main branch up
        to date

        ```sh
        cd $WORKING_DIR/elcarro-oracle-operator
        git fetch upstream
        git checkout main
        git rebase upstream/main
        ```

    -   Create and checkout a branch as follows

        ```sh
        git checkout -b myfeature
        ```

    -   Make your changes. As you make changes, you can and should keep your
        branch in sync with the upstream by running:

        ```sh
        git fetch upstream
        git rebase upstream/main
        ```

        Use `git rebase` over `git pull` as `git pull` creates merge commits
        which pollutes the commit history

4.  Commit and push your changes

    -   Once you're done making your changes, stage and commit them by running:

        ```sh
        git add .
        git commit -m "Detailed commit message"
        ```

        Details on how to structure commit messages is provided in the guide for
        [pull requests](pull-requests.md).

    -   Push your changes to your fork by running:

        ```sh
        git push -f origin myfeature
        ```

5.  Create a Pull Request

    -   Visit your fork at
        https://github.com/$GITHUB_USER/elcarro-oracle-operator
    -   Click the Compare & Pull Request button next to your myfeature branch.
        Your Pull Request should be opened against the main El Carro repository
        as shown in
    -   Check out the the guide for [pull requests](pull-requests.md) for
        guidance on how to structure and manage your pull requests.
