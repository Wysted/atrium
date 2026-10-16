#!/usr/bin/env bash
# revoke.sh <email> <initiative-key> <role-key>: removes the direct grant created by grant.sh.
exec "$(dirname "${BASH_SOURCE[0]}")/grant.sh" --revoke "$@"
