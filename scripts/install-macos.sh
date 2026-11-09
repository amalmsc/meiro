#!/usr/bin/env bash
# Builds the arm64 macOS app, replaces /Applications/Meiro.app, and opens it.
#
#   scripts/install-macos.sh
set -euo pipefail
bazel run //:dist -- darwin-arm64 && osascript -e 'quit app "Meiro"' && rm -rf /Applications/Meiro.app && ditto build/darwin-arm64/Meiro.app /Applications/Meiro.app && open /Applications/Meiro.app
