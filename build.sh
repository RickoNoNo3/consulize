#!/bin/bash
cd "$(dirname "$0")" || exit 127

rm -rf build.bak 2>/dev/null || true
mv build build.bak 2>/dev/null || true
rm -rf build 2>/dev/null || true
mkdir build

cp ./run-consulize.sh ./build/

go get
go build -o ./build/consulize .
zip -r ./build/consulize_linux_amd64.zip ./build/*
