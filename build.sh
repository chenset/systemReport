#! /usr/bin/env bash

# killall systemReport >/dev/null 2>/dev/null

#git reset HEAD --hard --quiet && git pull --rebase --quiet
#if [ $? -ne 0 ];then
#    echo 'Update failed!'
#    exit 1;
#fi

env CGO_ENABLED=0 go build -ldflags "-s -w" -o systemReport .
