#!/bin/bash

if [[ -z ${1} ]] ; then
	echo "Missing rhsm file path"
	exit 22
fi

for target in fedora4{4,5} rhel{8..10} ; do
	podman build --secret id=rhsm,src=${1}   \
		-f containers/Containerfile.${target} \
		-t dns-online-builder:${target} .
	podman run --rm -v .:/src:ro,Z           \
		-v "./dist/${target}:/out:Z"          \
		"dns-online-builder:${target}"
done
