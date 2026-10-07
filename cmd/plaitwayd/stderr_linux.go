//go:build linux

package main

import "golang.org/x/sys/unix"

const getTermios = unix.TCGETS
