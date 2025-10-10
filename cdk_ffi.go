package cdkgo

/*
#cgo CFLAGS: -I.
#cgo LDFLAGS: -L./lib -lcdk_ffi
#include "cdk_ffi.h"
*/

import "C"

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"runtime/cgo"
	"sync"
	"sync/atomic"
	"unsafe"
)

// This is needed, because as of go 1.24
// type RustBuffer C.RustBuffer cannot have methods,
// RustBuffer is treated as non-local type
type GoRustBuffer struct {
	inner C.RustBuffer
}

type RustBufferI interface {
	AsReader() *bytes.Reader
	Free()
	ToGoBytes() []byte
	Data() unsafe.Pointer
	Len() uint64
	Capacity() uint64
}

func RustBufferFromExternal(b RustBufferI) GoRustBuffer {
	return GoRustBuffer{
		inner: C.RustBuffer{
			capacity: C.uint64_t(b.Capacity()),
			len:      C.uint64_t(b.Len()),
			data:     (*C.uchar)(b.Data()),
		},
	}
}

func (cb GoRustBuffer) Capacity() uint64 {
	return uint64(cb.inner.capacity)
}

func (cb GoRustBuffer) Len() uint64 {
	return uint64(cb.inner.len)
}

func (cb GoRustBuffer) Data() unsafe.Pointer {
	return unsafe.Pointer(cb.inner.data)
}

func (cb GoRustBuffer) AsReader() *bytes.Reader {
	b := unsafe.Slice((*byte)(cb.inner.data), C.uint64_t(cb.inner.len))
	return bytes.NewReader(b)
}

func (cb GoRustBuffer) Free() {
	rustCall(func(status *C.RustCallStatus) bool {
		C.ffi_cdk_ffi_rustbuffer_free(cb.inner, status)
		return false
	})
}

func (cb GoRustBuffer) ToGoBytes() []byte {
	return C.GoBytes(unsafe.Pointer(cb.inner.data), C.int(cb.inner.len))
}

func stringToRustBuffer(str string) C.RustBuffer {
	return bytesToRustBuffer([]byte(str))
}

func bytesToRustBuffer(b []byte) C.RustBuffer {
	if len(b) == 0 {
		return C.RustBuffer{}
	}
	// We can pass the pointer along here, as it is pinned
	// for the duration of this call
	foreign := C.ForeignBytes{
		len:  C.int(len(b)),
		data: (*C.uchar)(unsafe.Pointer(&b[0])),
	}

	return rustCall(func(status *C.RustCallStatus) C.RustBuffer {
		return C.ffi_cdk_ffi_rustbuffer_from_bytes(foreign, status)
	})
}

type BufLifter[GoType any] interface {
	Lift(value RustBufferI) GoType
}

type BufLowerer[GoType any] interface {
	Lower(value GoType) C.RustBuffer
}

type BufReader[GoType any] interface {
	Read(reader io.Reader) GoType
}

type BufWriter[GoType any] interface {
	Write(writer io.Writer, value GoType)
}

func LowerIntoRustBuffer[GoType any](bufWriter BufWriter[GoType], value GoType) C.RustBuffer {
	// This might be not the most efficient way but it does not require knowing allocation size
	// beforehand
	var buffer bytes.Buffer
	bufWriter.Write(&buffer, value)

	bytes, err := io.ReadAll(&buffer)
	if err != nil {
		panic(fmt.Errorf("reading written data: %w", err))
	}
	return bytesToRustBuffer(bytes)
}

func LiftFromRustBuffer[GoType any](bufReader BufReader[GoType], rbuf RustBufferI) GoType {
	defer rbuf.Free()
	reader := rbuf.AsReader()
	item := bufReader.Read(reader)
	if reader.Len() > 0 {
		// TODO: Remove this
		leftover, _ := io.ReadAll(reader)
		panic(fmt.Errorf("Junk remaining in buffer after lifting: %s", string(leftover)))
	}
	return item
}

func rustCallWithError[E any, U any](converter BufReader[*E], callback func(*C.RustCallStatus) U) (U, *E) {
	var status C.RustCallStatus
	returnValue := callback(&status)
	err := checkCallStatus(converter, status)
	return returnValue, err
}

func checkCallStatus[E any](converter BufReader[*E], status C.RustCallStatus) *E {
	switch status.code {
	case 0:
		return nil
	case 1:
		return LiftFromRustBuffer(converter, GoRustBuffer{inner: status.errorBuf})
	case 2:
		// when the rust code sees a panic, it tries to construct a rustBuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(GoRustBuffer{inner: status.errorBuf})))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		panic(fmt.Errorf("unknown status code: %d", status.code))
	}
}

func checkCallStatusUnknown(status C.RustCallStatus) error {
	switch status.code {
	case 0:
		return nil
	case 1:
		panic(fmt.Errorf("function not returning an error returned an error"))
	case 2:
		// when the rust code sees a panic, it tries to construct a C.RustBuffer
		// with the message.  but if that code panics, then it just sends back
		// an empty buffer.
		if status.errorBuf.len > 0 {
			panic(fmt.Errorf("%s", FfiConverterStringINSTANCE.Lift(GoRustBuffer{
				inner: status.errorBuf,
			})))
		} else {
			panic(fmt.Errorf("Rust panicked while handling Rust panic"))
		}
	default:
		return fmt.Errorf("unknown status code: %d", status.code)
	}
}

func rustCall[U any](callback func(*C.RustCallStatus) U) U {
	returnValue, err := rustCallWithError[error](nil, callback)
	if err != nil {
		panic(err)
	}
	return returnValue
}

type NativeError interface {
	AsError() error
}

func writeInt8(writer io.Writer, value int8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint8(writer io.Writer, value uint8) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt16(writer io.Writer, value int16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint16(writer io.Writer, value uint16) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt32(writer io.Writer, value int32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint32(writer io.Writer, value uint32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeInt64(writer io.Writer, value int64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeUint64(writer io.Writer, value uint64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat32(writer io.Writer, value float32) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func writeFloat64(writer io.Writer, value float64) {
	if err := binary.Write(writer, binary.BigEndian, value); err != nil {
		panic(err)
	}
}

func readInt8(reader io.Reader) int8 {
	var result int8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint8(reader io.Reader) uint8 {
	var result uint8
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt16(reader io.Reader) int16 {
	var result int16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint16(reader io.Reader) uint16 {
	var result uint16
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt32(reader io.Reader) int32 {
	var result int32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint32(reader io.Reader) uint32 {
	var result uint32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readInt64(reader io.Reader) int64 {
	var result int64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readUint64(reader io.Reader) uint64 {
	var result uint64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat32(reader io.Reader) float32 {
	var result float32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func readFloat64(reader io.Reader) float64 {
	var result float64
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		panic(err)
	}
	return result
}

func init() {

	FfiConverterWalletDatabaseINSTANCE.register()
	uniffiCheckChecksums()
}

func uniffiCheckChecksums() {
	// Get the bindings contract version from our ComponentInterface
	bindingsContractVersion := 26
	// Get the scaffolding contract version by calling the into the dylib
	scaffoldingContractVersion := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint32_t {
		return C.ffi_cdk_ffi_uniffi_contract_version()
	})
	if bindingsContractVersion != int(scaffoldingContractVersion) {
		// If this happens try cleaning and rebuilding your project
		panic("cdk_ffi: UniFFI contract version mismatch")
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_create_wallet_db()
		})
		if checksum != 38981 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_create_wallet_db: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_auth_proof()
		})
		if checksum != 22357 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_auth_proof: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_conditions()
		})
		if checksum != 18453 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_conditions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_contact_info()
		})
		if checksum != 40231 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_contact_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_key_set()
		})
		if checksum != 64139 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_key_set: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_key_set_info()
		})
		if checksum != 26774 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_key_set_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_keys()
		})
		if checksum != 38114 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_melt_quote()
		})
		if checksum != 31843 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_mint_info()
		})
		if checksum != 4255 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_mint_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_mint_quote()
		})
		if checksum != 12595 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_mint_version()
		})
		if checksum != 54734 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_mint_version: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_nuts()
		})
		if checksum != 23702 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_nuts: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_proof_info()
		})
		if checksum != 19899 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_proof_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_proof_state_update()
		})
		if checksum != 25192 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_proof_state_update: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_receive_options()
		})
		if checksum != 46457 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_receive_options: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_send_memo()
		})
		if checksum != 6016 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_send_memo: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_send_options()
		})
		if checksum != 43827 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_send_options: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_subscribe_params()
		})
		if checksum != 6793 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_subscribe_params: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_decode_transaction()
		})
		if checksum != 48687 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_decode_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_auth_proof()
		})
		if checksum != 15755 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_auth_proof: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_conditions()
		})
		if checksum != 48516 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_conditions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_contact_info()
		})
		if checksum != 44629 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_contact_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_key_set()
		})
		if checksum != 10879 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_key_set: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_key_set_info()
		})
		if checksum != 18895 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_key_set_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_keys()
		})
		if checksum != 20045 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_melt_quote()
		})
		if checksum != 25080 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_mint_info()
		})
		if checksum != 31825 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_mint_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_mint_quote()
		})
		if checksum != 52375 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_mint_version()
		})
		if checksum != 3369 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_mint_version: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_nuts()
		})
		if checksum != 30942 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_nuts: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_proof_info()
		})
		if checksum != 32664 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_proof_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_proof_state_update()
		})
		if checksum != 62126 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_proof_state_update: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_receive_options()
		})
		if checksum != 34534 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_receive_options: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_send_memo()
		})
		if checksum != 10559 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_send_memo: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_send_options()
		})
		if checksum != 12512 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_send_options: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_subscribe_params()
		})
		if checksum != 58897 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_subscribe_params: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_encode_transaction()
		})
		if checksum != 38295 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_encode_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_generate_mnemonic()
		})
		if checksum != 17512 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_generate_mnemonic: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_func_mnemonic_to_entropy()
		})
		if checksum != 58572 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_func_mnemonic_to_entropy: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_activesubscription_id()
		})
		if checksum != 53295 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_activesubscription_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_activesubscription_recv()
		})
		if checksum != 64493 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_activesubscription_recv: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_activesubscription_try_recv()
		})
		if checksum != 8454 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_activesubscription_try_recv: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_amount()
		})
		if checksum != 52429 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_expiry()
		})
		if checksum != 54308 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_expiry: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_fee_reserve()
		})
		if checksum != 51947 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_fee_reserve: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_payment_preimage()
		})
		if checksum != 18401 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_payment_preimage: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_quote()
		})
		if checksum != 22053 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_request()
		})
		if checksum != 35924 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_request: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_state()
		})
		if checksum != 404 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_state: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_meltquotebolt11response_unit()
		})
		if checksum != 35868 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_meltquotebolt11response_unit: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_mintquotebolt11response_amount()
		})
		if checksum != 22699 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_mintquotebolt11response_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_mintquotebolt11response_expiry()
		})
		if checksum != 45849 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_mintquotebolt11response_expiry: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_mintquotebolt11response_pubkey()
		})
		if checksum != 41072 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_mintquotebolt11response_pubkey: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_mintquotebolt11response_quote()
		})
		if checksum != 10050 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_mintquotebolt11response_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_mintquotebolt11response_request()
		})
		if checksum != 23152 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_mintquotebolt11response_request: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_mintquotebolt11response_state()
		})
		if checksum != 39833 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_mintquotebolt11response_state: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_mintquotebolt11response_unit()
		})
		if checksum != 19782 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_mintquotebolt11response_unit: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_add_mint()
		})
		if checksum != 58913 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_add_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_check_all_mint_quotes()
		})
		if checksum != 50601 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_check_all_mint_quotes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_check_mint_quote()
		})
		if checksum != 50836 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_check_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_consolidate()
		})
		if checksum != 51458 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_consolidate: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_get_balances()
		})
		if checksum != 10177 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_get_balances: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_get_mint_urls()
		})
		if checksum != 40736 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_get_mint_urls: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_has_mint()
		})
		if checksum != 31683 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_has_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_list_proofs()
		})
		if checksum != 62650 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_list_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_list_transactions()
		})
		if checksum != 18245 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_list_transactions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_melt()
		})
		if checksum != 16024 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_melt: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_melt_quote()
		})
		if checksum != 24971 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_mint()
		})
		if checksum != 13280 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_mint_quote()
		})
		if checksum != 56223 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_prepare_send()
		})
		if checksum != 35914 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_prepare_send: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_receive()
		})
		if checksum != 54819 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_receive: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_remove_mint()
		})
		if checksum != 60048 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_remove_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_restore()
		})
		if checksum != 11050 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_restore: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_swap()
		})
		if checksum != 27574 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_swap: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_total_balance()
		})
		if checksum != 42451 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_total_balance: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_transfer()
		})
		if checksum != 29742 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_transfer: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_unit()
		})
		if checksum != 64911 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_unit: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_verify_token_dleq()
		})
		if checksum != 8825 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_verify_token_dleq: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_multimintwallet_wait_for_mint_quote()
		})
		if checksum != 35963 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_multimintwallet_wait_for_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_preparedsend_amount()
		})
		if checksum != 62180 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_preparedsend_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_preparedsend_cancel()
		})
		if checksum != 48000 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_preparedsend_cancel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_preparedsend_confirm()
		})
		if checksum != 5962 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_preparedsend_confirm: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_preparedsend_fee()
		})
		if checksum != 37119 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_preparedsend_fee: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_preparedsend_id()
		})
		if checksum != 18191 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_preparedsend_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_preparedsend_proofs()
		})
		if checksum != 23923 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_preparedsend_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_amount()
		})
		if checksum != 46072 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_c()
		})
		if checksum != 3807 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_c: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_dleq()
		})
		if checksum != 11014 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_dleq: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_has_dleq()
		})
		if checksum != 6642 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_has_dleq: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_is_active()
		})
		if checksum != 40357 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_is_active: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_keyset_id()
		})
		if checksum != 30712 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_keyset_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_secret()
		})
		if checksum != 58524 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_secret: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_witness()
		})
		if checksum != 11014 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_witness: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_proof_y()
		})
		if checksum != 27624 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_proof_y: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_encode()
		})
		if checksum != 53245 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_encode: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_htlc_hashes()
		})
		if checksum != 14335 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_htlc_hashes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_locktimes()
		})
		if checksum != 44524 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_locktimes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_memo()
		})
		if checksum != 28883 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_memo: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_mint_url()
		})
		if checksum != 16820 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_mint_url: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_p2pk_pubkeys()
		})
		if checksum != 56348 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_p2pk_pubkeys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_p2pk_refund_pubkeys()
		})
		if checksum != 16072 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_p2pk_refund_pubkeys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_proofs_simple()
		})
		if checksum != 24034 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_proofs_simple: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_spending_conditions()
		})
		if checksum != 55293 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_spending_conditions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_to_raw_bytes()
		})
		if checksum != 25396 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_to_raw_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_unit()
		})
		if checksum != 55723 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_unit: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_token_value()
		})
		if checksum != 22223 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_token_value: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_calculate_fee()
		})
		if checksum != 1751 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_calculate_fee: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_check_all_pending_proofs()
		})
		if checksum != 3292 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_check_all_pending_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_check_proofs_spent()
		})
		if checksum != 48196 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_check_proofs_spent: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_get_active_keyset()
		})
		if checksum != 55608 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_get_active_keyset: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_get_keyset_fees_by_id()
		})
		if checksum != 51180 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_get_keyset_fees_by_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_get_mint_info()
		})
		if checksum != 46501 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_get_mint_info: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_get_proofs_by_states()
		})
		if checksum != 63476 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_get_proofs_by_states: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_get_transaction()
		})
		if checksum != 62811 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_get_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_get_unspent_auth_proofs()
		})
		if checksum != 31137 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_get_unspent_auth_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_list_transactions()
		})
		if checksum != 20673 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_list_transactions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_melt()
		})
		if checksum != 33983 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_melt: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_melt_bip353_quote()
		})
		if checksum != 56775 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_melt_bip353_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_melt_bolt12_quote()
		})
		if checksum != 33749 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_melt_bolt12_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_melt_quote()
		})
		if checksum != 16819 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_mint()
		})
		if checksum != 61108 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_mint_blind_auth()
		})
		if checksum != 39214 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_mint_blind_auth: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_mint_bolt12()
		})
		if checksum != 60444 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_mint_bolt12: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_mint_bolt12_quote()
		})
		if checksum != 56408 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_mint_bolt12_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_mint_quote()
		})
		if checksum != 48314 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_mint_url()
		})
		if checksum != 6804 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_mint_url: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_prepare_send()
		})
		if checksum != 18579 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_prepare_send: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_receive()
		})
		if checksum != 34397 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_receive: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_receive_proofs()
		})
		if checksum != 17448 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_receive_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_reclaim_unspent()
		})
		if checksum != 35245 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_reclaim_unspent: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_refresh_access_token()
		})
		if checksum != 63251 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_refresh_access_token: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_refresh_keysets()
		})
		if checksum != 60028 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_refresh_keysets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_restore()
		})
		if checksum != 48186 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_restore: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_revert_transaction()
		})
		if checksum != 31115 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_revert_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_set_cat()
		})
		if checksum != 29016 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_set_cat: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_set_refresh_token()
		})
		if checksum != 28616 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_set_refresh_token: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_subscribe()
		})
		if checksum != 26376 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_subscribe: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_swap()
		})
		if checksum != 54923 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_swap: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_total_balance()
		})
		if checksum != 37325 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_total_balance: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_total_pending_balance()
		})
		if checksum != 26959 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_total_pending_balance: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_total_reserved_balance()
		})
		if checksum != 65325 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_total_reserved_balance: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_unit()
		})
		if checksum != 33359 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_unit: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_wallet_verify_token_dleq()
		})
		if checksum != 53589 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_wallet_verify_token_dleq: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_add_mint()
		})
		if checksum != 8275 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_add_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_remove_mint()
		})
		if checksum != 59506 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_remove_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint()
		})
		if checksum != 63376 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_mints()
		})
		if checksum != 52728 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_mints: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_update_mint_url()
		})
		if checksum != 60825 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_update_mint_url: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_add_mint_keysets()
		})
		if checksum != 37141 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_add_mint_keysets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint_keysets()
		})
		if checksum != 62744 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint_keysets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_keyset_by_id()
		})
		if checksum != 46829 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_keyset_by_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_add_mint_quote()
		})
		if checksum != 42839 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_add_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint_quote()
		})
		if checksum != 29507 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint_quotes()
		})
		if checksum != 16761 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_mint_quotes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_remove_mint_quote()
		})
		if checksum != 27636 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_remove_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_add_melt_quote()
		})
		if checksum != 55901 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_add_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_melt_quote()
		})
		if checksum != 59625 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_melt_quotes()
		})
		if checksum != 4012 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_melt_quotes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_remove_melt_quote()
		})
		if checksum != 12849 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_remove_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_add_keys()
		})
		if checksum != 33500 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_add_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_keys()
		})
		if checksum != 17359 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_remove_keys()
		})
		if checksum != 50621 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_remove_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_update_proofs()
		})
		if checksum != 18069 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_update_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_proofs()
		})
		if checksum != 10055 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_balance()
		})
		if checksum != 45326 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_balance: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_update_proofs_state()
		})
		if checksum != 45263 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_update_proofs_state: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_increment_keyset_counter()
		})
		if checksum != 46275 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_increment_keyset_counter: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_add_transaction()
		})
		if checksum != 22182 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_add_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_get_transaction()
		})
		if checksum != 30893 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_get_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_list_transactions()
		})
		if checksum != 60538 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_list_transactions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletdatabase_remove_transaction()
		})
		if checksum != 45463 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletdatabase_remove_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_keys()
		})
		if checksum != 56387 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_melt_quote()
		})
		if checksum != 14392 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_mint()
		})
		if checksum != 29694 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_mint_keysets()
		})
		if checksum != 63125 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_mint_keysets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_mint_quote()
		})
		if checksum != 18330 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_transaction()
		})
		if checksum != 60425 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_add_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_clone_as_trait()
		})
		if checksum != 16090 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_clone_as_trait: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_balance()
		})
		if checksum != 26475 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_balance: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_keys()
		})
		if checksum != 1364 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_keyset_by_id()
		})
		if checksum != 47211 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_keyset_by_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_melt_quote()
		})
		if checksum != 15686 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_melt_quotes()
		})
		if checksum != 61301 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_melt_quotes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint()
		})
		if checksum != 1440 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint_keysets()
		})
		if checksum != 52552 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint_keysets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint_quote()
		})
		if checksum != 62393 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint_quotes()
		})
		if checksum != 37612 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mint_quotes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mints()
		})
		if checksum != 51201 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_mints: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_proofs()
		})
		if checksum != 17876 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_transaction()
		})
		if checksum != 16334 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_get_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_increment_keyset_counter()
		})
		if checksum != 11359 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_increment_keyset_counter: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_list_transactions()
		})
		if checksum != 57613 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_list_transactions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_keys()
		})
		if checksum != 3270 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_melt_quote()
		})
		if checksum != 13050 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_mint()
		})
		if checksum != 52702 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_mint_quote()
		})
		if checksum != 40583 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_transaction()
		})
		if checksum != 19625 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_remove_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_update_mint_url()
		})
		if checksum != 44171 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_update_mint_url: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_update_proofs()
		})
		if checksum != 54294 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_update_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_update_proofs_state()
		})
		if checksum != 58913 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletpostgresdatabase_update_proofs_state: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_keys()
		})
		if checksum != 5879 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_melt_quote()
		})
		if checksum != 34892 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_mint()
		})
		if checksum != 44674 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_mint_keysets()
		})
		if checksum != 13932 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_mint_keysets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_mint_quote()
		})
		if checksum != 62077 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_transaction()
		})
		if checksum != 26193 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_add_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_balance()
		})
		if checksum != 3300 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_balance: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_keys()
		})
		if checksum != 41498 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_keyset_by_id()
		})
		if checksum != 37425 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_keyset_by_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_melt_quote()
		})
		if checksum != 31302 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_melt_quotes()
		})
		if checksum != 1543 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_melt_quotes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint()
		})
		if checksum != 23917 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint_keysets()
		})
		if checksum != 13541 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint_keysets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint_quote()
		})
		if checksum != 57388 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint_quotes()
		})
		if checksum != 50536 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mint_quotes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mints()
		})
		if checksum != 14065 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_mints: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_proofs()
		})
		if checksum != 48231 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_transaction()
		})
		if checksum != 52949 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_get_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_increment_keyset_counter()
		})
		if checksum != 61780 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_increment_keyset_counter: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_list_transactions()
		})
		if checksum != 22793 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_list_transactions: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_keys()
		})
		if checksum != 64071 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_keys: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_melt_quote()
		})
		if checksum != 16969 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_melt_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_mint()
		})
		if checksum != 32740 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_mint: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_mint_quote()
		})
		if checksum != 55358 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_mint_quote: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_transaction()
		})
		if checksum != 38835 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_remove_transaction: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_update_mint_url()
		})
		if checksum != 2109 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_update_mint_url: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_update_proofs()
		})
		if checksum != 23133 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_update_proofs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_update_proofs_state()
		})
		if checksum != 51402 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_method_walletsqlitedatabase_update_proofs_state: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_multimintwallet_new()
		})
		if checksum != 56682 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_multimintwallet_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_multimintwallet_new_with_proxy()
		})
		if checksum != 52208 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_multimintwallet_new_with_proxy: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_token_decode()
		})
		if checksum != 17843 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_token_decode: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_token_from_string()
		})
		if checksum != 43724 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_token_from_string: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_wallet_new()
		})
		if checksum != 10944 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_wallet_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_walletpostgresdatabase_new()
		})
		if checksum != 43914 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_walletpostgresdatabase_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_walletsqlitedatabase_new()
		})
		if checksum != 10235 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_walletsqlitedatabase_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_cdk_ffi_checksum_constructor_walletsqlitedatabase_new_in_memory()
		})
		if checksum != 41747 {
			// If this happens try cleaning and rebuilding your project
			panic("cdk_ffi: uniffi_cdk_ffi_checksum_constructor_walletsqlitedatabase_new_in_memory: UniFFI API checksum mismatch")
		}
	}
}

type FfiConverterUint8 struct{}

var FfiConverterUint8INSTANCE = FfiConverterUint8{}

func (FfiConverterUint8) Lower(value uint8) C.uint8_t {
	return C.uint8_t(value)
}

func (FfiConverterUint8) Write(writer io.Writer, value uint8) {
	writeUint8(writer, value)
}

func (FfiConverterUint8) Lift(value C.uint8_t) uint8 {
	return uint8(value)
}

func (FfiConverterUint8) Read(reader io.Reader) uint8 {
	return readUint8(reader)
}

type FfiDestroyerUint8 struct{}

func (FfiDestroyerUint8) Destroy(_ uint8) {}

type FfiConverterUint32 struct{}

var FfiConverterUint32INSTANCE = FfiConverterUint32{}

func (FfiConverterUint32) Lower(value uint32) C.uint32_t {
	return C.uint32_t(value)
}

func (FfiConverterUint32) Write(writer io.Writer, value uint32) {
	writeUint32(writer, value)
}

func (FfiConverterUint32) Lift(value C.uint32_t) uint32 {
	return uint32(value)
}

func (FfiConverterUint32) Read(reader io.Reader) uint32 {
	return readUint32(reader)
}

type FfiDestroyerUint32 struct{}

func (FfiDestroyerUint32) Destroy(_ uint32) {}

type FfiConverterUint64 struct{}

var FfiConverterUint64INSTANCE = FfiConverterUint64{}

func (FfiConverterUint64) Lower(value uint64) C.uint64_t {
	return C.uint64_t(value)
}

func (FfiConverterUint64) Write(writer io.Writer, value uint64) {
	writeUint64(writer, value)
}

func (FfiConverterUint64) Lift(value C.uint64_t) uint64 {
	return uint64(value)
}

func (FfiConverterUint64) Read(reader io.Reader) uint64 {
	return readUint64(reader)
}

type FfiDestroyerUint64 struct{}

func (FfiDestroyerUint64) Destroy(_ uint64) {}

type FfiConverterBool struct{}

var FfiConverterBoolINSTANCE = FfiConverterBool{}

func (FfiConverterBool) Lower(value bool) C.int8_t {
	if value {
		return C.int8_t(1)
	}
	return C.int8_t(0)
}

func (FfiConverterBool) Write(writer io.Writer, value bool) {
	if value {
		writeInt8(writer, 1)
	} else {
		writeInt8(writer, 0)
	}
}

func (FfiConverterBool) Lift(value C.int8_t) bool {
	return value != 0
}

func (FfiConverterBool) Read(reader io.Reader) bool {
	return readInt8(reader) != 0
}

type FfiDestroyerBool struct{}

func (FfiDestroyerBool) Destroy(_ bool) {}

type FfiConverterString struct{}

var FfiConverterStringINSTANCE = FfiConverterString{}

func (FfiConverterString) Lift(rb RustBufferI) string {
	defer rb.Free()
	reader := rb.AsReader()
	b, err := io.ReadAll(reader)
	if err != nil {
		panic(fmt.Errorf("reading reader: %w", err))
	}
	return string(b)
}

func (FfiConverterString) Read(reader io.Reader) string {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading string, expected %d, read %d", length, read_length))
	}
	return string(buffer)
}

func (FfiConverterString) Lower(value string) C.RustBuffer {
	return stringToRustBuffer(value)
}

func (FfiConverterString) Write(writer io.Writer, value string) {
	if len(value) > math.MaxInt32 {
		panic("String is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := io.WriteString(writer, value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing string, expected %d, written %d", len(value), write_length))
	}
}

type FfiDestroyerString struct{}

func (FfiDestroyerString) Destroy(_ string) {}

type FfiConverterBytes struct{}

var FfiConverterBytesINSTANCE = FfiConverterBytes{}

func (c FfiConverterBytes) Lower(value []byte) C.RustBuffer {
	return LowerIntoRustBuffer[[]byte](c, value)
}

func (c FfiConverterBytes) Write(writer io.Writer, value []byte) {
	if len(value) > math.MaxInt32 {
		panic("[]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	write_length, err := writer.Write(value)
	if err != nil {
		panic(err)
	}
	if write_length != len(value) {
		panic(fmt.Errorf("bad write length when writing []byte, expected %d, written %d", len(value), write_length))
	}
}

func (c FfiConverterBytes) Lift(rb RustBufferI) []byte {
	return LiftFromRustBuffer[[]byte](c, rb)
}

func (c FfiConverterBytes) Read(reader io.Reader) []byte {
	length := readInt32(reader)
	buffer := make([]byte, length)
	read_length, err := reader.Read(buffer)
	if err != nil && err != io.EOF {
		panic(err)
	}
	if read_length != int(length) {
		panic(fmt.Errorf("bad read length when reading []byte, expected %d, read %d", length, read_length))
	}
	return buffer
}

type FfiDestroyerBytes struct{}

func (FfiDestroyerBytes) Destroy(_ []byte) {}

// Below is an implementation of synchronization requirements outlined in the link.
// https://github.com/mozilla/uniffi-rs/blob/0dc031132d9493ca812c3af6e7dd60ad2ea95bf0/uniffi_bindgen/src/bindings/kotlin/templates/ObjectRuntime.kt#L31

type FfiObject struct {
	pointer       unsafe.Pointer
	callCounter   atomic.Int64
	cloneFunction func(unsafe.Pointer, *C.RustCallStatus) unsafe.Pointer
	freeFunction  func(unsafe.Pointer, *C.RustCallStatus)
	destroyed     atomic.Bool
}

func newFfiObject(
	pointer unsafe.Pointer,
	cloneFunction func(unsafe.Pointer, *C.RustCallStatus) unsafe.Pointer,
	freeFunction func(unsafe.Pointer, *C.RustCallStatus),
) FfiObject {
	return FfiObject{
		pointer:       pointer,
		cloneFunction: cloneFunction,
		freeFunction:  freeFunction,
	}
}

func (ffiObject *FfiObject) incrementPointer(debugName string) unsafe.Pointer {
	for {
		counter := ffiObject.callCounter.Load()
		if counter <= -1 {
			panic(fmt.Errorf("%v object has already been destroyed", debugName))
		}
		if counter == math.MaxInt64 {
			panic(fmt.Errorf("%v object call counter would overflow", debugName))
		}
		if ffiObject.callCounter.CompareAndSwap(counter, counter+1) {
			break
		}
	}

	return rustCall(func(status *C.RustCallStatus) unsafe.Pointer {
		return ffiObject.cloneFunction(ffiObject.pointer, status)
	})
}

func (ffiObject *FfiObject) decrementPointer() {
	if ffiObject.callCounter.Add(-1) == -1 {
		ffiObject.freeRustArcPtr()
	}
}

func (ffiObject *FfiObject) destroy() {
	if ffiObject.destroyed.CompareAndSwap(false, true) {
		if ffiObject.callCounter.Add(-1) == -1 {
			ffiObject.freeRustArcPtr()
		}
	}
}

func (ffiObject *FfiObject) freeRustArcPtr() {
	rustCall(func(status *C.RustCallStatus) int32 {
		ffiObject.freeFunction(ffiObject.pointer, status)
		return 0
	})
}

// FFI-compatible ActiveSubscription
type ActiveSubscriptionInterface interface {
	// Get the subscription ID
	Id() string
	// Receive the next notification
	Recv() (NotificationPayload, error)
	// Try to receive a notification without blocking
	TryRecv() (*NotificationPayload, error)
}

// FFI-compatible ActiveSubscription
type ActiveSubscription struct {
	ffiObject FfiObject
}

// Get the subscription ID
func (_self *ActiveSubscription) Id() string {
	_pointer := _self.ffiObject.incrementPointer("*ActiveSubscription")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_activesubscription_id(
				_pointer, _uniffiStatus),
		}
	}))
}

