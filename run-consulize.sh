#!/bin/bash
# Run Consulize

#---------------- Customizable ------------------
# You can save default value here, to connect to your Consul and target server.
# If no external env variables set, the value here will be used.
# For all configurables, see README.md
#
# Template:
#   export ENV_NAME=${ENV_NAME:-value}
#                              ↑ look here

export TARGET=${TARGET:-http://127.0.0.1:80}
export TAGS=${TAGS:-'["urlprefix-/myApp strip=/myApp", "v1.0"]'}
export CONSUL_HTTP_ADDR=${CONSUL_HTTP_ADDR:-127.0.0.1:8500}
export SERVICE_NAME=${SERVICE_NAME:-consulize}

#
#---------------- NEVER EDIT -----------------
cd "$(dirname "$0")" || exit 127
if [[ -f "./consulize" ]]; then
  ./consulize
else
  echo 'No Consulize executable found. Is it built?' >&2
  exit 1
fi
