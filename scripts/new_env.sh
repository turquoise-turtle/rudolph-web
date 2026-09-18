#!/usr/bin/env bash
set -e

# Figure out the project's basepath
SOURCE="${BASH_SOURCE[0]}"
while [ -h "$SOURCE" ]; do SOURCE="$(readlink "$SOURCE")"; done
DIR="$(cd -P "$(dirname "$SOURCE")/.." && pwd)"

TF_ENV_DIR=$DIR/deployments/environments
EXAMPLE_ENV_DIR=$TF_ENV_DIR/example

if [ $# -eq 0 ]; then
  echo "Not enough arguments: pass the environment name"
  exit 1
fi

NEW_ENV=$1
NEW_ENV_DIR=$TF_ENV_DIR/$NEW_ENV
echo "new env dir:"
echo "$NEW_ENV_DIR"

if [[ -d $NEW_ENV_DIR ]]; then
  echo "Warning: environment $NEW_ENV already exists. Exiting."
  exit 1
fi

mkdir "$NEW_ENV_DIR"
cd "$NEW_ENV_DIR"

# Environments hold account specific settings, so keep them out of git.
echo "*" >>.gitignore

# Environments have the same layout as the example environment. main.tf sources the local module
# through ../../terraform_modules/rudolph_web, so the new environment deploys this checkout.
cp "$EXAMPLE_ENV_DIR/main.tf" ./
cp "$EXAMPLE_ENV_DIR/_variables.tf" ./
cp "$EXAMPLE_ENV_DIR/_backend.tf" ./
cp "$EXAMPLE_ENV_DIR/_outputs.tf" ./
cp "$EXAMPLE_ENV_DIR/config.auto.tfvars.json" ./
cp "$EXAMPLE_ENV_DIR/versions.tf" ./

echo "created $NEW_ENV_DIR; edit _backend.tf and config.auto.tfvars.json before deploying"
