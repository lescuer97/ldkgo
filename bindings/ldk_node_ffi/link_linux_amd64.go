//go:build linux && amd64

package ldk_node

// #cgo LDFLAGS: -L${SRCDIR}/native/linux_amd64 -lldk_node -Wl,-rpath,${SRCDIR}/native/linux_amd64 -lm -ldl
import "C"