// Receive the next notification
func (_self *ActiveSubscription) Recv() (NotificationPayload, error) {
	_pointer := _self.ffiObject.incrementPointer("*ActiveSubscription")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) NotificationPayload {
			return FfiConverterNotificationPayloadINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_activesubscription_recv(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Try to receive a notification without blocking
func (_self *ActiveSubscription) TryRecv() (*NotificationPayload, error) {
	_pointer := _self.ffiObject.incrementPointer("*ActiveSubscription")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *NotificationPayload {
			return FfiConverterOptionalNotificationPayloadINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_activesubscription_try_recv(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}
func (object *ActiveSubscription) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterActiveSubscription struct{}

var FfiConverterActiveSubscriptionINSTANCE = FfiConverterActiveSubscription{}

func (c FfiConverterActiveSubscription) Lift(pointer unsafe.Pointer) *ActiveSubscription {
	result := &ActiveSubscription{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_activesubscription(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_activesubscription(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*ActiveSubscription).Destroy)
	return result
}

func (c FfiConverterActiveSubscription) Read(reader io.Reader) *ActiveSubscription {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterActiveSubscription) Lower(value *ActiveSubscription) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*ActiveSubscription")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterActiveSubscription) Write(writer io.Writer, value *ActiveSubscription) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerActiveSubscription struct{}

func (_ FfiDestroyerActiveSubscription) Destroy(value *ActiveSubscription) {
	value.Destroy()
}

// FFI-compatible MeltQuoteBolt11Response
type MeltQuoteBolt11ResponseInterface interface {
	// Get amount
	Amount() Amount
	// Get expiry
	Expiry() uint64
	// Get fee reserve
	FeeReserve() Amount
	// Get payment preimage
	PaymentPreimage() *string
	// Get quote ID
	Quote() string
	// Get request
	Request() *string
	// Get state
	State() QuoteState
	// Get unit
	Unit() *CurrencyUnit
}

// FFI-compatible MeltQuoteBolt11Response
type MeltQuoteBolt11Response struct {
	ffiObject FfiObject
}

// Get amount
func (_self *MeltQuoteBolt11Response) Amount() Amount {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_amount(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get expiry
func (_self *MeltQuoteBolt11Response) Expiry() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_expiry(
			_pointer, _uniffiStatus)
	}))
}

// Get fee reserve
func (_self *MeltQuoteBolt11Response) FeeReserve() Amount {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_fee_reserve(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get payment preimage
func (_self *MeltQuoteBolt11Response) PaymentPreimage() *string {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_payment_preimage(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get quote ID
func (_self *MeltQuoteBolt11Response) Quote() string {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_quote(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get request
func (_self *MeltQuoteBolt11Response) Request() *string {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_request(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get state
func (_self *MeltQuoteBolt11Response) State() QuoteState {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterQuoteStateINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_state(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get unit
func (_self *MeltQuoteBolt11Response) Unit() *CurrencyUnit {
	_pointer := _self.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalCurrencyUnitINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_meltquotebolt11response_unit(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *MeltQuoteBolt11Response) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterMeltQuoteBolt11Response struct{}

var FfiConverterMeltQuoteBolt11ResponseINSTANCE = FfiConverterMeltQuoteBolt11Response{}

func (c FfiConverterMeltQuoteBolt11Response) Lift(pointer unsafe.Pointer) *MeltQuoteBolt11Response {
	result := &MeltQuoteBolt11Response{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_meltquotebolt11response(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_meltquotebolt11response(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*MeltQuoteBolt11Response).Destroy)
	return result
}

func (c FfiConverterMeltQuoteBolt11Response) Read(reader io.Reader) *MeltQuoteBolt11Response {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterMeltQuoteBolt11Response) Lower(value *MeltQuoteBolt11Response) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*MeltQuoteBolt11Response")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterMeltQuoteBolt11Response) Write(writer io.Writer, value *MeltQuoteBolt11Response) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerMeltQuoteBolt11Response struct{}

func (_ FfiDestroyerMeltQuoteBolt11Response) Destroy(value *MeltQuoteBolt11Response) {
	value.Destroy()
}

// FFI-compatible MintQuoteBolt11Response
type MintQuoteBolt11ResponseInterface interface {
	// Get amount
	Amount() *Amount
	// Get expiry
	Expiry() *uint64
	// Get pubkey
	Pubkey() *string
	// Get quote ID
	Quote() string
	// Get request string
	Request() string
	// Get state
	State() QuoteState
	// Get unit
	Unit() *CurrencyUnit
}

// FFI-compatible MintQuoteBolt11Response
type MintQuoteBolt11Response struct {
	ffiObject FfiObject
}

// Get amount
func (_self *MintQuoteBolt11Response) Amount() *Amount {
	_pointer := _self.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_mintquotebolt11response_amount(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get expiry
func (_self *MintQuoteBolt11Response) Expiry() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_mintquotebolt11response_expiry(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get pubkey
func (_self *MintQuoteBolt11Response) Pubkey() *string {
	_pointer := _self.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_mintquotebolt11response_pubkey(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get quote ID
func (_self *MintQuoteBolt11Response) Quote() string {
	_pointer := _self.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_mintquotebolt11response_quote(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get request string
func (_self *MintQuoteBolt11Response) Request() string {
	_pointer := _self.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_mintquotebolt11response_request(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get state
func (_self *MintQuoteBolt11Response) State() QuoteState {
	_pointer := _self.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterQuoteStateINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_mintquotebolt11response_state(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get unit
func (_self *MintQuoteBolt11Response) Unit() *CurrencyUnit {
	_pointer := _self.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalCurrencyUnitINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_mintquotebolt11response_unit(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *MintQuoteBolt11Response) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterMintQuoteBolt11Response struct{}

var FfiConverterMintQuoteBolt11ResponseINSTANCE = FfiConverterMintQuoteBolt11Response{}

func (c FfiConverterMintQuoteBolt11Response) Lift(pointer unsafe.Pointer) *MintQuoteBolt11Response {
	result := &MintQuoteBolt11Response{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_mintquotebolt11response(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_mintquotebolt11response(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*MintQuoteBolt11Response).Destroy)
	return result
}

func (c FfiConverterMintQuoteBolt11Response) Read(reader io.Reader) *MintQuoteBolt11Response {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterMintQuoteBolt11Response) Lower(value *MintQuoteBolt11Response) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*MintQuoteBolt11Response")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterMintQuoteBolt11Response) Write(writer io.Writer, value *MintQuoteBolt11Response) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerMintQuoteBolt11Response struct{}

func (_ FfiDestroyerMintQuoteBolt11Response) Destroy(value *MintQuoteBolt11Response) {
	value.Destroy()
}

// FFI-compatible MultiMintWallet
type MultiMintWalletInterface interface {
	// Add a mint to this MultiMintWallet
	AddMint(mintUrl MintUrl, targetProofCount *uint32) error
	// Check all mint quotes and mint if paid
	CheckAllMintQuotes(mintUrl *MintUrl) (Amount, error)
	// Check a specific mint quote status
	CheckMintQuote(mintUrl MintUrl, quoteId string) (MintQuote, error)
	// Consolidate proofs across all mints
	Consolidate() (Amount, error)
	// Get wallet balances for all mints
	GetBalances() (map[string]Amount, error)
	// Get list of mint URLs
	GetMintUrls() []string
	// Check if mint is in wallet
	HasMint(mintUrl MintUrl) bool
	// List proofs for all mints
	ListProofs() (map[string][]*Proof, error)
	// List transactions from all mints
	ListTransactions(direction *TransactionDirection) ([]Transaction, error)
	// Melt tokens (pay a bolt11 invoice)
	Melt(bolt11 string, options *MeltOptions, maxFee *Amount) (Melted, error)
	// Get a melt quote from a specific mint
	MeltQuote(mintUrl MintUrl, request string, options *MeltOptions) (MeltQuote, error)
	// Mint tokens at a specific mint
	Mint(mintUrl MintUrl, quoteId string, spendingConditions *SpendingConditions) ([]*Proof, error)
	// Get a mint quote from a specific mint
	MintQuote(mintUrl MintUrl, amount Amount, description *string) (MintQuote, error)
	// Prepare a send operation from a specific mint
	PrepareSend(mintUrl MintUrl, amount Amount, options MultiMintSendOptions) (*PreparedSend, error)
	// Receive token
	Receive(token *Token, options MultiMintReceiveOptions) (Amount, error)
	// Remove mint from MultiMintWallet
	RemoveMint(mintUrl MintUrl)
	// Restore wallets for a specific mint
	Restore(mintUrl MintUrl) (Amount, error)
	// Swap proofs with automatic wallet selection
	Swap(amount *Amount, spendingConditions *SpendingConditions) (*[]*Proof, error)
	// Get total balance across all mints
	TotalBalance() (Amount, error)
	// Transfer funds between mints
	Transfer(sourceMint MintUrl, targetMint MintUrl, transferMode TransferMode) (TransferResult, error)
	// Get the currency unit for this wallet
	Unit() CurrencyUnit
	// Verify token DLEQ proofs
	VerifyTokenDleq(token *Token) error
	// Wait for a mint quote to be paid and automatically mint the proofs
	WaitForMintQuote(mintUrl MintUrl, quoteId string, splitTarget SplitTarget, spendingConditions *SpendingConditions, timeoutSecs uint64) ([]*Proof, error)
}

// FFI-compatible MultiMintWallet
type MultiMintWallet struct {
	ffiObject FfiObject
}

// Create a new MultiMintWallet from mnemonic using WalletDatabase trait
func NewMultiMintWallet(unit CurrencyUnit, mnemonic string, db WalletDatabase) (*MultiMintWallet, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_multimintwallet_new(FfiConverterCurrencyUnitINSTANCE.Lower(unit), FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterWalletDatabaseINSTANCE.Lower(db), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *MultiMintWallet
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterMultiMintWalletINSTANCE.Lift(_uniffiRV), nil
	}
}

// Create a new MultiMintWallet with proxy configuration
func MultiMintWalletNewWithProxy(unit CurrencyUnit, mnemonic string, db WalletDatabase, proxyUrl string) (*MultiMintWallet, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_multimintwallet_new_with_proxy(FfiConverterCurrencyUnitINSTANCE.Lower(unit), FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterWalletDatabaseINSTANCE.Lower(db), FfiConverterStringINSTANCE.Lower(proxyUrl), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *MultiMintWallet
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterMultiMintWalletINSTANCE.Lift(_uniffiRV), nil
	}
}

// Add a mint to this MultiMintWallet
func (_self *MultiMintWallet) AddMint(mintUrl MintUrl, targetProofCount *uint32) error {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_multimintwallet_add_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalUint32INSTANCE.Lower(targetProofCount)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Check all mint quotes and mint if paid
func (_self *MultiMintWallet) CheckAllMintQuotes(mintUrl *MintUrl) (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_check_all_mint_quotes(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Check a specific mint quote status
func (_self *MultiMintWallet) CheckMintQuote(mintUrl MintUrl, quoteId string) (MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MintQuote {
			return FfiConverterMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_check_mint_quote(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Consolidate proofs across all mints
func (_self *MultiMintWallet) Consolidate() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_consolidate(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get wallet balances for all mints
func (_self *MultiMintWallet) GetBalances() (map[string]Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) map[string]Amount {
			return FfiConverterMapStringAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_get_balances(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get list of mint URLs
func (_self *MultiMintWallet) GetMintUrls() []string {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, _ := uniffiRustCallAsync[error](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []string {
			return FfiConverterSequenceStringINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_get_mint_urls(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	return res
}

// Check if mint is in wallet
func (_self *MultiMintWallet) HasMint(mintUrl MintUrl) bool {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, _ := uniffiRustCallAsync[error](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.int8_t {
			res := C.ffi_cdk_ffi_rust_future_complete_i8(handle, status)
			return res
		},
		// liftFn
		func(ffi C.int8_t) bool {
			return FfiConverterBoolINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_has_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_i8(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_i8(handle)
		},
	)

	return res
}

// List proofs for all mints
func (_self *MultiMintWallet) ListProofs() (map[string][]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) map[string][]*Proof {
			return FfiConverterMapStringSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_list_proofs(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// List transactions from all mints
func (_self *MultiMintWallet) ListTransactions(direction *TransactionDirection) ([]Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []Transaction {
			return FfiConverterSequenceTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_list_transactions(
			_pointer, FfiConverterOptionalTransactionDirectionINSTANCE.Lower(direction)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Melt tokens (pay a bolt11 invoice)
func (_self *MultiMintWallet) Melt(bolt11 string, options *MeltOptions, maxFee *Amount) (Melted, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Melted {
			return FfiConverterMeltedINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_melt(
			_pointer, FfiConverterStringINSTANCE.Lower(bolt11), FfiConverterOptionalMeltOptionsINSTANCE.Lower(options), FfiConverterOptionalAmountINSTANCE.Lower(maxFee)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get a melt quote from a specific mint
func (_self *MultiMintWallet) MeltQuote(mintUrl MintUrl, request string, options *MeltOptions) (MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MeltQuote {
			return FfiConverterMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_melt_quote(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterStringINSTANCE.Lower(request), FfiConverterOptionalMeltOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Mint tokens at a specific mint
func (_self *MultiMintWallet) Mint(mintUrl MintUrl, quoteId string, spendingConditions *SpendingConditions) ([]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Proof {
			return FfiConverterSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterStringINSTANCE.Lower(quoteId), FfiConverterOptionalSpendingConditionsINSTANCE.Lower(spendingConditions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get a mint quote from a specific mint
func (_self *MultiMintWallet) MintQuote(mintUrl MintUrl, amount Amount, description *string) (MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MintQuote {
			return FfiConverterMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_mint_quote(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterAmountINSTANCE.Lower(amount), FfiConverterOptionalStringINSTANCE.Lower(description)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Prepare a send operation from a specific mint
func (_self *MultiMintWallet) PrepareSend(mintUrl MintUrl, amount Amount, options MultiMintSendOptions) (*PreparedSend, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_cdk_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *PreparedSend {
			return FfiConverterPreparedSendINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_prepare_send(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterAmountINSTANCE.Lower(amount), FfiConverterMultiMintSendOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_pointer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Receive token
func (_self *MultiMintWallet) Receive(token *Token, options MultiMintReceiveOptions) (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_receive(
			_pointer, FfiConverterTokenINSTANCE.Lower(token), FfiConverterMultiMintReceiveOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Remove mint from MultiMintWallet
func (_self *MultiMintWallet) RemoveMint(mintUrl MintUrl) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	uniffiRustCallAsync[error](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_multimintwallet_remove_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

}

// Restore wallets for a specific mint
func (_self *MultiMintWallet) Restore(mintUrl MintUrl) (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_restore(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Swap proofs with automatic wallet selection
func (_self *MultiMintWallet) Swap(amount *Amount, spendingConditions *SpendingConditions) (*[]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *[]*Proof {
			return FfiConverterOptionalSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_swap(
			_pointer, FfiConverterOptionalAmountINSTANCE.Lower(amount), FfiConverterOptionalSpendingConditionsINSTANCE.Lower(spendingConditions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get total balance across all mints
func (_self *MultiMintWallet) TotalBalance() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_total_balance(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Transfer funds between mints
func (_self *MultiMintWallet) Transfer(sourceMint MintUrl, targetMint MintUrl, transferMode TransferMode) (TransferResult, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) TransferResult {
			return FfiConverterTransferResultINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_transfer(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(sourceMint), FfiConverterMintUrlINSTANCE.Lower(targetMint), FfiConverterTransferModeINSTANCE.Lower(transferMode)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get the currency unit for this wallet
func (_self *MultiMintWallet) Unit() CurrencyUnit {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterCurrencyUnitINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_multimintwallet_unit(
				_pointer, _uniffiStatus),
		}
	}))
}

// Verify token DLEQ proofs
func (_self *MultiMintWallet) VerifyTokenDleq(token *Token) error {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_multimintwallet_verify_token_dleq(
			_pointer, FfiConverterTokenINSTANCE.Lower(token)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Wait for a mint quote to be paid and automatically mint the proofs
func (_self *MultiMintWallet) WaitForMintQuote(mintUrl MintUrl, quoteId string, splitTarget SplitTarget, spendingConditions *SpendingConditions, timeoutSecs uint64) ([]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*MultiMintWallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Proof {
			return FfiConverterSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_multimintwallet_wait_for_mint_quote(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterStringINSTANCE.Lower(quoteId), FfiConverterSplitTargetINSTANCE.Lower(splitTarget), FfiConverterOptionalSpendingConditionsINSTANCE.Lower(spendingConditions), FfiConverterUint64INSTANCE.Lower(timeoutSecs)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}
func (object *MultiMintWallet) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterMultiMintWallet struct{}

var FfiConverterMultiMintWalletINSTANCE = FfiConverterMultiMintWallet{}

func (c FfiConverterMultiMintWallet) Lift(pointer unsafe.Pointer) *MultiMintWallet {
	result := &MultiMintWallet{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_multimintwallet(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_multimintwallet(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*MultiMintWallet).Destroy)
	return result
}

func (c FfiConverterMultiMintWallet) Read(reader io.Reader) *MultiMintWallet {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterMultiMintWallet) Lower(value *MultiMintWallet) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*MultiMintWallet")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterMultiMintWallet) Write(writer io.Writer, value *MultiMintWallet) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerMultiMintWallet struct{}

func (_ FfiDestroyerMultiMintWallet) Destroy(value *MultiMintWallet) {
	value.Destroy()
}

// FFI-compatible PreparedSend
type PreparedSendInterface interface {
	// Get the amount to send
	Amount() Amount
	// Cancel the prepared send operation
	Cancel() error
	// Confirm the prepared send and create a token
	Confirm(memo *string) (*Token, error)
	// Get the total fee for this send operation
	Fee() Amount
	// Get the prepared send ID
	Id() string
	// Get the proofs that will be used
	Proofs() []*Proof
}

// FFI-compatible PreparedSend
type PreparedSend struct {
	ffiObject FfiObject
}

// Get the amount to send
func (_self *PreparedSend) Amount() Amount {
	_pointer := _self.ffiObject.incrementPointer("*PreparedSend")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_preparedsend_amount(
				_pointer, _uniffiStatus),
		}
	}))
}

// Cancel the prepared send operation
func (_self *PreparedSend) Cancel() error {
	_pointer := _self.ffiObject.incrementPointer("*PreparedSend")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_preparedsend_cancel(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Confirm the prepared send and create a token
func (_self *PreparedSend) Confirm(memo *string) (*Token, error) {
	_pointer := _self.ffiObject.incrementPointer("*PreparedSend")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_cdk_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *Token {
			return FfiConverterTokenINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_preparedsend_confirm(
			_pointer, FfiConverterOptionalStringINSTANCE.Lower(memo)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_pointer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get the total fee for this send operation
func (_self *PreparedSend) Fee() Amount {
	_pointer := _self.ffiObject.incrementPointer("*PreparedSend")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_preparedsend_fee(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the prepared send ID
func (_self *PreparedSend) Id() string {
	_pointer := _self.ffiObject.incrementPointer("*PreparedSend")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_preparedsend_id(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the proofs that will be used
func (_self *PreparedSend) Proofs() []*Proof {
	_pointer := _self.ffiObject.incrementPointer("*PreparedSend")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceProofINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_preparedsend_proofs(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *PreparedSend) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterPreparedSend struct{}

var FfiConverterPreparedSendINSTANCE = FfiConverterPreparedSend{}

func (c FfiConverterPreparedSend) Lift(pointer unsafe.Pointer) *PreparedSend {
	result := &PreparedSend{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_preparedsend(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_preparedsend(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*PreparedSend).Destroy)
	return result
}

func (c FfiConverterPreparedSend) Read(reader io.Reader) *PreparedSend {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterPreparedSend) Lower(value *PreparedSend) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*PreparedSend")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterPreparedSend) Write(writer io.Writer, value *PreparedSend) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerPreparedSend struct{}

func (_ FfiDestroyerPreparedSend) Destroy(value *PreparedSend) {
	value.Destroy()
}

// FFI-compatible Proof
type ProofInterface interface {
	// Get the amount
	Amount() Amount
	// Get the unblinded signature (C) as string
	C() string
	// Get the DLEQ proof if present
	Dleq() *ProofDleq
	// Check if proof has DLEQ proof
	HasDleq() bool
	// Check if proof is active with given keyset IDs
	IsActive(activeKeysetIds []string) bool
	// Get the keyset ID as string
	KeysetId() string
	// Get the secret as string
	Secret() string
	// Get the witness
	Witness() *Witness
	// Get the Y value (hash_to_curve of secret)
	Y() (string, error)
}

// FFI-compatible Proof
type Proof struct {
	ffiObject FfiObject
}

// Get the amount
func (_self *Proof) Amount() Amount {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_proof_amount(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the unblinded signature (C) as string
func (_self *Proof) C() string {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_proof_c(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the DLEQ proof if present
func (_self *Proof) Dleq() *ProofDleq {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalProofDleqINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_proof_dleq(
				_pointer, _uniffiStatus),
		}
	}))
}

// Check if proof has DLEQ proof
func (_self *Proof) HasDleq() bool {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_cdk_ffi_fn_method_proof_has_dleq(
			_pointer, _uniffiStatus)
	}))
}

// Check if proof is active with given keyset IDs
func (_self *Proof) IsActive(activeKeysetIds []string) bool {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_cdk_ffi_fn_method_proof_is_active(
			_pointer, FfiConverterSequenceStringINSTANCE.Lower(activeKeysetIds), _uniffiStatus)
	}))
}

// Get the keyset ID as string
func (_self *Proof) KeysetId() string {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_proof_keyset_id(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the secret as string
func (_self *Proof) Secret() string {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_proof_secret(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the witness
func (_self *Proof) Witness() *Witness {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalWitnessINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_proof_witness(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the Y value (hash_to_curve of secret)
func (_self *Proof) Y() (string, error) {
	_pointer := _self.ffiObject.incrementPointer("*Proof")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_proof_y(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}
func (object *Proof) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterProof struct{}

var FfiConverterProofINSTANCE = FfiConverterProof{}

func (c FfiConverterProof) Lift(pointer unsafe.Pointer) *Proof {
	result := &Proof{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_proof(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_proof(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Proof).Destroy)
	return result
}

func (c FfiConverterProof) Read(reader io.Reader) *Proof {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterProof) Lower(value *Proof) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Proof")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterProof) Write(writer io.Writer, value *Proof) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerProof struct{}

func (_ FfiDestroyerProof) Destroy(value *Proof) {
	value.Destroy()
}

// FFI-compatible Token
type TokenInterface interface {
	// Encode token to string representation
	Encode() string
	// Return all HTLC hashes from spending conditions
	HtlcHashes() []string
	// Return all locktimes from spending conditions (sorted ascending)
	Locktimes() []uint64
	// Get the memo from the token
	Memo() *string
	// Get the mint URL
	MintUrl() (MintUrl, error)
	// Return all P2PK pubkeys referenced by this token's spending conditions
	P2pkPubkeys() []string
	// Return all refund pubkeys from P2PK spending conditions
	P2pkRefundPubkeys() []string
	// Get proofs from the token (simplified - no keyset filtering for now)
	ProofsSimple() ([]*Proof, error)
	// Return unique spending conditions across all proofs in this token
	SpendingConditions() []SpendingConditions
	// Convert token to raw bytes
	ToRawBytes() ([]byte, error)
	// Get the currency unit
	Unit() *CurrencyUnit
	// Get the total value of the token
	Value() (Amount, error)
}

// FFI-compatible Token
type Token struct {
	ffiObject FfiObject
}

// Decode token from string representation
func TokenDecode(encodedToken string) (*Token, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_token_decode(FfiConverterStringINSTANCE.Lower(encodedToken), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Token
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTokenINSTANCE.Lift(_uniffiRV), nil
	}
}

// Create a new Token from string
func TokenFromString(encodedToken string) (*Token, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_token_from_string(FfiConverterStringINSTANCE.Lower(encodedToken), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Token
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTokenINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode token to string representation
func (_self *Token) Encode() string {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_encode(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return all HTLC hashes from spending conditions
func (_self *Token) HtlcHashes() []string {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_htlc_hashes(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return all locktimes from spending conditions (sorted ascending)
func (_self *Token) Locktimes() []uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_locktimes(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the memo from the token
func (_self *Token) Memo() *string {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_memo(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the mint URL
func (_self *Token) MintUrl() (MintUrl, error) {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_mint_url(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue MintUrl
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterMintUrlINSTANCE.Lift(_uniffiRV), nil
	}
}

// Return all P2PK pubkeys referenced by this token's spending conditions
func (_self *Token) P2pkPubkeys() []string {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_p2pk_pubkeys(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return all refund pubkeys from P2PK spending conditions
func (_self *Token) P2pkRefundPubkeys() []string {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_p2pk_refund_pubkeys(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get proofs from the token (simplified - no keyset filtering for now)
func (_self *Token) ProofsSimple() ([]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_proofs_simple(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []*Proof
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSequenceProofINSTANCE.Lift(_uniffiRV), nil
	}
}

// Return unique spending conditions across all proofs in this token
func (_self *Token) SpendingConditions() []SpendingConditions {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceSpendingConditionsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_spending_conditions(
				_pointer, _uniffiStatus),
		}
	}))
}

// Convert token to raw bytes
func (_self *Token) ToRawBytes() ([]byte, error) {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_to_raw_bytes(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), nil
	}
}

// Get the currency unit
func (_self *Token) Unit() *CurrencyUnit {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalCurrencyUnitINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_unit(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the total value of the token
func (_self *Token) Value() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Token")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_token_value(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Amount
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterAmountINSTANCE.Lift(_uniffiRV), nil
	}
}
func (object *Token) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterToken struct{}

var FfiConverterTokenINSTANCE = FfiConverterToken{}

func (c FfiConverterToken) Lift(pointer unsafe.Pointer) *Token {
	result := &Token{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_token(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_token(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Token).Destroy)
	return result
}

func (c FfiConverterToken) Read(reader io.Reader) *Token {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterToken) Lower(value *Token) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Token")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterToken) Write(writer io.Writer, value *Token) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerToken struct{}

func (_ FfiDestroyerToken) Destroy(value *Token) {
	value.Destroy()
}

// FFI-compatible Wallet
type WalletInterface interface {
	// Calculate fee for a given number of proofs with the specified keyset
	CalculateFee(proofCount uint32, keysetId string) (Amount, error)
	// Check all pending proofs and return the total amount reclaimed
	CheckAllPendingProofs() (Amount, error)
	// Check if proofs are spent
	CheckProofsSpent(proofs []*Proof) ([]bool, error)
	// Get the active keyset for the wallet's unit
	GetActiveKeyset() (KeySetInfo, error)
	// Get fees for a specific keyset ID
	GetKeysetFeesById(keysetId string) (uint64, error)
	// Get mint info
	GetMintInfo() (*MintInfo, error)
	// Get proofs by states
	GetProofsByStates(states []ProofState) ([]*Proof, error)
	// Get transaction by ID
	GetTransaction(id TransactionId) (*Transaction, error)
	// Get unspent auth proofs
	GetUnspentAuthProofs() ([]AuthProof, error)
	// List transactions
	ListTransactions(direction *TransactionDirection) ([]Transaction, error)
	// Melt tokens
	Melt(quoteId string) (Melted, error)
	// Get a quote for a BIP353 melt
	//
	// This method resolves a BIP353 address (e.g., "alice@example.com") to a Lightning offer
	// and then creates a melt quote for that offer.
	MeltBip353Quote(bip353Address string, amountMsat Amount) (MeltQuote, error)
	// Get a quote for a bolt12 melt
	MeltBolt12Quote(request string, options *MeltOptions) (MeltQuote, error)
	// Get a melt quote
	MeltQuote(request string, options *MeltOptions) (MeltQuote, error)
	// Mint tokens
	Mint(quoteId string, amountSplitTarget SplitTarget, spendingConditions *SpendingConditions) ([]*Proof, error)
	// Mint blind auth tokens
	MintBlindAuth(amount Amount) ([]*Proof, error)
	// Mint tokens using bolt12
	MintBolt12(quoteId string, amount *Amount, amountSplitTarget SplitTarget, spendingConditions *SpendingConditions) ([]*Proof, error)
	// Get a quote for a bolt12 mint
	MintBolt12Quote(amount *Amount, description *string) (MintQuote, error)
	// Get a mint quote
	MintQuote(amount Amount, description *string) (MintQuote, error)
	// Get the mint URL
	MintUrl() MintUrl
	// Prepare a send operation
	PrepareSend(amount Amount, options SendOptions) (*PreparedSend, error)
	// Receive tokens
	Receive(token *Token, options ReceiveOptions) (Amount, error)
	// Receive proofs directly
	ReceiveProofs(proofs []*Proof, options ReceiveOptions, memo *string) (Amount, error)
	// Reclaim unspent proofs (mark them as unspent in the database)
	ReclaimUnspent(proofs []*Proof) error
	// Refresh access token using the stored refresh token
	RefreshAccessToken() error
	// Refresh keysets from the mint
	RefreshKeysets() ([]KeySetInfo, error)
	// Restore wallet from seed
	Restore() (Amount, error)
	// Revert a transaction
	RevertTransaction(id TransactionId) error
	// Set Clear Auth Token (CAT) for authentication
	SetCat(cat string) error
	// Set refresh token for authentication
	SetRefreshToken(refreshToken string) error
	// Subscribe to wallet events
	Subscribe(params SubscribeParams) (*ActiveSubscription, error)
	// Swap proofs
	Swap(amount *Amount, amountSplitTarget SplitTarget, inputProofs []*Proof, spendingConditions *SpendingConditions, includeFees bool) (*[]*Proof, error)
	// Get total balance
	TotalBalance() (Amount, error)
	// Get total pending balance
	TotalPendingBalance() (Amount, error)
	// Get total reserved balance
	TotalReservedBalance() (Amount, error)
	// Get the currency unit
	Unit() CurrencyUnit
	// Verify token DLEQ proofs
	VerifyTokenDleq(token *Token) error
}

// FFI-compatible Wallet
type Wallet struct {
	ffiObject FfiObject
}

// Create a new Wallet from mnemonic using WalletDatabase trait
func NewWallet(mintUrl string, unit CurrencyUnit, mnemonic string, db WalletDatabase, config WalletConfig) (*Wallet, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_wallet_new(FfiConverterStringINSTANCE.Lower(mintUrl), FfiConverterCurrencyUnitINSTANCE.Lower(unit), FfiConverterStringINSTANCE.Lower(mnemonic), FfiConverterWalletDatabaseINSTANCE.Lower(db), FfiConverterWalletConfigINSTANCE.Lower(config), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Wallet
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterWalletINSTANCE.Lift(_uniffiRV), nil
	}
}

// Calculate fee for a given number of proofs with the specified keyset
func (_self *Wallet) CalculateFee(proofCount uint32, keysetId string) (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_calculate_fee(
			_pointer, FfiConverterUint32INSTANCE.Lower(proofCount), FfiConverterStringINSTANCE.Lower(keysetId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Check all pending proofs and return the total amount reclaimed
func (_self *Wallet) CheckAllPendingProofs() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_check_all_pending_proofs(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Check if proofs are spent
func (_self *Wallet) CheckProofsSpent(proofs []*Proof) ([]bool, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []bool {
			return FfiConverterSequenceBoolINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_check_proofs_spent(
			_pointer, FfiConverterSequenceProofINSTANCE.Lower(proofs)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get the active keyset for the wallet's unit
func (_self *Wallet) GetActiveKeyset() (KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) KeySetInfo {
			return FfiConverterKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_get_active_keyset(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get fees for a specific keyset ID
func (_self *Wallet) GetKeysetFeesById(keysetId string) (uint64, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint64_t {
			res := C.ffi_cdk_ffi_rust_future_complete_u64(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint64_t) uint64 {
			return FfiConverterUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_get_keyset_fees_by_id(
			_pointer, FfiConverterStringINSTANCE.Lower(keysetId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_u64(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_u64(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get mint info
func (_self *Wallet) GetMintInfo() (*MintInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MintInfo {
			return FfiConverterOptionalMintInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_get_mint_info(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get proofs by states
func (_self *Wallet) GetProofsByStates(states []ProofState) ([]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Proof {
			return FfiConverterSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_get_proofs_by_states(
			_pointer, FfiConverterSequenceProofStateINSTANCE.Lower(states)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get transaction by ID
func (_self *Wallet) GetTransaction(id TransactionId) (*Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *Transaction {
			return FfiConverterOptionalTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_get_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get unspent auth proofs
func (_self *Wallet) GetUnspentAuthProofs() ([]AuthProof, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []AuthProof {
			return FfiConverterSequenceAuthProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_get_unspent_auth_proofs(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// List transactions
func (_self *Wallet) ListTransactions(direction *TransactionDirection) ([]Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []Transaction {
			return FfiConverterSequenceTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_list_transactions(
			_pointer, FfiConverterOptionalTransactionDirectionINSTANCE.Lower(direction)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Melt tokens
func (_self *Wallet) Melt(quoteId string) (Melted, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Melted {
			return FfiConverterMeltedINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_melt(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get a quote for a BIP353 melt
//
// This method resolves a BIP353 address (e.g., "alice@example.com") to a Lightning offer
// and then creates a melt quote for that offer.
func (_self *Wallet) MeltBip353Quote(bip353Address string, amountMsat Amount) (MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MeltQuote {
			return FfiConverterMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_melt_bip353_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(bip353Address), FfiConverterAmountINSTANCE.Lower(amountMsat)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get a quote for a bolt12 melt
func (_self *Wallet) MeltBolt12Quote(request string, options *MeltOptions) (MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MeltQuote {
			return FfiConverterMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_melt_bolt12_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(request), FfiConverterOptionalMeltOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get a melt quote
func (_self *Wallet) MeltQuote(request string, options *MeltOptions) (MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MeltQuote {
			return FfiConverterMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_melt_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(request), FfiConverterOptionalMeltOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Mint tokens
func (_self *Wallet) Mint(quoteId string, amountSplitTarget SplitTarget, spendingConditions *SpendingConditions) ([]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Proof {
			return FfiConverterSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_mint(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId), FfiConverterSplitTargetINSTANCE.Lower(amountSplitTarget), FfiConverterOptionalSpendingConditionsINSTANCE.Lower(spendingConditions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Mint blind auth tokens
func (_self *Wallet) MintBlindAuth(amount Amount) ([]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Proof {
			return FfiConverterSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_mint_blind_auth(
			_pointer, FfiConverterAmountINSTANCE.Lower(amount)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Mint tokens using bolt12
func (_self *Wallet) MintBolt12(quoteId string, amount *Amount, amountSplitTarget SplitTarget, spendingConditions *SpendingConditions) ([]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []*Proof {
			return FfiConverterSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_mint_bolt12(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId), FfiConverterOptionalAmountINSTANCE.Lower(amount), FfiConverterSplitTargetINSTANCE.Lower(amountSplitTarget), FfiConverterOptionalSpendingConditionsINSTANCE.Lower(spendingConditions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get a quote for a bolt12 mint
func (_self *Wallet) MintBolt12Quote(amount *Amount, description *string) (MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MintQuote {
			return FfiConverterMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_mint_bolt12_quote(
			_pointer, FfiConverterOptionalAmountINSTANCE.Lower(amount), FfiConverterOptionalStringINSTANCE.Lower(description)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get a mint quote
func (_self *Wallet) MintQuote(amount Amount, description *string) (MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) MintQuote {
			return FfiConverterMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_mint_quote(
			_pointer, FfiConverterAmountINSTANCE.Lower(amount), FfiConverterOptionalStringINSTANCE.Lower(description)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get the mint URL
func (_self *Wallet) MintUrl() MintUrl {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterMintUrlINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_wallet_mint_url(
				_pointer, _uniffiStatus),
		}
	}))
}

// Prepare a send operation
func (_self *Wallet) PrepareSend(amount Amount, options SendOptions) (*PreparedSend, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_cdk_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *PreparedSend {
			return FfiConverterPreparedSendINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_prepare_send(
			_pointer, FfiConverterAmountINSTANCE.Lower(amount), FfiConverterSendOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_pointer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Receive tokens
func (_self *Wallet) Receive(token *Token, options ReceiveOptions) (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_receive(
			_pointer, FfiConverterTokenINSTANCE.Lower(token), FfiConverterReceiveOptionsINSTANCE.Lower(options)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Receive proofs directly
func (_self *Wallet) ReceiveProofs(proofs []*Proof, options ReceiveOptions, memo *string) (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_receive_proofs(
			_pointer, FfiConverterSequenceProofINSTANCE.Lower(proofs), FfiConverterReceiveOptionsINSTANCE.Lower(options), FfiConverterOptionalStringINSTANCE.Lower(memo)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Reclaim unspent proofs (mark them as unspent in the database)
func (_self *Wallet) ReclaimUnspent(proofs []*Proof) error {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_wallet_reclaim_unspent(
			_pointer, FfiConverterSequenceProofINSTANCE.Lower(proofs)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Refresh access token using the stored refresh token
func (_self *Wallet) RefreshAccessToken() error {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_wallet_refresh_access_token(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Refresh keysets from the mint
func (_self *Wallet) RefreshKeysets() ([]KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []KeySetInfo {
			return FfiConverterSequenceKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_refresh_keysets(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Restore wallet from seed
func (_self *Wallet) Restore() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_restore(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Revert a transaction
func (_self *Wallet) RevertTransaction(id TransactionId) error {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_wallet_revert_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Set Clear Auth Token (CAT) for authentication
func (_self *Wallet) SetCat(cat string) error {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_wallet_set_cat(
			_pointer, FfiConverterStringINSTANCE.Lower(cat)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Set refresh token for authentication
func (_self *Wallet) SetRefreshToken(refreshToken string) error {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_wallet_set_refresh_token(
			_pointer, FfiConverterStringINSTANCE.Lower(refreshToken)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Subscribe to wallet events
func (_self *Wallet) Subscribe(params SubscribeParams) (*ActiveSubscription, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) unsafe.Pointer {
			res := C.ffi_cdk_ffi_rust_future_complete_pointer(handle, status)
			return res
		},
		// liftFn
		func(ffi unsafe.Pointer) *ActiveSubscription {
			return FfiConverterActiveSubscriptionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_subscribe(
			_pointer, FfiConverterSubscribeParamsINSTANCE.Lower(params)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_pointer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_pointer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Swap proofs
func (_self *Wallet) Swap(amount *Amount, amountSplitTarget SplitTarget, inputProofs []*Proof, spendingConditions *SpendingConditions, includeFees bool) (*[]*Proof, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *[]*Proof {
			return FfiConverterOptionalSequenceProofINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_swap(
			_pointer, FfiConverterOptionalAmountINSTANCE.Lower(amount), FfiConverterSplitTargetINSTANCE.Lower(amountSplitTarget), FfiConverterSequenceProofINSTANCE.Lower(inputProofs), FfiConverterOptionalSpendingConditionsINSTANCE.Lower(spendingConditions), FfiConverterBoolINSTANCE.Lower(includeFees)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get total balance
func (_self *Wallet) TotalBalance() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_total_balance(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get total pending balance
func (_self *Wallet) TotalPendingBalance() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_total_pending_balance(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get total reserved balance
func (_self *Wallet) TotalReservedBalance() (Amount, error) {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Amount {
			return FfiConverterAmountINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_wallet_total_reserved_balance(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get the currency unit
func (_self *Wallet) Unit() CurrencyUnit {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterCurrencyUnitINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_method_wallet_unit(
				_pointer, _uniffiStatus),
		}
	}))
}

// Verify token DLEQ proofs
func (_self *Wallet) VerifyTokenDleq(token *Token) error {
	_pointer := _self.ffiObject.incrementPointer("*Wallet")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_wallet_verify_token_dleq(
			_pointer, FfiConverterTokenINSTANCE.Lower(token)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}
func (object *Wallet) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterWallet struct{}

var FfiConverterWalletINSTANCE = FfiConverterWallet{}

func (c FfiConverterWallet) Lift(pointer unsafe.Pointer) *Wallet {
	result := &Wallet{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_wallet(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_wallet(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Wallet).Destroy)
	return result
}

func (c FfiConverterWallet) Read(reader io.Reader) *Wallet {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterWallet) Lower(value *Wallet) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Wallet")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterWallet) Write(writer io.Writer, value *Wallet) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerWallet struct{}

func (_ FfiDestroyerWallet) Destroy(value *Wallet) {
	value.Destroy()
}

// FFI-compatible trait for wallet database operations
// This trait mirrors the CDK WalletDatabase trait but uses FFI-compatible types
type WalletDatabase interface {
	// Add Mint to storage
	AddMint(mintUrl MintUrl, mintInfo *MintInfo) error
	// Remove Mint from storage
	RemoveMint(mintUrl MintUrl) error
	// Get mint from storage
	GetMint(mintUrl MintUrl) (*MintInfo, error)
	// Get all mints from storage
	GetMints() (map[MintUrl]*MintInfo, error)
	// Update mint url
	UpdateMintUrl(oldMintUrl MintUrl, newMintUrl MintUrl) error
	// Add mint keyset to storage
	AddMintKeysets(mintUrl MintUrl, keysets []KeySetInfo) error
	// Get mint keysets for mint url
	GetMintKeysets(mintUrl MintUrl) (*[]KeySetInfo, error)
	// Get mint keyset by id
	GetKeysetById(keysetId Id) (*KeySetInfo, error)
	// Add mint quote to storage
	AddMintQuote(quote MintQuote) error
	// Get mint quote from storage
	GetMintQuote(quoteId string) (*MintQuote, error)
	// Get mint quotes from storage
	GetMintQuotes() ([]MintQuote, error)
	// Remove mint quote from storage
	RemoveMintQuote(quoteId string) error
	// Add melt quote to storage
	AddMeltQuote(quote MeltQuote) error
	// Get melt quote from storage
	GetMeltQuote(quoteId string) (*MeltQuote, error)
	// Get melt quotes from storage
	GetMeltQuotes() ([]MeltQuote, error)
	// Remove melt quote from storage
	RemoveMeltQuote(quoteId string) error
	// Add Keys to storage
	AddKeys(keyset KeySet) error
	// Get Keys from storage
	GetKeys(id Id) (*Keys, error)
	// Remove Keys from storage
	RemoveKeys(id Id) error
	// Update the proofs in storage by adding new proofs or removing proofs by their Y value
	UpdateProofs(added []ProofInfo, removedYs []PublicKey) error
	// Get proofs from storage
	GetProofs(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState, spendingConditions *[]SpendingConditions) ([]ProofInfo, error)
	// Get balance efficiently using SQL aggregation
	GetBalance(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState) (uint64, error)
	// Update proofs state in storage
	UpdateProofsState(ys []PublicKey, state ProofState) error
	// Increment Keyset counter
	IncrementKeysetCounter(keysetId Id, count uint32) (uint32, error)
	// Add transaction to storage
	AddTransaction(transaction Transaction) error
	// Get transaction from storage
	GetTransaction(transactionId TransactionId) (*Transaction, error)
	// List transactions from storage
	ListTransactions(mintUrl *MintUrl, direction *TransactionDirection, unit *CurrencyUnit) ([]Transaction, error)
	// Remove transaction from storage
	RemoveTransaction(transactionId TransactionId) error
}

// FFI-compatible trait for wallet database operations
// This trait mirrors the CDK WalletDatabase trait but uses FFI-compatible types
type WalletDatabaseImpl struct {
	ffiObject FfiObject
}

// Add Mint to storage
func (_self *WalletDatabaseImpl) AddMint(mintUrl MintUrl, mintInfo *MintInfo) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_add_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalMintInfoINSTANCE.Lower(mintInfo)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Remove Mint from storage
func (_self *WalletDatabaseImpl) RemoveMint(mintUrl MintUrl) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_remove_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Get mint from storage
func (_self *WalletDatabaseImpl) GetMint(mintUrl MintUrl) (*MintInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MintInfo {
			return FfiConverterOptionalMintInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get all mints from storage
func (_self *WalletDatabaseImpl) GetMints() (map[MintUrl]*MintInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) map[MintUrl]*MintInfo {
			return FfiConverterMapMintUrlOptionalMintInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_mints(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Update mint url
func (_self *WalletDatabaseImpl) UpdateMintUrl(oldMintUrl MintUrl, newMintUrl MintUrl) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_update_mint_url(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(oldMintUrl), FfiConverterMintUrlINSTANCE.Lower(newMintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Add mint keyset to storage
func (_self *WalletDatabaseImpl) AddMintKeysets(mintUrl MintUrl, keysets []KeySetInfo) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_add_mint_keysets(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterSequenceKeySetInfoINSTANCE.Lower(keysets)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Get mint keysets for mint url
func (_self *WalletDatabaseImpl) GetMintKeysets(mintUrl MintUrl) (*[]KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *[]KeySetInfo {
			return FfiConverterOptionalSequenceKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_mint_keysets(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get mint keyset by id
func (_self *WalletDatabaseImpl) GetKeysetById(keysetId Id) (*KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *KeySetInfo {
			return FfiConverterOptionalKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_keyset_by_id(
			_pointer, FfiConverterIdINSTANCE.Lower(keysetId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Add mint quote to storage
func (_self *WalletDatabaseImpl) AddMintQuote(quote MintQuote) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_add_mint_quote(
			_pointer, FfiConverterMintQuoteINSTANCE.Lower(quote)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Get mint quote from storage
func (_self *WalletDatabaseImpl) GetMintQuote(quoteId string) (*MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MintQuote {
			return FfiConverterOptionalMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_mint_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get mint quotes from storage
func (_self *WalletDatabaseImpl) GetMintQuotes() ([]MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []MintQuote {
			return FfiConverterSequenceMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_mint_quotes(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Remove mint quote from storage
func (_self *WalletDatabaseImpl) RemoveMintQuote(quoteId string) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_remove_mint_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Add melt quote to storage
func (_self *WalletDatabaseImpl) AddMeltQuote(quote MeltQuote) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_add_melt_quote(
			_pointer, FfiConverterMeltQuoteINSTANCE.Lower(quote)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Get melt quote from storage
func (_self *WalletDatabaseImpl) GetMeltQuote(quoteId string) (*MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MeltQuote {
			return FfiConverterOptionalMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_melt_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get melt quotes from storage
func (_self *WalletDatabaseImpl) GetMeltQuotes() ([]MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []MeltQuote {
			return FfiConverterSequenceMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_melt_quotes(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Remove melt quote from storage
func (_self *WalletDatabaseImpl) RemoveMeltQuote(quoteId string) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_remove_melt_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Add Keys to storage
func (_self *WalletDatabaseImpl) AddKeys(keyset KeySet) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_add_keys(
			_pointer, FfiConverterKeySetINSTANCE.Lower(keyset)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Get Keys from storage
func (_self *WalletDatabaseImpl) GetKeys(id Id) (*Keys, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *Keys {
			return FfiConverterOptionalKeysINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_keys(
			_pointer, FfiConverterIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Remove Keys from storage
func (_self *WalletDatabaseImpl) RemoveKeys(id Id) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_remove_keys(
			_pointer, FfiConverterIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Update the proofs in storage by adding new proofs or removing proofs by their Y value
func (_self *WalletDatabaseImpl) UpdateProofs(added []ProofInfo, removedYs []PublicKey) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_update_proofs(
			_pointer, FfiConverterSequenceProofInfoINSTANCE.Lower(added), FfiConverterSequencePublicKeyINSTANCE.Lower(removedYs)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Get proofs from storage
func (_self *WalletDatabaseImpl) GetProofs(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState, spendingConditions *[]SpendingConditions) ([]ProofInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []ProofInfo {
			return FfiConverterSequenceProofInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_proofs(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit), FfiConverterOptionalSequenceProofStateINSTANCE.Lower(state), FfiConverterOptionalSequenceSpendingConditionsINSTANCE.Lower(spendingConditions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Get balance efficiently using SQL aggregation
func (_self *WalletDatabaseImpl) GetBalance(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState) (uint64, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint64_t {
			res := C.ffi_cdk_ffi_rust_future_complete_u64(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint64_t) uint64 {
			return FfiConverterUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_balance(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit), FfiConverterOptionalSequenceProofStateINSTANCE.Lower(state)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_u64(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_u64(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Update proofs state in storage
func (_self *WalletDatabaseImpl) UpdateProofsState(ys []PublicKey, state ProofState) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_update_proofs_state(
			_pointer, FfiConverterSequencePublicKeyINSTANCE.Lower(ys), FfiConverterProofStateINSTANCE.Lower(state)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Increment Keyset counter
func (_self *WalletDatabaseImpl) IncrementKeysetCounter(keysetId Id, count uint32) (uint32, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint32_t {
			res := C.ffi_cdk_ffi_rust_future_complete_u32(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint32_t) uint32 {
			return FfiConverterUint32INSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_increment_keyset_counter(
			_pointer, FfiConverterIdINSTANCE.Lower(keysetId), FfiConverterUint32INSTANCE.Lower(count)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_u32(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_u32(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Add transaction to storage
func (_self *WalletDatabaseImpl) AddTransaction(transaction Transaction) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_add_transaction(
			_pointer, FfiConverterTransactionINSTANCE.Lower(transaction)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

// Get transaction from storage
func (_self *WalletDatabaseImpl) GetTransaction(transactionId TransactionId) (*Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *Transaction {
			return FfiConverterOptionalTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_get_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(transactionId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// List transactions from storage
func (_self *WalletDatabaseImpl) ListTransactions(mintUrl *MintUrl, direction *TransactionDirection, unit *CurrencyUnit) ([]Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []Transaction {
			return FfiConverterSequenceTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletdatabase_list_transactions(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalTransactionDirectionINSTANCE.Lower(direction), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

// Remove transaction from storage
func (_self *WalletDatabaseImpl) RemoveTransaction(transactionId TransactionId) error {
	_pointer := _self.ffiObject.incrementPointer("WalletDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletdatabase_remove_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(transactionId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}
func (object *WalletDatabaseImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterWalletDatabase struct {
	handleMap *concurrentHandleMap[WalletDatabase]
}

var FfiConverterWalletDatabaseINSTANCE = FfiConverterWalletDatabase{
	handleMap: newConcurrentHandleMap[WalletDatabase](),
}

func (c FfiConverterWalletDatabase) Lift(pointer unsafe.Pointer) WalletDatabase {
	result := &WalletDatabaseImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_walletdatabase(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_walletdatabase(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*WalletDatabaseImpl).Destroy)
	return result
}

func (c FfiConverterWalletDatabase) Read(reader io.Reader) WalletDatabase {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterWalletDatabase) Lower(value WalletDatabase) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterWalletDatabase) Write(writer io.Writer, value WalletDatabase) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerWalletDatabase struct{}

func (_ FfiDestroyerWalletDatabase) Destroy(value WalletDatabase) {
	if val, ok := value.(*WalletDatabaseImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *WalletDatabaseImpl")
	}
}

type uniffiCallbackResult C.int8_t

const (
	uniffiIdxCallbackFree               uniffiCallbackResult = 0
	uniffiCallbackResultSuccess         uniffiCallbackResult = 0
	uniffiCallbackResultError           uniffiCallbackResult = 1
	uniffiCallbackUnexpectedResultError uniffiCallbackResult = 2
	uniffiCallbackCancelled             uniffiCallbackResult = 3
)

type concurrentHandleMap[T any] struct {
	handles       map[uint64]T
	currentHandle uint64
	lock          sync.RWMutex
}

func newConcurrentHandleMap[T any]() *concurrentHandleMap[T] {
	return &concurrentHandleMap[T]{
		handles: map[uint64]T{},
	}
}

func (cm *concurrentHandleMap[T]) insert(obj T) uint64 {
	cm.lock.Lock()
	defer cm.lock.Unlock()

	cm.currentHandle = cm.currentHandle + 1
	cm.handles[cm.currentHandle] = obj
	return cm.currentHandle
}

func (cm *concurrentHandleMap[T]) remove(handle uint64) {
	cm.lock.Lock()
	defer cm.lock.Unlock()

	delete(cm.handles, handle)
}

func (cm *concurrentHandleMap[T]) tryGet(handle uint64) (T, bool) {
	cm.lock.RLock()
	defer cm.lock.RUnlock()

	val, ok := cm.handles[handle]
	return val, ok
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod0
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod0(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, mintInfo C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.AddMint(
				FfiConverterMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
				FfiConverterOptionalMintInfoINSTANCE.Lift(GoRustBuffer{
					inner: mintInfo,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod1
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod1(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.RemoveMint(
				FfiConverterMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod2
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod2(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetMint(
				FfiConverterMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterOptionalMintInfoINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod3
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod3(uniffiHandle C.uint64_t, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetMints()

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterMapMintUrlOptionalMintInfoINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod4
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod4(uniffiHandle C.uint64_t, oldMintUrl C.RustBuffer, newMintUrl C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.UpdateMintUrl(
				FfiConverterMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: oldMintUrl,
				}),
				FfiConverterMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: newMintUrl,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod5
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod5(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, keysets C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.AddMintKeysets(
				FfiConverterMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
				FfiConverterSequenceKeySetInfoINSTANCE.Lift(GoRustBuffer{
					inner: keysets,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod6
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod6(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetMintKeysets(
				FfiConverterMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterOptionalSequenceKeySetInfoINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod7
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod7(uniffiHandle C.uint64_t, keysetId C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetKeysetById(
				FfiConverterIdINSTANCE.Lift(GoRustBuffer{
					inner: keysetId,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterOptionalKeySetInfoINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod8
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod8(uniffiHandle C.uint64_t, quote C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.AddMintQuote(
				FfiConverterMintQuoteINSTANCE.Lift(GoRustBuffer{
					inner: quote,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod9
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod9(uniffiHandle C.uint64_t, quoteId C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetMintQuote(
				FfiConverterStringINSTANCE.Lift(GoRustBuffer{
					inner: quoteId,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterOptionalMintQuoteINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod10
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod10(uniffiHandle C.uint64_t, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetMintQuotes()

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterSequenceMintQuoteINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod11
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod11(uniffiHandle C.uint64_t, quoteId C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.RemoveMintQuote(
				FfiConverterStringINSTANCE.Lift(GoRustBuffer{
					inner: quoteId,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod12
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod12(uniffiHandle C.uint64_t, quote C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.AddMeltQuote(
				FfiConverterMeltQuoteINSTANCE.Lift(GoRustBuffer{
					inner: quote,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod13
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod13(uniffiHandle C.uint64_t, quoteId C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetMeltQuote(
				FfiConverterStringINSTANCE.Lift(GoRustBuffer{
					inner: quoteId,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterOptionalMeltQuoteINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod14
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod14(uniffiHandle C.uint64_t, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetMeltQuotes()

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterSequenceMeltQuoteINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod15
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod15(uniffiHandle C.uint64_t, quoteId C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.RemoveMeltQuote(
				FfiConverterStringINSTANCE.Lift(GoRustBuffer{
					inner: quoteId,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod16
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod16(uniffiHandle C.uint64_t, keyset C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.AddKeys(
				FfiConverterKeySetINSTANCE.Lift(GoRustBuffer{
					inner: keyset,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod17
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod17(uniffiHandle C.uint64_t, id C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetKeys(
				FfiConverterIdINSTANCE.Lift(GoRustBuffer{
					inner: id,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterOptionalKeysINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod18
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod18(uniffiHandle C.uint64_t, id C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.RemoveKeys(
				FfiConverterIdINSTANCE.Lift(GoRustBuffer{
					inner: id,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod19
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod19(uniffiHandle C.uint64_t, added C.RustBuffer, removedYs C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.UpdateProofs(
				FfiConverterSequenceProofInfoINSTANCE.Lift(GoRustBuffer{
					inner: added,
				}),
				FfiConverterSequencePublicKeyINSTANCE.Lift(GoRustBuffer{
					inner: removedYs,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod20
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod20(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, unit C.RustBuffer, state C.RustBuffer, spendingConditions C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetProofs(
				FfiConverterOptionalMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
				FfiConverterOptionalCurrencyUnitINSTANCE.Lift(GoRustBuffer{
					inner: unit,
				}),
				FfiConverterOptionalSequenceProofStateINSTANCE.Lift(GoRustBuffer{
					inner: state,
				}),
				FfiConverterOptionalSequenceSpendingConditionsINSTANCE.Lift(GoRustBuffer{
					inner: spendingConditions,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterSequenceProofInfoINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod21
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod21(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, unit C.RustBuffer, state C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteU64, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructU64, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteU64(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructU64{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetBalance(
				FfiConverterOptionalMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
				FfiConverterOptionalCurrencyUnitINSTANCE.Lift(GoRustBuffer{
					inner: unit,
				}),
				FfiConverterOptionalSequenceProofStateINSTANCE.Lift(GoRustBuffer{
					inner: state,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterUint64INSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod22
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod22(uniffiHandle C.uint64_t, ys C.RustBuffer, state C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.UpdateProofsState(
				FfiConverterSequencePublicKeyINSTANCE.Lift(GoRustBuffer{
					inner: ys,
				}),
				FfiConverterProofStateINSTANCE.Lift(GoRustBuffer{
					inner: state,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod23
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod23(uniffiHandle C.uint64_t, keysetId C.RustBuffer, count C.uint32_t, uniffiFutureCallback C.UniffiForeignFutureCompleteU32, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructU32, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteU32(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructU32{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.IncrementKeysetCounter(
				FfiConverterIdINSTANCE.Lift(GoRustBuffer{
					inner: keysetId,
				}),
				FfiConverterUint32INSTANCE.Lift(count),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterUint32INSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod24
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod24(uniffiHandle C.uint64_t, transaction C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.AddTransaction(
				FfiConverterTransactionINSTANCE.Lift(GoRustBuffer{
					inner: transaction,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod25
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod25(uniffiHandle C.uint64_t, transactionId C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.GetTransaction(
				FfiConverterTransactionIdINSTANCE.Lift(GoRustBuffer{
					inner: transactionId,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterOptionalTransactionINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod26
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod26(uniffiHandle C.uint64_t, mintUrl C.RustBuffer, direction C.RustBuffer, unit C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteRustBuffer(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructRustBuffer{}
		uniffiOutReturn := &asyncResult.returnValue
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		res, err :=
			uniffiObj.ListTransactions(
				FfiConverterOptionalMintUrlINSTANCE.Lift(GoRustBuffer{
					inner: mintUrl,
				}),
				FfiConverterOptionalTransactionDirectionINSTANCE.Lift(GoRustBuffer{
					inner: direction,
				}),
				FfiConverterOptionalCurrencyUnitINSTANCE.Lift(GoRustBuffer{
					inner: unit,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterSequenceTransactionINSTANCE.Lower(res)
	}()
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod27
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod27(uniffiHandle C.uint64_t, transactionId C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteVoid, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterWalletDatabaseINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructVoid, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.cdk_ffi_uniffiFreeGorutine),
	}

	// Wait for compleation or cancel
	go func() {
		select {
		case <-cancel:
		case res := <-result:
			C.call_UniffiForeignFutureCompleteVoid(uniffiFutureCallback, uniffiCallbackData, res)
		}
	}()

	// Eval callback asynchroniously
	go func() {
		asyncResult := &C.UniffiForeignFutureStructVoid{}
		callStatus := &asyncResult.callStatus
		defer func() {
			result <- *asyncResult
		}()

		err :=
			uniffiObj.RemoveTransaction(
				FfiConverterTransactionIdINSTANCE.Lift(GoRustBuffer{
					inner: transactionId,
				}),
			)

		if err != nil {
			var actualError *FfiError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterFfiErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

	}()
}

var UniffiVTableCallbackInterfaceWalletDatabaseINSTANCE = C.UniffiVTableCallbackInterfaceWalletDatabase{
	addMint:                (C.UniffiCallbackInterfaceWalletDatabaseMethod0)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod0),
	removeMint:             (C.UniffiCallbackInterfaceWalletDatabaseMethod1)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod1),
	getMint:                (C.UniffiCallbackInterfaceWalletDatabaseMethod2)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod2),
	getMints:               (C.UniffiCallbackInterfaceWalletDatabaseMethod3)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod3),
	updateMintUrl:          (C.UniffiCallbackInterfaceWalletDatabaseMethod4)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod4),
	addMintKeysets:         (C.UniffiCallbackInterfaceWalletDatabaseMethod5)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod5),
	getMintKeysets:         (C.UniffiCallbackInterfaceWalletDatabaseMethod6)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod6),
	getKeysetById:          (C.UniffiCallbackInterfaceWalletDatabaseMethod7)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod7),
	addMintQuote:           (C.UniffiCallbackInterfaceWalletDatabaseMethod8)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod8),
	getMintQuote:           (C.UniffiCallbackInterfaceWalletDatabaseMethod9)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod9),
	getMintQuotes:          (C.UniffiCallbackInterfaceWalletDatabaseMethod10)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod10),
	removeMintQuote:        (C.UniffiCallbackInterfaceWalletDatabaseMethod11)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod11),
	addMeltQuote:           (C.UniffiCallbackInterfaceWalletDatabaseMethod12)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod12),
	getMeltQuote:           (C.UniffiCallbackInterfaceWalletDatabaseMethod13)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod13),
	getMeltQuotes:          (C.UniffiCallbackInterfaceWalletDatabaseMethod14)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod14),
	removeMeltQuote:        (C.UniffiCallbackInterfaceWalletDatabaseMethod15)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod15),
	addKeys:                (C.UniffiCallbackInterfaceWalletDatabaseMethod16)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod16),
	getKeys:                (C.UniffiCallbackInterfaceWalletDatabaseMethod17)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod17),
	removeKeys:             (C.UniffiCallbackInterfaceWalletDatabaseMethod18)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod18),
	updateProofs:           (C.UniffiCallbackInterfaceWalletDatabaseMethod19)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod19),
	getProofs:              (C.UniffiCallbackInterfaceWalletDatabaseMethod20)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod20),
	getBalance:             (C.UniffiCallbackInterfaceWalletDatabaseMethod21)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod21),
	updateProofsState:      (C.UniffiCallbackInterfaceWalletDatabaseMethod22)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod22),
	incrementKeysetCounter: (C.UniffiCallbackInterfaceWalletDatabaseMethod23)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod23),
	addTransaction:         (C.UniffiCallbackInterfaceWalletDatabaseMethod24)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod24),
	getTransaction:         (C.UniffiCallbackInterfaceWalletDatabaseMethod25)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod25),
	listTransactions:       (C.UniffiCallbackInterfaceWalletDatabaseMethod26)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod26),
	removeTransaction:      (C.UniffiCallbackInterfaceWalletDatabaseMethod27)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseMethod27),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseFree),
}

//export cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseFree
func cdk_ffi_cgo_dispatchCallbackInterfaceWalletDatabaseFree(handle C.uint64_t) {
	FfiConverterWalletDatabaseINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterWalletDatabase) register() {
	C.uniffi_cdk_ffi_fn_init_callback_vtable_walletdatabase(&UniffiVTableCallbackInterfaceWalletDatabaseINSTANCE)
}

type WalletPostgresDatabaseInterface interface {
	AddKeys(keyset KeySet) error
	AddMeltQuote(quote MeltQuote) error
	AddMint(mintUrl MintUrl, mintInfo *MintInfo) error
	AddMintKeysets(mintUrl MintUrl, keysets []KeySetInfo) error
	AddMintQuote(quote MintQuote) error
	AddTransaction(transaction Transaction) error
	CloneAsTrait() WalletDatabase
	GetBalance(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState) (uint64, error)
	GetKeys(id Id) (*Keys, error)
	GetKeysetById(keysetId Id) (*KeySetInfo, error)
	GetMeltQuote(quoteId string) (*MeltQuote, error)
	GetMeltQuotes() ([]MeltQuote, error)
	GetMint(mintUrl MintUrl) (*MintInfo, error)
	GetMintKeysets(mintUrl MintUrl) (*[]KeySetInfo, error)
	GetMintQuote(quoteId string) (*MintQuote, error)
	GetMintQuotes() ([]MintQuote, error)
	GetMints() (map[MintUrl]*MintInfo, error)
	GetProofs(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState, spendingConditions *[]SpendingConditions) ([]ProofInfo, error)
	GetTransaction(transactionId TransactionId) (*Transaction, error)
	IncrementKeysetCounter(keysetId Id, count uint32) (uint32, error)
	ListTransactions(mintUrl *MintUrl, direction *TransactionDirection, unit *CurrencyUnit) ([]Transaction, error)
	RemoveKeys(id Id) error
	RemoveMeltQuote(quoteId string) error
	RemoveMint(mintUrl MintUrl) error
	RemoveMintQuote(quoteId string) error
	RemoveTransaction(transactionId TransactionId) error
	UpdateMintUrl(oldMintUrl MintUrl, newMintUrl MintUrl) error
	UpdateProofs(added []ProofInfo, removedYs []PublicKey) error
	UpdateProofsState(ys []PublicKey, state ProofState) error
}
type WalletPostgresDatabase struct {
	ffiObject FfiObject
}

// Create a new Postgres-backed wallet database
// Requires cdk-ffi to be built with feature "postgres".
// Example URL:
// "host=localhost user=test password=test dbname=testdb port=5433 schema=wallet sslmode=prefer"
func NewWalletPostgresDatabase(url string) (*WalletPostgresDatabase, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_walletpostgresdatabase_new(FfiConverterStringINSTANCE.Lower(url), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *WalletPostgresDatabase
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterWalletPostgresDatabaseINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *WalletPostgresDatabase) AddKeys(keyset KeySet) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_add_keys(
			_pointer, FfiConverterKeySetINSTANCE.Lower(keyset)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) AddMeltQuote(quote MeltQuote) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_add_melt_quote(
			_pointer, FfiConverterMeltQuoteINSTANCE.Lower(quote)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) AddMint(mintUrl MintUrl, mintInfo *MintInfo) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_add_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalMintInfoINSTANCE.Lower(mintInfo)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) AddMintKeysets(mintUrl MintUrl, keysets []KeySetInfo) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_add_mint_keysets(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterSequenceKeySetInfoINSTANCE.Lower(keysets)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) AddMintQuote(quote MintQuote) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_add_mint_quote(
			_pointer, FfiConverterMintQuoteINSTANCE.Lower(quote)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) AddTransaction(transaction Transaction) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_add_transaction(
			_pointer, FfiConverterTransactionINSTANCE.Lower(transaction)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) CloneAsTrait() WalletDatabase {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterWalletDatabaseINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_clone_as_trait(
			_pointer, _uniffiStatus)
	}))
}

func (_self *WalletPostgresDatabase) GetBalance(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState) (uint64, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint64_t {
			res := C.ffi_cdk_ffi_rust_future_complete_u64(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint64_t) uint64 {
			return FfiConverterUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_balance(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit), FfiConverterOptionalSequenceProofStateINSTANCE.Lower(state)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_u64(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_u64(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetKeys(id Id) (*Keys, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *Keys {
			return FfiConverterOptionalKeysINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_keys(
			_pointer, FfiConverterIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetKeysetById(keysetId Id) (*KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *KeySetInfo {
			return FfiConverterOptionalKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_keyset_by_id(
			_pointer, FfiConverterIdINSTANCE.Lower(keysetId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetMeltQuote(quoteId string) (*MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MeltQuote {
			return FfiConverterOptionalMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_melt_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetMeltQuotes() ([]MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []MeltQuote {
			return FfiConverterSequenceMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_melt_quotes(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetMint(mintUrl MintUrl) (*MintInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MintInfo {
			return FfiConverterOptionalMintInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetMintKeysets(mintUrl MintUrl) (*[]KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *[]KeySetInfo {
			return FfiConverterOptionalSequenceKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_mint_keysets(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetMintQuote(quoteId string) (*MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MintQuote {
			return FfiConverterOptionalMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_mint_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetMintQuotes() ([]MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []MintQuote {
			return FfiConverterSequenceMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_mint_quotes(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetMints() (map[MintUrl]*MintInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) map[MintUrl]*MintInfo {
			return FfiConverterMapMintUrlOptionalMintInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_mints(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetProofs(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState, spendingConditions *[]SpendingConditions) ([]ProofInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []ProofInfo {
			return FfiConverterSequenceProofInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_proofs(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit), FfiConverterOptionalSequenceProofStateINSTANCE.Lower(state), FfiConverterOptionalSequenceSpendingConditionsINSTANCE.Lower(spendingConditions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) GetTransaction(transactionId TransactionId) (*Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *Transaction {
			return FfiConverterOptionalTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_get_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(transactionId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) IncrementKeysetCounter(keysetId Id, count uint32) (uint32, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint32_t {
			res := C.ffi_cdk_ffi_rust_future_complete_u32(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint32_t) uint32 {
			return FfiConverterUint32INSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_increment_keyset_counter(
			_pointer, FfiConverterIdINSTANCE.Lower(keysetId), FfiConverterUint32INSTANCE.Lower(count)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_u32(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_u32(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) ListTransactions(mintUrl *MintUrl, direction *TransactionDirection, unit *CurrencyUnit) ([]Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []Transaction {
			return FfiConverterSequenceTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_list_transactions(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalTransactionDirectionINSTANCE.Lower(direction), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletPostgresDatabase) RemoveKeys(id Id) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_remove_keys(
			_pointer, FfiConverterIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) RemoveMeltQuote(quoteId string) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_remove_melt_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) RemoveMint(mintUrl MintUrl) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_remove_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) RemoveMintQuote(quoteId string) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_remove_mint_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) RemoveTransaction(transactionId TransactionId) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_remove_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(transactionId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) UpdateMintUrl(oldMintUrl MintUrl, newMintUrl MintUrl) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_update_mint_url(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(oldMintUrl), FfiConverterMintUrlINSTANCE.Lower(newMintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) UpdateProofs(added []ProofInfo, removedYs []PublicKey) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_update_proofs(
			_pointer, FfiConverterSequenceProofInfoINSTANCE.Lower(added), FfiConverterSequencePublicKeyINSTANCE.Lower(removedYs)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletPostgresDatabase) UpdateProofsState(ys []PublicKey, state ProofState) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletpostgresdatabase_update_proofs_state(
			_pointer, FfiConverterSequencePublicKeyINSTANCE.Lower(ys), FfiConverterProofStateINSTANCE.Lower(state)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}
func (object *WalletPostgresDatabase) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterWalletPostgresDatabase struct{}

var FfiConverterWalletPostgresDatabaseINSTANCE = FfiConverterWalletPostgresDatabase{}

func (c FfiConverterWalletPostgresDatabase) Lift(pointer unsafe.Pointer) *WalletPostgresDatabase {
	result := &WalletPostgresDatabase{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_walletpostgresdatabase(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_walletpostgresdatabase(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*WalletPostgresDatabase).Destroy)
	return result
}

func (c FfiConverterWalletPostgresDatabase) Read(reader io.Reader) *WalletPostgresDatabase {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterWalletPostgresDatabase) Lower(value *WalletPostgresDatabase) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*WalletPostgresDatabase")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterWalletPostgresDatabase) Write(writer io.Writer, value *WalletPostgresDatabase) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerWalletPostgresDatabase struct{}

func (_ FfiDestroyerWalletPostgresDatabase) Destroy(value *WalletPostgresDatabase) {
	value.Destroy()
}

// FFI-compatible WalletSqliteDatabase implementation that implements the WalletDatabase trait
type WalletSqliteDatabaseInterface interface {
	AddKeys(keyset KeySet) error
	AddMeltQuote(quote MeltQuote) error
	AddMint(mintUrl MintUrl, mintInfo *MintInfo) error
	AddMintKeysets(mintUrl MintUrl, keysets []KeySetInfo) error
	AddMintQuote(quote MintQuote) error
	AddTransaction(transaction Transaction) error
	GetBalance(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState) (uint64, error)
	GetKeys(id Id) (*Keys, error)
	GetKeysetById(keysetId Id) (*KeySetInfo, error)
	GetMeltQuote(quoteId string) (*MeltQuote, error)
	GetMeltQuotes() ([]MeltQuote, error)
	GetMint(mintUrl MintUrl) (*MintInfo, error)
	GetMintKeysets(mintUrl MintUrl) (*[]KeySetInfo, error)
	GetMintQuote(quoteId string) (*MintQuote, error)
	GetMintQuotes() ([]MintQuote, error)
	GetMints() (map[MintUrl]*MintInfo, error)
	GetProofs(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState, spendingConditions *[]SpendingConditions) ([]ProofInfo, error)
	GetTransaction(transactionId TransactionId) (*Transaction, error)
	IncrementKeysetCounter(keysetId Id, count uint32) (uint32, error)
	ListTransactions(mintUrl *MintUrl, direction *TransactionDirection, unit *CurrencyUnit) ([]Transaction, error)
	RemoveKeys(id Id) error
	RemoveMeltQuote(quoteId string) error
	RemoveMint(mintUrl MintUrl) error
	RemoveMintQuote(quoteId string) error
	RemoveTransaction(transactionId TransactionId) error
	UpdateMintUrl(oldMintUrl MintUrl, newMintUrl MintUrl) error
	UpdateProofs(added []ProofInfo, removedYs []PublicKey) error
	UpdateProofsState(ys []PublicKey, state ProofState) error
}

// FFI-compatible WalletSqliteDatabase implementation that implements the WalletDatabase trait
type WalletSqliteDatabase struct {
	ffiObject FfiObject
}

// Create a new WalletSqliteDatabase with the given work directory
func NewWalletSqliteDatabase(filePath string) (*WalletSqliteDatabase, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_walletsqlitedatabase_new(FfiConverterStringINSTANCE.Lower(filePath), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *WalletSqliteDatabase
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterWalletSqliteDatabaseINSTANCE.Lift(_uniffiRV), nil
	}
}

// Create an in-memory database
func WalletSqliteDatabaseNewInMemory() (*WalletSqliteDatabase, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_constructor_walletsqlitedatabase_new_in_memory(_uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *WalletSqliteDatabase
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterWalletSqliteDatabaseINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *WalletSqliteDatabase) AddKeys(keyset KeySet) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_add_keys(
			_pointer, FfiConverterKeySetINSTANCE.Lower(keyset)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) AddMeltQuote(quote MeltQuote) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_add_melt_quote(
			_pointer, FfiConverterMeltQuoteINSTANCE.Lower(quote)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) AddMint(mintUrl MintUrl, mintInfo *MintInfo) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_add_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalMintInfoINSTANCE.Lower(mintInfo)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) AddMintKeysets(mintUrl MintUrl, keysets []KeySetInfo) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_add_mint_keysets(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl), FfiConverterSequenceKeySetInfoINSTANCE.Lower(keysets)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) AddMintQuote(quote MintQuote) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_add_mint_quote(
			_pointer, FfiConverterMintQuoteINSTANCE.Lower(quote)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) AddTransaction(transaction Transaction) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_add_transaction(
			_pointer, FfiConverterTransactionINSTANCE.Lower(transaction)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) GetBalance(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState) (uint64, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint64_t {
			res := C.ffi_cdk_ffi_rust_future_complete_u64(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint64_t) uint64 {
			return FfiConverterUint64INSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_balance(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit), FfiConverterOptionalSequenceProofStateINSTANCE.Lower(state)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_u64(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_u64(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetKeys(id Id) (*Keys, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *Keys {
			return FfiConverterOptionalKeysINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_keys(
			_pointer, FfiConverterIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetKeysetById(keysetId Id) (*KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *KeySetInfo {
			return FfiConverterOptionalKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_keyset_by_id(
			_pointer, FfiConverterIdINSTANCE.Lower(keysetId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetMeltQuote(quoteId string) (*MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MeltQuote {
			return FfiConverterOptionalMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_melt_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetMeltQuotes() ([]MeltQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []MeltQuote {
			return FfiConverterSequenceMeltQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_melt_quotes(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetMint(mintUrl MintUrl) (*MintInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MintInfo {
			return FfiConverterOptionalMintInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetMintKeysets(mintUrl MintUrl) (*[]KeySetInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *[]KeySetInfo {
			return FfiConverterOptionalSequenceKeySetInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_mint_keysets(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetMintQuote(quoteId string) (*MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *MintQuote {
			return FfiConverterOptionalMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_mint_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetMintQuotes() ([]MintQuote, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []MintQuote {
			return FfiConverterSequenceMintQuoteINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_mint_quotes(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetMints() (map[MintUrl]*MintInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) map[MintUrl]*MintInfo {
			return FfiConverterMapMintUrlOptionalMintInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_mints(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetProofs(mintUrl *MintUrl, unit *CurrencyUnit, state *[]ProofState, spendingConditions *[]SpendingConditions) ([]ProofInfo, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []ProofInfo {
			return FfiConverterSequenceProofInfoINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_proofs(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit), FfiConverterOptionalSequenceProofStateINSTANCE.Lower(state), FfiConverterOptionalSequenceSpendingConditionsINSTANCE.Lower(spendingConditions)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) GetTransaction(transactionId TransactionId) (*Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) *Transaction {
			return FfiConverterOptionalTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_get_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(transactionId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) IncrementKeysetCounter(keysetId Id, count uint32) (uint32, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) C.uint32_t {
			res := C.ffi_cdk_ffi_rust_future_complete_u32(handle, status)
			return res
		},
		// liftFn
		func(ffi C.uint32_t) uint32 {
			return FfiConverterUint32INSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_increment_keyset_counter(
			_pointer, FfiConverterIdINSTANCE.Lower(keysetId), FfiConverterUint32INSTANCE.Lower(count)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_u32(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_u32(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) ListTransactions(mintUrl *MintUrl, direction *TransactionDirection, unit *CurrencyUnit) ([]Transaction, error) {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_cdk_ffi_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) []Transaction {
			return FfiConverterSequenceTransactionINSTANCE.Lift(ffi)
		},
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_list_transactions(
			_pointer, FfiConverterOptionalMintUrlINSTANCE.Lower(mintUrl), FfiConverterOptionalTransactionDirectionINSTANCE.Lower(direction), FfiConverterOptionalCurrencyUnitINSTANCE.Lower(unit)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}

func (_self *WalletSqliteDatabase) RemoveKeys(id Id) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_remove_keys(
			_pointer, FfiConverterIdINSTANCE.Lower(id)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) RemoveMeltQuote(quoteId string) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_remove_melt_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) RemoveMint(mintUrl MintUrl) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_remove_mint(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(mintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) RemoveMintQuote(quoteId string) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_remove_mint_quote(
			_pointer, FfiConverterStringINSTANCE.Lower(quoteId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) RemoveTransaction(transactionId TransactionId) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_remove_transaction(
			_pointer, FfiConverterTransactionIdINSTANCE.Lower(transactionId)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) UpdateMintUrl(oldMintUrl MintUrl, newMintUrl MintUrl) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_update_mint_url(
			_pointer, FfiConverterMintUrlINSTANCE.Lower(oldMintUrl), FfiConverterMintUrlINSTANCE.Lower(newMintUrl)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) UpdateProofs(added []ProofInfo, removedYs []PublicKey) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_update_proofs(
			_pointer, FfiConverterSequenceProofInfoINSTANCE.Lower(added), FfiConverterSequencePublicKeyINSTANCE.Lower(removedYs)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}

func (_self *WalletSqliteDatabase) UpdateProofsState(ys []PublicKey, state ProofState) error {
	_pointer := _self.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer _self.ffiObject.decrementPointer()
	_, err := uniffiRustCallAsync[FfiError](
		FfiConverterFfiErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) struct{} {
			C.ffi_cdk_ffi_rust_future_complete_void(handle, status)
			return struct{}{}
		},
		// liftFn
		func(_ struct{}) struct{} { return struct{}{} },
		C.uniffi_cdk_ffi_fn_method_walletsqlitedatabase_update_proofs_state(
			_pointer, FfiConverterSequencePublicKeyINSTANCE.Lower(ys), FfiConverterProofStateINSTANCE.Lower(state)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_poll_void(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_cdk_ffi_rust_future_free_void(handle)
		},
	)

	if err == nil {
		return nil
	}

	return err
}
func (object *WalletSqliteDatabase) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterWalletSqliteDatabase struct{}

var FfiConverterWalletSqliteDatabaseINSTANCE = FfiConverterWalletSqliteDatabase{}

func (c FfiConverterWalletSqliteDatabase) Lift(pointer unsafe.Pointer) *WalletSqliteDatabase {
	result := &WalletSqliteDatabase{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_cdk_ffi_fn_clone_walletsqlitedatabase(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_cdk_ffi_fn_free_walletsqlitedatabase(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*WalletSqliteDatabase).Destroy)
	return result
}

func (c FfiConverterWalletSqliteDatabase) Read(reader io.Reader) *WalletSqliteDatabase {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterWalletSqliteDatabase) Lower(value *WalletSqliteDatabase) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*WalletSqliteDatabase")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterWalletSqliteDatabase) Write(writer io.Writer, value *WalletSqliteDatabase) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerWalletSqliteDatabase struct{}

func (_ FfiDestroyerWalletSqliteDatabase) Destroy(value *WalletSqliteDatabase) {
	value.Destroy()
}

// FFI-compatible Amount type
type Amount struct {
	Value uint64
}

func (r *Amount) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.Value)
}

type FfiConverterAmount struct{}

var FfiConverterAmountINSTANCE = FfiConverterAmount{}

func (c FfiConverterAmount) Lift(rb RustBufferI) Amount {
	return LiftFromRustBuffer[Amount](c, rb)
}

func (c FfiConverterAmount) Read(reader io.Reader) Amount {
	return Amount{
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterAmount) Lower(value Amount) C.RustBuffer {
	return LowerIntoRustBuffer[Amount](c, value)
}

func (c FfiConverterAmount) Write(writer io.Writer, value Amount) {
	FfiConverterUint64INSTANCE.Write(writer, value.Value)
}

type FfiDestroyerAmount struct{}

func (_ FfiDestroyerAmount) Destroy(value Amount) {
	value.Destroy()
}

// FFI-compatible AuthProof
type AuthProof struct {
	// Keyset ID
	KeysetId string
	// Secret message
	Secret string
	// Unblinded signature (C)
	C string
	// Y value (hash_to_curve of secret)
	Y string
}

func (r *AuthProof) Destroy() {
	FfiDestroyerString{}.Destroy(r.KeysetId)
	FfiDestroyerString{}.Destroy(r.Secret)
	FfiDestroyerString{}.Destroy(r.C)
	FfiDestroyerString{}.Destroy(r.Y)
}

type FfiConverterAuthProof struct{}

var FfiConverterAuthProofINSTANCE = FfiConverterAuthProof{}

func (c FfiConverterAuthProof) Lift(rb RustBufferI) AuthProof {
	return LiftFromRustBuffer[AuthProof](c, rb)
}

func (c FfiConverterAuthProof) Read(reader io.Reader) AuthProof {
	return AuthProof{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterAuthProof) Lower(value AuthProof) C.RustBuffer {
	return LowerIntoRustBuffer[AuthProof](c, value)
}

func (c FfiConverterAuthProof) Write(writer io.Writer, value AuthProof) {
	FfiConverterStringINSTANCE.Write(writer, value.KeysetId)
	FfiConverterStringINSTANCE.Write(writer, value.Secret)
	FfiConverterStringINSTANCE.Write(writer, value.C)
	FfiConverterStringINSTANCE.Write(writer, value.Y)
}

type FfiDestroyerAuthProof struct{}

func (_ FfiDestroyerAuthProof) Destroy(value AuthProof) {
	value.Destroy()
}

// FFI-compatible BlindAuthSettings (NUT-22)
type BlindAuthSettings struct {
	// Maximum number of blind auth tokens that can be minted per request
	BatMaxMint uint64
	// Protected endpoints requiring blind authentication
	ProtectedEndpoints []ProtectedEndpoint
}

func (r *BlindAuthSettings) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.BatMaxMint)
	FfiDestroyerSequenceProtectedEndpoint{}.Destroy(r.ProtectedEndpoints)
}

type FfiConverterBlindAuthSettings struct{}

var FfiConverterBlindAuthSettingsINSTANCE = FfiConverterBlindAuthSettings{}

func (c FfiConverterBlindAuthSettings) Lift(rb RustBufferI) BlindAuthSettings {
	return LiftFromRustBuffer[BlindAuthSettings](c, rb)
}

func (c FfiConverterBlindAuthSettings) Read(reader io.Reader) BlindAuthSettings {
	return BlindAuthSettings{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterSequenceProtectedEndpointINSTANCE.Read(reader),
	}
}

func (c FfiConverterBlindAuthSettings) Lower(value BlindAuthSettings) C.RustBuffer {
	return LowerIntoRustBuffer[BlindAuthSettings](c, value)
}

func (c FfiConverterBlindAuthSettings) Write(writer io.Writer, value BlindAuthSettings) {
	FfiConverterUint64INSTANCE.Write(writer, value.BatMaxMint)
	FfiConverterSequenceProtectedEndpointINSTANCE.Write(writer, value.ProtectedEndpoints)
}

type FfiDestroyerBlindAuthSettings struct{}

func (_ FfiDestroyerBlindAuthSettings) Destroy(value BlindAuthSettings) {
	value.Destroy()
}

// FFI-compatible DLEQ proof for blind signatures
type BlindSignatureDleq struct {
	// e value (hex-encoded SecretKey)
	E string
	// s value (hex-encoded SecretKey)
	S string
}

func (r *BlindSignatureDleq) Destroy() {
	FfiDestroyerString{}.Destroy(r.E)
	FfiDestroyerString{}.Destroy(r.S)
}

type FfiConverterBlindSignatureDleq struct{}

var FfiConverterBlindSignatureDleqINSTANCE = FfiConverterBlindSignatureDleq{}

func (c FfiConverterBlindSignatureDleq) Lift(rb RustBufferI) BlindSignatureDleq {
	return LiftFromRustBuffer[BlindSignatureDleq](c, rb)
}

func (c FfiConverterBlindSignatureDleq) Read(reader io.Reader) BlindSignatureDleq {
	return BlindSignatureDleq{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterBlindSignatureDleq) Lower(value BlindSignatureDleq) C.RustBuffer {
	return LowerIntoRustBuffer[BlindSignatureDleq](c, value)
}

func (c FfiConverterBlindSignatureDleq) Write(writer io.Writer, value BlindSignatureDleq) {
	FfiConverterStringINSTANCE.Write(writer, value.E)
	FfiConverterStringINSTANCE.Write(writer, value.S)
}

type FfiDestroyerBlindSignatureDleq struct{}

func (_ FfiDestroyerBlindSignatureDleq) Destroy(value BlindSignatureDleq) {
	value.Destroy()
}

// FFI-compatible ClearAuthSettings (NUT-21)
type ClearAuthSettings struct {
	// OpenID Connect discovery URL
	OpenidDiscovery string
	// OAuth 2.0 client ID
	ClientId string
	// Protected endpoints requiring clear authentication
	ProtectedEndpoints []ProtectedEndpoint
}

func (r *ClearAuthSettings) Destroy() {
	FfiDestroyerString{}.Destroy(r.OpenidDiscovery)
	FfiDestroyerString{}.Destroy(r.ClientId)
	FfiDestroyerSequenceProtectedEndpoint{}.Destroy(r.ProtectedEndpoints)
}

type FfiConverterClearAuthSettings struct{}

var FfiConverterClearAuthSettingsINSTANCE = FfiConverterClearAuthSettings{}

func (c FfiConverterClearAuthSettings) Lift(rb RustBufferI) ClearAuthSettings {
	return LiftFromRustBuffer[ClearAuthSettings](c, rb)
}

func (c FfiConverterClearAuthSettings) Read(reader io.Reader) ClearAuthSettings {
	return ClearAuthSettings{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterSequenceProtectedEndpointINSTANCE.Read(reader),
	}
}

func (c FfiConverterClearAuthSettings) Lower(value ClearAuthSettings) C.RustBuffer {
	return LowerIntoRustBuffer[ClearAuthSettings](c, value)
}

func (c FfiConverterClearAuthSettings) Write(writer io.Writer, value ClearAuthSettings) {
	FfiConverterStringINSTANCE.Write(writer, value.OpenidDiscovery)
	FfiConverterStringINSTANCE.Write(writer, value.ClientId)
	FfiConverterSequenceProtectedEndpointINSTANCE.Write(writer, value.ProtectedEndpoints)
}

type FfiDestroyerClearAuthSettings struct{}

func (_ FfiDestroyerClearAuthSettings) Destroy(value ClearAuthSettings) {
	value.Destroy()
}

// FFI-compatible Conditions (for spending conditions)
type Conditions struct {
	// Unix locktime after which refund keys can be used
	Locktime *uint64
	// Additional Public keys (as hex strings)
	Pubkeys []string
	// Refund keys (as hex strings)
	RefundKeys []string
	// Number of signatures required (default 1)
	NumSigs *uint64
	// Signature flag (0 = SigInputs, 1 = SigAll)
	SigFlag uint8
	// Number of refund signatures required (default 1)
	NumSigsRefund *uint64
}

func (r *Conditions) Destroy() {
	FfiDestroyerOptionalUint64{}.Destroy(r.Locktime)
	FfiDestroyerSequenceString{}.Destroy(r.Pubkeys)
	FfiDestroyerSequenceString{}.Destroy(r.RefundKeys)
	FfiDestroyerOptionalUint64{}.Destroy(r.NumSigs)
	FfiDestroyerUint8{}.Destroy(r.SigFlag)
	FfiDestroyerOptionalUint64{}.Destroy(r.NumSigsRefund)
}

type FfiConverterConditions struct{}

var FfiConverterConditionsINSTANCE = FfiConverterConditions{}

func (c FfiConverterConditions) Lift(rb RustBufferI) Conditions {
	return LiftFromRustBuffer[Conditions](c, rb)
}

func (c FfiConverterConditions) Read(reader io.Reader) Conditions {
	return Conditions{
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterUint8INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterConditions) Lower(value Conditions) C.RustBuffer {
	return LowerIntoRustBuffer[Conditions](c, value)
}

func (c FfiConverterConditions) Write(writer io.Writer, value Conditions) {
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.Locktime)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.Pubkeys)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.RefundKeys)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.NumSigs)
	FfiConverterUint8INSTANCE.Write(writer, value.SigFlag)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.NumSigsRefund)
}

type FfiDestroyerConditions struct{}

func (_ FfiDestroyerConditions) Destroy(value Conditions) {
	value.Destroy()
}

// FFI-compatible ContactInfo
type ContactInfo struct {
	// Contact Method i.e. nostr
	Method string
	// Contact info i.e. npub...
	Info string
}

func (r *ContactInfo) Destroy() {
	FfiDestroyerString{}.Destroy(r.Method)
	FfiDestroyerString{}.Destroy(r.Info)
}

type FfiConverterContactInfo struct{}

var FfiConverterContactInfoINSTANCE = FfiConverterContactInfo{}

func (c FfiConverterContactInfo) Lift(rb RustBufferI) ContactInfo {
	return LiftFromRustBuffer[ContactInfo](c, rb)
}

func (c FfiConverterContactInfo) Read(reader io.Reader) ContactInfo {
	return ContactInfo{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterContactInfo) Lower(value ContactInfo) C.RustBuffer {
	return LowerIntoRustBuffer[ContactInfo](c, value)
}

func (c FfiConverterContactInfo) Write(writer io.Writer, value ContactInfo) {
	FfiConverterStringINSTANCE.Write(writer, value.Method)
	FfiConverterStringINSTANCE.Write(writer, value.Info)
}

type FfiDestroyerContactInfo struct{}

func (_ FfiDestroyerContactInfo) Destroy(value ContactInfo) {
	value.Destroy()
}

// FFI-compatible Id (for keyset IDs)
type Id struct {
	Hex string
}

func (r *Id) Destroy() {
	FfiDestroyerString{}.Destroy(r.Hex)
}

type FfiConverterId struct{}

var FfiConverterIdINSTANCE = FfiConverterId{}

func (c FfiConverterId) Lift(rb RustBufferI) Id {
	return LiftFromRustBuffer[Id](c, rb)
}

func (c FfiConverterId) Read(reader io.Reader) Id {
	return Id{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterId) Lower(value Id) C.RustBuffer {
	return LowerIntoRustBuffer[Id](c, value)
}

func (c FfiConverterId) Write(writer io.Writer, value Id) {
	FfiConverterStringINSTANCE.Write(writer, value.Hex)
}

type FfiDestroyerId struct{}

func (_ FfiDestroyerId) Destroy(value Id) {
	value.Destroy()
}

// FFI-compatible KeySet
type KeySet struct {
	// Keyset ID
	Id string
	// Currency unit
	Unit CurrencyUnit
	// The keys (map of amount to public key hex)
	Keys map[uint64]string
	// Optional expiry timestamp
	FinalExpiry *uint64
}

func (r *KeySet) Destroy() {
	FfiDestroyerString{}.Destroy(r.Id)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerMapUint64String{}.Destroy(r.Keys)
	FfiDestroyerOptionalUint64{}.Destroy(r.FinalExpiry)
}

type FfiConverterKeySet struct{}

var FfiConverterKeySetINSTANCE = FfiConverterKeySet{}

func (c FfiConverterKeySet) Lift(rb RustBufferI) KeySet {
	return LiftFromRustBuffer[KeySet](c, rb)
}

func (c FfiConverterKeySet) Read(reader io.Reader) KeySet {
	return KeySet{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterMapUint64StringINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterKeySet) Lower(value KeySet) C.RustBuffer {
	return LowerIntoRustBuffer[KeySet](c, value)
}

func (c FfiConverterKeySet) Write(writer io.Writer, value KeySet) {
	FfiConverterStringINSTANCE.Write(writer, value.Id)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterMapUint64StringINSTANCE.Write(writer, value.Keys)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.FinalExpiry)
}

type FfiDestroyerKeySet struct{}

func (_ FfiDestroyerKeySet) Destroy(value KeySet) {
	value.Destroy()
}

// FFI-compatible KeySetInfo
type KeySetInfo struct {
	Id     string
	Unit   CurrencyUnit
	Active bool
	// Input fee per thousand (ppk)
	InputFeePpk uint64
}

func (r *KeySetInfo) Destroy() {
	FfiDestroyerString{}.Destroy(r.Id)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerBool{}.Destroy(r.Active)
	FfiDestroyerUint64{}.Destroy(r.InputFeePpk)
}

type FfiConverterKeySetInfo struct{}

var FfiConverterKeySetInfoINSTANCE = FfiConverterKeySetInfo{}

func (c FfiConverterKeySetInfo) Lift(rb RustBufferI) KeySetInfo {
	return LiftFromRustBuffer[KeySetInfo](c, rb)
}

func (c FfiConverterKeySetInfo) Read(reader io.Reader) KeySetInfo {
	return KeySetInfo{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterKeySetInfo) Lower(value KeySetInfo) C.RustBuffer {
	return LowerIntoRustBuffer[KeySetInfo](c, value)
}

func (c FfiConverterKeySetInfo) Write(writer io.Writer, value KeySetInfo) {
	FfiConverterStringINSTANCE.Write(writer, value.Id)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterBoolINSTANCE.Write(writer, value.Active)
	FfiConverterUint64INSTANCE.Write(writer, value.InputFeePpk)
}

type FfiDestroyerKeySetInfo struct{}

func (_ FfiDestroyerKeySetInfo) Destroy(value KeySetInfo) {
	value.Destroy()
}

// FFI-compatible Keys (simplified - contains only essential info)
type Keys struct {
	// Keyset ID
	Id string
	// Currency unit
	Unit CurrencyUnit
	// Map of amount to public key hex (simplified from BTreeMap)
	Keys map[uint64]string
}

func (r *Keys) Destroy() {
	FfiDestroyerString{}.Destroy(r.Id)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerMapUint64String{}.Destroy(r.Keys)
}

type FfiConverterKeys struct{}

var FfiConverterKeysINSTANCE = FfiConverterKeys{}

func (c FfiConverterKeys) Lift(rb RustBufferI) Keys {
	return LiftFromRustBuffer[Keys](c, rb)
}

func (c FfiConverterKeys) Read(reader io.Reader) Keys {
	return Keys{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterMapUint64StringINSTANCE.Read(reader),
	}
}

func (c FfiConverterKeys) Lower(value Keys) C.RustBuffer {
	return LowerIntoRustBuffer[Keys](c, value)
}

func (c FfiConverterKeys) Write(writer io.Writer, value Keys) {
	FfiConverterStringINSTANCE.Write(writer, value.Id)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterMapUint64StringINSTANCE.Write(writer, value.Keys)
}

type FfiDestroyerKeys struct{}

func (_ FfiDestroyerKeys) Destroy(value Keys) {
	value.Destroy()
}

// FFI-compatible MeltMethodSettings (NUT-05)
type MeltMethodSettings struct {
	Method    PaymentMethod
	Unit      CurrencyUnit
	MinAmount *Amount
	MaxAmount *Amount
	// For bolt11, whether mint supports amountless invoices
	Amountless *bool
}

func (r *MeltMethodSettings) Destroy() {
	FfiDestroyerPaymentMethod{}.Destroy(r.Method)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerOptionalAmount{}.Destroy(r.MinAmount)
	FfiDestroyerOptionalAmount{}.Destroy(r.MaxAmount)
	FfiDestroyerOptionalBool{}.Destroy(r.Amountless)
}

type FfiConverterMeltMethodSettings struct{}

var FfiConverterMeltMethodSettingsINSTANCE = FfiConverterMeltMethodSettings{}

func (c FfiConverterMeltMethodSettings) Lift(rb RustBufferI) MeltMethodSettings {
	return LiftFromRustBuffer[MeltMethodSettings](c, rb)
}

func (c FfiConverterMeltMethodSettings) Read(reader io.Reader) MeltMethodSettings {
	return MeltMethodSettings{
		FfiConverterPaymentMethodINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterOptionalAmountINSTANCE.Read(reader),
		FfiConverterOptionalAmountINSTANCE.Read(reader),
		FfiConverterOptionalBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterMeltMethodSettings) Lower(value MeltMethodSettings) C.RustBuffer {
	return LowerIntoRustBuffer[MeltMethodSettings](c, value)
}

func (c FfiConverterMeltMethodSettings) Write(writer io.Writer, value MeltMethodSettings) {
	FfiConverterPaymentMethodINSTANCE.Write(writer, value.Method)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterOptionalAmountINSTANCE.Write(writer, value.MinAmount)
	FfiConverterOptionalAmountINSTANCE.Write(writer, value.MaxAmount)
	FfiConverterOptionalBoolINSTANCE.Write(writer, value.Amountless)
}

type FfiDestroyerMeltMethodSettings struct{}

func (_ FfiDestroyerMeltMethodSettings) Destroy(value MeltMethodSettings) {
	value.Destroy()
}

// FFI-compatible MeltQuote
type MeltQuote struct {
	// Quote ID
	Id string
	// Quote amount
	Amount Amount
	// Currency unit
	Unit CurrencyUnit
	// Payment request
	Request string
	// Fee reserve
	FeeReserve Amount
	// Quote state
	State QuoteState
	// Expiry timestamp
	Expiry uint64
	// Payment preimage
	PaymentPreimage *string
	// Payment method
	PaymentMethod PaymentMethod
}

func (r *MeltQuote) Destroy() {
	FfiDestroyerString{}.Destroy(r.Id)
	FfiDestroyerAmount{}.Destroy(r.Amount)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerString{}.Destroy(r.Request)
	FfiDestroyerAmount{}.Destroy(r.FeeReserve)
	FfiDestroyerQuoteState{}.Destroy(r.State)
	FfiDestroyerUint64{}.Destroy(r.Expiry)
	FfiDestroyerOptionalString{}.Destroy(r.PaymentPreimage)
	FfiDestroyerPaymentMethod{}.Destroy(r.PaymentMethod)
}

type FfiConverterMeltQuote struct{}

var FfiConverterMeltQuoteINSTANCE = FfiConverterMeltQuote{}

func (c FfiConverterMeltQuote) Lift(rb RustBufferI) MeltQuote {
	return LiftFromRustBuffer[MeltQuote](c, rb)
}

func (c FfiConverterMeltQuote) Read(reader io.Reader) MeltQuote {
	return MeltQuote{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterQuoteStateINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterPaymentMethodINSTANCE.Read(reader),
	}
}

func (c FfiConverterMeltQuote) Lower(value MeltQuote) C.RustBuffer {
	return LowerIntoRustBuffer[MeltQuote](c, value)
}

func (c FfiConverterMeltQuote) Write(writer io.Writer, value MeltQuote) {
	FfiConverterStringINSTANCE.Write(writer, value.Id)
	FfiConverterAmountINSTANCE.Write(writer, value.Amount)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterStringINSTANCE.Write(writer, value.Request)
	FfiConverterAmountINSTANCE.Write(writer, value.FeeReserve)
	FfiConverterQuoteStateINSTANCE.Write(writer, value.State)
	FfiConverterUint64INSTANCE.Write(writer, value.Expiry)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.PaymentPreimage)
	FfiConverterPaymentMethodINSTANCE.Write(writer, value.PaymentMethod)
}

type FfiDestroyerMeltQuote struct{}

func (_ FfiDestroyerMeltQuote) Destroy(value MeltQuote) {
	value.Destroy()
}

// FFI-compatible Melted result
type Melted struct {
	State    QuoteState
	Preimage *string
	Change   *[]*Proof
	Amount   Amount
	FeePaid  Amount
}

func (r *Melted) Destroy() {
	FfiDestroyerQuoteState{}.Destroy(r.State)
	FfiDestroyerOptionalString{}.Destroy(r.Preimage)
	FfiDestroyerOptionalSequenceProof{}.Destroy(r.Change)
	FfiDestroyerAmount{}.Destroy(r.Amount)
	FfiDestroyerAmount{}.Destroy(r.FeePaid)
}

type FfiConverterMelted struct{}

var FfiConverterMeltedINSTANCE = FfiConverterMelted{}

func (c FfiConverterMelted) Lift(rb RustBufferI) Melted {
	return LiftFromRustBuffer[Melted](c, rb)
}

func (c FfiConverterMelted) Read(reader io.Reader) Melted {
	return Melted{
		FfiConverterQuoteStateINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalSequenceProofINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
	}
}

func (c FfiConverterMelted) Lower(value Melted) C.RustBuffer {
	return LowerIntoRustBuffer[Melted](c, value)
}

func (c FfiConverterMelted) Write(writer io.Writer, value Melted) {
	FfiConverterQuoteStateINSTANCE.Write(writer, value.State)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Preimage)
	FfiConverterOptionalSequenceProofINSTANCE.Write(writer, value.Change)
	FfiConverterAmountINSTANCE.Write(writer, value.Amount)
	FfiConverterAmountINSTANCE.Write(writer, value.FeePaid)
}

type FfiDestroyerMelted struct{}

func (_ FfiDestroyerMelted) Destroy(value Melted) {
	value.Destroy()
}

// FFI-compatible MintInfo
type MintInfo struct {
	// name of the mint and should be recognizable
	Name *string
	// hex pubkey of the mint
	Pubkey *string
	// implementation name and the version running
	Version *MintVersion
	// short description of the mint
	Description *string
	// long description
	DescriptionLong *string
	// Contact info
	Contact *[]ContactInfo
	// shows which NUTs the mint supports
	Nuts Nuts
	// Mint's icon URL
	IconUrl *string
	// Mint's endpoint URLs
	Urls *[]string
	// message of the day that the wallet must display to the user
	Motd *string
	// server unix timestamp
	Time *uint64
	// terms of url service of the mint
	TosUrl *string
}

func (r *MintInfo) Destroy() {
	FfiDestroyerOptionalString{}.Destroy(r.Name)
	FfiDestroyerOptionalString{}.Destroy(r.Pubkey)
	FfiDestroyerOptionalMintVersion{}.Destroy(r.Version)
	FfiDestroyerOptionalString{}.Destroy(r.Description)
	FfiDestroyerOptionalString{}.Destroy(r.DescriptionLong)
	FfiDestroyerOptionalSequenceContactInfo{}.Destroy(r.Contact)
	FfiDestroyerNuts{}.Destroy(r.Nuts)
	FfiDestroyerOptionalString{}.Destroy(r.IconUrl)
	FfiDestroyerOptionalSequenceString{}.Destroy(r.Urls)
	FfiDestroyerOptionalString{}.Destroy(r.Motd)
	FfiDestroyerOptionalUint64{}.Destroy(r.Time)
	FfiDestroyerOptionalString{}.Destroy(r.TosUrl)
}

type FfiConverterMintInfo struct{}

var FfiConverterMintInfoINSTANCE = FfiConverterMintInfo{}

func (c FfiConverterMintInfo) Lift(rb RustBufferI) MintInfo {
	return LiftFromRustBuffer[MintInfo](c, rb)
}

func (c FfiConverterMintInfo) Read(reader io.Reader) MintInfo {
	return MintInfo{
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalMintVersionINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalSequenceContactInfoINSTANCE.Read(reader),
		FfiConverterNutsINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalSequenceStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterMintInfo) Lower(value MintInfo) C.RustBuffer {
	return LowerIntoRustBuffer[MintInfo](c, value)
}

func (c FfiConverterMintInfo) Write(writer io.Writer, value MintInfo) {
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Name)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Pubkey)
	FfiConverterOptionalMintVersionINSTANCE.Write(writer, value.Version)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Description)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.DescriptionLong)
	FfiConverterOptionalSequenceContactInfoINSTANCE.Write(writer, value.Contact)
	FfiConverterNutsINSTANCE.Write(writer, value.Nuts)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.IconUrl)
	FfiConverterOptionalSequenceStringINSTANCE.Write(writer, value.Urls)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Motd)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.Time)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.TosUrl)
}

type FfiDestroyerMintInfo struct{}

func (_ FfiDestroyerMintInfo) Destroy(value MintInfo) {
	value.Destroy()
}

// FFI-compatible MintMethodSettings (NUT-04)
type MintMethodSettings struct {
	Method    PaymentMethod
	Unit      CurrencyUnit
	MinAmount *Amount
	MaxAmount *Amount
	// For bolt11, whether mint supports setting invoice description
	Description *bool
}

func (r *MintMethodSettings) Destroy() {
	FfiDestroyerPaymentMethod{}.Destroy(r.Method)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerOptionalAmount{}.Destroy(r.MinAmount)
	FfiDestroyerOptionalAmount{}.Destroy(r.MaxAmount)
	FfiDestroyerOptionalBool{}.Destroy(r.Description)
}

type FfiConverterMintMethodSettings struct{}

var FfiConverterMintMethodSettingsINSTANCE = FfiConverterMintMethodSettings{}

func (c FfiConverterMintMethodSettings) Lift(rb RustBufferI) MintMethodSettings {
	return LiftFromRustBuffer[MintMethodSettings](c, rb)
}

func (c FfiConverterMintMethodSettings) Read(reader io.Reader) MintMethodSettings {
	return MintMethodSettings{
		FfiConverterPaymentMethodINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterOptionalAmountINSTANCE.Read(reader),
		FfiConverterOptionalAmountINSTANCE.Read(reader),
		FfiConverterOptionalBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterMintMethodSettings) Lower(value MintMethodSettings) C.RustBuffer {
	return LowerIntoRustBuffer[MintMethodSettings](c, value)
}

func (c FfiConverterMintMethodSettings) Write(writer io.Writer, value MintMethodSettings) {
	FfiConverterPaymentMethodINSTANCE.Write(writer, value.Method)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterOptionalAmountINSTANCE.Write(writer, value.MinAmount)
	FfiConverterOptionalAmountINSTANCE.Write(writer, value.MaxAmount)
	FfiConverterOptionalBoolINSTANCE.Write(writer, value.Description)
}

type FfiDestroyerMintMethodSettings struct{}

func (_ FfiDestroyerMintMethodSettings) Destroy(value MintMethodSettings) {
	value.Destroy()
}

// FFI-compatible MintQuote
type MintQuote struct {
	// Quote ID
	Id string
	// Quote amount
	Amount *Amount
	// Currency unit
	Unit CurrencyUnit
	// Payment request
	Request string
	// Quote state
	State QuoteState
	// Expiry timestamp
	Expiry uint64
	// Mint URL
	MintUrl MintUrl
	// Amount issued
	AmountIssued Amount
	// Amount paid
	AmountPaid Amount
	// Payment method
	PaymentMethod PaymentMethod
	// Secret key (optional, hex-encoded)
	SecretKey *string
}

func (r *MintQuote) Destroy() {
	FfiDestroyerString{}.Destroy(r.Id)
	FfiDestroyerOptionalAmount{}.Destroy(r.Amount)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerString{}.Destroy(r.Request)
	FfiDestroyerQuoteState{}.Destroy(r.State)
	FfiDestroyerUint64{}.Destroy(r.Expiry)
	FfiDestroyerMintUrl{}.Destroy(r.MintUrl)
	FfiDestroyerAmount{}.Destroy(r.AmountIssued)
	FfiDestroyerAmount{}.Destroy(r.AmountPaid)
	FfiDestroyerPaymentMethod{}.Destroy(r.PaymentMethod)
	FfiDestroyerOptionalString{}.Destroy(r.SecretKey)
}

type FfiConverterMintQuote struct{}

var FfiConverterMintQuoteINSTANCE = FfiConverterMintQuote{}

func (c FfiConverterMintQuote) Lift(rb RustBufferI) MintQuote {
	return LiftFromRustBuffer[MintQuote](c, rb)
}

func (c FfiConverterMintQuote) Read(reader io.Reader) MintQuote {
	return MintQuote{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterOptionalAmountINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterQuoteStateINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterMintUrlINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterPaymentMethodINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterMintQuote) Lower(value MintQuote) C.RustBuffer {
	return LowerIntoRustBuffer[MintQuote](c, value)
}

func (c FfiConverterMintQuote) Write(writer io.Writer, value MintQuote) {
	FfiConverterStringINSTANCE.Write(writer, value.Id)
	FfiConverterOptionalAmountINSTANCE.Write(writer, value.Amount)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterStringINSTANCE.Write(writer, value.Request)
	FfiConverterQuoteStateINSTANCE.Write(writer, value.State)
	FfiConverterUint64INSTANCE.Write(writer, value.Expiry)
	FfiConverterMintUrlINSTANCE.Write(writer, value.MintUrl)
	FfiConverterAmountINSTANCE.Write(writer, value.AmountIssued)
	FfiConverterAmountINSTANCE.Write(writer, value.AmountPaid)
	FfiConverterPaymentMethodINSTANCE.Write(writer, value.PaymentMethod)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.SecretKey)
}

type FfiDestroyerMintQuote struct{}

func (_ FfiDestroyerMintQuote) Destroy(value MintQuote) {
	value.Destroy()
}

// FFI-compatible Mint URL
type MintUrl struct {
	Url string
}

func (r *MintUrl) Destroy() {
	FfiDestroyerString{}.Destroy(r.Url)
}

type FfiConverterMintUrl struct{}

var FfiConverterMintUrlINSTANCE = FfiConverterMintUrl{}

func (c FfiConverterMintUrl) Lift(rb RustBufferI) MintUrl {
	return LiftFromRustBuffer[MintUrl](c, rb)
}

func (c FfiConverterMintUrl) Read(reader io.Reader) MintUrl {
	return MintUrl{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterMintUrl) Lower(value MintUrl) C.RustBuffer {
	return LowerIntoRustBuffer[MintUrl](c, value)
}

func (c FfiConverterMintUrl) Write(writer io.Writer, value MintUrl) {
	FfiConverterStringINSTANCE.Write(writer, value.Url)
}

type FfiDestroyerMintUrl struct{}

func (_ FfiDestroyerMintUrl) Destroy(value MintUrl) {
	value.Destroy()
}

// FFI-compatible MintVersion
type MintVersion struct {
	// Mint Software name
	Name string
	// Mint Version
	Version string
}

func (r *MintVersion) Destroy() {
	FfiDestroyerString{}.Destroy(r.Name)
	FfiDestroyerString{}.Destroy(r.Version)
}

type FfiConverterMintVersion struct{}

var FfiConverterMintVersionINSTANCE = FfiConverterMintVersion{}

func (c FfiConverterMintVersion) Lift(rb RustBufferI) MintVersion {
	return LiftFromRustBuffer[MintVersion](c, rb)
}

func (c FfiConverterMintVersion) Read(reader io.Reader) MintVersion {
	return MintVersion{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterMintVersion) Lower(value MintVersion) C.RustBuffer {
	return LowerIntoRustBuffer[MintVersion](c, value)
}

func (c FfiConverterMintVersion) Write(writer io.Writer, value MintVersion) {
	FfiConverterStringINSTANCE.Write(writer, value.Name)
	FfiConverterStringINSTANCE.Write(writer, value.Version)
}

type FfiDestroyerMintVersion struct{}

func (_ FfiDestroyerMintVersion) Destroy(value MintVersion) {
	value.Destroy()
}

// Options for receiving tokens in multi-mint context
type MultiMintReceiveOptions struct {
	// Whether to allow receiving from untrusted (not yet added) mints
	AllowUntrusted bool
	// Mint URL to transfer tokens to from untrusted mints (None means keep in original mint)
	TransferToMint *MintUrl
	// Base receive options to apply to the wallet receive
	ReceiveOptions ReceiveOptions
}

func (r *MultiMintReceiveOptions) Destroy() {
	FfiDestroyerBool{}.Destroy(r.AllowUntrusted)
	FfiDestroyerOptionalMintUrl{}.Destroy(r.TransferToMint)
	FfiDestroyerReceiveOptions{}.Destroy(r.ReceiveOptions)
}

type FfiConverterMultiMintReceiveOptions struct{}

var FfiConverterMultiMintReceiveOptionsINSTANCE = FfiConverterMultiMintReceiveOptions{}

func (c FfiConverterMultiMintReceiveOptions) Lift(rb RustBufferI) MultiMintReceiveOptions {
	return LiftFromRustBuffer[MultiMintReceiveOptions](c, rb)
}

func (c FfiConverterMultiMintReceiveOptions) Read(reader io.Reader) MultiMintReceiveOptions {
	return MultiMintReceiveOptions{
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterOptionalMintUrlINSTANCE.Read(reader),
		FfiConverterReceiveOptionsINSTANCE.Read(reader),
	}
}

func (c FfiConverterMultiMintReceiveOptions) Lower(value MultiMintReceiveOptions) C.RustBuffer {
	return LowerIntoRustBuffer[MultiMintReceiveOptions](c, value)
}

func (c FfiConverterMultiMintReceiveOptions) Write(writer io.Writer, value MultiMintReceiveOptions) {
	FfiConverterBoolINSTANCE.Write(writer, value.AllowUntrusted)
	FfiConverterOptionalMintUrlINSTANCE.Write(writer, value.TransferToMint)
	FfiConverterReceiveOptionsINSTANCE.Write(writer, value.ReceiveOptions)
}

type FfiDestroyerMultiMintReceiveOptions struct{}

func (_ FfiDestroyerMultiMintReceiveOptions) Destroy(value MultiMintReceiveOptions) {
	value.Destroy()
}

// Options for sending tokens in multi-mint context
type MultiMintSendOptions struct {
	// Whether to allow transferring funds from other mints if needed
	AllowTransfer bool
	// Maximum amount to transfer from other mints (optional limit)
	MaxTransferAmount *Amount
	// Specific mint URLs allowed for transfers (empty means all mints allowed)
	AllowedMints []MintUrl
	// Specific mint URLs to exclude from transfers
	ExcludedMints []MintUrl
	// Base send options to apply to the wallet send
	SendOptions SendOptions
}

func (r *MultiMintSendOptions) Destroy() {
	FfiDestroyerBool{}.Destroy(r.AllowTransfer)
	FfiDestroyerOptionalAmount{}.Destroy(r.MaxTransferAmount)
	FfiDestroyerSequenceMintUrl{}.Destroy(r.AllowedMints)
	FfiDestroyerSequenceMintUrl{}.Destroy(r.ExcludedMints)
	FfiDestroyerSendOptions{}.Destroy(r.SendOptions)
}

type FfiConverterMultiMintSendOptions struct{}

var FfiConverterMultiMintSendOptionsINSTANCE = FfiConverterMultiMintSendOptions{}

func (c FfiConverterMultiMintSendOptions) Lift(rb RustBufferI) MultiMintSendOptions {
	return LiftFromRustBuffer[MultiMintSendOptions](c, rb)
}

func (c FfiConverterMultiMintSendOptions) Read(reader io.Reader) MultiMintSendOptions {
	return MultiMintSendOptions{
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterOptionalAmountINSTANCE.Read(reader),
		FfiConverterSequenceMintUrlINSTANCE.Read(reader),
		FfiConverterSequenceMintUrlINSTANCE.Read(reader),
		FfiConverterSendOptionsINSTANCE.Read(reader),
	}
}

func (c FfiConverterMultiMintSendOptions) Lower(value MultiMintSendOptions) C.RustBuffer {
	return LowerIntoRustBuffer[MultiMintSendOptions](c, value)
}

func (c FfiConverterMultiMintSendOptions) Write(writer io.Writer, value MultiMintSendOptions) {
	FfiConverterBoolINSTANCE.Write(writer, value.AllowTransfer)
	FfiConverterOptionalAmountINSTANCE.Write(writer, value.MaxTransferAmount)
	FfiConverterSequenceMintUrlINSTANCE.Write(writer, value.AllowedMints)
	FfiConverterSequenceMintUrlINSTANCE.Write(writer, value.ExcludedMints)
	FfiConverterSendOptionsINSTANCE.Write(writer, value.SendOptions)
}

type FfiDestroyerMultiMintSendOptions struct{}

func (_ FfiDestroyerMultiMintSendOptions) Destroy(value MultiMintSendOptions) {
	value.Destroy()
}

// FFI-compatible Nut04 Settings
type Nut04Settings struct {
	Methods  []MintMethodSettings
	Disabled bool
}

func (r *Nut04Settings) Destroy() {
	FfiDestroyerSequenceMintMethodSettings{}.Destroy(r.Methods)
	FfiDestroyerBool{}.Destroy(r.Disabled)
}

type FfiConverterNut04Settings struct{}

var FfiConverterNut04SettingsINSTANCE = FfiConverterNut04Settings{}

func (c FfiConverterNut04Settings) Lift(rb RustBufferI) Nut04Settings {
	return LiftFromRustBuffer[Nut04Settings](c, rb)
}

func (c FfiConverterNut04Settings) Read(reader io.Reader) Nut04Settings {
	return Nut04Settings{
		FfiConverterSequenceMintMethodSettingsINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterNut04Settings) Lower(value Nut04Settings) C.RustBuffer {
	return LowerIntoRustBuffer[Nut04Settings](c, value)
}

func (c FfiConverterNut04Settings) Write(writer io.Writer, value Nut04Settings) {
	FfiConverterSequenceMintMethodSettingsINSTANCE.Write(writer, value.Methods)
	FfiConverterBoolINSTANCE.Write(writer, value.Disabled)
}

type FfiDestroyerNut04Settings struct{}

func (_ FfiDestroyerNut04Settings) Destroy(value Nut04Settings) {
	value.Destroy()
}

// FFI-compatible Nut05 Settings
type Nut05Settings struct {
	Methods  []MeltMethodSettings
	Disabled bool
}

func (r *Nut05Settings) Destroy() {
	FfiDestroyerSequenceMeltMethodSettings{}.Destroy(r.Methods)
	FfiDestroyerBool{}.Destroy(r.Disabled)
}

type FfiConverterNut05Settings struct{}

var FfiConverterNut05SettingsINSTANCE = FfiConverterNut05Settings{}

func (c FfiConverterNut05Settings) Lift(rb RustBufferI) Nut05Settings {
	return LiftFromRustBuffer[Nut05Settings](c, rb)
}

func (c FfiConverterNut05Settings) Read(reader io.Reader) Nut05Settings {
	return Nut05Settings{
		FfiConverterSequenceMeltMethodSettingsINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterNut05Settings) Lower(value Nut05Settings) C.RustBuffer {
	return LowerIntoRustBuffer[Nut05Settings](c, value)
}

func (c FfiConverterNut05Settings) Write(writer io.Writer, value Nut05Settings) {
	FfiConverterSequenceMeltMethodSettingsINSTANCE.Write(writer, value.Methods)
	FfiConverterBoolINSTANCE.Write(writer, value.Disabled)
}

type FfiDestroyerNut05Settings struct{}

func (_ FfiDestroyerNut05Settings) Destroy(value Nut05Settings) {
	value.Destroy()
}

// FFI-compatible Nuts settings (extended to include NUT-04 and NUT-05 settings)
type Nuts struct {
	// NUT04 Settings
	Nut04 Nut04Settings
	// NUT05 Settings
	Nut05 Nut05Settings
	// NUT07 Settings - Token state check
	Nut07Supported bool
	// NUT08 Settings - Lightning fee return
	Nut08Supported bool
	// NUT09 Settings - Restore signature
	Nut09Supported bool
	// NUT10 Settings - Spending conditions
	Nut10Supported bool
	// NUT11 Settings - Pay to Public Key Hash
	Nut11Supported bool
	// NUT12 Settings - DLEQ proofs
	Nut12Supported bool
	// NUT14 Settings - Hashed Time Locked Contracts
	Nut14Supported bool
	// NUT20 Settings - Web sockets
	Nut20Supported bool
	// NUT21 Settings - Clear authentication
	Nut21 *ClearAuthSettings
	// NUT22 Settings - Blind authentication
	Nut22 *BlindAuthSettings
	// Supported currency units for minting
	MintUnits []CurrencyUnit
	// Supported currency units for melting
	MeltUnits []CurrencyUnit
}

func (r *Nuts) Destroy() {
	FfiDestroyerNut04Settings{}.Destroy(r.Nut04)
	FfiDestroyerNut05Settings{}.Destroy(r.Nut05)
	FfiDestroyerBool{}.Destroy(r.Nut07Supported)
	FfiDestroyerBool{}.Destroy(r.Nut08Supported)
	FfiDestroyerBool{}.Destroy(r.Nut09Supported)
	FfiDestroyerBool{}.Destroy(r.Nut10Supported)
	FfiDestroyerBool{}.Destroy(r.Nut11Supported)
	FfiDestroyerBool{}.Destroy(r.Nut12Supported)
	FfiDestroyerBool{}.Destroy(r.Nut14Supported)
	FfiDestroyerBool{}.Destroy(r.Nut20Supported)
	FfiDestroyerOptionalClearAuthSettings{}.Destroy(r.Nut21)
	FfiDestroyerOptionalBlindAuthSettings{}.Destroy(r.Nut22)
	FfiDestroyerSequenceCurrencyUnit{}.Destroy(r.MintUnits)
	FfiDestroyerSequenceCurrencyUnit{}.Destroy(r.MeltUnits)
}

type FfiConverterNuts struct{}

var FfiConverterNutsINSTANCE = FfiConverterNuts{}

func (c FfiConverterNuts) Lift(rb RustBufferI) Nuts {
	return LiftFromRustBuffer[Nuts](c, rb)
}

func (c FfiConverterNuts) Read(reader io.Reader) Nuts {
	return Nuts{
		FfiConverterNut04SettingsINSTANCE.Read(reader),
		FfiConverterNut05SettingsINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterOptionalClearAuthSettingsINSTANCE.Read(reader),
		FfiConverterOptionalBlindAuthSettingsINSTANCE.Read(reader),
		FfiConverterSequenceCurrencyUnitINSTANCE.Read(reader),
		FfiConverterSequenceCurrencyUnitINSTANCE.Read(reader),
	}
}

func (c FfiConverterNuts) Lower(value Nuts) C.RustBuffer {
	return LowerIntoRustBuffer[Nuts](c, value)
}

func (c FfiConverterNuts) Write(writer io.Writer, value Nuts) {
	FfiConverterNut04SettingsINSTANCE.Write(writer, value.Nut04)
	FfiConverterNut05SettingsINSTANCE.Write(writer, value.Nut05)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut07Supported)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut08Supported)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut09Supported)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut10Supported)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut11Supported)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut12Supported)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut14Supported)
	FfiConverterBoolINSTANCE.Write(writer, value.Nut20Supported)
	FfiConverterOptionalClearAuthSettingsINSTANCE.Write(writer, value.Nut21)
	FfiConverterOptionalBlindAuthSettingsINSTANCE.Write(writer, value.Nut22)
	FfiConverterSequenceCurrencyUnitINSTANCE.Write(writer, value.MintUnits)
	FfiConverterSequenceCurrencyUnitINSTANCE.Write(writer, value.MeltUnits)
}

type FfiDestroyerNuts struct{}

func (_ FfiDestroyerNuts) Destroy(value Nuts) {
	value.Destroy()
}

// FFI-compatible DLEQ proof for proofs
type ProofDleq struct {
	// e value (hex-encoded SecretKey)
	E string
	// s value (hex-encoded SecretKey)
	S string
	// r value - blinding factor (hex-encoded SecretKey)
	R string
}

func (r *ProofDleq) Destroy() {
	FfiDestroyerString{}.Destroy(r.E)
	FfiDestroyerString{}.Destroy(r.S)
	FfiDestroyerString{}.Destroy(r.R)
}

type FfiConverterProofDleq struct{}

var FfiConverterProofDleqINSTANCE = FfiConverterProofDleq{}

func (c FfiConverterProofDleq) Lift(rb RustBufferI) ProofDleq {
	return LiftFromRustBuffer[ProofDleq](c, rb)
}

func (c FfiConverterProofDleq) Read(reader io.Reader) ProofDleq {
	return ProofDleq{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterProofDleq) Lower(value ProofDleq) C.RustBuffer {
	return LowerIntoRustBuffer[ProofDleq](c, value)
}

func (c FfiConverterProofDleq) Write(writer io.Writer, value ProofDleq) {
	FfiConverterStringINSTANCE.Write(writer, value.E)
	FfiConverterStringINSTANCE.Write(writer, value.S)
	FfiConverterStringINSTANCE.Write(writer, value.R)
}

type FfiDestroyerProofDleq struct{}

func (_ FfiDestroyerProofDleq) Destroy(value ProofDleq) {
	value.Destroy()
}

// FFI-compatible ProofInfo
type ProofInfo struct {
	// Proof
	Proof *Proof
	// Y value (hash_to_curve of secret)
	Y PublicKey
	// Mint URL
	MintUrl MintUrl
	// Proof state
	State ProofState
	// Proof Spending Conditions
	SpendingCondition *SpendingConditions
	// Currency unit
	Unit CurrencyUnit
}

func (r *ProofInfo) Destroy() {
	FfiDestroyerProof{}.Destroy(r.Proof)
	FfiDestroyerPublicKey{}.Destroy(r.Y)
	FfiDestroyerMintUrl{}.Destroy(r.MintUrl)
	FfiDestroyerProofState{}.Destroy(r.State)
	FfiDestroyerOptionalSpendingConditions{}.Destroy(r.SpendingCondition)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
}

type FfiConverterProofInfo struct{}

var FfiConverterProofInfoINSTANCE = FfiConverterProofInfo{}

func (c FfiConverterProofInfo) Lift(rb RustBufferI) ProofInfo {
	return LiftFromRustBuffer[ProofInfo](c, rb)
}

func (c FfiConverterProofInfo) Read(reader io.Reader) ProofInfo {
	return ProofInfo{
		FfiConverterProofINSTANCE.Read(reader),
		FfiConverterPublicKeyINSTANCE.Read(reader),
		FfiConverterMintUrlINSTANCE.Read(reader),
		FfiConverterProofStateINSTANCE.Read(reader),
		FfiConverterOptionalSpendingConditionsINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
	}
}

func (c FfiConverterProofInfo) Lower(value ProofInfo) C.RustBuffer {
	return LowerIntoRustBuffer[ProofInfo](c, value)
}

func (c FfiConverterProofInfo) Write(writer io.Writer, value ProofInfo) {
	FfiConverterProofINSTANCE.Write(writer, value.Proof)
	FfiConverterPublicKeyINSTANCE.Write(writer, value.Y)
	FfiConverterMintUrlINSTANCE.Write(writer, value.MintUrl)
	FfiConverterProofStateINSTANCE.Write(writer, value.State)
	FfiConverterOptionalSpendingConditionsINSTANCE.Write(writer, value.SpendingCondition)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
}

type FfiDestroyerProofInfo struct{}

func (_ FfiDestroyerProofInfo) Destroy(value ProofInfo) {
	value.Destroy()
}

// FFI-compatible ProofStateUpdate
type ProofStateUpdate struct {
	// Y value (hash_to_curve of secret)
	Y string
	// Current state
	State ProofState
	// Optional witness data
	Witness *string
}

func (r *ProofStateUpdate) Destroy() {
	FfiDestroyerString{}.Destroy(r.Y)
	FfiDestroyerProofState{}.Destroy(r.State)
	FfiDestroyerOptionalString{}.Destroy(r.Witness)
}

type FfiConverterProofStateUpdate struct{}

var FfiConverterProofStateUpdateINSTANCE = FfiConverterProofStateUpdate{}

func (c FfiConverterProofStateUpdate) Lift(rb RustBufferI) ProofStateUpdate {
	return LiftFromRustBuffer[ProofStateUpdate](c, rb)
}

func (c FfiConverterProofStateUpdate) Read(reader io.Reader) ProofStateUpdate {
	return ProofStateUpdate{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterProofStateINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterProofStateUpdate) Lower(value ProofStateUpdate) C.RustBuffer {
	return LowerIntoRustBuffer[ProofStateUpdate](c, value)
}

func (c FfiConverterProofStateUpdate) Write(writer io.Writer, value ProofStateUpdate) {
	FfiConverterStringINSTANCE.Write(writer, value.Y)
	FfiConverterProofStateINSTANCE.Write(writer, value.State)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Witness)
}

type FfiDestroyerProofStateUpdate struct{}

func (_ FfiDestroyerProofStateUpdate) Destroy(value ProofStateUpdate) {
	value.Destroy()
}

// FFI-compatible ProtectedEndpoint (for auth nuts)
type ProtectedEndpoint struct {
	// HTTP method (GET, POST, etc.)
	Method string
	// Endpoint path
	Path string
}

func (r *ProtectedEndpoint) Destroy() {
	FfiDestroyerString{}.Destroy(r.Method)
	FfiDestroyerString{}.Destroy(r.Path)
}

type FfiConverterProtectedEndpoint struct{}

var FfiConverterProtectedEndpointINSTANCE = FfiConverterProtectedEndpoint{}

func (c FfiConverterProtectedEndpoint) Lift(rb RustBufferI) ProtectedEndpoint {
	return LiftFromRustBuffer[ProtectedEndpoint](c, rb)
}

func (c FfiConverterProtectedEndpoint) Read(reader io.Reader) ProtectedEndpoint {
	return ProtectedEndpoint{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterProtectedEndpoint) Lower(value ProtectedEndpoint) C.RustBuffer {
	return LowerIntoRustBuffer[ProtectedEndpoint](c, value)
}

func (c FfiConverterProtectedEndpoint) Write(writer io.Writer, value ProtectedEndpoint) {
	FfiConverterStringINSTANCE.Write(writer, value.Method)
	FfiConverterStringINSTANCE.Write(writer, value.Path)
}

type FfiDestroyerProtectedEndpoint struct{}

func (_ FfiDestroyerProtectedEndpoint) Destroy(value ProtectedEndpoint) {
	value.Destroy()
}

// FFI-compatible PublicKey
type PublicKey struct {
	// Hex-encoded public key
	Hex string
}

func (r *PublicKey) Destroy() {
	FfiDestroyerString{}.Destroy(r.Hex)
}

type FfiConverterPublicKey struct{}

var FfiConverterPublicKeyINSTANCE = FfiConverterPublicKey{}

func (c FfiConverterPublicKey) Lift(rb RustBufferI) PublicKey {
	return LiftFromRustBuffer[PublicKey](c, rb)
}

func (c FfiConverterPublicKey) Read(reader io.Reader) PublicKey {
	return PublicKey{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterPublicKey) Lower(value PublicKey) C.RustBuffer {
	return LowerIntoRustBuffer[PublicKey](c, value)
}

func (c FfiConverterPublicKey) Write(writer io.Writer, value PublicKey) {
	FfiConverterStringINSTANCE.Write(writer, value.Hex)
}

type FfiDestroyerPublicKey struct{}

func (_ FfiDestroyerPublicKey) Destroy(value PublicKey) {
	value.Destroy()
}

// FFI-compatible Receive options
type ReceiveOptions struct {
	// Amount split target
	AmountSplitTarget SplitTarget
	// P2PK signing keys
	P2pkSigningKeys []SecretKey
	// Preimages for HTLC conditions
	Preimages []string
	// Metadata
	Metadata map[string]string
}

func (r *ReceiveOptions) Destroy() {
	FfiDestroyerSplitTarget{}.Destroy(r.AmountSplitTarget)
	FfiDestroyerSequenceSecretKey{}.Destroy(r.P2pkSigningKeys)
	FfiDestroyerSequenceString{}.Destroy(r.Preimages)
	FfiDestroyerMapStringString{}.Destroy(r.Metadata)
}

type FfiConverterReceiveOptions struct{}

var FfiConverterReceiveOptionsINSTANCE = FfiConverterReceiveOptions{}

func (c FfiConverterReceiveOptions) Lift(rb RustBufferI) ReceiveOptions {
	return LiftFromRustBuffer[ReceiveOptions](c, rb)
}

func (c FfiConverterReceiveOptions) Read(reader io.Reader) ReceiveOptions {
	return ReceiveOptions{
		FfiConverterSplitTargetINSTANCE.Read(reader),
		FfiConverterSequenceSecretKeyINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
		FfiConverterMapStringStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterReceiveOptions) Lower(value ReceiveOptions) C.RustBuffer {
	return LowerIntoRustBuffer[ReceiveOptions](c, value)
}

func (c FfiConverterReceiveOptions) Write(writer io.Writer, value ReceiveOptions) {
	FfiConverterSplitTargetINSTANCE.Write(writer, value.AmountSplitTarget)
	FfiConverterSequenceSecretKeyINSTANCE.Write(writer, value.P2pkSigningKeys)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.Preimages)
	FfiConverterMapStringStringINSTANCE.Write(writer, value.Metadata)
}

type FfiDestroyerReceiveOptions struct{}

func (_ FfiDestroyerReceiveOptions) Destroy(value ReceiveOptions) {
	value.Destroy()
}

// FFI-compatible SecretKey
type SecretKey struct {
	// Hex-encoded secret key (64 characters)
	Hex string
}

func (r *SecretKey) Destroy() {
	FfiDestroyerString{}.Destroy(r.Hex)
}

type FfiConverterSecretKey struct{}

var FfiConverterSecretKeyINSTANCE = FfiConverterSecretKey{}

func (c FfiConverterSecretKey) Lift(rb RustBufferI) SecretKey {
	return LiftFromRustBuffer[SecretKey](c, rb)
}

func (c FfiConverterSecretKey) Read(reader io.Reader) SecretKey {
	return SecretKey{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterSecretKey) Lower(value SecretKey) C.RustBuffer {
	return LowerIntoRustBuffer[SecretKey](c, value)
}

func (c FfiConverterSecretKey) Write(writer io.Writer, value SecretKey) {
	FfiConverterStringINSTANCE.Write(writer, value.Hex)
}

type FfiDestroyerSecretKey struct{}

func (_ FfiDestroyerSecretKey) Destroy(value SecretKey) {
	value.Destroy()
}

// FFI-compatible SendMemo
type SendMemo struct {
	// Memo text
	Memo string
	// Include memo in token
	IncludeMemo bool
}

func (r *SendMemo) Destroy() {
	FfiDestroyerString{}.Destroy(r.Memo)
	FfiDestroyerBool{}.Destroy(r.IncludeMemo)
}

type FfiConverterSendMemo struct{}

var FfiConverterSendMemoINSTANCE = FfiConverterSendMemo{}

func (c FfiConverterSendMemo) Lift(rb RustBufferI) SendMemo {
	return LiftFromRustBuffer[SendMemo](c, rb)
}

func (c FfiConverterSendMemo) Read(reader io.Reader) SendMemo {
	return SendMemo{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterSendMemo) Lower(value SendMemo) C.RustBuffer {
	return LowerIntoRustBuffer[SendMemo](c, value)
}

func (c FfiConverterSendMemo) Write(writer io.Writer, value SendMemo) {
	FfiConverterStringINSTANCE.Write(writer, value.Memo)
	FfiConverterBoolINSTANCE.Write(writer, value.IncludeMemo)
}

type FfiDestroyerSendMemo struct{}

func (_ FfiDestroyerSendMemo) Destroy(value SendMemo) {
	value.Destroy()
}

// FFI-compatible Send options
type SendOptions struct {
	// Memo
	Memo *SendMemo
	// Spending conditions
	Conditions *SpendingConditions
	// Amount split target
	AmountSplitTarget SplitTarget
	// Send kind
	SendKind SendKind
	// Include fee
	IncludeFee bool
	// Maximum number of proofs to include in the token
	MaxProofs *uint32
	// Metadata
	Metadata map[string]string
}

func (r *SendOptions) Destroy() {
	FfiDestroyerOptionalSendMemo{}.Destroy(r.Memo)
	FfiDestroyerOptionalSpendingConditions{}.Destroy(r.Conditions)
	FfiDestroyerSplitTarget{}.Destroy(r.AmountSplitTarget)
	FfiDestroyerSendKind{}.Destroy(r.SendKind)
	FfiDestroyerBool{}.Destroy(r.IncludeFee)
	FfiDestroyerOptionalUint32{}.Destroy(r.MaxProofs)
	FfiDestroyerMapStringString{}.Destroy(r.Metadata)
}

type FfiConverterSendOptions struct{}

var FfiConverterSendOptionsINSTANCE = FfiConverterSendOptions{}

func (c FfiConverterSendOptions) Lift(rb RustBufferI) SendOptions {
	return LiftFromRustBuffer[SendOptions](c, rb)
}

func (c FfiConverterSendOptions) Read(reader io.Reader) SendOptions {
	return SendOptions{
		FfiConverterOptionalSendMemoINSTANCE.Read(reader),
		FfiConverterOptionalSpendingConditionsINSTANCE.Read(reader),
		FfiConverterSplitTargetINSTANCE.Read(reader),
		FfiConverterSendKindINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterOptionalUint32INSTANCE.Read(reader),
		FfiConverterMapStringStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterSendOptions) Lower(value SendOptions) C.RustBuffer {
	return LowerIntoRustBuffer[SendOptions](c, value)
}

func (c FfiConverterSendOptions) Write(writer io.Writer, value SendOptions) {
	FfiConverterOptionalSendMemoINSTANCE.Write(writer, value.Memo)
	FfiConverterOptionalSpendingConditionsINSTANCE.Write(writer, value.Conditions)
	FfiConverterSplitTargetINSTANCE.Write(writer, value.AmountSplitTarget)
	FfiConverterSendKindINSTANCE.Write(writer, value.SendKind)
	FfiConverterBoolINSTANCE.Write(writer, value.IncludeFee)
	FfiConverterOptionalUint32INSTANCE.Write(writer, value.MaxProofs)
	FfiConverterMapStringStringINSTANCE.Write(writer, value.Metadata)
}

type FfiDestroyerSendOptions struct{}

func (_ FfiDestroyerSendOptions) Destroy(value SendOptions) {
	value.Destroy()
}

// FFI-compatible SubscribeParams
type SubscribeParams struct {
	// Subscription kind
	Kind SubscriptionKind
	// Filters
	Filters []string
	// Subscription ID (optional, will be generated if not provided)
	Id *string
}

func (r *SubscribeParams) Destroy() {
	FfiDestroyerSubscriptionKind{}.Destroy(r.Kind)
	FfiDestroyerSequenceString{}.Destroy(r.Filters)
	FfiDestroyerOptionalString{}.Destroy(r.Id)
}

type FfiConverterSubscribeParams struct{}

var FfiConverterSubscribeParamsINSTANCE = FfiConverterSubscribeParams{}

func (c FfiConverterSubscribeParams) Lift(rb RustBufferI) SubscribeParams {
	return LiftFromRustBuffer[SubscribeParams](c, rb)
}

func (c FfiConverterSubscribeParams) Read(reader io.Reader) SubscribeParams {
	return SubscribeParams{
		FfiConverterSubscriptionKindINSTANCE.Read(reader),
		FfiConverterSequenceStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterSubscribeParams) Lower(value SubscribeParams) C.RustBuffer {
	return LowerIntoRustBuffer[SubscribeParams](c, value)
}

func (c FfiConverterSubscribeParams) Write(writer io.Writer, value SubscribeParams) {
	FfiConverterSubscriptionKindINSTANCE.Write(writer, value.Kind)
	FfiConverterSequenceStringINSTANCE.Write(writer, value.Filters)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Id)
}

type FfiDestroyerSubscribeParams struct{}

func (_ FfiDestroyerSubscribeParams) Destroy(value SubscribeParams) {
	value.Destroy()
}

// FFI-compatible SupportedSettings
type SupportedSettings struct {
	// Setting supported
	Supported bool
}

func (r *SupportedSettings) Destroy() {
	FfiDestroyerBool{}.Destroy(r.Supported)
}

type FfiConverterSupportedSettings struct{}

var FfiConverterSupportedSettingsINSTANCE = FfiConverterSupportedSettings{}

func (c FfiConverterSupportedSettings) Lift(rb RustBufferI) SupportedSettings {
	return LiftFromRustBuffer[SupportedSettings](c, rb)
}

func (c FfiConverterSupportedSettings) Read(reader io.Reader) SupportedSettings {
	return SupportedSettings{
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterSupportedSettings) Lower(value SupportedSettings) C.RustBuffer {
	return LowerIntoRustBuffer[SupportedSettings](c, value)
}

func (c FfiConverterSupportedSettings) Write(writer io.Writer, value SupportedSettings) {
	FfiConverterBoolINSTANCE.Write(writer, value.Supported)
}

type FfiDestroyerSupportedSettings struct{}

func (_ FfiDestroyerSupportedSettings) Destroy(value SupportedSettings) {
	value.Destroy()
}

// FFI-compatible Transaction
type Transaction struct {
	// Transaction ID
	Id TransactionId
	// Mint URL
	MintUrl MintUrl
	// Transaction direction
	Direction TransactionDirection
	// Amount
	Amount Amount
	// Fee
	Fee Amount
	// Currency Unit
	Unit CurrencyUnit
	// Proof Ys (Y values from proofs)
	Ys []PublicKey
	// Unix timestamp
	Timestamp uint64
	// Memo
	Memo *string
	// User-defined metadata
	Metadata map[string]string
	// Quote ID if this is a mint or melt transaction
	QuoteId *string
	// Payment request (e.g., BOLT11 invoice, BOLT12 offer)
	PaymentRequest *string
	// Payment proof (e.g., preimage for Lightning melt transactions)
	PaymentProof *string
}

func (r *Transaction) Destroy() {
	FfiDestroyerTransactionId{}.Destroy(r.Id)
	FfiDestroyerMintUrl{}.Destroy(r.MintUrl)
	FfiDestroyerTransactionDirection{}.Destroy(r.Direction)
	FfiDestroyerAmount{}.Destroy(r.Amount)
	FfiDestroyerAmount{}.Destroy(r.Fee)
	FfiDestroyerCurrencyUnit{}.Destroy(r.Unit)
	FfiDestroyerSequencePublicKey{}.Destroy(r.Ys)
	FfiDestroyerUint64{}.Destroy(r.Timestamp)
	FfiDestroyerOptionalString{}.Destroy(r.Memo)
	FfiDestroyerMapStringString{}.Destroy(r.Metadata)
	FfiDestroyerOptionalString{}.Destroy(r.QuoteId)
	FfiDestroyerOptionalString{}.Destroy(r.PaymentRequest)
	FfiDestroyerOptionalString{}.Destroy(r.PaymentProof)
}

type FfiConverterTransaction struct{}

var FfiConverterTransactionINSTANCE = FfiConverterTransaction{}

func (c FfiConverterTransaction) Lift(rb RustBufferI) Transaction {
	return LiftFromRustBuffer[Transaction](c, rb)
}

func (c FfiConverterTransaction) Read(reader io.Reader) Transaction {
	return Transaction{
		FfiConverterTransactionIdINSTANCE.Read(reader),
		FfiConverterMintUrlINSTANCE.Read(reader),
		FfiConverterTransactionDirectionINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterCurrencyUnitINSTANCE.Read(reader),
		FfiConverterSequencePublicKeyINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterMapStringStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterTransaction) Lower(value Transaction) C.RustBuffer {
	return LowerIntoRustBuffer[Transaction](c, value)
}

func (c FfiConverterTransaction) Write(writer io.Writer, value Transaction) {
	FfiConverterTransactionIdINSTANCE.Write(writer, value.Id)
	FfiConverterMintUrlINSTANCE.Write(writer, value.MintUrl)
	FfiConverterTransactionDirectionINSTANCE.Write(writer, value.Direction)
	FfiConverterAmountINSTANCE.Write(writer, value.Amount)
	FfiConverterAmountINSTANCE.Write(writer, value.Fee)
	FfiConverterCurrencyUnitINSTANCE.Write(writer, value.Unit)
	FfiConverterSequencePublicKeyINSTANCE.Write(writer, value.Ys)
	FfiConverterUint64INSTANCE.Write(writer, value.Timestamp)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Memo)
	FfiConverterMapStringStringINSTANCE.Write(writer, value.Metadata)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.QuoteId)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.PaymentRequest)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.PaymentProof)
}

type FfiDestroyerTransaction struct{}

func (_ FfiDestroyerTransaction) Destroy(value Transaction) {
	value.Destroy()
}

// FFI-compatible TransactionId
type TransactionId struct {
	// Hex-encoded transaction ID (64 characters)
	Hex string
}

func (r *TransactionId) Destroy() {
	FfiDestroyerString{}.Destroy(r.Hex)
}

type FfiConverterTransactionId struct{}

var FfiConverterTransactionIdINSTANCE = FfiConverterTransactionId{}

func (c FfiConverterTransactionId) Lift(rb RustBufferI) TransactionId {
	return LiftFromRustBuffer[TransactionId](c, rb)
}

func (c FfiConverterTransactionId) Read(reader io.Reader) TransactionId {
	return TransactionId{
		FfiConverterStringINSTANCE.Read(reader),
	}
}

func (c FfiConverterTransactionId) Lower(value TransactionId) C.RustBuffer {
	return LowerIntoRustBuffer[TransactionId](c, value)
}

func (c FfiConverterTransactionId) Write(writer io.Writer, value TransactionId) {
	FfiConverterStringINSTANCE.Write(writer, value.Hex)
}

type FfiDestroyerTransactionId struct{}

func (_ FfiDestroyerTransactionId) Destroy(value TransactionId) {
	value.Destroy()
}

// Result of a transfer operation with detailed breakdown
type TransferResult struct {
	// Amount deducted from source mint
	AmountSent Amount
	// Amount received at target mint
	AmountReceived Amount
	// Total fees paid for the transfer
	FeesPaid Amount
	// Remaining balance in source mint after transfer
	SourceBalanceAfter Amount
	// New balance in target mint after transfer
	TargetBalanceAfter Amount
}

func (r *TransferResult) Destroy() {
	FfiDestroyerAmount{}.Destroy(r.AmountSent)
	FfiDestroyerAmount{}.Destroy(r.AmountReceived)
	FfiDestroyerAmount{}.Destroy(r.FeesPaid)
	FfiDestroyerAmount{}.Destroy(r.SourceBalanceAfter)
	FfiDestroyerAmount{}.Destroy(r.TargetBalanceAfter)
}

type FfiConverterTransferResult struct{}

var FfiConverterTransferResultINSTANCE = FfiConverterTransferResult{}

func (c FfiConverterTransferResult) Lift(rb RustBufferI) TransferResult {
	return LiftFromRustBuffer[TransferResult](c, rb)
}

func (c FfiConverterTransferResult) Read(reader io.Reader) TransferResult {
	return TransferResult{
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
		FfiConverterAmountINSTANCE.Read(reader),
	}
}

func (c FfiConverterTransferResult) Lower(value TransferResult) C.RustBuffer {
	return LowerIntoRustBuffer[TransferResult](c, value)
}

func (c FfiConverterTransferResult) Write(writer io.Writer, value TransferResult) {
	FfiConverterAmountINSTANCE.Write(writer, value.AmountSent)
	FfiConverterAmountINSTANCE.Write(writer, value.AmountReceived)
	FfiConverterAmountINSTANCE.Write(writer, value.FeesPaid)
	FfiConverterAmountINSTANCE.Write(writer, value.SourceBalanceAfter)
	FfiConverterAmountINSTANCE.Write(writer, value.TargetBalanceAfter)
}

type FfiDestroyerTransferResult struct{}

func (_ FfiDestroyerTransferResult) Destroy(value TransferResult) {
	value.Destroy()
}

// Configuration for creating wallets
type WalletConfig struct {
	TargetProofCount *uint32
}

func (r *WalletConfig) Destroy() {
	FfiDestroyerOptionalUint32{}.Destroy(r.TargetProofCount)
}

type FfiConverterWalletConfig struct{}

var FfiConverterWalletConfigINSTANCE = FfiConverterWalletConfig{}

func (c FfiConverterWalletConfig) Lift(rb RustBufferI) WalletConfig {
	return LiftFromRustBuffer[WalletConfig](c, rb)
}

func (c FfiConverterWalletConfig) Read(reader io.Reader) WalletConfig {
	return WalletConfig{
		FfiConverterOptionalUint32INSTANCE.Read(reader),
	}
}

func (c FfiConverterWalletConfig) Lower(value WalletConfig) C.RustBuffer {
	return LowerIntoRustBuffer[WalletConfig](c, value)
}

func (c FfiConverterWalletConfig) Write(writer io.Writer, value WalletConfig) {
	FfiConverterOptionalUint32INSTANCE.Write(writer, value.TargetProofCount)
}

type FfiDestroyerWalletConfig struct{}

func (_ FfiDestroyerWalletConfig) Destroy(value WalletConfig) {
	value.Destroy()
}

// FFI-compatible Currency Unit
type CurrencyUnit interface {
	Destroy()
}
type CurrencyUnitSat struct {
}

func (e CurrencyUnitSat) Destroy() {
}

type CurrencyUnitMsat struct {
}

func (e CurrencyUnitMsat) Destroy() {
}

type CurrencyUnitUsd struct {
}

func (e CurrencyUnitUsd) Destroy() {
}

type CurrencyUnitEur struct {
}

func (e CurrencyUnitEur) Destroy() {
}

type CurrencyUnitAuth struct {
}

func (e CurrencyUnitAuth) Destroy() {
}

type CurrencyUnitCustom struct {
	Unit string
}

func (e CurrencyUnitCustom) Destroy() {
	FfiDestroyerString{}.Destroy(e.Unit)
}

type FfiConverterCurrencyUnit struct{}

var FfiConverterCurrencyUnitINSTANCE = FfiConverterCurrencyUnit{}

func (c FfiConverterCurrencyUnit) Lift(rb RustBufferI) CurrencyUnit {
	return LiftFromRustBuffer[CurrencyUnit](c, rb)
}

func (c FfiConverterCurrencyUnit) Lower(value CurrencyUnit) C.RustBuffer {
	return LowerIntoRustBuffer[CurrencyUnit](c, value)
}
func (FfiConverterCurrencyUnit) Read(reader io.Reader) CurrencyUnit {
	id := readInt32(reader)
	switch id {
	case 1:
		return CurrencyUnitSat{}
	case 2:
		return CurrencyUnitMsat{}
	case 3:
		return CurrencyUnitUsd{}
	case 4:
		return CurrencyUnitEur{}
	case 5:
		return CurrencyUnitAuth{}
	case 6:
		return CurrencyUnitCustom{
			FfiConverterStringINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterCurrencyUnit.Read()", id))
	}
}

func (FfiConverterCurrencyUnit) Write(writer io.Writer, value CurrencyUnit) {
	switch variant_value := value.(type) {
	case CurrencyUnitSat:
		writeInt32(writer, 1)
	case CurrencyUnitMsat:
		writeInt32(writer, 2)
	case CurrencyUnitUsd:
		writeInt32(writer, 3)
	case CurrencyUnitEur:
		writeInt32(writer, 4)
	case CurrencyUnitAuth:
		writeInt32(writer, 5)
	case CurrencyUnitCustom:
		writeInt32(writer, 6)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Unit)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterCurrencyUnit.Write", value))
	}
}

type FfiDestroyerCurrencyUnit struct{}

func (_ FfiDestroyerCurrencyUnit) Destroy(value CurrencyUnit) {
	value.Destroy()
}

// FFI Error type that wraps CDK errors for cross-language use
type FfiError struct {
	err error
}

// Convience method to turn *FfiError into error
// Avoiding treating nil pointer as non nil error interface
func (err *FfiError) AsError() error {
	if err == nil {
		return nil
	} else {
		return err
	}
}

func (err FfiError) Error() string {
	return fmt.Sprintf("FfiError: %s", err.err.Error())
}

func (err FfiError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var ErrFfiErrorGeneric = fmt.Errorf("FfiErrorGeneric")
var ErrFfiErrorAmountOverflow = fmt.Errorf("FfiErrorAmountOverflow")
var ErrFfiErrorDivisionByZero = fmt.Errorf("FfiErrorDivisionByZero")
var ErrFfiErrorAmount = fmt.Errorf("FfiErrorAmount")
var ErrFfiErrorPaymentFailed = fmt.Errorf("FfiErrorPaymentFailed")
var ErrFfiErrorPaymentPending = fmt.Errorf("FfiErrorPaymentPending")
var ErrFfiErrorInsufficientFunds = fmt.Errorf("FfiErrorInsufficientFunds")
var ErrFfiErrorDatabase = fmt.Errorf("FfiErrorDatabase")
var ErrFfiErrorNetwork = fmt.Errorf("FfiErrorNetwork")
var ErrFfiErrorInvalidToken = fmt.Errorf("FfiErrorInvalidToken")
var ErrFfiErrorWallet = fmt.Errorf("FfiErrorWallet")
var ErrFfiErrorKeysetUnknown = fmt.Errorf("FfiErrorKeysetUnknown")
var ErrFfiErrorUnitNotSupported = fmt.Errorf("FfiErrorUnitNotSupported")
var ErrFfiErrorRuntimeTaskJoin = fmt.Errorf("FfiErrorRuntimeTaskJoin")
var ErrFfiErrorInvalidMnemonic = fmt.Errorf("FfiErrorInvalidMnemonic")
var ErrFfiErrorInvalidUrl = fmt.Errorf("FfiErrorInvalidUrl")
var ErrFfiErrorInvalidHex = fmt.Errorf("FfiErrorInvalidHex")
var ErrFfiErrorInvalidCryptographicKey = fmt.Errorf("FfiErrorInvalidCryptographicKey")
var ErrFfiErrorSerialization = fmt.Errorf("FfiErrorSerialization")

// Variant structs
// Generic error with message
type FfiErrorGeneric struct {
	message string
}

// Generic error with message
func NewFfiErrorGeneric() *FfiError {
	return &FfiError{err: &FfiErrorGeneric{}}
}

func (e FfiErrorGeneric) destroy() {
}

func (err FfiErrorGeneric) Error() string {
	return fmt.Sprintf("Generic: %s", err.message)
}

func (self FfiErrorGeneric) Is(target error) bool {
	return target == ErrFfiErrorGeneric
}

// Amount overflow
type FfiErrorAmountOverflow struct {
	message string
}

// Amount overflow
func NewFfiErrorAmountOverflow() *FfiError {
	return &FfiError{err: &FfiErrorAmountOverflow{}}
}

func (e FfiErrorAmountOverflow) destroy() {
}

func (err FfiErrorAmountOverflow) Error() string {
	return fmt.Sprintf("AmountOverflow: %s", err.message)
}

func (self FfiErrorAmountOverflow) Is(target error) bool {
	return target == ErrFfiErrorAmountOverflow
}

// Division by zero
type FfiErrorDivisionByZero struct {
	message string
}

// Division by zero
func NewFfiErrorDivisionByZero() *FfiError {
	return &FfiError{err: &FfiErrorDivisionByZero{}}
}

func (e FfiErrorDivisionByZero) destroy() {
}

func (err FfiErrorDivisionByZero) Error() string {
	return fmt.Sprintf("DivisionByZero: %s", err.message)
}

func (self FfiErrorDivisionByZero) Is(target error) bool {
	return target == ErrFfiErrorDivisionByZero
}

// Amount error
type FfiErrorAmount struct {
	message string
}

// Amount error
func NewFfiErrorAmount() *FfiError {
	return &FfiError{err: &FfiErrorAmount{}}
}

func (e FfiErrorAmount) destroy() {
}

func (err FfiErrorAmount) Error() string {
	return fmt.Sprintf("Amount: %s", err.message)
}

func (self FfiErrorAmount) Is(target error) bool {
	return target == ErrFfiErrorAmount
}

// Payment failed
type FfiErrorPaymentFailed struct {
	message string
}

// Payment failed
func NewFfiErrorPaymentFailed() *FfiError {
	return &FfiError{err: &FfiErrorPaymentFailed{}}
}

func (e FfiErrorPaymentFailed) destroy() {
}

func (err FfiErrorPaymentFailed) Error() string {
	return fmt.Sprintf("PaymentFailed: %s", err.message)
}

func (self FfiErrorPaymentFailed) Is(target error) bool {
	return target == ErrFfiErrorPaymentFailed
}

// Payment pending
type FfiErrorPaymentPending struct {
	message string
}

// Payment pending
func NewFfiErrorPaymentPending() *FfiError {
	return &FfiError{err: &FfiErrorPaymentPending{}}
}

func (e FfiErrorPaymentPending) destroy() {
}

func (err FfiErrorPaymentPending) Error() string {
	return fmt.Sprintf("PaymentPending: %s", err.message)
}

func (self FfiErrorPaymentPending) Is(target error) bool {
	return target == ErrFfiErrorPaymentPending
}

// Insufficient funds
type FfiErrorInsufficientFunds struct {
	message string
}

// Insufficient funds
func NewFfiErrorInsufficientFunds() *FfiError {
	return &FfiError{err: &FfiErrorInsufficientFunds{}}
}

func (e FfiErrorInsufficientFunds) destroy() {
}

func (err FfiErrorInsufficientFunds) Error() string {
	return fmt.Sprintf("InsufficientFunds: %s", err.message)
}

func (self FfiErrorInsufficientFunds) Is(target error) bool {
	return target == ErrFfiErrorInsufficientFunds
}

// Database error
type FfiErrorDatabase struct {
	message string
}

// Database error
func NewFfiErrorDatabase() *FfiError {
	return &FfiError{err: &FfiErrorDatabase{}}
}

func (e FfiErrorDatabase) destroy() {
}

func (err FfiErrorDatabase) Error() string {
	return fmt.Sprintf("Database: %s", err.message)
}

func (self FfiErrorDatabase) Is(target error) bool {
	return target == ErrFfiErrorDatabase
}

// Network error
type FfiErrorNetwork struct {
	message string
}

// Network error
func NewFfiErrorNetwork() *FfiError {
	return &FfiError{err: &FfiErrorNetwork{}}
}

func (e FfiErrorNetwork) destroy() {
}

func (err FfiErrorNetwork) Error() string {
	return fmt.Sprintf("Network: %s", err.message)
}

func (self FfiErrorNetwork) Is(target error) bool {
	return target == ErrFfiErrorNetwork
}

// Invalid token
type FfiErrorInvalidToken struct {
	message string
}

// Invalid token
func NewFfiErrorInvalidToken() *FfiError {
	return &FfiError{err: &FfiErrorInvalidToken{}}
}

func (e FfiErrorInvalidToken) destroy() {
}

func (err FfiErrorInvalidToken) Error() string {
	return fmt.Sprintf("InvalidToken: %s", err.message)
}

func (self FfiErrorInvalidToken) Is(target error) bool {
	return target == ErrFfiErrorInvalidToken
}

// Wallet error
type FfiErrorWallet struct {
	message string
}

// Wallet error
func NewFfiErrorWallet() *FfiError {
	return &FfiError{err: &FfiErrorWallet{}}
}

func (e FfiErrorWallet) destroy() {
}

func (err FfiErrorWallet) Error() string {
	return fmt.Sprintf("Wallet: %s", err.message)
}

func (self FfiErrorWallet) Is(target error) bool {
	return target == ErrFfiErrorWallet
}

// Keyset unknown
type FfiErrorKeysetUnknown struct {
	message string
}

// Keyset unknown
func NewFfiErrorKeysetUnknown() *FfiError {
	return &FfiError{err: &FfiErrorKeysetUnknown{}}
}

func (e FfiErrorKeysetUnknown) destroy() {
}

func (err FfiErrorKeysetUnknown) Error() string {
	return fmt.Sprintf("KeysetUnknown: %s", err.message)
}

func (self FfiErrorKeysetUnknown) Is(target error) bool {
	return target == ErrFfiErrorKeysetUnknown
}

// Unit not supported
type FfiErrorUnitNotSupported struct {
	message string
}

// Unit not supported
func NewFfiErrorUnitNotSupported() *FfiError {
	return &FfiError{err: &FfiErrorUnitNotSupported{}}
}

func (e FfiErrorUnitNotSupported) destroy() {
}

func (err FfiErrorUnitNotSupported) Error() string {
	return fmt.Sprintf("UnitNotSupported: %s", err.message)
}

func (self FfiErrorUnitNotSupported) Is(target error) bool {
	return target == ErrFfiErrorUnitNotSupported
}

// Runtime task join error
type FfiErrorRuntimeTaskJoin struct {
	message string
}

// Runtime task join error
func NewFfiErrorRuntimeTaskJoin() *FfiError {
	return &FfiError{err: &FfiErrorRuntimeTaskJoin{}}
}

func (e FfiErrorRuntimeTaskJoin) destroy() {
}

func (err FfiErrorRuntimeTaskJoin) Error() string {
	return fmt.Sprintf("RuntimeTaskJoin: %s", err.message)
}

func (self FfiErrorRuntimeTaskJoin) Is(target error) bool {
	return target == ErrFfiErrorRuntimeTaskJoin
}

// Invalid mnemonic phrase
type FfiErrorInvalidMnemonic struct {
	message string
}

// Invalid mnemonic phrase
func NewFfiErrorInvalidMnemonic() *FfiError {
	return &FfiError{err: &FfiErrorInvalidMnemonic{}}
}

func (e FfiErrorInvalidMnemonic) destroy() {
}

func (err FfiErrorInvalidMnemonic) Error() string {
	return fmt.Sprintf("InvalidMnemonic: %s", err.message)
}

func (self FfiErrorInvalidMnemonic) Is(target error) bool {
	return target == ErrFfiErrorInvalidMnemonic
}

// URL parsing error
type FfiErrorInvalidUrl struct {
	message string
}

// URL parsing error
func NewFfiErrorInvalidUrl() *FfiError {
	return &FfiError{err: &FfiErrorInvalidUrl{}}
}

func (e FfiErrorInvalidUrl) destroy() {
}

func (err FfiErrorInvalidUrl) Error() string {
	return fmt.Sprintf("InvalidUrl: %s", err.message)
}

func (self FfiErrorInvalidUrl) Is(target error) bool {
	return target == ErrFfiErrorInvalidUrl
}

// Hex format error
type FfiErrorInvalidHex struct {
	message string
}

// Hex format error
func NewFfiErrorInvalidHex() *FfiError {
	return &FfiError{err: &FfiErrorInvalidHex{}}
}

func (e FfiErrorInvalidHex) destroy() {
}

func (err FfiErrorInvalidHex) Error() string {
	return fmt.Sprintf("InvalidHex: %s", err.message)
}

func (self FfiErrorInvalidHex) Is(target error) bool {
	return target == ErrFfiErrorInvalidHex
}

// Cryptographic key parsing error
type FfiErrorInvalidCryptographicKey struct {
	message string
}

// Cryptographic key parsing error
func NewFfiErrorInvalidCryptographicKey() *FfiError {
	return &FfiError{err: &FfiErrorInvalidCryptographicKey{}}
}

func (e FfiErrorInvalidCryptographicKey) destroy() {
}

func (err FfiErrorInvalidCryptographicKey) Error() string {
	return fmt.Sprintf("InvalidCryptographicKey: %s", err.message)
}

func (self FfiErrorInvalidCryptographicKey) Is(target error) bool {
	return target == ErrFfiErrorInvalidCryptographicKey
}

// Serialization/deserialization error
type FfiErrorSerialization struct {
	message string
}

// Serialization/deserialization error
func NewFfiErrorSerialization() *FfiError {
	return &FfiError{err: &FfiErrorSerialization{}}
}

func (e FfiErrorSerialization) destroy() {
}

func (err FfiErrorSerialization) Error() string {
	return fmt.Sprintf("Serialization: %s", err.message)
}

func (self FfiErrorSerialization) Is(target error) bool {
	return target == ErrFfiErrorSerialization
}

type FfiConverterFfiError struct{}

var FfiConverterFfiErrorINSTANCE = FfiConverterFfiError{}

func (c FfiConverterFfiError) Lift(eb RustBufferI) *FfiError {
	return LiftFromRustBuffer[*FfiError](c, eb)
}

func (c FfiConverterFfiError) Lower(value *FfiError) C.RustBuffer {
	return LowerIntoRustBuffer[*FfiError](c, value)
}

func (c FfiConverterFfiError) Read(reader io.Reader) *FfiError {
	errorID := readUint32(reader)

	message := FfiConverterStringINSTANCE.Read(reader)
	switch errorID {
	case 1:
		return &FfiError{&FfiErrorGeneric{message}}
	case 2:
		return &FfiError{&FfiErrorAmountOverflow{message}}
	case 3:
		return &FfiError{&FfiErrorDivisionByZero{message}}
	case 4:
		return &FfiError{&FfiErrorAmount{message}}
	case 5:
		return &FfiError{&FfiErrorPaymentFailed{message}}
	case 6:
		return &FfiError{&FfiErrorPaymentPending{message}}
	case 7:
		return &FfiError{&FfiErrorInsufficientFunds{message}}
	case 8:
		return &FfiError{&FfiErrorDatabase{message}}
	case 9:
		return &FfiError{&FfiErrorNetwork{message}}
	case 10:
		return &FfiError{&FfiErrorInvalidToken{message}}
	case 11:
		return &FfiError{&FfiErrorWallet{message}}
	case 12:
		return &FfiError{&FfiErrorKeysetUnknown{message}}
	case 13:
		return &FfiError{&FfiErrorUnitNotSupported{message}}
	case 14:
		return &FfiError{&FfiErrorRuntimeTaskJoin{message}}
	case 15:
		return &FfiError{&FfiErrorInvalidMnemonic{message}}
	case 16:
		return &FfiError{&FfiErrorInvalidUrl{message}}
	case 17:
		return &FfiError{&FfiErrorInvalidHex{message}}
	case 18:
		return &FfiError{&FfiErrorInvalidCryptographicKey{message}}
	case 19:
		return &FfiError{&FfiErrorSerialization{message}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterFfiError.Read()", errorID))
	}

}

func (c FfiConverterFfiError) Write(writer io.Writer, value *FfiError) {
	switch variantValue := value.err.(type) {
	case *FfiErrorGeneric:
		writeInt32(writer, 1)
	case *FfiErrorAmountOverflow:
		writeInt32(writer, 2)
	case *FfiErrorDivisionByZero:
		writeInt32(writer, 3)
	case *FfiErrorAmount:
		writeInt32(writer, 4)
	case *FfiErrorPaymentFailed:
		writeInt32(writer, 5)
	case *FfiErrorPaymentPending:
		writeInt32(writer, 6)
	case *FfiErrorInsufficientFunds:
		writeInt32(writer, 7)
	case *FfiErrorDatabase:
		writeInt32(writer, 8)
	case *FfiErrorNetwork:
		writeInt32(writer, 9)
	case *FfiErrorInvalidToken:
		writeInt32(writer, 10)
	case *FfiErrorWallet:
		writeInt32(writer, 11)
	case *FfiErrorKeysetUnknown:
		writeInt32(writer, 12)
	case *FfiErrorUnitNotSupported:
		writeInt32(writer, 13)
	case *FfiErrorRuntimeTaskJoin:
		writeInt32(writer, 14)
	case *FfiErrorInvalidMnemonic:
		writeInt32(writer, 15)
	case *FfiErrorInvalidUrl:
		writeInt32(writer, 16)
	case *FfiErrorInvalidHex:
		writeInt32(writer, 17)
	case *FfiErrorInvalidCryptographicKey:
		writeInt32(writer, 18)
	case *FfiErrorSerialization:
		writeInt32(writer, 19)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterFfiError.Write", value))
	}
}

type FfiDestroyerFfiError struct{}

func (_ FfiDestroyerFfiError) Destroy(value *FfiError) {
	switch variantValue := value.err.(type) {
	case FfiErrorGeneric:
		variantValue.destroy()
	case FfiErrorAmountOverflow:
		variantValue.destroy()
	case FfiErrorDivisionByZero:
		variantValue.destroy()
	case FfiErrorAmount:
		variantValue.destroy()
	case FfiErrorPaymentFailed:
		variantValue.destroy()
	case FfiErrorPaymentPending:
		variantValue.destroy()
	case FfiErrorInsufficientFunds:
		variantValue.destroy()
	case FfiErrorDatabase:
		variantValue.destroy()
	case FfiErrorNetwork:
		variantValue.destroy()
	case FfiErrorInvalidToken:
		variantValue.destroy()
	case FfiErrorWallet:
		variantValue.destroy()
	case FfiErrorKeysetUnknown:
		variantValue.destroy()
	case FfiErrorUnitNotSupported:
		variantValue.destroy()
	case FfiErrorRuntimeTaskJoin:
		variantValue.destroy()
	case FfiErrorInvalidMnemonic:
		variantValue.destroy()
	case FfiErrorInvalidUrl:
		variantValue.destroy()
	case FfiErrorInvalidHex:
		variantValue.destroy()
	case FfiErrorInvalidCryptographicKey:
		variantValue.destroy()
	case FfiErrorSerialization:
		variantValue.destroy()
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiDestroyerFfiError.Destroy", value))
	}
}

// FFI-compatible MeltOptions
type MeltOptions interface {
	Destroy()
}

// MPP (Multi-Part Payments) options
type MeltOptionsMpp struct {
	Amount Amount
}

func (e MeltOptionsMpp) Destroy() {
	FfiDestroyerAmount{}.Destroy(e.Amount)
}

// Amountless options
type MeltOptionsAmountless struct {
	AmountMsat Amount
}

func (e MeltOptionsAmountless) Destroy() {
	FfiDestroyerAmount{}.Destroy(e.AmountMsat)
}

type FfiConverterMeltOptions struct{}

var FfiConverterMeltOptionsINSTANCE = FfiConverterMeltOptions{}

func (c FfiConverterMeltOptions) Lift(rb RustBufferI) MeltOptions {
	return LiftFromRustBuffer[MeltOptions](c, rb)
}

func (c FfiConverterMeltOptions) Lower(value MeltOptions) C.RustBuffer {
	return LowerIntoRustBuffer[MeltOptions](c, value)
}
func (FfiConverterMeltOptions) Read(reader io.Reader) MeltOptions {
	id := readInt32(reader)
	switch id {
	case 1:
		return MeltOptionsMpp{
			FfiConverterAmountINSTANCE.Read(reader),
		}
	case 2:
		return MeltOptionsAmountless{
			FfiConverterAmountINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterMeltOptions.Read()", id))
	}
}

func (FfiConverterMeltOptions) Write(writer io.Writer, value MeltOptions) {
	switch variant_value := value.(type) {
	case MeltOptionsMpp:
		writeInt32(writer, 1)
		FfiConverterAmountINSTANCE.Write(writer, variant_value.Amount)
	case MeltOptionsAmountless:
		writeInt32(writer, 2)
		FfiConverterAmountINSTANCE.Write(writer, variant_value.AmountMsat)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterMeltOptions.Write", value))
	}
}

type FfiDestroyerMeltOptions struct{}

func (_ FfiDestroyerMeltOptions) Destroy(value MeltOptions) {
	value.Destroy()
}

// FFI-compatible NotificationPayload
type NotificationPayload interface {
	Destroy()
}

// Proof state update
type NotificationPayloadProofState struct {
	ProofStates []ProofStateUpdate
}

func (e NotificationPayloadProofState) Destroy() {
	FfiDestroyerSequenceProofStateUpdate{}.Destroy(e.ProofStates)
}

// Mint quote update
type NotificationPayloadMintQuoteUpdate struct {
	Quote *MintQuoteBolt11Response
}

func (e NotificationPayloadMintQuoteUpdate) Destroy() {
	FfiDestroyerMintQuoteBolt11Response{}.Destroy(e.Quote)
}

// Melt quote update
type NotificationPayloadMeltQuoteUpdate struct {
	Quote *MeltQuoteBolt11Response
}

func (e NotificationPayloadMeltQuoteUpdate) Destroy() {
	FfiDestroyerMeltQuoteBolt11Response{}.Destroy(e.Quote)
}

type FfiConverterNotificationPayload struct{}

var FfiConverterNotificationPayloadINSTANCE = FfiConverterNotificationPayload{}

func (c FfiConverterNotificationPayload) Lift(rb RustBufferI) NotificationPayload {
	return LiftFromRustBuffer[NotificationPayload](c, rb)
}

func (c FfiConverterNotificationPayload) Lower(value NotificationPayload) C.RustBuffer {
	return LowerIntoRustBuffer[NotificationPayload](c, value)
}
func (FfiConverterNotificationPayload) Read(reader io.Reader) NotificationPayload {
	id := readInt32(reader)
	switch id {
	case 1:
		return NotificationPayloadProofState{
			FfiConverterSequenceProofStateUpdateINSTANCE.Read(reader),
		}
	case 2:
		return NotificationPayloadMintQuoteUpdate{
			FfiConverterMintQuoteBolt11ResponseINSTANCE.Read(reader),
		}
	case 3:
		return NotificationPayloadMeltQuoteUpdate{
			FfiConverterMeltQuoteBolt11ResponseINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterNotificationPayload.Read()", id))
	}
}

func (FfiConverterNotificationPayload) Write(writer io.Writer, value NotificationPayload) {
	switch variant_value := value.(type) {
	case NotificationPayloadProofState:
		writeInt32(writer, 1)
		FfiConverterSequenceProofStateUpdateINSTANCE.Write(writer, variant_value.ProofStates)
	case NotificationPayloadMintQuoteUpdate:
		writeInt32(writer, 2)
		FfiConverterMintQuoteBolt11ResponseINSTANCE.Write(writer, variant_value.Quote)
	case NotificationPayloadMeltQuoteUpdate:
		writeInt32(writer, 3)
		FfiConverterMeltQuoteBolt11ResponseINSTANCE.Write(writer, variant_value.Quote)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterNotificationPayload.Write", value))
	}
}

type FfiDestroyerNotificationPayload struct{}

func (_ FfiDestroyerNotificationPayload) Destroy(value NotificationPayload) {
	value.Destroy()
}

// FFI-compatible PaymentMethod
type PaymentMethod interface {
	Destroy()
}

// Bolt11 payment type
type PaymentMethodBolt11 struct {
}

func (e PaymentMethodBolt11) Destroy() {
}

// Bolt12 payment type
type PaymentMethodBolt12 struct {
}

func (e PaymentMethodBolt12) Destroy() {
}

// Custom payment type
type PaymentMethodCustom struct {
	Method string
}

func (e PaymentMethodCustom) Destroy() {
	FfiDestroyerString{}.Destroy(e.Method)
}

type FfiConverterPaymentMethod struct{}

var FfiConverterPaymentMethodINSTANCE = FfiConverterPaymentMethod{}

func (c FfiConverterPaymentMethod) Lift(rb RustBufferI) PaymentMethod {
	return LiftFromRustBuffer[PaymentMethod](c, rb)
}

func (c FfiConverterPaymentMethod) Lower(value PaymentMethod) C.RustBuffer {
	return LowerIntoRustBuffer[PaymentMethod](c, value)
}
func (FfiConverterPaymentMethod) Read(reader io.Reader) PaymentMethod {
	id := readInt32(reader)
	switch id {
	case 1:
		return PaymentMethodBolt11{}
	case 2:
		return PaymentMethodBolt12{}
	case 3:
		return PaymentMethodCustom{
			FfiConverterStringINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterPaymentMethod.Read()", id))
	}
}

func (FfiConverterPaymentMethod) Write(writer io.Writer, value PaymentMethod) {
	switch variant_value := value.(type) {
	case PaymentMethodBolt11:
		writeInt32(writer, 1)
	case PaymentMethodBolt12:
		writeInt32(writer, 2)
	case PaymentMethodCustom:
		writeInt32(writer, 3)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Method)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterPaymentMethod.Write", value))
	}
}

type FfiDestroyerPaymentMethod struct{}

func (_ FfiDestroyerPaymentMethod) Destroy(value PaymentMethod) {
	value.Destroy()
}

// FFI-compatible Proof state
type ProofState uint

const (
	ProofStateUnspent      ProofState = 1
	ProofStatePending      ProofState = 2
	ProofStateSpent        ProofState = 3
	ProofStateReserved     ProofState = 4
	ProofStatePendingSpent ProofState = 5
)

type FfiConverterProofState struct{}

var FfiConverterProofStateINSTANCE = FfiConverterProofState{}

func (c FfiConverterProofState) Lift(rb RustBufferI) ProofState {
	return LiftFromRustBuffer[ProofState](c, rb)
}

func (c FfiConverterProofState) Lower(value ProofState) C.RustBuffer {
	return LowerIntoRustBuffer[ProofState](c, value)
}
func (FfiConverterProofState) Read(reader io.Reader) ProofState {
	id := readInt32(reader)
	return ProofState(id)
}

func (FfiConverterProofState) Write(writer io.Writer, value ProofState) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerProofState struct{}

func (_ FfiDestroyerProofState) Destroy(value ProofState) {
}

// FFI-compatible QuoteState
type QuoteState uint

const (
	QuoteStateUnpaid  QuoteState = 1
	QuoteStatePaid    QuoteState = 2
	QuoteStatePending QuoteState = 3
	QuoteStateIssued  QuoteState = 4
)

type FfiConverterQuoteState struct{}

var FfiConverterQuoteStateINSTANCE = FfiConverterQuoteState{}

func (c FfiConverterQuoteState) Lift(rb RustBufferI) QuoteState {
	return LiftFromRustBuffer[QuoteState](c, rb)
}

func (c FfiConverterQuoteState) Lower(value QuoteState) C.RustBuffer {
	return LowerIntoRustBuffer[QuoteState](c, value)
}
func (FfiConverterQuoteState) Read(reader io.Reader) QuoteState {
	id := readInt32(reader)
	return QuoteState(id)
}

func (FfiConverterQuoteState) Write(writer io.Writer, value QuoteState) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerQuoteState struct{}

func (_ FfiDestroyerQuoteState) Destroy(value QuoteState) {
}

// FFI-compatible SendKind
type SendKind interface {
	Destroy()
}

// Allow online swap before send if wallet does not have exact amount
type SendKindOnlineExact struct {
}

func (e SendKindOnlineExact) Destroy() {
}

// Prefer offline send if difference is less than tolerance
type SendKindOnlineTolerance struct {
	Tolerance Amount
}

func (e SendKindOnlineTolerance) Destroy() {
	FfiDestroyerAmount{}.Destroy(e.Tolerance)
}

// Wallet cannot do an online swap and selected proof must be exactly send amount
type SendKindOfflineExact struct {
}

func (e SendKindOfflineExact) Destroy() {
}

// Wallet must remain offline but can over pay if below tolerance
type SendKindOfflineTolerance struct {
	Tolerance Amount
}

func (e SendKindOfflineTolerance) Destroy() {
	FfiDestroyerAmount{}.Destroy(e.Tolerance)
}

type FfiConverterSendKind struct{}

var FfiConverterSendKindINSTANCE = FfiConverterSendKind{}

func (c FfiConverterSendKind) Lift(rb RustBufferI) SendKind {
	return LiftFromRustBuffer[SendKind](c, rb)
}

func (c FfiConverterSendKind) Lower(value SendKind) C.RustBuffer {
	return LowerIntoRustBuffer[SendKind](c, value)
}
func (FfiConverterSendKind) Read(reader io.Reader) SendKind {
	id := readInt32(reader)
	switch id {
	case 1:
		return SendKindOnlineExact{}
	case 2:
		return SendKindOnlineTolerance{
			FfiConverterAmountINSTANCE.Read(reader),
		}
	case 3:
		return SendKindOfflineExact{}
	case 4:
		return SendKindOfflineTolerance{
			FfiConverterAmountINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterSendKind.Read()", id))
	}
}

func (FfiConverterSendKind) Write(writer io.Writer, value SendKind) {
	switch variant_value := value.(type) {
	case SendKindOnlineExact:
		writeInt32(writer, 1)
	case SendKindOnlineTolerance:
		writeInt32(writer, 2)
		FfiConverterAmountINSTANCE.Write(writer, variant_value.Tolerance)
	case SendKindOfflineExact:
		writeInt32(writer, 3)
	case SendKindOfflineTolerance:
		writeInt32(writer, 4)
		FfiConverterAmountINSTANCE.Write(writer, variant_value.Tolerance)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterSendKind.Write", value))
	}
}

type FfiDestroyerSendKind struct{}

func (_ FfiDestroyerSendKind) Destroy(value SendKind) {
	value.Destroy()
}

// FFI-compatible SpendingConditions
type SpendingConditions interface {
	Destroy()
}

// P2PK (Pay to Public Key) conditions
type SpendingConditionsP2pk struct {
	Pubkey     string
	Conditions *Conditions
}

func (e SpendingConditionsP2pk) Destroy() {
	FfiDestroyerString{}.Destroy(e.Pubkey)
	FfiDestroyerOptionalConditions{}.Destroy(e.Conditions)
}

// HTLC (Hash Time Locked Contract) conditions
type SpendingConditionsHtlc struct {
	Hash       string
	Conditions *Conditions
}

func (e SpendingConditionsHtlc) Destroy() {
	FfiDestroyerString{}.Destroy(e.Hash)
	FfiDestroyerOptionalConditions{}.Destroy(e.Conditions)
}

type FfiConverterSpendingConditions struct{}

var FfiConverterSpendingConditionsINSTANCE = FfiConverterSpendingConditions{}

func (c FfiConverterSpendingConditions) Lift(rb RustBufferI) SpendingConditions {
	return LiftFromRustBuffer[SpendingConditions](c, rb)
}

func (c FfiConverterSpendingConditions) Lower(value SpendingConditions) C.RustBuffer {
	return LowerIntoRustBuffer[SpendingConditions](c, value)
}
func (FfiConverterSpendingConditions) Read(reader io.Reader) SpendingConditions {
	id := readInt32(reader)
	switch id {
	case 1:
		return SpendingConditionsP2pk{
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterOptionalConditionsINSTANCE.Read(reader),
		}
	case 2:
		return SpendingConditionsHtlc{
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterOptionalConditionsINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterSpendingConditions.Read()", id))
	}
}

func (FfiConverterSpendingConditions) Write(writer io.Writer, value SpendingConditions) {
	switch variant_value := value.(type) {
	case SpendingConditionsP2pk:
		writeInt32(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Pubkey)
		FfiConverterOptionalConditionsINSTANCE.Write(writer, variant_value.Conditions)
	case SpendingConditionsHtlc:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Hash)
		FfiConverterOptionalConditionsINSTANCE.Write(writer, variant_value.Conditions)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterSpendingConditions.Write", value))
	}
}

type FfiDestroyerSpendingConditions struct{}

func (_ FfiDestroyerSpendingConditions) Destroy(value SpendingConditions) {
	value.Destroy()
}

// FFI-compatible SplitTarget
type SplitTarget interface {
	Destroy()
}

// Default target; least amount of proofs
type SplitTargetNone struct {
}

func (e SplitTargetNone) Destroy() {
}

// Target amount for wallet to have most proofs that add up to value
type SplitTargetValue struct {
	Amount Amount
}

func (e SplitTargetValue) Destroy() {
	FfiDestroyerAmount{}.Destroy(e.Amount)
}

// Specific amounts to split into (must equal amount being split)
type SplitTargetValues struct {
	Amounts []Amount
}

func (e SplitTargetValues) Destroy() {
	FfiDestroyerSequenceAmount{}.Destroy(e.Amounts)
}

type FfiConverterSplitTarget struct{}

var FfiConverterSplitTargetINSTANCE = FfiConverterSplitTarget{}

func (c FfiConverterSplitTarget) Lift(rb RustBufferI) SplitTarget {
	return LiftFromRustBuffer[SplitTarget](c, rb)
}

func (c FfiConverterSplitTarget) Lower(value SplitTarget) C.RustBuffer {
	return LowerIntoRustBuffer[SplitTarget](c, value)
}
func (FfiConverterSplitTarget) Read(reader io.Reader) SplitTarget {
	id := readInt32(reader)
	switch id {
	case 1:
		return SplitTargetNone{}
	case 2:
		return SplitTargetValue{
			FfiConverterAmountINSTANCE.Read(reader),
		}
	case 3:
		return SplitTargetValues{
			FfiConverterSequenceAmountINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterSplitTarget.Read()", id))
	}
}

func (FfiConverterSplitTarget) Write(writer io.Writer, value SplitTarget) {
	switch variant_value := value.(type) {
	case SplitTargetNone:
		writeInt32(writer, 1)
	case SplitTargetValue:
		writeInt32(writer, 2)
		FfiConverterAmountINSTANCE.Write(writer, variant_value.Amount)
	case SplitTargetValues:
		writeInt32(writer, 3)
		FfiConverterSequenceAmountINSTANCE.Write(writer, variant_value.Amounts)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterSplitTarget.Write", value))
	}
}

type FfiDestroyerSplitTarget struct{}

func (_ FfiDestroyerSplitTarget) Destroy(value SplitTarget) {
	value.Destroy()
}

// FFI-compatible SubscriptionKind
type SubscriptionKind uint

const (
	// Bolt 11 Melt Quote
	SubscriptionKindBolt11MeltQuote SubscriptionKind = 1
	// Bolt 11 Mint Quote
	SubscriptionKindBolt11MintQuote SubscriptionKind = 2
	// Bolt 12 Mint Quote
	SubscriptionKindBolt12MintQuote SubscriptionKind = 3
	// Proof State
	SubscriptionKindProofState SubscriptionKind = 4
)

type FfiConverterSubscriptionKind struct{}

var FfiConverterSubscriptionKindINSTANCE = FfiConverterSubscriptionKind{}

func (c FfiConverterSubscriptionKind) Lift(rb RustBufferI) SubscriptionKind {
	return LiftFromRustBuffer[SubscriptionKind](c, rb)
}

func (c FfiConverterSubscriptionKind) Lower(value SubscriptionKind) C.RustBuffer {
	return LowerIntoRustBuffer[SubscriptionKind](c, value)
}
func (FfiConverterSubscriptionKind) Read(reader io.Reader) SubscriptionKind {
	id := readInt32(reader)
	return SubscriptionKind(id)
}

func (FfiConverterSubscriptionKind) Write(writer io.Writer, value SubscriptionKind) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerSubscriptionKind struct{}

func (_ FfiDestroyerSubscriptionKind) Destroy(value SubscriptionKind) {
}

// FFI-compatible TransactionDirection
type TransactionDirection uint

const (
	// Incoming transaction (i.e., receive or mint)
	TransactionDirectionIncoming TransactionDirection = 1
	// Outgoing transaction (i.e., send or melt)
	TransactionDirectionOutgoing TransactionDirection = 2
)

type FfiConverterTransactionDirection struct{}

var FfiConverterTransactionDirectionINSTANCE = FfiConverterTransactionDirection{}

func (c FfiConverterTransactionDirection) Lift(rb RustBufferI) TransactionDirection {
	return LiftFromRustBuffer[TransactionDirection](c, rb)
}

func (c FfiConverterTransactionDirection) Lower(value TransactionDirection) C.RustBuffer {
	return LowerIntoRustBuffer[TransactionDirection](c, value)
}
func (FfiConverterTransactionDirection) Read(reader io.Reader) TransactionDirection {
	id := readInt32(reader)
	return TransactionDirection(id)
}

func (FfiConverterTransactionDirection) Write(writer io.Writer, value TransactionDirection) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerTransactionDirection struct{}

func (_ FfiDestroyerTransactionDirection) Destroy(value TransactionDirection) {
}

// Transfer mode for mint-to-mint transfers
type TransferMode interface {
	Destroy()
}

// Transfer exact amount to target (target receives specified amount)
type TransferModeExactReceive struct {
	Amount Amount
}

func (e TransferModeExactReceive) Destroy() {
	FfiDestroyerAmount{}.Destroy(e.Amount)
}

// Transfer all available balance (source will be emptied)
type TransferModeFullBalance struct {
}

func (e TransferModeFullBalance) Destroy() {
}

type FfiConverterTransferMode struct{}

var FfiConverterTransferModeINSTANCE = FfiConverterTransferMode{}

func (c FfiConverterTransferMode) Lift(rb RustBufferI) TransferMode {
	return LiftFromRustBuffer[TransferMode](c, rb)
}

func (c FfiConverterTransferMode) Lower(value TransferMode) C.RustBuffer {
	return LowerIntoRustBuffer[TransferMode](c, value)
}
func (FfiConverterTransferMode) Read(reader io.Reader) TransferMode {
	id := readInt32(reader)
	switch id {
	case 1:
		return TransferModeExactReceive{
			FfiConverterAmountINSTANCE.Read(reader),
		}
	case 2:
		return TransferModeFullBalance{}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterTransferMode.Read()", id))
	}
}

func (FfiConverterTransferMode) Write(writer io.Writer, value TransferMode) {
	switch variant_value := value.(type) {
	case TransferModeExactReceive:
		writeInt32(writer, 1)
		FfiConverterAmountINSTANCE.Write(writer, variant_value.Amount)
	case TransferModeFullBalance:
		writeInt32(writer, 2)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterTransferMode.Write", value))
	}
}

type FfiDestroyerTransferMode struct{}

func (_ FfiDestroyerTransferMode) Destroy(value TransferMode) {
	value.Destroy()
}

// FFI-safe wallet database backend selection
type WalletDbBackend interface {
	Destroy()
}
type WalletDbBackendSqlite struct {
	Path string
}

func (e WalletDbBackendSqlite) Destroy() {
	FfiDestroyerString{}.Destroy(e.Path)
}

type WalletDbBackendPostgres struct {
	Url string
}

func (e WalletDbBackendPostgres) Destroy() {
	FfiDestroyerString{}.Destroy(e.Url)
}

type FfiConverterWalletDbBackend struct{}

var FfiConverterWalletDbBackendINSTANCE = FfiConverterWalletDbBackend{}

func (c FfiConverterWalletDbBackend) Lift(rb RustBufferI) WalletDbBackend {
	return LiftFromRustBuffer[WalletDbBackend](c, rb)
}

func (c FfiConverterWalletDbBackend) Lower(value WalletDbBackend) C.RustBuffer {
	return LowerIntoRustBuffer[WalletDbBackend](c, value)
}
func (FfiConverterWalletDbBackend) Read(reader io.Reader) WalletDbBackend {
	id := readInt32(reader)
	switch id {
	case 1:
		return WalletDbBackendSqlite{
			FfiConverterStringINSTANCE.Read(reader),
		}
	case 2:
		return WalletDbBackendPostgres{
			FfiConverterStringINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterWalletDbBackend.Read()", id))
	}
}

func (FfiConverterWalletDbBackend) Write(writer io.Writer, value WalletDbBackend) {
	switch variant_value := value.(type) {
	case WalletDbBackendSqlite:
		writeInt32(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Path)
	case WalletDbBackendPostgres:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Url)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterWalletDbBackend.Write", value))
	}
}

type FfiDestroyerWalletDbBackend struct{}

func (_ FfiDestroyerWalletDbBackend) Destroy(value WalletDbBackend) {
	value.Destroy()
}

// FFI-compatible Witness
type Witness interface {
	Destroy()
}

// P2PK Witness
type WitnessP2pk struct {
	Signatures []string
}

func (e WitnessP2pk) Destroy() {
	FfiDestroyerSequenceString{}.Destroy(e.Signatures)
}

// HTLC Witness
type WitnessHtlc struct {
	Preimage   string
	Signatures *[]string
}

func (e WitnessHtlc) Destroy() {
	FfiDestroyerString{}.Destroy(e.Preimage)
	FfiDestroyerOptionalSequenceString{}.Destroy(e.Signatures)
}

type FfiConverterWitness struct{}

var FfiConverterWitnessINSTANCE = FfiConverterWitness{}

func (c FfiConverterWitness) Lift(rb RustBufferI) Witness {
	return LiftFromRustBuffer[Witness](c, rb)
}

func (c FfiConverterWitness) Lower(value Witness) C.RustBuffer {
	return LowerIntoRustBuffer[Witness](c, value)
}
func (FfiConverterWitness) Read(reader io.Reader) Witness {
	id := readInt32(reader)
	switch id {
	case 1:
		return WitnessP2pk{
			FfiConverterSequenceStringINSTANCE.Read(reader),
		}
	case 2:
		return WitnessHtlc{
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterOptionalSequenceStringINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterWitness.Read()", id))
	}
}

func (FfiConverterWitness) Write(writer io.Writer, value Witness) {
	switch variant_value := value.(type) {
	case WitnessP2pk:
		writeInt32(writer, 1)
		FfiConverterSequenceStringINSTANCE.Write(writer, variant_value.Signatures)
	case WitnessHtlc:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Preimage)
		FfiConverterOptionalSequenceStringINSTANCE.Write(writer, variant_value.Signatures)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterWitness.Write", value))
	}
}

type FfiDestroyerWitness struct{}

func (_ FfiDestroyerWitness) Destroy(value Witness) {
	value.Destroy()
}

type FfiConverterOptionalUint32 struct{}

var FfiConverterOptionalUint32INSTANCE = FfiConverterOptionalUint32{}

func (c FfiConverterOptionalUint32) Lift(rb RustBufferI) *uint32 {
	return LiftFromRustBuffer[*uint32](c, rb)
}

func (_ FfiConverterOptionalUint32) Read(reader io.Reader) *uint32 {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterUint32INSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalUint32) Lower(value *uint32) C.RustBuffer {
	return LowerIntoRustBuffer[*uint32](c, value)
}

func (_ FfiConverterOptionalUint32) Write(writer io.Writer, value *uint32) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterUint32INSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalUint32 struct{}

func (_ FfiDestroyerOptionalUint32) Destroy(value *uint32) {
	if value != nil {
		FfiDestroyerUint32{}.Destroy(*value)
	}
}

type FfiConverterOptionalUint64 struct{}

var FfiConverterOptionalUint64INSTANCE = FfiConverterOptionalUint64{}

func (c FfiConverterOptionalUint64) Lift(rb RustBufferI) *uint64 {
	return LiftFromRustBuffer[*uint64](c, rb)
}

func (_ FfiConverterOptionalUint64) Read(reader io.Reader) *uint64 {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterUint64INSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalUint64) Lower(value *uint64) C.RustBuffer {
	return LowerIntoRustBuffer[*uint64](c, value)
}

func (_ FfiConverterOptionalUint64) Write(writer io.Writer, value *uint64) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterUint64INSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalUint64 struct{}

func (_ FfiDestroyerOptionalUint64) Destroy(value *uint64) {
	if value != nil {
		FfiDestroyerUint64{}.Destroy(*value)
	}
}

type FfiConverterOptionalBool struct{}

var FfiConverterOptionalBoolINSTANCE = FfiConverterOptionalBool{}

func (c FfiConverterOptionalBool) Lift(rb RustBufferI) *bool {
	return LiftFromRustBuffer[*bool](c, rb)
}

func (_ FfiConverterOptionalBool) Read(reader io.Reader) *bool {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterBoolINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalBool) Lower(value *bool) C.RustBuffer {
	return LowerIntoRustBuffer[*bool](c, value)
}

func (_ FfiConverterOptionalBool) Write(writer io.Writer, value *bool) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterBoolINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalBool struct{}

func (_ FfiDestroyerOptionalBool) Destroy(value *bool) {
	if value != nil {
		FfiDestroyerBool{}.Destroy(*value)
	}
}

type FfiConverterOptionalString struct{}

var FfiConverterOptionalStringINSTANCE = FfiConverterOptionalString{}

func (c FfiConverterOptionalString) Lift(rb RustBufferI) *string {
	return LiftFromRustBuffer[*string](c, rb)
}

func (_ FfiConverterOptionalString) Read(reader io.Reader) *string {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterStringINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalString) Lower(value *string) C.RustBuffer {
	return LowerIntoRustBuffer[*string](c, value)
}

func (_ FfiConverterOptionalString) Write(writer io.Writer, value *string) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalString struct{}

func (_ FfiDestroyerOptionalString) Destroy(value *string) {
	if value != nil {
		FfiDestroyerString{}.Destroy(*value)
	}
}

type FfiConverterOptionalAmount struct{}

var FfiConverterOptionalAmountINSTANCE = FfiConverterOptionalAmount{}

func (c FfiConverterOptionalAmount) Lift(rb RustBufferI) *Amount {
	return LiftFromRustBuffer[*Amount](c, rb)
}

func (_ FfiConverterOptionalAmount) Read(reader io.Reader) *Amount {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterAmountINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalAmount) Lower(value *Amount) C.RustBuffer {
	return LowerIntoRustBuffer[*Amount](c, value)
}

func (_ FfiConverterOptionalAmount) Write(writer io.Writer, value *Amount) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterAmountINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalAmount struct{}

func (_ FfiDestroyerOptionalAmount) Destroy(value *Amount) {
	if value != nil {
		FfiDestroyerAmount{}.Destroy(*value)
	}
}

type FfiConverterOptionalBlindAuthSettings struct{}

var FfiConverterOptionalBlindAuthSettingsINSTANCE = FfiConverterOptionalBlindAuthSettings{}

func (c FfiConverterOptionalBlindAuthSettings) Lift(rb RustBufferI) *BlindAuthSettings {
	return LiftFromRustBuffer[*BlindAuthSettings](c, rb)
}

func (_ FfiConverterOptionalBlindAuthSettings) Read(reader io.Reader) *BlindAuthSettings {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterBlindAuthSettingsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalBlindAuthSettings) Lower(value *BlindAuthSettings) C.RustBuffer {
	return LowerIntoRustBuffer[*BlindAuthSettings](c, value)
}

func (_ FfiConverterOptionalBlindAuthSettings) Write(writer io.Writer, value *BlindAuthSettings) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterBlindAuthSettingsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalBlindAuthSettings struct{}

func (_ FfiDestroyerOptionalBlindAuthSettings) Destroy(value *BlindAuthSettings) {
	if value != nil {
		FfiDestroyerBlindAuthSettings{}.Destroy(*value)
	}
}

type FfiConverterOptionalClearAuthSettings struct{}

var FfiConverterOptionalClearAuthSettingsINSTANCE = FfiConverterOptionalClearAuthSettings{}

func (c FfiConverterOptionalClearAuthSettings) Lift(rb RustBufferI) *ClearAuthSettings {
	return LiftFromRustBuffer[*ClearAuthSettings](c, rb)
}

func (_ FfiConverterOptionalClearAuthSettings) Read(reader io.Reader) *ClearAuthSettings {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterClearAuthSettingsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalClearAuthSettings) Lower(value *ClearAuthSettings) C.RustBuffer {
	return LowerIntoRustBuffer[*ClearAuthSettings](c, value)
}

func (_ FfiConverterOptionalClearAuthSettings) Write(writer io.Writer, value *ClearAuthSettings) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterClearAuthSettingsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalClearAuthSettings struct{}

func (_ FfiDestroyerOptionalClearAuthSettings) Destroy(value *ClearAuthSettings) {
	if value != nil {
		FfiDestroyerClearAuthSettings{}.Destroy(*value)
	}
}

type FfiConverterOptionalConditions struct{}

var FfiConverterOptionalConditionsINSTANCE = FfiConverterOptionalConditions{}

func (c FfiConverterOptionalConditions) Lift(rb RustBufferI) *Conditions {
	return LiftFromRustBuffer[*Conditions](c, rb)
}

func (_ FfiConverterOptionalConditions) Read(reader io.Reader) *Conditions {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterConditionsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalConditions) Lower(value *Conditions) C.RustBuffer {
	return LowerIntoRustBuffer[*Conditions](c, value)
}

func (_ FfiConverterOptionalConditions) Write(writer io.Writer, value *Conditions) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterConditionsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalConditions struct{}

func (_ FfiDestroyerOptionalConditions) Destroy(value *Conditions) {
	if value != nil {
		FfiDestroyerConditions{}.Destroy(*value)
	}
}

type FfiConverterOptionalKeySetInfo struct{}

var FfiConverterOptionalKeySetInfoINSTANCE = FfiConverterOptionalKeySetInfo{}

func (c FfiConverterOptionalKeySetInfo) Lift(rb RustBufferI) *KeySetInfo {
	return LiftFromRustBuffer[*KeySetInfo](c, rb)
}

func (_ FfiConverterOptionalKeySetInfo) Read(reader io.Reader) *KeySetInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterKeySetInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalKeySetInfo) Lower(value *KeySetInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*KeySetInfo](c, value)
}

func (_ FfiConverterOptionalKeySetInfo) Write(writer io.Writer, value *KeySetInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterKeySetInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalKeySetInfo struct{}

func (_ FfiDestroyerOptionalKeySetInfo) Destroy(value *KeySetInfo) {
	if value != nil {
		FfiDestroyerKeySetInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalKeys struct{}

var FfiConverterOptionalKeysINSTANCE = FfiConverterOptionalKeys{}

func (c FfiConverterOptionalKeys) Lift(rb RustBufferI) *Keys {
	return LiftFromRustBuffer[*Keys](c, rb)
}

func (_ FfiConverterOptionalKeys) Read(reader io.Reader) *Keys {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterKeysINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalKeys) Lower(value *Keys) C.RustBuffer {
	return LowerIntoRustBuffer[*Keys](c, value)
}

func (_ FfiConverterOptionalKeys) Write(writer io.Writer, value *Keys) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterKeysINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalKeys struct{}

func (_ FfiDestroyerOptionalKeys) Destroy(value *Keys) {
	if value != nil {
		FfiDestroyerKeys{}.Destroy(*value)
	}
}

type FfiConverterOptionalMeltQuote struct{}

var FfiConverterOptionalMeltQuoteINSTANCE = FfiConverterOptionalMeltQuote{}

func (c FfiConverterOptionalMeltQuote) Lift(rb RustBufferI) *MeltQuote {
	return LiftFromRustBuffer[*MeltQuote](c, rb)
}

func (_ FfiConverterOptionalMeltQuote) Read(reader io.Reader) *MeltQuote {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterMeltQuoteINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalMeltQuote) Lower(value *MeltQuote) C.RustBuffer {
	return LowerIntoRustBuffer[*MeltQuote](c, value)
}

func (_ FfiConverterOptionalMeltQuote) Write(writer io.Writer, value *MeltQuote) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterMeltQuoteINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalMeltQuote struct{}

func (_ FfiDestroyerOptionalMeltQuote) Destroy(value *MeltQuote) {
	if value != nil {
		FfiDestroyerMeltQuote{}.Destroy(*value)
	}
}

type FfiConverterOptionalMintInfo struct{}

var FfiConverterOptionalMintInfoINSTANCE = FfiConverterOptionalMintInfo{}

func (c FfiConverterOptionalMintInfo) Lift(rb RustBufferI) *MintInfo {
	return LiftFromRustBuffer[*MintInfo](c, rb)
}

func (_ FfiConverterOptionalMintInfo) Read(reader io.Reader) *MintInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterMintInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalMintInfo) Lower(value *MintInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*MintInfo](c, value)
}

func (_ FfiConverterOptionalMintInfo) Write(writer io.Writer, value *MintInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterMintInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalMintInfo struct{}

func (_ FfiDestroyerOptionalMintInfo) Destroy(value *MintInfo) {
	if value != nil {
		FfiDestroyerMintInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalMintQuote struct{}

var FfiConverterOptionalMintQuoteINSTANCE = FfiConverterOptionalMintQuote{}

func (c FfiConverterOptionalMintQuote) Lift(rb RustBufferI) *MintQuote {
	return LiftFromRustBuffer[*MintQuote](c, rb)
}

func (_ FfiConverterOptionalMintQuote) Read(reader io.Reader) *MintQuote {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterMintQuoteINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalMintQuote) Lower(value *MintQuote) C.RustBuffer {
	return LowerIntoRustBuffer[*MintQuote](c, value)
}

func (_ FfiConverterOptionalMintQuote) Write(writer io.Writer, value *MintQuote) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterMintQuoteINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalMintQuote struct{}

func (_ FfiDestroyerOptionalMintQuote) Destroy(value *MintQuote) {
	if value != nil {
		FfiDestroyerMintQuote{}.Destroy(*value)
	}
}

type FfiConverterOptionalMintUrl struct{}

var FfiConverterOptionalMintUrlINSTANCE = FfiConverterOptionalMintUrl{}

func (c FfiConverterOptionalMintUrl) Lift(rb RustBufferI) *MintUrl {
	return LiftFromRustBuffer[*MintUrl](c, rb)
}

func (_ FfiConverterOptionalMintUrl) Read(reader io.Reader) *MintUrl {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterMintUrlINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalMintUrl) Lower(value *MintUrl) C.RustBuffer {
	return LowerIntoRustBuffer[*MintUrl](c, value)
}

func (_ FfiConverterOptionalMintUrl) Write(writer io.Writer, value *MintUrl) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterMintUrlINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalMintUrl struct{}

func (_ FfiDestroyerOptionalMintUrl) Destroy(value *MintUrl) {
	if value != nil {
		FfiDestroyerMintUrl{}.Destroy(*value)
	}
}

type FfiConverterOptionalMintVersion struct{}

var FfiConverterOptionalMintVersionINSTANCE = FfiConverterOptionalMintVersion{}

func (c FfiConverterOptionalMintVersion) Lift(rb RustBufferI) *MintVersion {
	return LiftFromRustBuffer[*MintVersion](c, rb)
}

func (_ FfiConverterOptionalMintVersion) Read(reader io.Reader) *MintVersion {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterMintVersionINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalMintVersion) Lower(value *MintVersion) C.RustBuffer {
	return LowerIntoRustBuffer[*MintVersion](c, value)
}

func (_ FfiConverterOptionalMintVersion) Write(writer io.Writer, value *MintVersion) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterMintVersionINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalMintVersion struct{}

func (_ FfiDestroyerOptionalMintVersion) Destroy(value *MintVersion) {
	if value != nil {
		FfiDestroyerMintVersion{}.Destroy(*value)
	}
}

type FfiConverterOptionalProofDleq struct{}

var FfiConverterOptionalProofDleqINSTANCE = FfiConverterOptionalProofDleq{}

func (c FfiConverterOptionalProofDleq) Lift(rb RustBufferI) *ProofDleq {
	return LiftFromRustBuffer[*ProofDleq](c, rb)
}

func (_ FfiConverterOptionalProofDleq) Read(reader io.Reader) *ProofDleq {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterProofDleqINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalProofDleq) Lower(value *ProofDleq) C.RustBuffer {
	return LowerIntoRustBuffer[*ProofDleq](c, value)
}

func (_ FfiConverterOptionalProofDleq) Write(writer io.Writer, value *ProofDleq) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterProofDleqINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalProofDleq struct{}

func (_ FfiDestroyerOptionalProofDleq) Destroy(value *ProofDleq) {
	if value != nil {
		FfiDestroyerProofDleq{}.Destroy(*value)
	}
}

type FfiConverterOptionalSendMemo struct{}

var FfiConverterOptionalSendMemoINSTANCE = FfiConverterOptionalSendMemo{}

func (c FfiConverterOptionalSendMemo) Lift(rb RustBufferI) *SendMemo {
	return LiftFromRustBuffer[*SendMemo](c, rb)
}

func (_ FfiConverterOptionalSendMemo) Read(reader io.Reader) *SendMemo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSendMemoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSendMemo) Lower(value *SendMemo) C.RustBuffer {
	return LowerIntoRustBuffer[*SendMemo](c, value)
}

func (_ FfiConverterOptionalSendMemo) Write(writer io.Writer, value *SendMemo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSendMemoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSendMemo struct{}

func (_ FfiDestroyerOptionalSendMemo) Destroy(value *SendMemo) {
	if value != nil {
		FfiDestroyerSendMemo{}.Destroy(*value)
	}
}

type FfiConverterOptionalTransaction struct{}

var FfiConverterOptionalTransactionINSTANCE = FfiConverterOptionalTransaction{}

func (c FfiConverterOptionalTransaction) Lift(rb RustBufferI) *Transaction {
	return LiftFromRustBuffer[*Transaction](c, rb)
}

func (_ FfiConverterOptionalTransaction) Read(reader io.Reader) *Transaction {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTransactionINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTransaction) Lower(value *Transaction) C.RustBuffer {
	return LowerIntoRustBuffer[*Transaction](c, value)
}

func (_ FfiConverterOptionalTransaction) Write(writer io.Writer, value *Transaction) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTransactionINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTransaction struct{}

func (_ FfiDestroyerOptionalTransaction) Destroy(value *Transaction) {
	if value != nil {
		FfiDestroyerTransaction{}.Destroy(*value)
	}
}

type FfiConverterOptionalCurrencyUnit struct{}

var FfiConverterOptionalCurrencyUnitINSTANCE = FfiConverterOptionalCurrencyUnit{}

func (c FfiConverterOptionalCurrencyUnit) Lift(rb RustBufferI) *CurrencyUnit {
	return LiftFromRustBuffer[*CurrencyUnit](c, rb)
}

func (_ FfiConverterOptionalCurrencyUnit) Read(reader io.Reader) *CurrencyUnit {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterCurrencyUnitINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalCurrencyUnit) Lower(value *CurrencyUnit) C.RustBuffer {
	return LowerIntoRustBuffer[*CurrencyUnit](c, value)
}

func (_ FfiConverterOptionalCurrencyUnit) Write(writer io.Writer, value *CurrencyUnit) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterCurrencyUnitINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalCurrencyUnit struct{}

func (_ FfiDestroyerOptionalCurrencyUnit) Destroy(value *CurrencyUnit) {
	if value != nil {
		FfiDestroyerCurrencyUnit{}.Destroy(*value)
	}
}

type FfiConverterOptionalMeltOptions struct{}

var FfiConverterOptionalMeltOptionsINSTANCE = FfiConverterOptionalMeltOptions{}

func (c FfiConverterOptionalMeltOptions) Lift(rb RustBufferI) *MeltOptions {
	return LiftFromRustBuffer[*MeltOptions](c, rb)
}

func (_ FfiConverterOptionalMeltOptions) Read(reader io.Reader) *MeltOptions {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterMeltOptionsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalMeltOptions) Lower(value *MeltOptions) C.RustBuffer {
	return LowerIntoRustBuffer[*MeltOptions](c, value)
}

func (_ FfiConverterOptionalMeltOptions) Write(writer io.Writer, value *MeltOptions) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterMeltOptionsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalMeltOptions struct{}

func (_ FfiDestroyerOptionalMeltOptions) Destroy(value *MeltOptions) {
	if value != nil {
		FfiDestroyerMeltOptions{}.Destroy(*value)
	}
}

type FfiConverterOptionalNotificationPayload struct{}

var FfiConverterOptionalNotificationPayloadINSTANCE = FfiConverterOptionalNotificationPayload{}

func (c FfiConverterOptionalNotificationPayload) Lift(rb RustBufferI) *NotificationPayload {
	return LiftFromRustBuffer[*NotificationPayload](c, rb)
}

func (_ FfiConverterOptionalNotificationPayload) Read(reader io.Reader) *NotificationPayload {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterNotificationPayloadINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalNotificationPayload) Lower(value *NotificationPayload) C.RustBuffer {
	return LowerIntoRustBuffer[*NotificationPayload](c, value)
}

func (_ FfiConverterOptionalNotificationPayload) Write(writer io.Writer, value *NotificationPayload) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterNotificationPayloadINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalNotificationPayload struct{}

func (_ FfiDestroyerOptionalNotificationPayload) Destroy(value *NotificationPayload) {
	if value != nil {
		FfiDestroyerNotificationPayload{}.Destroy(*value)
	}
}

type FfiConverterOptionalSpendingConditions struct{}

var FfiConverterOptionalSpendingConditionsINSTANCE = FfiConverterOptionalSpendingConditions{}

func (c FfiConverterOptionalSpendingConditions) Lift(rb RustBufferI) *SpendingConditions {
	return LiftFromRustBuffer[*SpendingConditions](c, rb)
}

func (_ FfiConverterOptionalSpendingConditions) Read(reader io.Reader) *SpendingConditions {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSpendingConditionsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSpendingConditions) Lower(value *SpendingConditions) C.RustBuffer {
	return LowerIntoRustBuffer[*SpendingConditions](c, value)
}

func (_ FfiConverterOptionalSpendingConditions) Write(writer io.Writer, value *SpendingConditions) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSpendingConditionsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSpendingConditions struct{}

func (_ FfiDestroyerOptionalSpendingConditions) Destroy(value *SpendingConditions) {
	if value != nil {
		FfiDestroyerSpendingConditions{}.Destroy(*value)
	}
}

type FfiConverterOptionalTransactionDirection struct{}

var FfiConverterOptionalTransactionDirectionINSTANCE = FfiConverterOptionalTransactionDirection{}

func (c FfiConverterOptionalTransactionDirection) Lift(rb RustBufferI) *TransactionDirection {
	return LiftFromRustBuffer[*TransactionDirection](c, rb)
}

func (_ FfiConverterOptionalTransactionDirection) Read(reader io.Reader) *TransactionDirection {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTransactionDirectionINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTransactionDirection) Lower(value *TransactionDirection) C.RustBuffer {
	return LowerIntoRustBuffer[*TransactionDirection](c, value)
}

func (_ FfiConverterOptionalTransactionDirection) Write(writer io.Writer, value *TransactionDirection) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTransactionDirectionINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTransactionDirection struct{}

func (_ FfiDestroyerOptionalTransactionDirection) Destroy(value *TransactionDirection) {
	if value != nil {
		FfiDestroyerTransactionDirection{}.Destroy(*value)
	}
}

type FfiConverterOptionalWitness struct{}

var FfiConverterOptionalWitnessINSTANCE = FfiConverterOptionalWitness{}

func (c FfiConverterOptionalWitness) Lift(rb RustBufferI) *Witness {
	return LiftFromRustBuffer[*Witness](c, rb)
}

func (_ FfiConverterOptionalWitness) Read(reader io.Reader) *Witness {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterWitnessINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalWitness) Lower(value *Witness) C.RustBuffer {
	return LowerIntoRustBuffer[*Witness](c, value)
}

func (_ FfiConverterOptionalWitness) Write(writer io.Writer, value *Witness) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterWitnessINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalWitness struct{}

func (_ FfiDestroyerOptionalWitness) Destroy(value *Witness) {
	if value != nil {
		FfiDestroyerWitness{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceString struct{}

var FfiConverterOptionalSequenceStringINSTANCE = FfiConverterOptionalSequenceString{}

func (c FfiConverterOptionalSequenceString) Lift(rb RustBufferI) *[]string {
	return LiftFromRustBuffer[*[]string](c, rb)
}

func (_ FfiConverterOptionalSequenceString) Read(reader io.Reader) *[]string {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceStringINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceString) Lower(value *[]string) C.RustBuffer {
	return LowerIntoRustBuffer[*[]string](c, value)
}

func (_ FfiConverterOptionalSequenceString) Write(writer io.Writer, value *[]string) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceStringINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceString struct{}

func (_ FfiDestroyerOptionalSequenceString) Destroy(value *[]string) {
	if value != nil {
		FfiDestroyerSequenceString{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceProof struct{}

var FfiConverterOptionalSequenceProofINSTANCE = FfiConverterOptionalSequenceProof{}

func (c FfiConverterOptionalSequenceProof) Lift(rb RustBufferI) *[]*Proof {
	return LiftFromRustBuffer[*[]*Proof](c, rb)
}

func (_ FfiConverterOptionalSequenceProof) Read(reader io.Reader) *[]*Proof {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceProofINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceProof) Lower(value *[]*Proof) C.RustBuffer {
	return LowerIntoRustBuffer[*[]*Proof](c, value)
}

func (_ FfiConverterOptionalSequenceProof) Write(writer io.Writer, value *[]*Proof) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceProofINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceProof struct{}

func (_ FfiDestroyerOptionalSequenceProof) Destroy(value *[]*Proof) {
	if value != nil {
		FfiDestroyerSequenceProof{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceContactInfo struct{}

var FfiConverterOptionalSequenceContactInfoINSTANCE = FfiConverterOptionalSequenceContactInfo{}

func (c FfiConverterOptionalSequenceContactInfo) Lift(rb RustBufferI) *[]ContactInfo {
	return LiftFromRustBuffer[*[]ContactInfo](c, rb)
}

func (_ FfiConverterOptionalSequenceContactInfo) Read(reader io.Reader) *[]ContactInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceContactInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceContactInfo) Lower(value *[]ContactInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*[]ContactInfo](c, value)
}

func (_ FfiConverterOptionalSequenceContactInfo) Write(writer io.Writer, value *[]ContactInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceContactInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceContactInfo struct{}

func (_ FfiDestroyerOptionalSequenceContactInfo) Destroy(value *[]ContactInfo) {
	if value != nil {
		FfiDestroyerSequenceContactInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceKeySetInfo struct{}

var FfiConverterOptionalSequenceKeySetInfoINSTANCE = FfiConverterOptionalSequenceKeySetInfo{}

func (c FfiConverterOptionalSequenceKeySetInfo) Lift(rb RustBufferI) *[]KeySetInfo {
	return LiftFromRustBuffer[*[]KeySetInfo](c, rb)
}

func (_ FfiConverterOptionalSequenceKeySetInfo) Read(reader io.Reader) *[]KeySetInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceKeySetInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceKeySetInfo) Lower(value *[]KeySetInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*[]KeySetInfo](c, value)
}

func (_ FfiConverterOptionalSequenceKeySetInfo) Write(writer io.Writer, value *[]KeySetInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceKeySetInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceKeySetInfo struct{}

func (_ FfiDestroyerOptionalSequenceKeySetInfo) Destroy(value *[]KeySetInfo) {
	if value != nil {
		FfiDestroyerSequenceKeySetInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceProofState struct{}

var FfiConverterOptionalSequenceProofStateINSTANCE = FfiConverterOptionalSequenceProofState{}

func (c FfiConverterOptionalSequenceProofState) Lift(rb RustBufferI) *[]ProofState {
	return LiftFromRustBuffer[*[]ProofState](c, rb)
}

func (_ FfiConverterOptionalSequenceProofState) Read(reader io.Reader) *[]ProofState {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceProofStateINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceProofState) Lower(value *[]ProofState) C.RustBuffer {
	return LowerIntoRustBuffer[*[]ProofState](c, value)
}

func (_ FfiConverterOptionalSequenceProofState) Write(writer io.Writer, value *[]ProofState) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceProofStateINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceProofState struct{}

func (_ FfiDestroyerOptionalSequenceProofState) Destroy(value *[]ProofState) {
	if value != nil {
		FfiDestroyerSequenceProofState{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceSpendingConditions struct{}

var FfiConverterOptionalSequenceSpendingConditionsINSTANCE = FfiConverterOptionalSequenceSpendingConditions{}

func (c FfiConverterOptionalSequenceSpendingConditions) Lift(rb RustBufferI) *[]SpendingConditions {
	return LiftFromRustBuffer[*[]SpendingConditions](c, rb)
}

func (_ FfiConverterOptionalSequenceSpendingConditions) Read(reader io.Reader) *[]SpendingConditions {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceSpendingConditionsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceSpendingConditions) Lower(value *[]SpendingConditions) C.RustBuffer {
	return LowerIntoRustBuffer[*[]SpendingConditions](c, value)
}

func (_ FfiConverterOptionalSequenceSpendingConditions) Write(writer io.Writer, value *[]SpendingConditions) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceSpendingConditionsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceSpendingConditions struct{}

func (_ FfiDestroyerOptionalSequenceSpendingConditions) Destroy(value *[]SpendingConditions) {
	if value != nil {
		FfiDestroyerSequenceSpendingConditions{}.Destroy(*value)
	}
}

type FfiConverterSequenceUint64 struct{}

var FfiConverterSequenceUint64INSTANCE = FfiConverterSequenceUint64{}

func (c FfiConverterSequenceUint64) Lift(rb RustBufferI) []uint64 {
	return LiftFromRustBuffer[[]uint64](c, rb)
}

func (c FfiConverterSequenceUint64) Read(reader io.Reader) []uint64 {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]uint64, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterUint64INSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceUint64) Lower(value []uint64) C.RustBuffer {
	return LowerIntoRustBuffer[[]uint64](c, value)
}

func (c FfiConverterSequenceUint64) Write(writer io.Writer, value []uint64) {
	if len(value) > math.MaxInt32 {
		panic("[]uint64 is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterUint64INSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceUint64 struct{}

func (FfiDestroyerSequenceUint64) Destroy(sequence []uint64) {
	for _, value := range sequence {
		FfiDestroyerUint64{}.Destroy(value)
	}
}

type FfiConverterSequenceBool struct{}

var FfiConverterSequenceBoolINSTANCE = FfiConverterSequenceBool{}

func (c FfiConverterSequenceBool) Lift(rb RustBufferI) []bool {
	return LiftFromRustBuffer[[]bool](c, rb)
}

func (c FfiConverterSequenceBool) Read(reader io.Reader) []bool {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]bool, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterBoolINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceBool) Lower(value []bool) C.RustBuffer {
	return LowerIntoRustBuffer[[]bool](c, value)
}

func (c FfiConverterSequenceBool) Write(writer io.Writer, value []bool) {
	if len(value) > math.MaxInt32 {
		panic("[]bool is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterBoolINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceBool struct{}

func (FfiDestroyerSequenceBool) Destroy(sequence []bool) {
	for _, value := range sequence {
		FfiDestroyerBool{}.Destroy(value)
	}
}

type FfiConverterSequenceString struct{}

var FfiConverterSequenceStringINSTANCE = FfiConverterSequenceString{}

func (c FfiConverterSequenceString) Lift(rb RustBufferI) []string {
	return LiftFromRustBuffer[[]string](c, rb)
}

func (c FfiConverterSequenceString) Read(reader io.Reader) []string {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]string, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterStringINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceString) Lower(value []string) C.RustBuffer {
	return LowerIntoRustBuffer[[]string](c, value)
}

func (c FfiConverterSequenceString) Write(writer io.Writer, value []string) {
	if len(value) > math.MaxInt32 {
		panic("[]string is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterStringINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceString struct{}

func (FfiDestroyerSequenceString) Destroy(sequence []string) {
	for _, value := range sequence {
		FfiDestroyerString{}.Destroy(value)
	}
}

type FfiConverterSequenceProof struct{}

var FfiConverterSequenceProofINSTANCE = FfiConverterSequenceProof{}

func (c FfiConverterSequenceProof) Lift(rb RustBufferI) []*Proof {
	return LiftFromRustBuffer[[]*Proof](c, rb)
}

func (c FfiConverterSequenceProof) Read(reader io.Reader) []*Proof {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]*Proof, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterProofINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceProof) Lower(value []*Proof) C.RustBuffer {
	return LowerIntoRustBuffer[[]*Proof](c, value)
}

func (c FfiConverterSequenceProof) Write(writer io.Writer, value []*Proof) {
	if len(value) > math.MaxInt32 {
		panic("[]*Proof is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterProofINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceProof struct{}

func (FfiDestroyerSequenceProof) Destroy(sequence []*Proof) {
	for _, value := range sequence {
		FfiDestroyerProof{}.Destroy(value)
	}
}

type FfiConverterSequenceAmount struct{}

var FfiConverterSequenceAmountINSTANCE = FfiConverterSequenceAmount{}

func (c FfiConverterSequenceAmount) Lift(rb RustBufferI) []Amount {
	return LiftFromRustBuffer[[]Amount](c, rb)
}

func (c FfiConverterSequenceAmount) Read(reader io.Reader) []Amount {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]Amount, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterAmountINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceAmount) Lower(value []Amount) C.RustBuffer {
	return LowerIntoRustBuffer[[]Amount](c, value)
}

func (c FfiConverterSequenceAmount) Write(writer io.Writer, value []Amount) {
	if len(value) > math.MaxInt32 {
		panic("[]Amount is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterAmountINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceAmount struct{}

func (FfiDestroyerSequenceAmount) Destroy(sequence []Amount) {
	for _, value := range sequence {
		FfiDestroyerAmount{}.Destroy(value)
	}
}

type FfiConverterSequenceAuthProof struct{}

var FfiConverterSequenceAuthProofINSTANCE = FfiConverterSequenceAuthProof{}

func (c FfiConverterSequenceAuthProof) Lift(rb RustBufferI) []AuthProof {
	return LiftFromRustBuffer[[]AuthProof](c, rb)
}

func (c FfiConverterSequenceAuthProof) Read(reader io.Reader) []AuthProof {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]AuthProof, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterAuthProofINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceAuthProof) Lower(value []AuthProof) C.RustBuffer {
	return LowerIntoRustBuffer[[]AuthProof](c, value)
}

func (c FfiConverterSequenceAuthProof) Write(writer io.Writer, value []AuthProof) {
	if len(value) > math.MaxInt32 {
		panic("[]AuthProof is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterAuthProofINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceAuthProof struct{}

func (FfiDestroyerSequenceAuthProof) Destroy(sequence []AuthProof) {
	for _, value := range sequence {
		FfiDestroyerAuthProof{}.Destroy(value)
	}
}

type FfiConverterSequenceContactInfo struct{}

var FfiConverterSequenceContactInfoINSTANCE = FfiConverterSequenceContactInfo{}

func (c FfiConverterSequenceContactInfo) Lift(rb RustBufferI) []ContactInfo {
	return LiftFromRustBuffer[[]ContactInfo](c, rb)
}

func (c FfiConverterSequenceContactInfo) Read(reader io.Reader) []ContactInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]ContactInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterContactInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceContactInfo) Lower(value []ContactInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]ContactInfo](c, value)
}

func (c FfiConverterSequenceContactInfo) Write(writer io.Writer, value []ContactInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]ContactInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterContactInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceContactInfo struct{}

func (FfiDestroyerSequenceContactInfo) Destroy(sequence []ContactInfo) {
	for _, value := range sequence {
		FfiDestroyerContactInfo{}.Destroy(value)
	}
}

type FfiConverterSequenceKeySetInfo struct{}

var FfiConverterSequenceKeySetInfoINSTANCE = FfiConverterSequenceKeySetInfo{}

func (c FfiConverterSequenceKeySetInfo) Lift(rb RustBufferI) []KeySetInfo {
	return LiftFromRustBuffer[[]KeySetInfo](c, rb)
}

func (c FfiConverterSequenceKeySetInfo) Read(reader io.Reader) []KeySetInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]KeySetInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterKeySetInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceKeySetInfo) Lower(value []KeySetInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]KeySetInfo](c, value)
}

func (c FfiConverterSequenceKeySetInfo) Write(writer io.Writer, value []KeySetInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]KeySetInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterKeySetInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceKeySetInfo struct{}

func (FfiDestroyerSequenceKeySetInfo) Destroy(sequence []KeySetInfo) {
	for _, value := range sequence {
		FfiDestroyerKeySetInfo{}.Destroy(value)
	}
}

type FfiConverterSequenceMeltMethodSettings struct{}

var FfiConverterSequenceMeltMethodSettingsINSTANCE = FfiConverterSequenceMeltMethodSettings{}

func (c FfiConverterSequenceMeltMethodSettings) Lift(rb RustBufferI) []MeltMethodSettings {
	return LiftFromRustBuffer[[]MeltMethodSettings](c, rb)
}

func (c FfiConverterSequenceMeltMethodSettings) Read(reader io.Reader) []MeltMethodSettings {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]MeltMethodSettings, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterMeltMethodSettingsINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceMeltMethodSettings) Lower(value []MeltMethodSettings) C.RustBuffer {
	return LowerIntoRustBuffer[[]MeltMethodSettings](c, value)
}

func (c FfiConverterSequenceMeltMethodSettings) Write(writer io.Writer, value []MeltMethodSettings) {
	if len(value) > math.MaxInt32 {
		panic("[]MeltMethodSettings is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterMeltMethodSettingsINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceMeltMethodSettings struct{}

func (FfiDestroyerSequenceMeltMethodSettings) Destroy(sequence []MeltMethodSettings) {
	for _, value := range sequence {
		FfiDestroyerMeltMethodSettings{}.Destroy(value)
	}
}

type FfiConverterSequenceMeltQuote struct{}

var FfiConverterSequenceMeltQuoteINSTANCE = FfiConverterSequenceMeltQuote{}

func (c FfiConverterSequenceMeltQuote) Lift(rb RustBufferI) []MeltQuote {
	return LiftFromRustBuffer[[]MeltQuote](c, rb)
}

func (c FfiConverterSequenceMeltQuote) Read(reader io.Reader) []MeltQuote {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]MeltQuote, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterMeltQuoteINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceMeltQuote) Lower(value []MeltQuote) C.RustBuffer {
	return LowerIntoRustBuffer[[]MeltQuote](c, value)
}

func (c FfiConverterSequenceMeltQuote) Write(writer io.Writer, value []MeltQuote) {
	if len(value) > math.MaxInt32 {
		panic("[]MeltQuote is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterMeltQuoteINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceMeltQuote struct{}

func (FfiDestroyerSequenceMeltQuote) Destroy(sequence []MeltQuote) {
	for _, value := range sequence {
		FfiDestroyerMeltQuote{}.Destroy(value)
	}
}

type FfiConverterSequenceMintMethodSettings struct{}

var FfiConverterSequenceMintMethodSettingsINSTANCE = FfiConverterSequenceMintMethodSettings{}

func (c FfiConverterSequenceMintMethodSettings) Lift(rb RustBufferI) []MintMethodSettings {
	return LiftFromRustBuffer[[]MintMethodSettings](c, rb)
}

func (c FfiConverterSequenceMintMethodSettings) Read(reader io.Reader) []MintMethodSettings {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]MintMethodSettings, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterMintMethodSettingsINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceMintMethodSettings) Lower(value []MintMethodSettings) C.RustBuffer {
	return LowerIntoRustBuffer[[]MintMethodSettings](c, value)
}

func (c FfiConverterSequenceMintMethodSettings) Write(writer io.Writer, value []MintMethodSettings) {
	if len(value) > math.MaxInt32 {
		panic("[]MintMethodSettings is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterMintMethodSettingsINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceMintMethodSettings struct{}

func (FfiDestroyerSequenceMintMethodSettings) Destroy(sequence []MintMethodSettings) {
	for _, value := range sequence {
		FfiDestroyerMintMethodSettings{}.Destroy(value)
	}
}

type FfiConverterSequenceMintQuote struct{}

var FfiConverterSequenceMintQuoteINSTANCE = FfiConverterSequenceMintQuote{}

func (c FfiConverterSequenceMintQuote) Lift(rb RustBufferI) []MintQuote {
	return LiftFromRustBuffer[[]MintQuote](c, rb)
}

func (c FfiConverterSequenceMintQuote) Read(reader io.Reader) []MintQuote {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]MintQuote, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterMintQuoteINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceMintQuote) Lower(value []MintQuote) C.RustBuffer {
	return LowerIntoRustBuffer[[]MintQuote](c, value)
}

func (c FfiConverterSequenceMintQuote) Write(writer io.Writer, value []MintQuote) {
	if len(value) > math.MaxInt32 {
		panic("[]MintQuote is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterMintQuoteINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceMintQuote struct{}

func (FfiDestroyerSequenceMintQuote) Destroy(sequence []MintQuote) {
	for _, value := range sequence {
		FfiDestroyerMintQuote{}.Destroy(value)
	}
}

type FfiConverterSequenceMintUrl struct{}

var FfiConverterSequenceMintUrlINSTANCE = FfiConverterSequenceMintUrl{}

func (c FfiConverterSequenceMintUrl) Lift(rb RustBufferI) []MintUrl {
	return LiftFromRustBuffer[[]MintUrl](c, rb)
}

func (c FfiConverterSequenceMintUrl) Read(reader io.Reader) []MintUrl {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]MintUrl, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterMintUrlINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceMintUrl) Lower(value []MintUrl) C.RustBuffer {
	return LowerIntoRustBuffer[[]MintUrl](c, value)
}

func (c FfiConverterSequenceMintUrl) Write(writer io.Writer, value []MintUrl) {
	if len(value) > math.MaxInt32 {
		panic("[]MintUrl is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterMintUrlINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceMintUrl struct{}

func (FfiDestroyerSequenceMintUrl) Destroy(sequence []MintUrl) {
	for _, value := range sequence {
		FfiDestroyerMintUrl{}.Destroy(value)
	}
}

type FfiConverterSequenceProofInfo struct{}

var FfiConverterSequenceProofInfoINSTANCE = FfiConverterSequenceProofInfo{}

func (c FfiConverterSequenceProofInfo) Lift(rb RustBufferI) []ProofInfo {
	return LiftFromRustBuffer[[]ProofInfo](c, rb)
}

func (c FfiConverterSequenceProofInfo) Read(reader io.Reader) []ProofInfo {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]ProofInfo, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterProofInfoINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceProofInfo) Lower(value []ProofInfo) C.RustBuffer {
	return LowerIntoRustBuffer[[]ProofInfo](c, value)
}

func (c FfiConverterSequenceProofInfo) Write(writer io.Writer, value []ProofInfo) {
	if len(value) > math.MaxInt32 {
		panic("[]ProofInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterProofInfoINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceProofInfo struct{}

func (FfiDestroyerSequenceProofInfo) Destroy(sequence []ProofInfo) {
	for _, value := range sequence {
		FfiDestroyerProofInfo{}.Destroy(value)
	}
}

type FfiConverterSequenceProofStateUpdate struct{}

var FfiConverterSequenceProofStateUpdateINSTANCE = FfiConverterSequenceProofStateUpdate{}

func (c FfiConverterSequenceProofStateUpdate) Lift(rb RustBufferI) []ProofStateUpdate {
	return LiftFromRustBuffer[[]ProofStateUpdate](c, rb)
}

func (c FfiConverterSequenceProofStateUpdate) Read(reader io.Reader) []ProofStateUpdate {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]ProofStateUpdate, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterProofStateUpdateINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceProofStateUpdate) Lower(value []ProofStateUpdate) C.RustBuffer {
	return LowerIntoRustBuffer[[]ProofStateUpdate](c, value)
}

func (c FfiConverterSequenceProofStateUpdate) Write(writer io.Writer, value []ProofStateUpdate) {
	if len(value) > math.MaxInt32 {
		panic("[]ProofStateUpdate is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterProofStateUpdateINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceProofStateUpdate struct{}

func (FfiDestroyerSequenceProofStateUpdate) Destroy(sequence []ProofStateUpdate) {
	for _, value := range sequence {
		FfiDestroyerProofStateUpdate{}.Destroy(value)
	}
}

type FfiConverterSequenceProtectedEndpoint struct{}

var FfiConverterSequenceProtectedEndpointINSTANCE = FfiConverterSequenceProtectedEndpoint{}

func (c FfiConverterSequenceProtectedEndpoint) Lift(rb RustBufferI) []ProtectedEndpoint {
	return LiftFromRustBuffer[[]ProtectedEndpoint](c, rb)
}

func (c FfiConverterSequenceProtectedEndpoint) Read(reader io.Reader) []ProtectedEndpoint {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]ProtectedEndpoint, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterProtectedEndpointINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceProtectedEndpoint) Lower(value []ProtectedEndpoint) C.RustBuffer {
	return LowerIntoRustBuffer[[]ProtectedEndpoint](c, value)
}

func (c FfiConverterSequenceProtectedEndpoint) Write(writer io.Writer, value []ProtectedEndpoint) {
	if len(value) > math.MaxInt32 {
		panic("[]ProtectedEndpoint is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterProtectedEndpointINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceProtectedEndpoint struct{}

func (FfiDestroyerSequenceProtectedEndpoint) Destroy(sequence []ProtectedEndpoint) {
	for _, value := range sequence {
		FfiDestroyerProtectedEndpoint{}.Destroy(value)
	}
}

type FfiConverterSequencePublicKey struct{}

var FfiConverterSequencePublicKeyINSTANCE = FfiConverterSequencePublicKey{}

func (c FfiConverterSequencePublicKey) Lift(rb RustBufferI) []PublicKey {
	return LiftFromRustBuffer[[]PublicKey](c, rb)
}

func (c FfiConverterSequencePublicKey) Read(reader io.Reader) []PublicKey {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]PublicKey, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterPublicKeyINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequencePublicKey) Lower(value []PublicKey) C.RustBuffer {
	return LowerIntoRustBuffer[[]PublicKey](c, value)
}

func (c FfiConverterSequencePublicKey) Write(writer io.Writer, value []PublicKey) {
	if len(value) > math.MaxInt32 {
		panic("[]PublicKey is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterPublicKeyINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequencePublicKey struct{}

func (FfiDestroyerSequencePublicKey) Destroy(sequence []PublicKey) {
	for _, value := range sequence {
		FfiDestroyerPublicKey{}.Destroy(value)
	}
}

type FfiConverterSequenceSecretKey struct{}

var FfiConverterSequenceSecretKeyINSTANCE = FfiConverterSequenceSecretKey{}

func (c FfiConverterSequenceSecretKey) Lift(rb RustBufferI) []SecretKey {
	return LiftFromRustBuffer[[]SecretKey](c, rb)
}

func (c FfiConverterSequenceSecretKey) Read(reader io.Reader) []SecretKey {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]SecretKey, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterSecretKeyINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceSecretKey) Lower(value []SecretKey) C.RustBuffer {
	return LowerIntoRustBuffer[[]SecretKey](c, value)
}

func (c FfiConverterSequenceSecretKey) Write(writer io.Writer, value []SecretKey) {
	if len(value) > math.MaxInt32 {
		panic("[]SecretKey is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterSecretKeyINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceSecretKey struct{}

func (FfiDestroyerSequenceSecretKey) Destroy(sequence []SecretKey) {
	for _, value := range sequence {
		FfiDestroyerSecretKey{}.Destroy(value)
	}
}

type FfiConverterSequenceTransaction struct{}

var FfiConverterSequenceTransactionINSTANCE = FfiConverterSequenceTransaction{}

func (c FfiConverterSequenceTransaction) Lift(rb RustBufferI) []Transaction {
	return LiftFromRustBuffer[[]Transaction](c, rb)
}

func (c FfiConverterSequenceTransaction) Read(reader io.Reader) []Transaction {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]Transaction, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTransactionINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTransaction) Lower(value []Transaction) C.RustBuffer {
	return LowerIntoRustBuffer[[]Transaction](c, value)
}

func (c FfiConverterSequenceTransaction) Write(writer io.Writer, value []Transaction) {
	if len(value) > math.MaxInt32 {
		panic("[]Transaction is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTransactionINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTransaction struct{}

func (FfiDestroyerSequenceTransaction) Destroy(sequence []Transaction) {
	for _, value := range sequence {
		FfiDestroyerTransaction{}.Destroy(value)
	}
}

type FfiConverterSequenceCurrencyUnit struct{}

var FfiConverterSequenceCurrencyUnitINSTANCE = FfiConverterSequenceCurrencyUnit{}

func (c FfiConverterSequenceCurrencyUnit) Lift(rb RustBufferI) []CurrencyUnit {
	return LiftFromRustBuffer[[]CurrencyUnit](c, rb)
}

func (c FfiConverterSequenceCurrencyUnit) Read(reader io.Reader) []CurrencyUnit {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]CurrencyUnit, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterCurrencyUnitINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceCurrencyUnit) Lower(value []CurrencyUnit) C.RustBuffer {
	return LowerIntoRustBuffer[[]CurrencyUnit](c, value)
}

func (c FfiConverterSequenceCurrencyUnit) Write(writer io.Writer, value []CurrencyUnit) {
	if len(value) > math.MaxInt32 {
		panic("[]CurrencyUnit is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterCurrencyUnitINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceCurrencyUnit struct{}

func (FfiDestroyerSequenceCurrencyUnit) Destroy(sequence []CurrencyUnit) {
	for _, value := range sequence {
		FfiDestroyerCurrencyUnit{}.Destroy(value)
	}
}

type FfiConverterSequenceProofState struct{}

var FfiConverterSequenceProofStateINSTANCE = FfiConverterSequenceProofState{}

func (c FfiConverterSequenceProofState) Lift(rb RustBufferI) []ProofState {
	return LiftFromRustBuffer[[]ProofState](c, rb)
}

func (c FfiConverterSequenceProofState) Read(reader io.Reader) []ProofState {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]ProofState, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterProofStateINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceProofState) Lower(value []ProofState) C.RustBuffer {
	return LowerIntoRustBuffer[[]ProofState](c, value)
}

func (c FfiConverterSequenceProofState) Write(writer io.Writer, value []ProofState) {
	if len(value) > math.MaxInt32 {
		panic("[]ProofState is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterProofStateINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceProofState struct{}

func (FfiDestroyerSequenceProofState) Destroy(sequence []ProofState) {
	for _, value := range sequence {
		FfiDestroyerProofState{}.Destroy(value)
	}
}

type FfiConverterSequenceSpendingConditions struct{}

var FfiConverterSequenceSpendingConditionsINSTANCE = FfiConverterSequenceSpendingConditions{}

func (c FfiConverterSequenceSpendingConditions) Lift(rb RustBufferI) []SpendingConditions {
	return LiftFromRustBuffer[[]SpendingConditions](c, rb)
}

func (c FfiConverterSequenceSpendingConditions) Read(reader io.Reader) []SpendingConditions {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]SpendingConditions, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterSpendingConditionsINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceSpendingConditions) Lower(value []SpendingConditions) C.RustBuffer {
	return LowerIntoRustBuffer[[]SpendingConditions](c, value)
}

func (c FfiConverterSequenceSpendingConditions) Write(writer io.Writer, value []SpendingConditions) {
	if len(value) > math.MaxInt32 {
		panic("[]SpendingConditions is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterSpendingConditionsINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceSpendingConditions struct{}

func (FfiDestroyerSequenceSpendingConditions) Destroy(sequence []SpendingConditions) {
	for _, value := range sequence {
		FfiDestroyerSpendingConditions{}.Destroy(value)
	}
}

type FfiConverterMapUint64String struct{}

var FfiConverterMapUint64StringINSTANCE = FfiConverterMapUint64String{}

func (c FfiConverterMapUint64String) Lift(rb RustBufferI) map[uint64]string {
	return LiftFromRustBuffer[map[uint64]string](c, rb)
}

func (_ FfiConverterMapUint64String) Read(reader io.Reader) map[uint64]string {
	result := make(map[uint64]string)
	length := readInt32(reader)
	for i := int32(0); i < length; i++ {
		key := FfiConverterUint64INSTANCE.Read(reader)
		value := FfiConverterStringINSTANCE.Read(reader)
		result[key] = value
	}
	return result
}

func (c FfiConverterMapUint64String) Lower(value map[uint64]string) C.RustBuffer {
	return LowerIntoRustBuffer[map[uint64]string](c, value)
}

func (_ FfiConverterMapUint64String) Write(writer io.Writer, mapValue map[uint64]string) {
	if len(mapValue) > math.MaxInt32 {
		panic("map[uint64]string is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(mapValue)))
	for key, value := range mapValue {
		FfiConverterUint64INSTANCE.Write(writer, key)
		FfiConverterStringINSTANCE.Write(writer, value)
	}
}

type FfiDestroyerMapUint64String struct{}

func (_ FfiDestroyerMapUint64String) Destroy(mapValue map[uint64]string) {
	for key, value := range mapValue {
		FfiDestroyerUint64{}.Destroy(key)
		FfiDestroyerString{}.Destroy(value)
	}
}

type FfiConverterMapStringString struct{}

var FfiConverterMapStringStringINSTANCE = FfiConverterMapStringString{}

func (c FfiConverterMapStringString) Lift(rb RustBufferI) map[string]string {
	return LiftFromRustBuffer[map[string]string](c, rb)
}

func (_ FfiConverterMapStringString) Read(reader io.Reader) map[string]string {
	result := make(map[string]string)
	length := readInt32(reader)
	for i := int32(0); i < length; i++ {
		key := FfiConverterStringINSTANCE.Read(reader)
		value := FfiConverterStringINSTANCE.Read(reader)
		result[key] = value
	}
	return result
}

func (c FfiConverterMapStringString) Lower(value map[string]string) C.RustBuffer {
	return LowerIntoRustBuffer[map[string]string](c, value)
}

func (_ FfiConverterMapStringString) Write(writer io.Writer, mapValue map[string]string) {
	if len(mapValue) > math.MaxInt32 {
		panic("map[string]string is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(mapValue)))
	for key, value := range mapValue {
		FfiConverterStringINSTANCE.Write(writer, key)
		FfiConverterStringINSTANCE.Write(writer, value)
	}
}

type FfiDestroyerMapStringString struct{}

func (_ FfiDestroyerMapStringString) Destroy(mapValue map[string]string) {
	for key, value := range mapValue {
		FfiDestroyerString{}.Destroy(key)
		FfiDestroyerString{}.Destroy(value)
	}
}

type FfiConverterMapStringAmount struct{}

var FfiConverterMapStringAmountINSTANCE = FfiConverterMapStringAmount{}

func (c FfiConverterMapStringAmount) Lift(rb RustBufferI) map[string]Amount {
	return LiftFromRustBuffer[map[string]Amount](c, rb)
}

func (_ FfiConverterMapStringAmount) Read(reader io.Reader) map[string]Amount {
	result := make(map[string]Amount)
	length := readInt32(reader)
	for i := int32(0); i < length; i++ {
		key := FfiConverterStringINSTANCE.Read(reader)
		value := FfiConverterAmountINSTANCE.Read(reader)
		result[key] = value
	}
	return result
}

func (c FfiConverterMapStringAmount) Lower(value map[string]Amount) C.RustBuffer {
	return LowerIntoRustBuffer[map[string]Amount](c, value)
}

func (_ FfiConverterMapStringAmount) Write(writer io.Writer, mapValue map[string]Amount) {
	if len(mapValue) > math.MaxInt32 {
		panic("map[string]Amount is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(mapValue)))
	for key, value := range mapValue {
		FfiConverterStringINSTANCE.Write(writer, key)
		FfiConverterAmountINSTANCE.Write(writer, value)
	}
}

type FfiDestroyerMapStringAmount struct{}

func (_ FfiDestroyerMapStringAmount) Destroy(mapValue map[string]Amount) {
	for key, value := range mapValue {
		FfiDestroyerString{}.Destroy(key)
		FfiDestroyerAmount{}.Destroy(value)
	}
}

type FfiConverterMapStringSequenceProof struct{}

var FfiConverterMapStringSequenceProofINSTANCE = FfiConverterMapStringSequenceProof{}

func (c FfiConverterMapStringSequenceProof) Lift(rb RustBufferI) map[string][]*Proof {
	return LiftFromRustBuffer[map[string][]*Proof](c, rb)
}

func (_ FfiConverterMapStringSequenceProof) Read(reader io.Reader) map[string][]*Proof {
	result := make(map[string][]*Proof)
	length := readInt32(reader)
	for i := int32(0); i < length; i++ {
		key := FfiConverterStringINSTANCE.Read(reader)
		value := FfiConverterSequenceProofINSTANCE.Read(reader)
		result[key] = value
	}
	return result
}

func (c FfiConverterMapStringSequenceProof) Lower(value map[string][]*Proof) C.RustBuffer {
	return LowerIntoRustBuffer[map[string][]*Proof](c, value)
}

func (_ FfiConverterMapStringSequenceProof) Write(writer io.Writer, mapValue map[string][]*Proof) {
	if len(mapValue) > math.MaxInt32 {
		panic("map[string][]*Proof is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(mapValue)))
	for key, value := range mapValue {
		FfiConverterStringINSTANCE.Write(writer, key)
		FfiConverterSequenceProofINSTANCE.Write(writer, value)
	}
}

type FfiDestroyerMapStringSequenceProof struct{}

func (_ FfiDestroyerMapStringSequenceProof) Destroy(mapValue map[string][]*Proof) {
	for key, value := range mapValue {
		FfiDestroyerString{}.Destroy(key)
		FfiDestroyerSequenceProof{}.Destroy(value)
	}
}

type FfiConverterMapMintUrlOptionalMintInfo struct{}

var FfiConverterMapMintUrlOptionalMintInfoINSTANCE = FfiConverterMapMintUrlOptionalMintInfo{}

func (c FfiConverterMapMintUrlOptionalMintInfo) Lift(rb RustBufferI) map[MintUrl]*MintInfo {
	return LiftFromRustBuffer[map[MintUrl]*MintInfo](c, rb)
}

func (_ FfiConverterMapMintUrlOptionalMintInfo) Read(reader io.Reader) map[MintUrl]*MintInfo {
	result := make(map[MintUrl]*MintInfo)
	length := readInt32(reader)
	for i := int32(0); i < length; i++ {
		key := FfiConverterMintUrlINSTANCE.Read(reader)
		value := FfiConverterOptionalMintInfoINSTANCE.Read(reader)
		result[key] = value
	}
	return result
}

func (c FfiConverterMapMintUrlOptionalMintInfo) Lower(value map[MintUrl]*MintInfo) C.RustBuffer {
	return LowerIntoRustBuffer[map[MintUrl]*MintInfo](c, value)
}

func (_ FfiConverterMapMintUrlOptionalMintInfo) Write(writer io.Writer, mapValue map[MintUrl]*MintInfo) {
	if len(mapValue) > math.MaxInt32 {
		panic("map[MintUrl]*MintInfo is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(mapValue)))
	for key, value := range mapValue {
		FfiConverterMintUrlINSTANCE.Write(writer, key)
		FfiConverterOptionalMintInfoINSTANCE.Write(writer, value)
	}
}

type FfiDestroyerMapMintUrlOptionalMintInfo struct{}

func (_ FfiDestroyerMapMintUrlOptionalMintInfo) Destroy(mapValue map[MintUrl]*MintInfo) {
	for key, value := range mapValue {
		FfiDestroyerMintUrl{}.Destroy(key)
		FfiDestroyerOptionalMintInfo{}.Destroy(value)
	}
}

const (
	uniffiRustFuturePollReady      int8 = 0
	uniffiRustFuturePollMaybeReady int8 = 1
)

type rustFuturePollFunc func(C.uint64_t, C.UniffiRustFutureContinuationCallback, C.uint64_t)
type rustFutureCompleteFunc[T any] func(C.uint64_t, *C.RustCallStatus) T
type rustFutureFreeFunc func(C.uint64_t)

//export cdk_ffi_uniffiFutureContinuationCallback
func cdk_ffi_uniffiFutureContinuationCallback(data C.uint64_t, pollResult C.int8_t) {
	h := cgo.Handle(uintptr(data))
	waiter := h.Value().(chan int8)
	waiter <- int8(pollResult)
}

func uniffiRustCallAsync[E any, T any, F any](
	errConverter BufReader[*E],
	completeFunc rustFutureCompleteFunc[F],
	liftFunc func(F) T,
	rustFuture C.uint64_t,
	pollFunc rustFuturePollFunc,
	freeFunc rustFutureFreeFunc,
) (T, *E) {
	defer freeFunc(rustFuture)

	pollResult := int8(-1)
	waiter := make(chan int8, 1)

	chanHandle := cgo.NewHandle(waiter)
	defer chanHandle.Delete()

	for pollResult != uniffiRustFuturePollReady {
		pollFunc(
			rustFuture,
			(C.UniffiRustFutureContinuationCallback)(C.cdk_ffi_uniffiFutureContinuationCallback),
			C.uint64_t(chanHandle),
		)
		pollResult = <-waiter
	}

	var goValue T
	var ffiValue F
	var err *E

	ffiValue, err = rustCallWithError(errConverter, func(status *C.RustCallStatus) F {
		return completeFunc(rustFuture, status)
	})
	if err != nil {
		return goValue, err
	}
	return liftFunc(ffiValue), nil
}

//export cdk_ffi_uniffiFreeGorutine
func cdk_ffi_uniffiFreeGorutine(data C.uint64_t) {
	handle := cgo.Handle(uintptr(data))
	defer handle.Delete()

	guard := handle.Value().(chan struct{})
	guard <- struct{}{}
}

// Factory helpers returning a CDK wallet database behind the FFI trait
func CreateWalletDb(backend WalletDbBackend) (WalletDatabase, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_cdk_ffi_fn_func_create_wallet_db(FfiConverterWalletDbBackendINSTANCE.Lower(backend), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue WalletDatabase
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterWalletDatabaseINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode AuthProof from JSON string
func DecodeAuthProof(json string) (AuthProof, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_auth_proof(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue AuthProof
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterAuthProofINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode Conditions from JSON string
func DecodeConditions(json string) (Conditions, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_conditions(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Conditions
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterConditionsINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode ContactInfo from JSON string
func DecodeContactInfo(json string) (ContactInfo, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_contact_info(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue ContactInfo
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterContactInfoINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode KeySet from JSON string
func DecodeKeySet(json string) (KeySet, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_key_set(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue KeySet
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterKeySetINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode KeySetInfo from JSON string
func DecodeKeySetInfo(json string) (KeySetInfo, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_key_set_info(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue KeySetInfo
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterKeySetInfoINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode Keys from JSON string
func DecodeKeys(json string) (Keys, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_keys(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Keys
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterKeysINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode MeltQuote from JSON string
func DecodeMeltQuote(json string) (MeltQuote, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_melt_quote(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue MeltQuote
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterMeltQuoteINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode MintInfo from JSON string
func DecodeMintInfo(json string) (MintInfo, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_mint_info(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue MintInfo
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterMintInfoINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode MintQuote from JSON string
func DecodeMintQuote(json string) (MintQuote, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_mint_quote(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue MintQuote
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterMintQuoteINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode MintVersion from JSON string
func DecodeMintVersion(json string) (MintVersion, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_mint_version(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue MintVersion
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterMintVersionINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode Nuts from JSON string
func DecodeNuts(json string) (Nuts, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_nuts(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Nuts
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNutsINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode ProofInfo from JSON string
func DecodeProofInfo(json string) (ProofInfo, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_proof_info(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue ProofInfo
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterProofInfoINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode ProofStateUpdate from JSON string
func DecodeProofStateUpdate(json string) (ProofStateUpdate, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_proof_state_update(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue ProofStateUpdate
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterProofStateUpdateINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode ReceiveOptions from JSON string
func DecodeReceiveOptions(json string) (ReceiveOptions, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_receive_options(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue ReceiveOptions
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterReceiveOptionsINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode SendMemo from JSON string
func DecodeSendMemo(json string) (SendMemo, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_send_memo(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue SendMemo
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSendMemoINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode SendOptions from JSON string
func DecodeSendOptions(json string) (SendOptions, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_send_options(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue SendOptions
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSendOptionsINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode SubscribeParams from JSON string
func DecodeSubscribeParams(json string) (SubscribeParams, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_subscribe_params(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue SubscribeParams
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterSubscribeParamsINSTANCE.Lift(_uniffiRV), nil
	}
}

// Decode Transaction from JSON string
func DecodeTransaction(json string) (Transaction, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_decode_transaction(FfiConverterStringINSTANCE.Lower(json), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Transaction
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTransactionINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode AuthProof to JSON string
func EncodeAuthProof(proof AuthProof) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_auth_proof(FfiConverterAuthProofINSTANCE.Lower(proof), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode Conditions to JSON string
func EncodeConditions(conditions Conditions) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_conditions(FfiConverterConditionsINSTANCE.Lower(conditions), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode ContactInfo to JSON string
func EncodeContactInfo(info ContactInfo) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_contact_info(FfiConverterContactInfoINSTANCE.Lower(info), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode KeySet to JSON string
func EncodeKeySet(keyset KeySet) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_key_set(FfiConverterKeySetINSTANCE.Lower(keyset), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode KeySetInfo to JSON string
func EncodeKeySetInfo(info KeySetInfo) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_key_set_info(FfiConverterKeySetInfoINSTANCE.Lower(info), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode Keys to JSON string
func EncodeKeys(keys Keys) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_keys(FfiConverterKeysINSTANCE.Lower(keys), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode MeltQuote to JSON string
func EncodeMeltQuote(quote MeltQuote) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_melt_quote(FfiConverterMeltQuoteINSTANCE.Lower(quote), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode MintInfo to JSON string
func EncodeMintInfo(info MintInfo) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_mint_info(FfiConverterMintInfoINSTANCE.Lower(info), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode MintQuote to JSON string
func EncodeMintQuote(quote MintQuote) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_mint_quote(FfiConverterMintQuoteINSTANCE.Lower(quote), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode MintVersion to JSON string
func EncodeMintVersion(version MintVersion) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_mint_version(FfiConverterMintVersionINSTANCE.Lower(version), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode Nuts to JSON string
func EncodeNuts(nuts Nuts) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_nuts(FfiConverterNutsINSTANCE.Lower(nuts), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode ProofInfo to JSON string
func EncodeProofInfo(info ProofInfo) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_proof_info(FfiConverterProofInfoINSTANCE.Lower(info), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode ProofStateUpdate to JSON string
func EncodeProofStateUpdate(update ProofStateUpdate) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_proof_state_update(FfiConverterProofStateUpdateINSTANCE.Lower(update), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode ReceiveOptions to JSON string
func EncodeReceiveOptions(options ReceiveOptions) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_receive_options(FfiConverterReceiveOptionsINSTANCE.Lower(options), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode SendMemo to JSON string
func EncodeSendMemo(memo SendMemo) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_send_memo(FfiConverterSendMemoINSTANCE.Lower(memo), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode SendOptions to JSON string
func EncodeSendOptions(options SendOptions) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_send_options(FfiConverterSendOptionsINSTANCE.Lower(options), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode SubscribeParams to JSON string
func EncodeSubscribeParams(params SubscribeParams) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_subscribe_params(FfiConverterSubscribeParamsINSTANCE.Lower(params), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Encode Transaction to JSON string
func EncodeTransaction(transaction Transaction) (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_encode_transaction(FfiConverterTransactionINSTANCE.Lower(transaction), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Generates a new random mnemonic phrase
func GenerateMnemonic() (string, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_generate_mnemonic(_uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Converts a mnemonic phrase to its entropy bytes
func MnemonicToEntropy(mnemonic string) ([]byte, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[FfiError](FfiConverterFfiError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_cdk_ffi_fn_func_mnemonic_to_entropy(FfiConverterStringINSTANCE.Lower(mnemonic), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), nil
	}
}
