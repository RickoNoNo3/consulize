#!/bin/bash
cd "$(dirname "$0")" || exit 127

rm -rf build.bak 2>/dev/null || true
mv build build.bak 2>/dev/null || true
rm -rf build 2>/dev/null || true
mkdir build
mkdir build/consulize

cp ./run-consulize.sh ./build/consulize/
cp ./docker-compose.yml ./build/consulize/
cp ./README.md ./build/consulize/
cp ./structure.jpg ./build/consulize/
cp ./LICENSE ./build/consulize/

go get -v
go build -o ./build/consulize/consulize .

cd build || exit 127
zip -r9 consulize_linux_amd64.zip consulize/
