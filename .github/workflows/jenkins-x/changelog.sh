#!/usr/bin/env sh

echo "HOME is $HOME"
echo current git configuration

# See https://github.com/actions/checkout/issues/766
git config --global --add safe.directory "$GITHUB_WORKSPACE"
git config --global --get user.name
git config --global --get user.email

echo "setting git user"

git config --global user.name jenkins-x-bot-test
git config --global user.email "jenkins-x@googlegroups.com"

git clean -f
git tag -fa v$VERSION -m "chore: release version $VERSION"
git push origin v$VERSION

jx changelog create --verbose --header-file=hack/changelog-header.md --version=v$VERSION --prerelease
