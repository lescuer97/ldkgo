//go:build darwin && arm64

package cdk_ffi

// #cgo LDFLAGS: -L${SRCDIR}/native/darwin_arm64 -lcdk_ffi -Wl,-rpath,${SRCDIR}/native/darwin_arm64 -lm
import "C"
