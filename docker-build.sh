#!/bin/bash
tag=rc
if [[ -n "$1" && "$1" == "latest" ]]; then
  tag=latest
fi

docker build -t rickonono3/consulize:$tag .
docker push rickonono3/consulize:$tag
