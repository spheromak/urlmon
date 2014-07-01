#!/bin/sh

rm  urlmon.*.64
GOOS=linux go build -o urlmon.linux.64
go build -o urlmon.darwin.64
