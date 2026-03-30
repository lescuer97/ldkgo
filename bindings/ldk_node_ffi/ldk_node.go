package ldk_node

// #include <ldk_node.h>
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

// C.RustBuffer fields exposed as an interface so they can be accessed in different Go packages.
// See https://github.com/golang/go/issues/13467
type ExternalCRustBuffer interface {
	Data() unsafe.Pointer
	Len() uint64
	Capacity() uint64
}

func RustBufferFromC(b C.RustBuffer) ExternalCRustBuffer {
	return GoRustBuffer{
		inner: b,
	}
}

func CFromRustBuffer(b ExternalCRustBuffer) C.RustBuffer {
	return C.RustBuffer{
		capacity: C.uint64_t(b.Capacity()),
		len:      C.uint64_t(b.Len()),
		data:     (*C.uchar)(b.Data()),
	}
}

func RustBufferFromExternal(b ExternalCRustBuffer) GoRustBuffer {
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
		C.ffi_ldk_node_rustbuffer_free(cb.inner, status)
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
		return C.ffi_ldk_node_rustbuffer_from_bytes(foreign, status)
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

	FfiConverterLogWriterINSTANCE.register()
	FfiConverterVssHeaderProviderINSTANCE.register()
	uniffiCheckChecksums()
}

func uniffiCheckChecksums() {
	// Get the bindings contract version from our ComponentInterface
	bindingsContractVersion := 29
	// Get the scaffolding contract version by calling the into the dylib
	scaffoldingContractVersion := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint32_t {
		return C.ffi_ldk_node_uniffi_contract_version()
	})
	if bindingsContractVersion != int(scaffoldingContractVersion) {
		// If this happens try cleaning and rebuilding your project
		panic("ldk_node: UniFFI contract version mismatch")
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_func_default_config()
		})
		if checksum != 55381 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_func_default_config: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_func_generate_entropy_mnemonic()
		})
		if checksum != 15455 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_func_generate_entropy_mnemonic: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_build()
		})
		if checksum != 64768 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_build: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_build_with_fs_store()
		})
		if checksum != 42069 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_build_with_fs_store: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_build_with_vss_store()
		})
		if checksum != 9022 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_build_with_vss_store: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_build_with_vss_store_and_fixed_headers()
		})
		if checksum != 64024 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_build_with_vss_store_and_fixed_headers: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_build_with_vss_store_and_header_provider()
		})
		if checksum != 29566 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_build_with_vss_store_and_header_provider: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_build_with_vss_store_and_lnurl_auth()
		})
		if checksum != 8141 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_build_with_vss_store_and_lnurl_auth: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_announcement_addresses()
		})
		if checksum != 21735 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_announcement_addresses: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_async_payments_role()
		})
		if checksum != 16463 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_async_payments_role: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_chain_source_bitcoind_rest()
		})
		if checksum != 37382 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_chain_source_bitcoind_rest: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_chain_source_bitcoind_rpc()
		})
		if checksum != 2111 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_chain_source_bitcoind_rpc: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_chain_source_electrum()
		})
		if checksum != 55552 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_chain_source_electrum: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_chain_source_esplora()
		})
		if checksum != 1781 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_chain_source_esplora: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_custom_logger()
		})
		if checksum != 51232 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_custom_logger: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_filesystem_logger()
		})
		if checksum != 10249 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_filesystem_logger: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_gossip_source_p2p()
		})
		if checksum != 9279 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_gossip_source_p2p: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_gossip_source_rgs()
		})
		if checksum != 64312 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_gossip_source_rgs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_liquidity_source_lsps1()
		})
		if checksum != 30329 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_liquidity_source_lsps1: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_liquidity_source_lsps2()
		})
		if checksum != 20666 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_liquidity_source_lsps2: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_listening_addresses()
		})
		if checksum != 57941 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_listening_addresses: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_log_facade_logger()
		})
		if checksum != 58410 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_log_facade_logger: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_network()
		})
		if checksum != 27539 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_network: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_node_alias()
		})
		if checksum != 18342 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_node_alias: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_pathfinding_scores_source()
		})
		if checksum != 63501 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_pathfinding_scores_source: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_storage_dir_path()
		})
		if checksum != 59019 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_storage_dir_path: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_tor_config()
		})
		if checksum != 53118 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_tor_config: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_builder_set_wallet_recovery_mode()
		})
		if checksum != 6703 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_builder_set_wallet_recovery_mode: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_feerate_to_sat_per_kwu()
		})
		if checksum != 58911 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_feerate_to_sat_per_kwu: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_feerate_to_sat_per_vb_ceil()
		})
		if checksum != 58575 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_feerate_to_sat_per_vb_ceil: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_feerate_to_sat_per_vb_floor()
		})
		if checksum != 59617 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_feerate_to_sat_per_vb_floor: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_logwriter_log()
		})
		if checksum != 3299 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_logwriter_log: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_announcement_addresses()
		})
		if checksum != 26379 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_announcement_addresses: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_bolt11_payment()
		})
		if checksum != 41402 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_bolt11_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_bolt12_payment()
		})
		if checksum != 49254 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_bolt12_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_close_channel()
		})
		if checksum != 19761 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_close_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_config()
		})
		if checksum != 7511 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_config: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_connect()
		})
		if checksum != 4107 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_connect: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_disconnect()
		})
		if checksum != 28878 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_disconnect: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_event_handled()
		})
		if checksum != 38712 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_event_handled: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_export_pathfinding_scores()
		})
		if checksum != 62331 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_export_pathfinding_scores: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_force_close_channel()
		})
		if checksum != 9265 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_force_close_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_list_balances()
		})
		if checksum != 57528 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_list_balances: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_list_channels()
		})
		if checksum != 7954 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_list_channels: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_list_payments()
		})
		if checksum != 35002 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_list_payments: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_list_peers()
		})
		if checksum != 14889 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_list_peers: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_listening_addresses()
		})
		if checksum != 2357 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_listening_addresses: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_lnurl_auth()
		})
		if checksum != 45487 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_lnurl_auth: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_lsps1_liquidity()
		})
		if checksum != 38201 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_lsps1_liquidity: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_network_graph()
		})
		if checksum != 2695 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_network_graph: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_next_event()
		})
		if checksum != 7682 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_next_event: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_next_event_async()
		})
		if checksum != 25426 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_next_event_async: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_node_alias()
		})
		if checksum != 54081 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_node_alias: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_node_id()
		})
		if checksum != 32528 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_node_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_onchain_payment()
		})
		if checksum != 6092 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_onchain_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_open_announced_channel()
		})
		if checksum != 42749 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_open_announced_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_open_announced_channel_with_all()
		})
		if checksum != 58472 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_open_announced_channel_with_all: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_open_channel()
		})
		if checksum != 7411 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_open_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_open_channel_with_all()
		})
		if checksum != 26760 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_open_channel_with_all: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_payment()
		})
		if checksum != 22178 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_remove_payment()
		})
		if checksum != 22427 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_remove_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_sign_message()
		})
		if checksum != 49319 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_sign_message: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_splice_in()
		})
		if checksum != 2355 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_splice_in: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_splice_in_with_all()
		})
		if checksum != 42260 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_splice_in_with_all: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_splice_out()
		})
		if checksum != 12130 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_splice_out: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_spontaneous_payment()
		})
		if checksum != 37403 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_spontaneous_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_start()
		})
		if checksum != 58480 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_start: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_status()
		})
		if checksum != 55952 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_status: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_stop()
		})
		if checksum != 42188 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_stop: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_sync_wallets()
		})
		if checksum != 32474 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_sync_wallets: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_unified_payment()
		})
		if checksum != 33932 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_unified_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_update_channel_config()
		})
		if checksum != 22596 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_update_channel_config: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_verify_signature()
		})
		if checksum != 60677 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_verify_signature: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_node_wait_next_event()
		})
		if checksum != 55101 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_node_wait_next_event: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_vssheaderprovider_get_headers()
		})
		if checksum != 53392 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_vssheaderprovider_get_headers: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_amount_milli_satoshis()
		})
		if checksum != 32418 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_amount_milli_satoshis: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_currency()
		})
		if checksum != 23097 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_currency: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_expiry_time_seconds()
		})
		if checksum != 13550 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_expiry_time_seconds: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_fallback_addresses()
		})
		if checksum != 43969 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_fallback_addresses: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_invoice_description()
		})
		if checksum != 58644 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_invoice_description: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_is_expired()
		})
		if checksum != 7799 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_is_expired: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_min_final_cltv_expiry_delta()
		})
		if checksum != 55712 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_min_final_cltv_expiry_delta: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_network()
		})
		if checksum != 48075 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_network: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_payment_hash()
		})
		if checksum != 30556 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_payment_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_payment_secret()
		})
		if checksum != 2591 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_payment_secret: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_recover_payee_pub_key()
		})
		if checksum != 29418 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_recover_payee_pub_key: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_route_hints()
		})
		if checksum != 40413 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_route_hints: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_seconds_since_epoch()
		})
		if checksum != 29057 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_seconds_since_epoch: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_seconds_until_expiry()
		})
		if checksum != 40162 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_seconds_until_expiry: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_signable_hash()
		})
		if checksum != 17620 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_signable_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11invoice_would_expire()
		})
		if checksum != 31077 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11invoice_would_expire: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_claim_for_hash()
		})
		if checksum != 20569 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_claim_for_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_fail_for_hash()
		})
		if checksum != 40917 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_fail_for_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive()
		})
		if checksum != 29930 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive_for_hash()
		})
		if checksum != 50974 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive_for_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount()
		})
		if checksum != 3285 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount_for_hash()
		})
		if checksum != 18560 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount_for_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount_via_jit_channel()
		})
		if checksum != 17693 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount_via_jit_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount_via_jit_channel_for_hash()
		})
		if checksum != 52380 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive_variable_amount_via_jit_channel_for_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive_via_jit_channel()
		})
		if checksum != 8559 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive_via_jit_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_receive_via_jit_channel_for_hash()
		})
		if checksum != 30774 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_receive_via_jit_channel_for_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_send()
		})
		if checksum != 37617 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_send: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_send_probes()
		})
		if checksum != 2041 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_send_probes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_send_probes_using_amount()
		})
		if checksum != 27145 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_send_probes_using_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt11payment_send_using_amount()
		})
		if checksum != 24457 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt11payment_send_using_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_absolute_expiry_seconds()
		})
		if checksum != 64960 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_absolute_expiry_seconds: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_amount()
		})
		if checksum != 49725 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_amount_msats()
		})
		if checksum != 60333 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_amount_msats: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_chain()
		})
		if checksum != 58655 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_chain: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_created_at()
		})
		if checksum != 49933 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_created_at: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_encode()
		})
		if checksum != 5305 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_encode: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_fallback_addresses()
		})
		if checksum != 44968 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_fallback_addresses: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_invoice_description()
		})
		if checksum != 40225 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_invoice_description: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_is_expired()
		})
		if checksum != 25200 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_is_expired: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_issuer()
		})
		if checksum != 30831 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_issuer: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_issuer_signing_pubkey()
		})
		if checksum != 64809 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_issuer_signing_pubkey: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_metadata()
		})
		if checksum != 46678 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_metadata: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_offer_chains()
		})
		if checksum != 26217 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_offer_chains: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_payer_note()
		})
		if checksum != 55340 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_payer_note: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_payer_signing_pubkey()
		})
		if checksum != 16324 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_payer_signing_pubkey: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_payment_hash()
		})
		if checksum != 26138 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_payment_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_quantity()
		})
		if checksum != 2731 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_quantity: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_relative_expiry()
		})
		if checksum != 2637 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_relative_expiry: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_signable_hash()
		})
		if checksum != 8693 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_signable_hash: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12invoice_signing_pubkey()
		})
		if checksum != 40070 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12invoice_signing_pubkey: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_blinded_paths_for_async_recipient()
		})
		if checksum != 63122 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_blinded_paths_for_async_recipient: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_initiate_refund()
		})
		if checksum != 1556 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_initiate_refund: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_receive()
		})
		if checksum != 62366 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_receive: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_receive_async()
		})
		if checksum != 18142 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_receive_async: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_receive_variable_amount()
		})
		if checksum != 38705 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_receive_variable_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_request_refund_payment()
		})
		if checksum != 64220 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_request_refund_payment: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_send()
		})
		if checksum != 39845 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_send: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_send_using_amount()
		})
		if checksum != 58098 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_send_using_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_bolt12payment_set_paths_to_static_invoice_server()
		})
		if checksum != 4432 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_bolt12payment_set_paths_to_static_invoice_server: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_humanreadablename_domain()
		})
		if checksum != 24546 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_humanreadablename_domain: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_humanreadablename_user()
		})
		if checksum != 19941 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_humanreadablename_user: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_lsps1liquidity_check_order_status()
		})
		if checksum != 56905 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_lsps1liquidity_check_order_status: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_lsps1liquidity_request_channel()
		})
		if checksum != 8762 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_lsps1liquidity_request_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_networkgraph_channel()
		})
		if checksum != 19476 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_networkgraph_channel: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_networkgraph_list_channels()
		})
		if checksum != 15785 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_networkgraph_list_channels: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_networkgraph_list_nodes()
		})
		if checksum != 362 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_networkgraph_list_nodes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_networkgraph_node()
		})
		if checksum != 57416 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_networkgraph_node: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_absolute_expiry_seconds()
		})
		if checksum != 63488 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_absolute_expiry_seconds: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_amount()
		})
		if checksum != 57542 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_chains()
		})
		if checksum != 452 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_chains: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_expects_quantity()
		})
		if checksum != 63436 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_expects_quantity: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_id()
		})
		if checksum != 37816 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_id: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_is_expired()
		})
		if checksum != 28193 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_is_expired: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_is_valid_quantity()
		})
		if checksum != 52411 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_is_valid_quantity: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_issuer()
		})
		if checksum != 3667 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_issuer: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_issuer_signing_pubkey()
		})
		if checksum != 24676 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_issuer_signing_pubkey: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_metadata()
		})
		if checksum != 38207 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_metadata: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_offer_description()
		})
		if checksum != 28248 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_offer_description: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_offer_supports_chain()
		})
		if checksum != 55723 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_offer_supports_chain: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_onchainpayment_bump_fee_rbf()
		})
		if checksum != 54102 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_onchainpayment_bump_fee_rbf: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_onchainpayment_new_address()
		})
		if checksum != 43992 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_onchainpayment_new_address: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_onchainpayment_send_all_to_address()
		})
		if checksum != 37128 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_onchainpayment_send_all_to_address: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_onchainpayment_send_to_address()
		})
		if checksum != 21558 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_onchainpayment_send_to_address: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_absolute_expiry_seconds()
		})
		if checksum != 1700 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_absolute_expiry_seconds: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_amount_msats()
		})
		if checksum != 14905 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_amount_msats: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_chain()
		})
		if checksum != 65505 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_chain: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_is_expired()
		})
		if checksum != 42373 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_is_expired: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_issuer()
		})
		if checksum != 16526 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_issuer: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_payer_metadata()
		})
		if checksum != 39486 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_payer_metadata: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_payer_note()
		})
		if checksum != 4011 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_payer_note: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_payer_signing_pubkey()
		})
		if checksum != 27530 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_payer_signing_pubkey: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_quantity()
		})
		if checksum != 7212 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_quantity: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_refund_refund_description()
		})
		if checksum != 28138 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_refund_refund_description: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_spontaneouspayment_send()
		})
		if checksum != 46594 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_spontaneouspayment_send: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_spontaneouspayment_send_probes()
		})
		if checksum != 14653 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_spontaneouspayment_send_probes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_spontaneouspayment_send_with_custom_tlvs()
		})
		if checksum != 56266 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_spontaneouspayment_send_with_custom_tlvs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_spontaneouspayment_send_with_preimage()
		})
		if checksum != 21182 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_spontaneouspayment_send_with_preimage: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_spontaneouspayment_send_with_preimage_and_custom_tlvs()
		})
		if checksum != 44297 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_spontaneouspayment_send_with_preimage_and_custom_tlvs: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_staticinvoice_amount()
		})
		if checksum != 49018 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_staticinvoice_amount: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_unifiedpayment_receive()
		})
		if checksum != 33768 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_unifiedpayment_receive: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_method_unifiedpayment_send()
		})
		if checksum != 54400 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_method_unifiedpayment_send: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_builder_from_config()
		})
		if checksum != 56211 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_builder_from_config: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_builder_new()
		})
		if checksum != 42021 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_builder_new: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_feerate_from_sat_per_kwu()
		})
		if checksum != 33347 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_feerate_from_sat_per_kwu: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_feerate_from_sat_per_vb_unchecked()
		})
		if checksum != 51694 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_feerate_from_sat_per_vb_unchecked: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_bolt11invoice_from_str()
		})
		if checksum != 6641 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_bolt11invoice_from_str: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_bolt12invoice_from_str()
		})
		if checksum != 2587 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_bolt12invoice_from_str: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_humanreadablename_from_encoded()
		})
		if checksum != 34127 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_humanreadablename_from_encoded: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_nodeentropy_from_bip39_mnemonic()
		})
		if checksum != 49277 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_nodeentropy_from_bip39_mnemonic: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_nodeentropy_from_seed_bytes()
		})
		if checksum != 13290 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_nodeentropy_from_seed_bytes: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_nodeentropy_from_seed_path()
		})
		if checksum != 60826 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_nodeentropy_from_seed_path: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_offer_from_str()
		})
		if checksum != 16902 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_offer_from_str: UniFFI API checksum mismatch")
		}
	}
	{
		checksum := rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint16_t {
			return C.uniffi_ldk_node_checksum_constructor_refund_from_str()
		})
		if checksum != 46403 {
			// If this happens try cleaning and rebuilding your project
			panic("ldk_node: uniffi_ldk_node_checksum_constructor_refund_from_str: UniFFI API checksum mismatch")
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

type FfiConverterUint16 struct{}

var FfiConverterUint16INSTANCE = FfiConverterUint16{}

func (FfiConverterUint16) Lower(value uint16) C.uint16_t {
	return C.uint16_t(value)
}

func (FfiConverterUint16) Write(writer io.Writer, value uint16) {
	writeUint16(writer, value)
}

func (FfiConverterUint16) Lift(value C.uint16_t) uint16 {
	return uint16(value)
}

func (FfiConverterUint16) Read(reader io.Reader) uint16 {
	return readUint16(reader)
}

type FfiDestroyerUint16 struct{}

func (FfiDestroyerUint16) Destroy(_ uint16) {}

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

func (c FfiConverterString) LowerExternal(value string) ExternalCRustBuffer {
	return RustBufferFromC(stringToRustBuffer(value))
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

func (c FfiConverterBytes) LowerExternal(value []byte) ExternalCRustBuffer {
	return RustBufferFromC(c.Lower(value))
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

// Represents a syntactically and semantically correct lightning BOLT11 invoice.
type Bolt11InvoiceInterface interface {
	// Returns the amount if specified in the invoice as millisatoshis.
	AmountMilliSatoshis() *uint64
	// Returns the currency for which the invoice was issued
	Currency() Currency
	// Returns the invoice's expiry time (in seconds), if present, otherwise [`DEFAULT_EXPIRY_TIME`].
	//
	// [`DEFAULT_EXPIRY_TIME`]: lightning_invoice::DEFAULT_EXPIRY_TIME
	ExpiryTimeSeconds() uint64
	// Returns a list of all fallback addresses as [`Address`]es
	FallbackAddresses() []Address
	// Return the description or a hash of it for longer ones
	InvoiceDescription() Bolt11InvoiceDescription
	// Returns whether the invoice has expired.
	IsExpired() bool
	// Returns the invoice's `min_final_cltv_expiry_delta` time, if present, otherwise
	// [`DEFAULT_MIN_FINAL_CLTV_EXPIRY_DELTA`].
	//
	// [`DEFAULT_MIN_FINAL_CLTV_EXPIRY_DELTA`]: lightning_invoice::DEFAULT_MIN_FINAL_CLTV_EXPIRY_DELTA
	MinFinalCltvExpiryDelta() uint64
	// Returns the network for which the invoice was issued
	Network() Network
	// Returns the hash to which we will receive the preimage on completion of the payment
	PaymentHash() PaymentHash
	// Get the payment secret if one was included in the invoice
	PaymentSecret() PaymentSecret
	// Recover the payee's public key (only to be used if none was included in the invoice)
	RecoverPayeePubKey() PublicKey
	// Returns a list of all routes included in the invoice as the underlying hints
	RouteHints() [][]RouteHintHop
	// Returns the `Bolt11Invoice`'s timestamp as seconds since the Unix epoch
	SecondsSinceEpoch() uint64
	// Returns the seconds remaining until the invoice expires.
	SecondsUntilExpiry() uint64
	// The hash of the [`RawBolt11Invoice`] that was signed.
	//
	// [`RawBolt11Invoice`]: lightning_invoice::RawBolt11Invoice
	SignableHash() []byte
	// Returns whether the expiry time would pass at the given point in time.
	// `at_time_seconds` is the timestamp as seconds since the Unix epoch.
	WouldExpire(atTimeSeconds uint64) bool
}

// Represents a syntactically and semantically correct lightning BOLT11 invoice.
type Bolt11Invoice struct {
	ffiObject FfiObject
}

func Bolt11InvoiceFromStr(invoiceStr string) (*Bolt11Invoice, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_bolt11invoice_from_str(FfiConverterStringINSTANCE.Lower(invoiceStr), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns the amount if specified in the invoice as millisatoshis.
func (_self *Bolt11Invoice) AmountMilliSatoshis() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_amount_milli_satoshis(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns the currency for which the invoice was issued
func (_self *Bolt11Invoice) Currency() Currency {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterCurrencyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_currency(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns the invoice's expiry time (in seconds), if present, otherwise [`DEFAULT_EXPIRY_TIME`].
//
// [`DEFAULT_EXPIRY_TIME`]: lightning_invoice::DEFAULT_EXPIRY_TIME
func (_self *Bolt11Invoice) ExpiryTimeSeconds() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_expiry_time_seconds(
			_pointer, _uniffiStatus)
	}))
}

// Returns a list of all fallback addresses as [`Address`]es
func (_self *Bolt11Invoice) FallbackAddresses() []Address {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceTypeAddressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_fallback_addresses(
				_pointer, _uniffiStatus),
		}
	}))
}

// Return the description or a hash of it for longer ones
func (_self *Bolt11Invoice) InvoiceDescription() Bolt11InvoiceDescription {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBolt11InvoiceDescriptionINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_invoice_description(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns whether the invoice has expired.
func (_self *Bolt11Invoice) IsExpired() bool {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_is_expired(
			_pointer, _uniffiStatus)
	}))
}

// Returns the invoice's `min_final_cltv_expiry_delta` time, if present, otherwise
// [`DEFAULT_MIN_FINAL_CLTV_EXPIRY_DELTA`].
//
// [`DEFAULT_MIN_FINAL_CLTV_EXPIRY_DELTA`]: lightning_invoice::DEFAULT_MIN_FINAL_CLTV_EXPIRY_DELTA
func (_self *Bolt11Invoice) MinFinalCltvExpiryDelta() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_min_final_cltv_expiry_delta(
			_pointer, _uniffiStatus)
	}))
}

// Returns the network for which the invoice was issued
func (_self *Bolt11Invoice) Network() Network {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNetworkINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_network(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns the hash to which we will receive the preimage on completion of the payment
func (_self *Bolt11Invoice) PaymentHash() PaymentHash {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePaymentHashINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_payment_hash(
				_pointer, _uniffiStatus),
		}
	}))
}

// Get the payment secret if one was included in the invoice
func (_self *Bolt11Invoice) PaymentSecret() PaymentSecret {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePaymentSecretINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_payment_secret(
				_pointer, _uniffiStatus),
		}
	}))
}

// Recover the payee's public key (only to be used if none was included in the invoice)
func (_self *Bolt11Invoice) RecoverPayeePubKey() PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_recover_payee_pub_key(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns a list of all routes included in the invoice as the underlying hints
func (_self *Bolt11Invoice) RouteHints() [][]RouteHintHop {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceSequenceRouteHintHopINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_route_hints(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns the `Bolt11Invoice`'s timestamp as seconds since the Unix epoch
func (_self *Bolt11Invoice) SecondsSinceEpoch() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_seconds_since_epoch(
			_pointer, _uniffiStatus)
	}))
}

// Returns the seconds remaining until the invoice expires.
func (_self *Bolt11Invoice) SecondsUntilExpiry() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_seconds_until_expiry(
			_pointer, _uniffiStatus)
	}))
}

// The hash of the [`RawBolt11Invoice`] that was signed.
//
// [`RawBolt11Invoice`]: lightning_invoice::RawBolt11Invoice
func (_self *Bolt11Invoice) SignableHash() []byte {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_signable_hash(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns whether the expiry time would pass at the given point in time.
// `at_time_seconds` is the timestamp as seconds since the Unix epoch.
func (_self *Bolt11Invoice) WouldExpire(atTimeSeconds uint64) bool {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_would_expire(
			_pointer, FfiConverterUint64INSTANCE.Lower(atTimeSeconds), _uniffiStatus)
	}))
}

func (_self *Bolt11Invoice) DebugString() string {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_uniffi_trait_debug(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Bolt11Invoice) String() string {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11invoice_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Bolt11Invoice) Eq(other *Bolt11Invoice) bool {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_uniffi_trait_eq_eq(
			_pointer, FfiConverterBolt11InvoiceINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (_self *Bolt11Invoice) Ne(other *Bolt11Invoice) bool {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_bolt11invoice_uniffi_trait_eq_ne(
			_pointer, FfiConverterBolt11InvoiceINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (object *Bolt11Invoice) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBolt11Invoice struct{}

var FfiConverterBolt11InvoiceINSTANCE = FfiConverterBolt11Invoice{}

func (c FfiConverterBolt11Invoice) Lift(pointer unsafe.Pointer) *Bolt11Invoice {
	result := &Bolt11Invoice{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_bolt11invoice(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_bolt11invoice(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Bolt11Invoice).Destroy)
	return result
}

func (c FfiConverterBolt11Invoice) Read(reader io.Reader) *Bolt11Invoice {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBolt11Invoice) Lower(value *Bolt11Invoice) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Bolt11Invoice")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBolt11Invoice) Write(writer io.Writer, value *Bolt11Invoice) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBolt11Invoice struct{}

func (_ FfiDestroyerBolt11Invoice) Destroy(value *Bolt11Invoice) {
	value.Destroy()
}

// A payment handler allowing to create and pay [BOLT 11] invoices.
//
// Should be retrieved by calling [`Node::bolt11_payment`].
//
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
// [`Node::bolt11_payment`]: crate::Node::bolt11_payment
type Bolt11PaymentInterface interface {
	// Allows to attempt manually claiming payments with the given preimage that have previously
	// been registered via [`receive_for_hash`] or [`receive_variable_amount_for_hash`].
	//
	// This should be called in reponse to a [`PaymentClaimable`] event as soon as the preimage is
	// available.
	//
	// Will check that the payment is known, and that the given preimage and claimable amount
	// match our expectations before attempting to claim the payment, and will return an error
	// otherwise.
	//
	// When claiming the payment has succeeded, a [`PaymentReceived`] event will be emitted.
	//
	// [`receive_for_hash`]: Self::receive_for_hash
	// [`receive_variable_amount_for_hash`]: Self::receive_variable_amount_for_hash
	// [`PaymentClaimable`]: crate::Event::PaymentClaimable
	// [`PaymentReceived`]: crate::Event::PaymentReceived
	ClaimForHash(paymentHash PaymentHash, claimableAmountMsat uint64, preimage PaymentPreimage) error
	// Allows to manually fail payments with the given hash that have previously
	// been registered via [`receive_for_hash`] or [`receive_variable_amount_for_hash`].
	//
	// This should be called in reponse to a [`PaymentClaimable`] event if the payment needs to be
	// failed back, e.g., if the correct preimage can't be retrieved in time before the claim
	// deadline has been reached.
	//
	// Will check that the payment is known before failing the payment, and will return an error
	// otherwise.
	//
	// [`receive_for_hash`]: Self::receive_for_hash
	// [`receive_variable_amount_for_hash`]: Self::receive_variable_amount_for_hash
	// [`PaymentClaimable`]: crate::Event::PaymentClaimable
	FailForHash(paymentHash PaymentHash) error
	// Returns a payable invoice that can be used to request and receive a payment of the amount
	// given.
	//
	// The inbound payment will be automatically claimed upon arrival.
	Receive(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32) (*Bolt11Invoice, error)
	// Returns a payable invoice that can be used to request a payment of the amount
	// given for the given payment hash.
	//
	// We will register the given payment hash and emit a [`PaymentClaimable`] event once
	// the inbound payment arrives.
	//
	// **Note:** users *MUST* handle this event and claim the payment manually via
	// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
	// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
	// [`fail_for_hash`].
	//
	// [`PaymentClaimable`]: crate::Event::PaymentClaimable
	// [`claim_for_hash`]: Self::claim_for_hash
	// [`fail_for_hash`]: Self::fail_for_hash
	ReceiveForHash(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32, paymentHash PaymentHash) (*Bolt11Invoice, error)
	// Returns a payable invoice that can be used to request and receive a payment for which the
	// amount is to be determined by the user, also known as a "zero-amount" invoice.
	//
	// The inbound payment will be automatically claimed upon arrival.
	ReceiveVariableAmount(description Bolt11InvoiceDescription, expirySecs uint32) (*Bolt11Invoice, error)
	// Returns a payable invoice that can be used to request a payment for the given payment hash
	// and the amount to be determined by the user, also known as a "zero-amount" invoice.
	//
	// We will register the given payment hash and emit a [`PaymentClaimable`] event once
	// the inbound payment arrives.
	//
	// **Note:** users *MUST* handle this event and claim the payment manually via
	// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
	// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
	// [`fail_for_hash`].
	//
	// [`PaymentClaimable`]: crate::Event::PaymentClaimable
	// [`claim_for_hash`]: Self::claim_for_hash
	// [`fail_for_hash`]: Self::fail_for_hash
	ReceiveVariableAmountForHash(description Bolt11InvoiceDescription, expirySecs uint32, paymentHash PaymentHash) (*Bolt11Invoice, error)
	// Returns a payable invoice that can be used to request a variable amount payment (also known
	// as "zero-amount" invoice) and receive it via a newly created just-in-time (JIT) channel.
	//
	// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
	// to us, supplying just-in-time inbound liquidity.
	//
	// If set, `max_proportional_lsp_fee_limit_ppm_msat` will limit how much proportional fee, in
	// parts-per-million millisatoshis, we allow the LSP to take for opening the channel to us.
	// We'll use its cheapest offer otherwise.
	//
	// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
	ReceiveVariableAmountViaJitChannel(description Bolt11InvoiceDescription, expirySecs uint32, maxProportionalLspFeeLimitPpmMsat *uint64) (*Bolt11Invoice, error)
	// Returns a payable invoice that can be used to request a variable amount payment (also known
	// as "zero-amount" invoice) and receive it via a newly created just-in-time (JIT) channel.
	//
	// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
	// to us, supplying just-in-time inbound liquidity.
	//
	// If set, `max_proportional_lsp_fee_limit_ppm_msat` will limit how much proportional fee, in
	// parts-per-million millisatoshis, we allow the LSP to take for opening the channel to us.
	// We'll use its cheapest offer otherwise.
	//
	// We will register the given payment hash and emit a [`PaymentClaimable`] event once
	// the inbound payment arrives. The check that [`counterparty_skimmed_fee_msat`] is within the limits
	// is performed *before* emitting the event.
	//
	// **Note:** users *MUST* handle this event and claim the payment manually via
	// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
	// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
	// [`fail_for_hash`].
	//
	// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
	// [`PaymentClaimable`]: crate::Event::PaymentClaimable
	// [`claim_for_hash`]: Self::claim_for_hash
	// [`fail_for_hash`]: Self::fail_for_hash
	// [`counterparty_skimmed_fee_msat`]: crate::payment::PaymentKind::Bolt11Jit::counterparty_skimmed_fee_msat
	ReceiveVariableAmountViaJitChannelForHash(description Bolt11InvoiceDescription, expirySecs uint32, maxProportionalLspFeeLimitPpmMsat *uint64, paymentHash PaymentHash) (*Bolt11Invoice, error)
	// Returns a payable invoice that can be used to request a payment of the amount given and
	// receive it via a newly created just-in-time (JIT) channel.
	//
	// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
	// to us, supplying just-in-time inbound liquidity.
	//
	// If set, `max_total_lsp_fee_limit_msat` will limit how much fee we allow the LSP to take for opening the
	// channel to us. We'll use its cheapest offer otherwise.
	//
	// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
	ReceiveViaJitChannel(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32, maxTotalLspFeeLimitMsat *uint64) (*Bolt11Invoice, error)
	// Returns a payable invoice that can be used to request a payment of the amount given and
	// receive it via a newly created just-in-time (JIT) channel.
	//
	// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
	// to us, supplying just-in-time inbound liquidity.
	//
	// If set, `max_total_lsp_fee_limit_msat` will limit how much fee we allow the LSP to take for opening the
	// channel to us. We'll use its cheapest offer otherwise.
	//
	// We will register the given payment hash and emit a [`PaymentClaimable`] event once
	// the inbound payment arrives. The check that [`counterparty_skimmed_fee_msat`] is within the limits
	// is performed *before* emitting the event.
	//
	// **Note:** users *MUST* handle this event and claim the payment manually via
	// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
	// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
	// [`fail_for_hash`].
	//
	// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
	// [`PaymentClaimable`]: crate::Event::PaymentClaimable
	// [`claim_for_hash`]: Self::claim_for_hash
	// [`fail_for_hash`]: Self::fail_for_hash
	// [`counterparty_skimmed_fee_msat`]: crate::payment::PaymentKind::Bolt11Jit::counterparty_skimmed_fee_msat
	ReceiveViaJitChannelForHash(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32, maxTotalLspFeeLimitMsat *uint64, paymentHash PaymentHash) (*Bolt11Invoice, error)
	// Send a payment given an invoice.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	Send(invoice *Bolt11Invoice, routeParameters *RouteParametersConfig) (PaymentId, error)
	// Sends payment probes over all paths of a route that would be used to pay the given invoice.
	//
	// This may be used to send "pre-flight" probes, i.e., to train our scorer before conducting
	// the actual payment. Note this is only useful if there likely is sufficient time for the
	// probe to settle before sending out the actual payment, e.g., when waiting for user
	// confirmation in a wallet UI.
	//
	// Otherwise, there is a chance the probe could take up some liquidity needed to complete the
	// actual payment. Users should therefore be cautious and might avoid sending probes if
	// liquidity is scarce and/or they don't expect the probe to return before they send the
	// payment. To mitigate this issue, channels with available liquidity less than the required
	// amount times [`Config::probing_liquidity_limit_multiplier`] won't be used to send
	// pre-flight probes.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	SendProbes(invoice *Bolt11Invoice, routeParameters *RouteParametersConfig) error
	// Sends payment probes over all paths of a route that would be used to pay the given
	// zero-value invoice using the given amount.
	//
	// This can be used to send pre-flight probes for a so-called "zero-amount" invoice, i.e., an
	// invoice that leaves the amount paid to be determined by the user.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	//
	// See [`Self::send_probes`] for more information.
	SendProbesUsingAmount(invoice *Bolt11Invoice, amountMsat uint64, routeParameters *RouteParametersConfig) error
	// Send a payment given an invoice and an amount in millisatoshis.
	//
	// This will fail if the amount given is less than the value required by the given invoice.
	//
	// This can be used to pay a so-called "zero-amount" invoice, i.e., an invoice that leaves the
	// amount paid to be determined by the user.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	SendUsingAmount(invoice *Bolt11Invoice, amountMsat uint64, routeParameters *RouteParametersConfig) (PaymentId, error)
}

// A payment handler allowing to create and pay [BOLT 11] invoices.
//
// Should be retrieved by calling [`Node::bolt11_payment`].
//
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
// [`Node::bolt11_payment`]: crate::Node::bolt11_payment
type Bolt11Payment struct {
	ffiObject FfiObject
}

// Allows to attempt manually claiming payments with the given preimage that have previously
// been registered via [`receive_for_hash`] or [`receive_variable_amount_for_hash`].
//
// This should be called in reponse to a [`PaymentClaimable`] event as soon as the preimage is
// available.
//
// Will check that the payment is known, and that the given preimage and claimable amount
// match our expectations before attempting to claim the payment, and will return an error
// otherwise.
//
// When claiming the payment has succeeded, a [`PaymentReceived`] event will be emitted.
//
// [`receive_for_hash`]: Self::receive_for_hash
// [`receive_variable_amount_for_hash`]: Self::receive_variable_amount_for_hash
// [`PaymentClaimable`]: crate::Event::PaymentClaimable
// [`PaymentReceived`]: crate::Event::PaymentReceived
func (_self *Bolt11Payment) ClaimForHash(paymentHash PaymentHash, claimableAmountMsat uint64, preimage PaymentPreimage) error {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_bolt11payment_claim_for_hash(
			_pointer, FfiConverterTypePaymentHashINSTANCE.Lower(paymentHash), FfiConverterUint64INSTANCE.Lower(claimableAmountMsat), FfiConverterTypePaymentPreimageINSTANCE.Lower(preimage), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

// Allows to manually fail payments with the given hash that have previously
// been registered via [`receive_for_hash`] or [`receive_variable_amount_for_hash`].
//
// This should be called in reponse to a [`PaymentClaimable`] event if the payment needs to be
// failed back, e.g., if the correct preimage can't be retrieved in time before the claim
// deadline has been reached.
//
// Will check that the payment is known before failing the payment, and will return an error
// otherwise.
//
// [`receive_for_hash`]: Self::receive_for_hash
// [`receive_variable_amount_for_hash`]: Self::receive_variable_amount_for_hash
// [`PaymentClaimable`]: crate::Event::PaymentClaimable
func (_self *Bolt11Payment) FailForHash(paymentHash PaymentHash) error {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_bolt11payment_fail_for_hash(
			_pointer, FfiConverterTypePaymentHashINSTANCE.Lower(paymentHash), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

// Returns a payable invoice that can be used to request and receive a payment of the amount
// given.
//
// The inbound payment will be automatically claimed upon arrival.
func (_self *Bolt11Payment) Receive(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive(
			_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable invoice that can be used to request a payment of the amount
// given for the given payment hash.
//
// We will register the given payment hash and emit a [`PaymentClaimable`] event once
// the inbound payment arrives.
//
// **Note:** users *MUST* handle this event and claim the payment manually via
// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
// [`fail_for_hash`].
//
// [`PaymentClaimable`]: crate::Event::PaymentClaimable
// [`claim_for_hash`]: Self::claim_for_hash
// [`fail_for_hash`]: Self::fail_for_hash
func (_self *Bolt11Payment) ReceiveForHash(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32, paymentHash PaymentHash) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive_for_hash(
			_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), FfiConverterTypePaymentHashINSTANCE.Lower(paymentHash), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable invoice that can be used to request and receive a payment for which the
// amount is to be determined by the user, also known as a "zero-amount" invoice.
//
// The inbound payment will be automatically claimed upon arrival.
func (_self *Bolt11Payment) ReceiveVariableAmount(description Bolt11InvoiceDescription, expirySecs uint32) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive_variable_amount(
			_pointer, FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable invoice that can be used to request a payment for the given payment hash
// and the amount to be determined by the user, also known as a "zero-amount" invoice.
//
// We will register the given payment hash and emit a [`PaymentClaimable`] event once
// the inbound payment arrives.
//
// **Note:** users *MUST* handle this event and claim the payment manually via
// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
// [`fail_for_hash`].
//
// [`PaymentClaimable`]: crate::Event::PaymentClaimable
// [`claim_for_hash`]: Self::claim_for_hash
// [`fail_for_hash`]: Self::fail_for_hash
func (_self *Bolt11Payment) ReceiveVariableAmountForHash(description Bolt11InvoiceDescription, expirySecs uint32, paymentHash PaymentHash) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive_variable_amount_for_hash(
			_pointer, FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), FfiConverterTypePaymentHashINSTANCE.Lower(paymentHash), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable invoice that can be used to request a variable amount payment (also known
// as "zero-amount" invoice) and receive it via a newly created just-in-time (JIT) channel.
//
// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
// to us, supplying just-in-time inbound liquidity.
//
// If set, `max_proportional_lsp_fee_limit_ppm_msat` will limit how much proportional fee, in
// parts-per-million millisatoshis, we allow the LSP to take for opening the channel to us.
// We'll use its cheapest offer otherwise.
//
// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
func (_self *Bolt11Payment) ReceiveVariableAmountViaJitChannel(description Bolt11InvoiceDescription, expirySecs uint32, maxProportionalLspFeeLimitPpmMsat *uint64) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive_variable_amount_via_jit_channel(
			_pointer, FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), FfiConverterOptionalUint64INSTANCE.Lower(maxProportionalLspFeeLimitPpmMsat), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable invoice that can be used to request a variable amount payment (also known
// as "zero-amount" invoice) and receive it via a newly created just-in-time (JIT) channel.
//
// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
// to us, supplying just-in-time inbound liquidity.
//
// If set, `max_proportional_lsp_fee_limit_ppm_msat` will limit how much proportional fee, in
// parts-per-million millisatoshis, we allow the LSP to take for opening the channel to us.
// We'll use its cheapest offer otherwise.
//
// We will register the given payment hash and emit a [`PaymentClaimable`] event once
// the inbound payment arrives. The check that [`counterparty_skimmed_fee_msat`] is within the limits
// is performed *before* emitting the event.
//
// **Note:** users *MUST* handle this event and claim the payment manually via
// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
// [`fail_for_hash`].
//
// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
// [`PaymentClaimable`]: crate::Event::PaymentClaimable
// [`claim_for_hash`]: Self::claim_for_hash
// [`fail_for_hash`]: Self::fail_for_hash
// [`counterparty_skimmed_fee_msat`]: crate::payment::PaymentKind::Bolt11Jit::counterparty_skimmed_fee_msat
func (_self *Bolt11Payment) ReceiveVariableAmountViaJitChannelForHash(description Bolt11InvoiceDescription, expirySecs uint32, maxProportionalLspFeeLimitPpmMsat *uint64, paymentHash PaymentHash) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive_variable_amount_via_jit_channel_for_hash(
			_pointer, FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), FfiConverterOptionalUint64INSTANCE.Lower(maxProportionalLspFeeLimitPpmMsat), FfiConverterTypePaymentHashINSTANCE.Lower(paymentHash), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable invoice that can be used to request a payment of the amount given and
// receive it via a newly created just-in-time (JIT) channel.
//
// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
// to us, supplying just-in-time inbound liquidity.
//
// If set, `max_total_lsp_fee_limit_msat` will limit how much fee we allow the LSP to take for opening the
// channel to us. We'll use its cheapest offer otherwise.
//
// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
func (_self *Bolt11Payment) ReceiveViaJitChannel(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32, maxTotalLspFeeLimitMsat *uint64) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive_via_jit_channel(
			_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), FfiConverterOptionalUint64INSTANCE.Lower(maxTotalLspFeeLimitMsat), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable invoice that can be used to request a payment of the amount given and
// receive it via a newly created just-in-time (JIT) channel.
//
// When the returned invoice is paid, the configured [LSPS2]-compliant LSP will open a channel
// to us, supplying just-in-time inbound liquidity.
//
// If set, `max_total_lsp_fee_limit_msat` will limit how much fee we allow the LSP to take for opening the
// channel to us. We'll use its cheapest offer otherwise.
//
// We will register the given payment hash and emit a [`PaymentClaimable`] event once
// the inbound payment arrives. The check that [`counterparty_skimmed_fee_msat`] is within the limits
// is performed *before* emitting the event.
//
// **Note:** users *MUST* handle this event and claim the payment manually via
// [`claim_for_hash`] as soon as they have obtained access to the preimage of the given
// payment hash. If they're unable to obtain the preimage, they *MUST* immediately fail the payment via
// [`fail_for_hash`].
//
// [LSPS2]: https://github.com/BitcoinAndLightningLayerSpecs/lsp/blob/main/LSPS2/README.md
// [`PaymentClaimable`]: crate::Event::PaymentClaimable
// [`claim_for_hash`]: Self::claim_for_hash
// [`fail_for_hash`]: Self::fail_for_hash
// [`counterparty_skimmed_fee_msat`]: crate::payment::PaymentKind::Bolt11Jit::counterparty_skimmed_fee_msat
func (_self *Bolt11Payment) ReceiveViaJitChannelForHash(amountMsat uint64, description Bolt11InvoiceDescription, expirySecs uint32, maxTotalLspFeeLimitMsat *uint64, paymentHash PaymentHash) (*Bolt11Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt11payment_receive_via_jit_channel_for_hash(
			_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterBolt11InvoiceDescriptionINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySecs), FfiConverterOptionalUint64INSTANCE.Lower(maxTotalLspFeeLimitMsat), FfiConverterTypePaymentHashINSTANCE.Lower(paymentHash), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt11Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt11InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Send a payment given an invoice.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
func (_self *Bolt11Payment) Send(invoice *Bolt11Invoice, routeParameters *RouteParametersConfig) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11payment_send(
				_pointer, FfiConverterBolt11InvoiceINSTANCE.Lower(invoice), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}

// Sends payment probes over all paths of a route that would be used to pay the given invoice.
//
// This may be used to send "pre-flight" probes, i.e., to train our scorer before conducting
// the actual payment. Note this is only useful if there likely is sufficient time for the
// probe to settle before sending out the actual payment, e.g., when waiting for user
// confirmation in a wallet UI.
//
// Otherwise, there is a chance the probe could take up some liquidity needed to complete the
// actual payment. Users should therefore be cautious and might avoid sending probes if
// liquidity is scarce and/or they don't expect the probe to return before they send the
// payment. To mitigate this issue, channels with available liquidity less than the required
// amount times [`Config::probing_liquidity_limit_multiplier`] won't be used to send
// pre-flight probes.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
func (_self *Bolt11Payment) SendProbes(invoice *Bolt11Invoice, routeParameters *RouteParametersConfig) error {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_bolt11payment_send_probes(
			_pointer, FfiConverterBolt11InvoiceINSTANCE.Lower(invoice), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

// Sends payment probes over all paths of a route that would be used to pay the given
// zero-value invoice using the given amount.
//
// This can be used to send pre-flight probes for a so-called "zero-amount" invoice, i.e., an
// invoice that leaves the amount paid to be determined by the user.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
//
// See [`Self::send_probes`] for more information.
func (_self *Bolt11Payment) SendProbesUsingAmount(invoice *Bolt11Invoice, amountMsat uint64, routeParameters *RouteParametersConfig) error {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_bolt11payment_send_probes_using_amount(
			_pointer, FfiConverterBolt11InvoiceINSTANCE.Lower(invoice), FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

// Send a payment given an invoice and an amount in millisatoshis.
//
// This will fail if the amount given is less than the value required by the given invoice.
//
// This can be used to pay a so-called "zero-amount" invoice, i.e., an invoice that leaves the
// amount paid to be determined by the user.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
func (_self *Bolt11Payment) SendUsingAmount(invoice *Bolt11Invoice, amountMsat uint64, routeParameters *RouteParametersConfig) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt11Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt11payment_send_using_amount(
				_pointer, FfiConverterBolt11InvoiceINSTANCE.Lower(invoice), FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}
func (object *Bolt11Payment) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBolt11Payment struct{}

var FfiConverterBolt11PaymentINSTANCE = FfiConverterBolt11Payment{}

func (c FfiConverterBolt11Payment) Lift(pointer unsafe.Pointer) *Bolt11Payment {
	result := &Bolt11Payment{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_bolt11payment(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_bolt11payment(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Bolt11Payment).Destroy)
	return result
}

func (c FfiConverterBolt11Payment) Read(reader io.Reader) *Bolt11Payment {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBolt11Payment) Lower(value *Bolt11Payment) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Bolt11Payment")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBolt11Payment) Write(writer io.Writer, value *Bolt11Payment) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBolt11Payment struct{}

func (_ FfiDestroyerBolt11Payment) Destroy(value *Bolt11Payment) {
	value.Destroy()
}

type Bolt12InvoiceInterface interface {
	// Seconds since the Unix epoch when an invoice should no longer be requested.
	//
	// From [`Offer::absolute_expiry`] or [`Refund::absolute_expiry`].
	//
	// [`Offer::absolute_expiry`]: lightning::offers::offer::Offer::absolute_expiry
	AbsoluteExpirySeconds() *uint64
	// The minimum amount required for a successful payment of a single item.
	//
	// From [`Offer::amount`]; `None` if the invoice was created in response to a [`Refund`] or if
	// the [`Offer`] did not set it.
	//
	// [`Offer`]: lightning::offers::offer::Offer
	// [`Offer::amount`]: lightning::offers::offer::Offer::amount
	// [`Refund`]: lightning::offers::refund::Refund
	Amount() *OfferAmount
	// The minimum amount required for a successful payment of the invoice.
	AmountMsats() uint64
	// The chain that must be used when paying the invoice; selected from [`offer_chains`] if the
	// invoice originated from an offer.
	//
	// From [`InvoiceRequest::chain`] or [`Refund::chain`].
	//
	// [`offer_chains`]: lightning::offers::invoice::Bolt12Invoice::offer_chains
	// [`InvoiceRequest::chain`]: lightning::offers::invoice_request::InvoiceRequest::chain
	// [`Refund::chain`]: lightning::offers::refund::Refund::chain
	Chain() []byte
	// Duration since the Unix epoch when the invoice was created.
	CreatedAt() uint64
	// Writes `self` out to a `Vec<u8>`.
	Encode() []byte
	// Fallback addresses for paying the invoice on-chain, in order of most-preferred to
	// least-preferred.
	FallbackAddresses() []Address
	// A complete description of the purpose of the originating offer or refund.
	//
	// From [`Offer::description`] or [`Refund::description`].
	//
	// [`Offer::description`]: lightning::offers::offer::Offer::description
	// [`Refund::description`]: lightning::offers::refund::Refund::description
	InvoiceDescription() *string
	// Whether the invoice has expired.
	IsExpired() bool
	// The issuer of the offer or refund.
	//
	// From [`Offer::issuer`] or [`Refund::issuer`].
	//
	// [`Offer::issuer`]: lightning::offers::offer::Offer::issuer
	// [`Refund::issuer`]: lightning::offers::refund::Refund::issuer
	Issuer() *string
	// The public key used by the recipient to sign invoices.
	//
	// From [`Offer::issuer_signing_pubkey`] and may be `None`; also `None` if the invoice was
	// created in response to a [`Refund`].
	//
	// [`Offer::issuer_signing_pubkey`]: lightning::offers::offer::Offer::issuer_signing_pubkey
	// [`Refund`]: lightning::offers::refund::Refund
	IssuerSigningPubkey() *PublicKey
	// Opaque bytes set by the originating [`Offer`].
	//
	// From [`Offer::metadata`]; `None` if the invoice was created in response to a [`Refund`] or
	// if the [`Offer`] did not set it.
	//
	// [`Offer`]: lightning::offers::offer::Offer
	// [`Offer::metadata`]: lightning::offers::offer::Offer::metadata
	// [`Refund`]: lightning::offers::refund::Refund
	Metadata() *[]byte
	// The chains that may be used when paying a requested invoice.
	//
	// From [`Offer::chains`]; `None` if the invoice was created in response to a [`Refund`].
	//
	// [`Offer::chains`]: lightning::offers::offer::Offer::chains
	// [`Refund`]: lightning::offers::refund::Refund
	OfferChains() *[][]byte
	// A payer-provided note reflected back in the invoice.
	//
	// From [`InvoiceRequest::payer_note`] or [`Refund::payer_note`].
	//
	// [`Refund::payer_note`]: lightning::offers::refund::Refund::payer_note
	PayerNote() *string
	// A possibly transient pubkey used to sign the invoice request or to send an invoice for a
	// refund in case there are no [`message_paths`].
	//
	// [`message_paths`]: lightning::offers::invoice::Bolt12Invoice
	PayerSigningPubkey() PublicKey
	// SHA256 hash of the payment preimage that will be given in return for paying the invoice.
	PaymentHash() PaymentHash
	// The quantity of items requested or refunded for.
	//
	// From [`InvoiceRequest::quantity`] or [`Refund::quantity`].
	//
	// [`Refund::quantity`]: lightning::offers::refund::Refund::quantity
	Quantity() *uint64
	// When the invoice has expired and therefore should no longer be paid.
	RelativeExpiry() uint64
	// Hash that was used for signing the invoice.
	SignableHash() []byte
	// A typically transient public key corresponding to the key used to sign the invoice.
	//
	// If the invoices was created in response to an [`Offer`], then this will be:
	// - [`Offer::issuer_signing_pubkey`] if it's `Some`, otherwise
	// - the final blinded node id from a [`BlindedMessagePath`] in [`Offer::paths`] if `None`.
	//
	// If the invoice was created in response to a [`Refund`], then it is a valid pubkey chosen by
	// the recipient.
	//
	// [`Offer`]: lightning::offers::offer::Offer
	// [`Offer::issuer_signing_pubkey`]: lightning::offers::offer::Offer::issuer_signing_pubkey
	// [`Offer::paths`]: lightning::offers::offer::Offer::paths
	// [`Refund`]: lightning::offers::refund::Refund
	SigningPubkey() PublicKey
}
type Bolt12Invoice struct {
	ffiObject FfiObject
}

func Bolt12InvoiceFromStr(invoiceStr string) (*Bolt12Invoice, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_bolt12invoice_from_str(FfiConverterStringINSTANCE.Lower(invoiceStr), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt12Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt12InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Seconds since the Unix epoch when an invoice should no longer be requested.
//
// From [`Offer::absolute_expiry`] or [`Refund::absolute_expiry`].
//
// [`Offer::absolute_expiry`]: lightning::offers::offer::Offer::absolute_expiry
func (_self *Bolt12Invoice) AbsoluteExpirySeconds() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_absolute_expiry_seconds(
				_pointer, _uniffiStatus),
		}
	}))
}

// The minimum amount required for a successful payment of a single item.
//
// From [`Offer::amount`]; `None` if the invoice was created in response to a [`Refund`] or if
// the [`Offer`] did not set it.
//
// [`Offer`]: lightning::offers::offer::Offer
// [`Offer::amount`]: lightning::offers::offer::Offer::amount
// [`Refund`]: lightning::offers::refund::Refund
func (_self *Bolt12Invoice) Amount() *OfferAmount {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalOfferAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_amount(
				_pointer, _uniffiStatus),
		}
	}))
}

// The minimum amount required for a successful payment of the invoice.
func (_self *Bolt12Invoice) AmountMsats() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_bolt12invoice_amount_msats(
			_pointer, _uniffiStatus)
	}))
}

// The chain that must be used when paying the invoice; selected from [`offer_chains`] if the
// invoice originated from an offer.
//
// From [`InvoiceRequest::chain`] or [`Refund::chain`].
//
// [`offer_chains`]: lightning::offers::invoice::Bolt12Invoice::offer_chains
// [`InvoiceRequest::chain`]: lightning::offers::invoice_request::InvoiceRequest::chain
// [`Refund::chain`]: lightning::offers::refund::Refund::chain
func (_self *Bolt12Invoice) Chain() []byte {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_chain(
				_pointer, _uniffiStatus),
		}
	}))
}

// Duration since the Unix epoch when the invoice was created.
func (_self *Bolt12Invoice) CreatedAt() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_bolt12invoice_created_at(
			_pointer, _uniffiStatus)
	}))
}

// Writes `self` out to a `Vec<u8>`.
func (_self *Bolt12Invoice) Encode() []byte {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_encode(
				_pointer, _uniffiStatus),
		}
	}))
}

// Fallback addresses for paying the invoice on-chain, in order of most-preferred to
// least-preferred.
func (_self *Bolt12Invoice) FallbackAddresses() []Address {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceTypeAddressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_fallback_addresses(
				_pointer, _uniffiStatus),
		}
	}))
}

// A complete description of the purpose of the originating offer or refund.
//
// From [`Offer::description`] or [`Refund::description`].
//
// [`Offer::description`]: lightning::offers::offer::Offer::description
// [`Refund::description`]: lightning::offers::refund::Refund::description
func (_self *Bolt12Invoice) InvoiceDescription() *string {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_invoice_description(
				_pointer, _uniffiStatus),
		}
	}))
}

// Whether the invoice has expired.
func (_self *Bolt12Invoice) IsExpired() bool {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_bolt12invoice_is_expired(
			_pointer, _uniffiStatus)
	}))
}

// The issuer of the offer or refund.
//
// From [`Offer::issuer`] or [`Refund::issuer`].
//
// [`Offer::issuer`]: lightning::offers::offer::Offer::issuer
// [`Refund::issuer`]: lightning::offers::refund::Refund::issuer
func (_self *Bolt12Invoice) Issuer() *string {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_issuer(
				_pointer, _uniffiStatus),
		}
	}))
}

// The public key used by the recipient to sign invoices.
//
// From [`Offer::issuer_signing_pubkey`] and may be `None`; also `None` if the invoice was
// created in response to a [`Refund`].
//
// [`Offer::issuer_signing_pubkey`]: lightning::offers::offer::Offer::issuer_signing_pubkey
// [`Refund`]: lightning::offers::refund::Refund
func (_self *Bolt12Invoice) IssuerSigningPubkey() *PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalTypePublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_issuer_signing_pubkey(
				_pointer, _uniffiStatus),
		}
	}))
}

// Opaque bytes set by the originating [`Offer`].
//
// From [`Offer::metadata`]; `None` if the invoice was created in response to a [`Refund`] or
// if the [`Offer`] did not set it.
//
// [`Offer`]: lightning::offers::offer::Offer
// [`Offer::metadata`]: lightning::offers::offer::Offer::metadata
// [`Refund`]: lightning::offers::refund::Refund
func (_self *Bolt12Invoice) Metadata() *[]byte {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_metadata(
				_pointer, _uniffiStatus),
		}
	}))
}

// The chains that may be used when paying a requested invoice.
//
// From [`Offer::chains`]; `None` if the invoice was created in response to a [`Refund`].
//
// [`Offer::chains`]: lightning::offers::offer::Offer::chains
// [`Refund`]: lightning::offers::refund::Refund
func (_self *Bolt12Invoice) OfferChains() *[][]byte {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalSequenceBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_offer_chains(
				_pointer, _uniffiStatus),
		}
	}))
}

// A payer-provided note reflected back in the invoice.
//
// From [`InvoiceRequest::payer_note`] or [`Refund::payer_note`].
//
// [`Refund::payer_note`]: lightning::offers::refund::Refund::payer_note
func (_self *Bolt12Invoice) PayerNote() *string {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_payer_note(
				_pointer, _uniffiStatus),
		}
	}))
}

// A possibly transient pubkey used to sign the invoice request or to send an invoice for a
// refund in case there are no [`message_paths`].
//
// [`message_paths`]: lightning::offers::invoice::Bolt12Invoice
func (_self *Bolt12Invoice) PayerSigningPubkey() PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_payer_signing_pubkey(
				_pointer, _uniffiStatus),
		}
	}))
}

// SHA256 hash of the payment preimage that will be given in return for paying the invoice.
func (_self *Bolt12Invoice) PaymentHash() PaymentHash {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePaymentHashINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_payment_hash(
				_pointer, _uniffiStatus),
		}
	}))
}

// The quantity of items requested or refunded for.
//
// From [`InvoiceRequest::quantity`] or [`Refund::quantity`].
//
// [`Refund::quantity`]: lightning::offers::refund::Refund::quantity
func (_self *Bolt12Invoice) Quantity() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_quantity(
				_pointer, _uniffiStatus),
		}
	}))
}

// When the invoice has expired and therefore should no longer be paid.
func (_self *Bolt12Invoice) RelativeExpiry() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_bolt12invoice_relative_expiry(
			_pointer, _uniffiStatus)
	}))
}

// Hash that was used for signing the invoice.
func (_self *Bolt12Invoice) SignableHash() []byte {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_signable_hash(
				_pointer, _uniffiStatus),
		}
	}))
}

// A typically transient public key corresponding to the key used to sign the invoice.
//
// If the invoices was created in response to an [`Offer`], then this will be:
// - [`Offer::issuer_signing_pubkey`] if it's `Some`, otherwise
// - the final blinded node id from a [`BlindedMessagePath`] in [`Offer::paths`] if `None`.
//
// If the invoice was created in response to a [`Refund`], then it is a valid pubkey chosen by
// the recipient.
//
// [`Offer`]: lightning::offers::offer::Offer
// [`Offer::issuer_signing_pubkey`]: lightning::offers::offer::Offer::issuer_signing_pubkey
// [`Offer::paths`]: lightning::offers::offer::Offer::paths
// [`Refund`]: lightning::offers::refund::Refund
func (_self *Bolt12Invoice) SigningPubkey() PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Invoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12invoice_signing_pubkey(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *Bolt12Invoice) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBolt12Invoice struct{}

var FfiConverterBolt12InvoiceINSTANCE = FfiConverterBolt12Invoice{}

func (c FfiConverterBolt12Invoice) Lift(pointer unsafe.Pointer) *Bolt12Invoice {
	result := &Bolt12Invoice{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_bolt12invoice(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_bolt12invoice(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Bolt12Invoice).Destroy)
	return result
}

func (c FfiConverterBolt12Invoice) Read(reader io.Reader) *Bolt12Invoice {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBolt12Invoice) Lower(value *Bolt12Invoice) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Bolt12Invoice")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBolt12Invoice) Write(writer io.Writer, value *Bolt12Invoice) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBolt12Invoice struct{}

func (_ FfiDestroyerBolt12Invoice) Destroy(value *Bolt12Invoice) {
	value.Destroy()
}

// A payment handler allowing to create and pay [BOLT 12] offers and refunds.
//
// Should be retrieved by calling [`Node::bolt12_payment`].
//
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
// [`Node::bolt12_payment`]: crate::Node::bolt12_payment
type Bolt12PaymentInterface interface {
	// [`BlindedMessagePath`]s for an async recipient to communicate with this node and interactively
	// build [`Offer`]s and [`StaticInvoice`]s for receiving async payments.
	//
	// **Caution**: Async payments support is considered experimental.
	//
	// [`Offer`]: lightning::offers::offer::Offer
	// [`StaticInvoice`]: lightning::offers::static_invoice::StaticInvoice
	BlindedPathsForAsyncRecipient(recipientId []byte) ([]byte, error)
	// Returns a [`Refund`] object that can be used to offer a refund payment of the amount given.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	//
	// [`Refund`]: lightning::offers::refund::Refund
	InitiateRefund(amountMsat uint64, expirySecs uint32, quantity *uint64, payerNote *string, routeParameters *RouteParametersConfig) (*Refund, error)
	// Returns a payable offer that can be used to request and receive a payment of the amount
	// given.
	Receive(amountMsat uint64, description string, expirySecs *uint32, quantity *uint64) (*Offer, error)
	// Retrieve an [`Offer`] for receiving async payments as an often-offline recipient.
	//
	// Will only return an offer if [`Bolt12Payment::set_paths_to_static_invoice_server`] was called and we succeeded
	// in interactively building a [`StaticInvoice`] with the static invoice server.
	//
	// Useful for posting offers to receive payments later, such as posting an offer on a website.
	//
	// **Caution**: Async payments support is considered experimental.
	//
	// [`StaticInvoice`]: lightning::offers::static_invoice::StaticInvoice
	// [`Offer`]: lightning::offers::offer::Offer
	ReceiveAsync() (*Offer, error)
	// Returns a payable offer that can be used to request and receive a payment for which the
	// amount is to be determined by the user, also known as a "zero-amount" offer.
	ReceiveVariableAmount(description string, expirySecs *uint32) (*Offer, error)
	// Requests a refund payment for the given [`Refund`].
	//
	// The returned [`Bolt12Invoice`] is for informational purposes only (i.e., isn't needed to
	// retrieve the refund).
	//
	// [`Refund`]: lightning::offers::refund::Refund
	// [`Bolt12Invoice`]: lightning::offers::invoice::Bolt12Invoice
	RequestRefundPayment(refund *Refund) (*Bolt12Invoice, error)
	// Send a payment given an offer.
	//
	// If `payer_note` is `Some` it will be seen by the recipient and reflected back in the invoice
	// response.
	//
	// If `quantity` is `Some` it represents the number of items requested.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	Send(offer *Offer, quantity *uint64, payerNote *string, routeParameters *RouteParametersConfig) (PaymentId, error)
	// Send a payment given an offer and an amount in millisatoshi.
	//
	// This will fail if the amount given is less than the value required by the given offer.
	//
	// This can be used to pay a so-called "zero-amount" offers, i.e., an offer that leaves the
	// amount paid to be determined by the user.
	//
	// If `payer_note` is `Some` it will be seen by the recipient and reflected back in the invoice
	// response.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	SendUsingAmount(offer *Offer, amountMsat uint64, quantity *uint64, payerNote *string, routeParameters *RouteParametersConfig) (PaymentId, error)
	// Sets the [`BlindedMessagePath`]s that we will use as an async recipient to interactively build [`Offer`]s with a
	// static invoice server, so the server can serve [`StaticInvoice`]s to payers on our behalf when we're offline.
	//
	// **Caution**: Async payments support is considered experimental.
	//
	// [`Offer`]: lightning::offers::offer::Offer
	// [`StaticInvoice`]: lightning::offers::static_invoice::StaticInvoice
	SetPathsToStaticInvoiceServer(paths []byte) error
}

// A payment handler allowing to create and pay [BOLT 12] offers and refunds.
//
// Should be retrieved by calling [`Node::bolt12_payment`].
//
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
// [`Node::bolt12_payment`]: crate::Node::bolt12_payment
type Bolt12Payment struct {
	ffiObject FfiObject
}

// [`BlindedMessagePath`]s for an async recipient to communicate with this node and interactively
// build [`Offer`]s and [`StaticInvoice`]s for receiving async payments.
//
// **Caution**: Async payments support is considered experimental.
//
// [`Offer`]: lightning::offers::offer::Offer
// [`StaticInvoice`]: lightning::offers::static_invoice::StaticInvoice
func (_self *Bolt12Payment) BlindedPathsForAsyncRecipient(recipientId []byte) ([]byte, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12payment_blinded_paths_for_async_recipient(
				_pointer, FfiConverterBytesINSTANCE.Lower(recipientId), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue []byte
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBytesINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a [`Refund`] object that can be used to offer a refund payment of the amount given.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
//
// [`Refund`]: lightning::offers::refund::Refund
func (_self *Bolt12Payment) InitiateRefund(amountMsat uint64, expirySecs uint32, quantity *uint64, payerNote *string, routeParameters *RouteParametersConfig) (*Refund, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt12payment_initiate_refund(
			_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterUint32INSTANCE.Lower(expirySecs), FfiConverterOptionalUint64INSTANCE.Lower(quantity), FfiConverterOptionalStringINSTANCE.Lower(payerNote), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Refund
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterRefundINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable offer that can be used to request and receive a payment of the amount
// given.
func (_self *Bolt12Payment) Receive(amountMsat uint64, description string, expirySecs *uint32, quantity *uint64) (*Offer, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt12payment_receive(
			_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterStringINSTANCE.Lower(description), FfiConverterOptionalUint32INSTANCE.Lower(expirySecs), FfiConverterOptionalUint64INSTANCE.Lower(quantity), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Offer
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterOfferINSTANCE.Lift(_uniffiRV), nil
	}
}

// Retrieve an [`Offer`] for receiving async payments as an often-offline recipient.
//
// Will only return an offer if [`Bolt12Payment::set_paths_to_static_invoice_server`] was called and we succeeded
// in interactively building a [`StaticInvoice`] with the static invoice server.
//
// Useful for posting offers to receive payments later, such as posting an offer on a website.
//
// **Caution**: Async payments support is considered experimental.
//
// [`StaticInvoice`]: lightning::offers::static_invoice::StaticInvoice
// [`Offer`]: lightning::offers::offer::Offer
func (_self *Bolt12Payment) ReceiveAsync() (*Offer, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt12payment_receive_async(
			_pointer, _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Offer
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterOfferINSTANCE.Lift(_uniffiRV), nil
	}
}

// Returns a payable offer that can be used to request and receive a payment for which the
// amount is to be determined by the user, also known as a "zero-amount" offer.
func (_self *Bolt12Payment) ReceiveVariableAmount(description string, expirySecs *uint32) (*Offer, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt12payment_receive_variable_amount(
			_pointer, FfiConverterStringINSTANCE.Lower(description), FfiConverterOptionalUint32INSTANCE.Lower(expirySecs), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Offer
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterOfferINSTANCE.Lift(_uniffiRV), nil
	}
}

// Requests a refund payment for the given [`Refund`].
//
// The returned [`Bolt12Invoice`] is for informational purposes only (i.e., isn't needed to
// retrieve the refund).
//
// [`Refund`]: lightning::offers::refund::Refund
// [`Bolt12Invoice`]: lightning::offers::invoice::Bolt12Invoice
func (_self *Bolt12Payment) RequestRefundPayment(refund *Refund) (*Bolt12Invoice, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_bolt12payment_request_refund_payment(
			_pointer, FfiConverterRefundINSTANCE.Lower(refund), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Bolt12Invoice
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterBolt12InvoiceINSTANCE.Lift(_uniffiRV), nil
	}
}

// Send a payment given an offer.
//
// If `payer_note` is `Some` it will be seen by the recipient and reflected back in the invoice
// response.
//
// If `quantity` is `Some` it represents the number of items requested.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
func (_self *Bolt12Payment) Send(offer *Offer, quantity *uint64, payerNote *string, routeParameters *RouteParametersConfig) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12payment_send(
				_pointer, FfiConverterOfferINSTANCE.Lower(offer), FfiConverterOptionalUint64INSTANCE.Lower(quantity), FfiConverterOptionalStringINSTANCE.Lower(payerNote), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}

// Send a payment given an offer and an amount in millisatoshi.
//
// This will fail if the amount given is less than the value required by the given offer.
//
// This can be used to pay a so-called "zero-amount" offers, i.e., an offer that leaves the
// amount paid to be determined by the user.
//
// If `payer_note` is `Some` it will be seen by the recipient and reflected back in the invoice
// response.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
func (_self *Bolt12Payment) SendUsingAmount(offer *Offer, amountMsat uint64, quantity *uint64, payerNote *string, routeParameters *RouteParametersConfig) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_bolt12payment_send_using_amount(
				_pointer, FfiConverterOfferINSTANCE.Lower(offer), FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterOptionalUint64INSTANCE.Lower(quantity), FfiConverterOptionalStringINSTANCE.Lower(payerNote), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}

// Sets the [`BlindedMessagePath`]s that we will use as an async recipient to interactively build [`Offer`]s with a
// static invoice server, so the server can serve [`StaticInvoice`]s to payers on our behalf when we're offline.
//
// **Caution**: Async payments support is considered experimental.
//
// [`Offer`]: lightning::offers::offer::Offer
// [`StaticInvoice`]: lightning::offers::static_invoice::StaticInvoice
func (_self *Bolt12Payment) SetPathsToStaticInvoiceServer(paths []byte) error {
	_pointer := _self.ffiObject.incrementPointer("*Bolt12Payment")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_bolt12payment_set_paths_to_static_invoice_server(
			_pointer, FfiConverterBytesINSTANCE.Lower(paths), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}
func (object *Bolt12Payment) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBolt12Payment struct{}

var FfiConverterBolt12PaymentINSTANCE = FfiConverterBolt12Payment{}

func (c FfiConverterBolt12Payment) Lift(pointer unsafe.Pointer) *Bolt12Payment {
	result := &Bolt12Payment{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_bolt12payment(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_bolt12payment(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Bolt12Payment).Destroy)
	return result
}

func (c FfiConverterBolt12Payment) Read(reader io.Reader) *Bolt12Payment {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBolt12Payment) Lower(value *Bolt12Payment) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Bolt12Payment")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBolt12Payment) Write(writer io.Writer, value *Bolt12Payment) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBolt12Payment struct{}

func (_ FfiDestroyerBolt12Payment) Destroy(value *Bolt12Payment) {
	value.Destroy()
}

type BuilderInterface interface {
	Build(nodeEntropy *NodeEntropy) (*Node, error)
	BuildWithFsStore(nodeEntropy *NodeEntropy) (*Node, error)
	BuildWithVssStore(nodeEntropy *NodeEntropy, vssUrl string, storeId string, fixedHeaders map[string]string) (*Node, error)
	BuildWithVssStoreAndFixedHeaders(nodeEntropy *NodeEntropy, vssUrl string, storeId string, fixedHeaders map[string]string) (*Node, error)
	BuildWithVssStoreAndHeaderProvider(nodeEntropy *NodeEntropy, vssUrl string, storeId string, headerProvider VssHeaderProvider) (*Node, error)
	BuildWithVssStoreAndLnurlAuth(nodeEntropy *NodeEntropy, vssUrl string, storeId string, lnurlAuthServerUrl string, fixedHeaders map[string]string) (*Node, error)
	SetAnnouncementAddresses(announcementAddresses []SocketAddress) error
	SetAsyncPaymentsRole(role *AsyncPaymentsRole) error
	SetChainSourceBitcoindRest(restHost string, restPort uint16, rpcHost string, rpcPort uint16, rpcUser string, rpcPassword string)
	SetChainSourceBitcoindRpc(rpcHost string, rpcPort uint16, rpcUser string, rpcPassword string)
	SetChainSourceElectrum(serverUrl string, config *ElectrumSyncConfig)
	SetChainSourceEsplora(serverUrl string, config *EsploraSyncConfig)
	SetCustomLogger(logWriter LogWriter)
	SetFilesystemLogger(logFilePath *string, maxLogLevel *LogLevel)
	SetGossipSourceP2p()
	SetGossipSourceRgs(rgsServerUrl string)
	SetLiquiditySourceLsps1(nodeId PublicKey, address SocketAddress, token *string)
	SetLiquiditySourceLsps2(nodeId PublicKey, address SocketAddress, token *string)
	SetListeningAddresses(listeningAddresses []SocketAddress) error
	SetLogFacadeLogger()
	SetNetwork(network Network)
	SetNodeAlias(nodeAlias string) error
	SetPathfindingScoresSource(url string)
	SetStorageDirPath(storageDirPath string)
	SetTorConfig(torConfig TorConfig) error
	SetWalletRecoveryMode()
}
type Builder struct {
	ffiObject FfiObject
}

func NewBuilder() *Builder {
	return FfiConverterBuilderINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_builder_new(_uniffiStatus)
	}))
}

func BuilderFromConfig(config Config) *Builder {
	return FfiConverterBuilderINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_builder_from_config(FfiConverterConfigINSTANCE.Lower(config), _uniffiStatus)
	}))
}

func (_self *Builder) Build(nodeEntropy *NodeEntropy) (*Node, error) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_builder_build(
			_pointer, FfiConverterNodeEntropyINSTANCE.Lower(nodeEntropy), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Node
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Builder) BuildWithFsStore(nodeEntropy *NodeEntropy) (*Node, error) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_builder_build_with_fs_store(
			_pointer, FfiConverterNodeEntropyINSTANCE.Lower(nodeEntropy), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Node
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Builder) BuildWithVssStore(nodeEntropy *NodeEntropy, vssUrl string, storeId string, fixedHeaders map[string]string) (*Node, error) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_builder_build_with_vss_store(
			_pointer, FfiConverterNodeEntropyINSTANCE.Lower(nodeEntropy), FfiConverterStringINSTANCE.Lower(vssUrl), FfiConverterStringINSTANCE.Lower(storeId), FfiConverterMapStringStringINSTANCE.Lower(fixedHeaders), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Node
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Builder) BuildWithVssStoreAndFixedHeaders(nodeEntropy *NodeEntropy, vssUrl string, storeId string, fixedHeaders map[string]string) (*Node, error) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_builder_build_with_vss_store_and_fixed_headers(
			_pointer, FfiConverterNodeEntropyINSTANCE.Lower(nodeEntropy), FfiConverterStringINSTANCE.Lower(vssUrl), FfiConverterStringINSTANCE.Lower(storeId), FfiConverterMapStringStringINSTANCE.Lower(fixedHeaders), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Node
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Builder) BuildWithVssStoreAndHeaderProvider(nodeEntropy *NodeEntropy, vssUrl string, storeId string, headerProvider VssHeaderProvider) (*Node, error) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_builder_build_with_vss_store_and_header_provider(
			_pointer, FfiConverterNodeEntropyINSTANCE.Lower(nodeEntropy), FfiConverterStringINSTANCE.Lower(vssUrl), FfiConverterStringINSTANCE.Lower(storeId), FfiConverterVssHeaderProviderINSTANCE.Lower(headerProvider), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Node
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Builder) BuildWithVssStoreAndLnurlAuth(nodeEntropy *NodeEntropy, vssUrl string, storeId string, lnurlAuthServerUrl string, fixedHeaders map[string]string) (*Node, error) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_builder_build_with_vss_store_and_lnurl_auth(
			_pointer, FfiConverterNodeEntropyINSTANCE.Lower(nodeEntropy), FfiConverterStringINSTANCE.Lower(vssUrl), FfiConverterStringINSTANCE.Lower(storeId), FfiConverterStringINSTANCE.Lower(lnurlAuthServerUrl), FfiConverterMapStringStringINSTANCE.Lower(fixedHeaders), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Node
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Builder) SetAnnouncementAddresses(announcementAddresses []SocketAddress) error {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_announcement_addresses(
			_pointer, FfiConverterSequenceTypeSocketAddressINSTANCE.Lower(announcementAddresses), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Builder) SetAsyncPaymentsRole(role *AsyncPaymentsRole) error {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_async_payments_role(
			_pointer, FfiConverterOptionalAsyncPaymentsRoleINSTANCE.Lower(role), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Builder) SetChainSourceBitcoindRest(restHost string, restPort uint16, rpcHost string, rpcPort uint16, rpcUser string, rpcPassword string) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_chain_source_bitcoind_rest(
			_pointer, FfiConverterStringINSTANCE.Lower(restHost), FfiConverterUint16INSTANCE.Lower(restPort), FfiConverterStringINSTANCE.Lower(rpcHost), FfiConverterUint16INSTANCE.Lower(rpcPort), FfiConverterStringINSTANCE.Lower(rpcUser), FfiConverterStringINSTANCE.Lower(rpcPassword), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetChainSourceBitcoindRpc(rpcHost string, rpcPort uint16, rpcUser string, rpcPassword string) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_chain_source_bitcoind_rpc(
			_pointer, FfiConverterStringINSTANCE.Lower(rpcHost), FfiConverterUint16INSTANCE.Lower(rpcPort), FfiConverterStringINSTANCE.Lower(rpcUser), FfiConverterStringINSTANCE.Lower(rpcPassword), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetChainSourceElectrum(serverUrl string, config *ElectrumSyncConfig) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_chain_source_electrum(
			_pointer, FfiConverterStringINSTANCE.Lower(serverUrl), FfiConverterOptionalElectrumSyncConfigINSTANCE.Lower(config), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetChainSourceEsplora(serverUrl string, config *EsploraSyncConfig) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_chain_source_esplora(
			_pointer, FfiConverterStringINSTANCE.Lower(serverUrl), FfiConverterOptionalEsploraSyncConfigINSTANCE.Lower(config), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetCustomLogger(logWriter LogWriter) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_custom_logger(
			_pointer, FfiConverterLogWriterINSTANCE.Lower(logWriter), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetFilesystemLogger(logFilePath *string, maxLogLevel *LogLevel) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_filesystem_logger(
			_pointer, FfiConverterOptionalStringINSTANCE.Lower(logFilePath), FfiConverterOptionalLogLevelINSTANCE.Lower(maxLogLevel), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetGossipSourceP2p() {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_gossip_source_p2p(
			_pointer, _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetGossipSourceRgs(rgsServerUrl string) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_gossip_source_rgs(
			_pointer, FfiConverterStringINSTANCE.Lower(rgsServerUrl), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetLiquiditySourceLsps1(nodeId PublicKey, address SocketAddress, token *string) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_liquidity_source_lsps1(
			_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypeSocketAddressINSTANCE.Lower(address), FfiConverterOptionalStringINSTANCE.Lower(token), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetLiquiditySourceLsps2(nodeId PublicKey, address SocketAddress, token *string) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_liquidity_source_lsps2(
			_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypeSocketAddressINSTANCE.Lower(address), FfiConverterOptionalStringINSTANCE.Lower(token), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetListeningAddresses(listeningAddresses []SocketAddress) error {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_listening_addresses(
			_pointer, FfiConverterSequenceTypeSocketAddressINSTANCE.Lower(listeningAddresses), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Builder) SetLogFacadeLogger() {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_log_facade_logger(
			_pointer, _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetNetwork(network Network) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_network(
			_pointer, FfiConverterNetworkINSTANCE.Lower(network), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetNodeAlias(nodeAlias string) error {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_node_alias(
			_pointer, FfiConverterStringINSTANCE.Lower(nodeAlias), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Builder) SetPathfindingScoresSource(url string) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_pathfinding_scores_source(
			_pointer, FfiConverterStringINSTANCE.Lower(url), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetStorageDirPath(storageDirPath string) {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_storage_dir_path(
			_pointer, FfiConverterStringINSTANCE.Lower(storageDirPath), _uniffiStatus)
		return false
	})
}

func (_self *Builder) SetTorConfig(torConfig TorConfig) error {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[BuildError](FfiConverterBuildError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_tor_config(
			_pointer, FfiConverterTorConfigINSTANCE.Lower(torConfig), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Builder) SetWalletRecoveryMode() {
	_pointer := _self.ffiObject.incrementPointer("*Builder")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_builder_set_wallet_recovery_mode(
			_pointer, _uniffiStatus)
		return false
	})
}
func (object *Builder) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterBuilder struct{}

var FfiConverterBuilderINSTANCE = FfiConverterBuilder{}

func (c FfiConverterBuilder) Lift(pointer unsafe.Pointer) *Builder {
	result := &Builder{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_builder(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_builder(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Builder).Destroy)
	return result
}

func (c FfiConverterBuilder) Read(reader io.Reader) *Builder {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterBuilder) Lower(value *Builder) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Builder")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterBuilder) Write(writer io.Writer, value *Builder) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerBuilder struct{}

func (_ FfiDestroyerBuilder) Destroy(value *Builder) {
	value.Destroy()
}

type FeeRateInterface interface {
	ToSatPerKwu() uint64
	ToSatPerVbCeil() uint64
	ToSatPerVbFloor() uint64
}
type FeeRate struct {
	ffiObject FfiObject
}

func FeeRateFromSatPerKwu(satKwu uint64) *FeeRate {
	return FfiConverterFeeRateINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_feerate_from_sat_per_kwu(FfiConverterUint64INSTANCE.Lower(satKwu), _uniffiStatus)
	}))
}

func FeeRateFromSatPerVbUnchecked(satVb uint64) *FeeRate {
	return FfiConverterFeeRateINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_feerate_from_sat_per_vb_unchecked(FfiConverterUint64INSTANCE.Lower(satVb), _uniffiStatus)
	}))
}

func (_self *FeeRate) ToSatPerKwu() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*FeeRate")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_feerate_to_sat_per_kwu(
			_pointer, _uniffiStatus)
	}))
}

func (_self *FeeRate) ToSatPerVbCeil() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*FeeRate")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_feerate_to_sat_per_vb_ceil(
			_pointer, _uniffiStatus)
	}))
}

func (_self *FeeRate) ToSatPerVbFloor() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*FeeRate")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_feerate_to_sat_per_vb_floor(
			_pointer, _uniffiStatus)
	}))
}
func (object *FeeRate) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterFeeRate struct{}

var FfiConverterFeeRateINSTANCE = FfiConverterFeeRate{}

func (c FfiConverterFeeRate) Lift(pointer unsafe.Pointer) *FeeRate {
	result := &FeeRate{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_feerate(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_feerate(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*FeeRate).Destroy)
	return result
}

func (c FfiConverterFeeRate) Read(reader io.Reader) *FeeRate {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterFeeRate) Lower(value *FeeRate) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*FeeRate")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterFeeRate) Write(writer io.Writer, value *FeeRate) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerFeeRate struct{}

func (_ FfiDestroyerFeeRate) Destroy(value *FeeRate) {
	value.Destroy()
}

// A struct containing the two parts of a BIP 353 Human-Readable Name - the user and domain parts.
//
// The `user` and `domain` parts combined cannot exceed 231 bytes in length;
// each DNS label within them must be non-empty and no longer than 63 bytes.
//
// If you intend to handle non-ASCII `user` or `domain` parts, you must handle [Homograph Attacks]
// and do punycode en-/de-coding yourself. This struct will always handle only plain ASCII `user`
// and `domain` parts.
//
// This struct can also be used for LN-Address recipients.
//
// [Homograph Attacks]: https://en.wikipedia.org/wiki/IDN_homograph_attack
type HumanReadableNameInterface interface {
	// Gets the `domain` part of this Human-Readable Name
	Domain() string
	// Gets the `user` part of this Human-Readable Name
	User() string
}

// A struct containing the two parts of a BIP 353 Human-Readable Name - the user and domain parts.
//
// The `user` and `domain` parts combined cannot exceed 231 bytes in length;
// each DNS label within them must be non-empty and no longer than 63 bytes.
//
// If you intend to handle non-ASCII `user` or `domain` parts, you must handle [Homograph Attacks]
// and do punycode en-/de-coding yourself. This struct will always handle only plain ASCII `user`
// and `domain` parts.
//
// This struct can also be used for LN-Address recipients.
//
// [Homograph Attacks]: https://en.wikipedia.org/wiki/IDN_homograph_attack
type HumanReadableName struct {
	ffiObject FfiObject
}

// Constructs a new [`HumanReadableName`] from the standard encoding - `user`@`domain`.
//
// If `user` includes the standard BIP 353 ₿ prefix it is automatically removed as required by
// BIP 353.
func HumanReadableNameFromEncoded(encoded string) (*HumanReadableName, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_humanreadablename_from_encoded(FfiConverterStringINSTANCE.Lower(encoded), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *HumanReadableName
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterHumanReadableNameINSTANCE.Lift(_uniffiRV), nil
	}
}

// Gets the `domain` part of this Human-Readable Name
func (_self *HumanReadableName) Domain() string {
	_pointer := _self.ffiObject.incrementPointer("*HumanReadableName")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_humanreadablename_domain(
				_pointer, _uniffiStatus),
		}
	}))
}

// Gets the `user` part of this Human-Readable Name
func (_self *HumanReadableName) User() string {
	_pointer := _self.ffiObject.incrementPointer("*HumanReadableName")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_humanreadablename_user(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *HumanReadableName) DebugString() string {
	_pointer := _self.ffiObject.incrementPointer("*HumanReadableName")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_humanreadablename_uniffi_trait_debug(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *HumanReadableName) String() string {
	_pointer := _self.ffiObject.incrementPointer("*HumanReadableName")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_humanreadablename_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *HumanReadableName) Eq(other *HumanReadableName) bool {
	_pointer := _self.ffiObject.incrementPointer("*HumanReadableName")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_humanreadablename_uniffi_trait_eq_eq(
			_pointer, FfiConverterHumanReadableNameINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (_self *HumanReadableName) Ne(other *HumanReadableName) bool {
	_pointer := _self.ffiObject.incrementPointer("*HumanReadableName")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_humanreadablename_uniffi_trait_eq_ne(
			_pointer, FfiConverterHumanReadableNameINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (object *HumanReadableName) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterHumanReadableName struct{}

var FfiConverterHumanReadableNameINSTANCE = FfiConverterHumanReadableName{}

func (c FfiConverterHumanReadableName) Lift(pointer unsafe.Pointer) *HumanReadableName {
	result := &HumanReadableName{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_humanreadablename(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_humanreadablename(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*HumanReadableName).Destroy)
	return result
}

func (c FfiConverterHumanReadableName) Read(reader io.Reader) *HumanReadableName {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterHumanReadableName) Lower(value *HumanReadableName) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*HumanReadableName")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterHumanReadableName) Write(writer io.Writer, value *HumanReadableName) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerHumanReadableName struct{}

func (_ FfiDestroyerHumanReadableName) Destroy(value *HumanReadableName) {
	value.Destroy()
}

// A liquidity handler allowing to request channels via the [bLIP-51 / LSPS1] protocol.
//
// Should be retrieved by calling [`Node::lsps1_liquidity`].
//
// To open [bLIP-52 / LSPS2] JIT channels, please refer to
// [`Bolt11Payment::receive_via_jit_channel`].
//
// [bLIP-51 / LSPS1]: https://github.com/lightning/blips/blob/master/blip-0051.md
// [bLIP-52 / LSPS2]: https://github.com/lightning/blips/blob/master/blip-0052.md
// [`Node::lsps1_liquidity`]: crate::Node::lsps1_liquidity
// [`Bolt11Payment::receive_via_jit_channel`]: crate::payment::Bolt11Payment::receive_via_jit_channel
type Lsps1LiquidityInterface interface {
	// Connects to the configured LSP and checks for the status of a previously-placed order.
	CheckOrderStatus(orderId LSPS1OrderId) (Lsps1OrderStatus, error)
	// Connects to the configured LSP and places an order for an inbound channel.
	//
	// The channel will be opened after one of the returned payment options has successfully been
	// paid.
	RequestChannel(lspBalanceSat uint64, clientBalanceSat uint64, channelExpiryBlocks uint32, announceChannel bool) (Lsps1OrderStatus, error)
}

// A liquidity handler allowing to request channels via the [bLIP-51 / LSPS1] protocol.
//
// Should be retrieved by calling [`Node::lsps1_liquidity`].
//
// To open [bLIP-52 / LSPS2] JIT channels, please refer to
// [`Bolt11Payment::receive_via_jit_channel`].
//
// [bLIP-51 / LSPS1]: https://github.com/lightning/blips/blob/master/blip-0051.md
// [bLIP-52 / LSPS2]: https://github.com/lightning/blips/blob/master/blip-0052.md
// [`Node::lsps1_liquidity`]: crate::Node::lsps1_liquidity
// [`Bolt11Payment::receive_via_jit_channel`]: crate::payment::Bolt11Payment::receive_via_jit_channel
type Lsps1Liquidity struct {
	ffiObject FfiObject
}

// Connects to the configured LSP and checks for the status of a previously-placed order.
func (_self *Lsps1Liquidity) CheckOrderStatus(orderId LSPS1OrderId) (Lsps1OrderStatus, error) {
	_pointer := _self.ffiObject.incrementPointer("*Lsps1Liquidity")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_lsps1liquidity_check_order_status(
				_pointer, FfiConverterTypeLSPS1OrderIdINSTANCE.Lower(orderId), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Lsps1OrderStatus
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterLsps1OrderStatusINSTANCE.Lift(_uniffiRV), nil
	}
}

// Connects to the configured LSP and places an order for an inbound channel.
//
// The channel will be opened after one of the returned payment options has successfully been
// paid.
func (_self *Lsps1Liquidity) RequestChannel(lspBalanceSat uint64, clientBalanceSat uint64, channelExpiryBlocks uint32, announceChannel bool) (Lsps1OrderStatus, error) {
	_pointer := _self.ffiObject.incrementPointer("*Lsps1Liquidity")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_lsps1liquidity_request_channel(
				_pointer, FfiConverterUint64INSTANCE.Lower(lspBalanceSat), FfiConverterUint64INSTANCE.Lower(clientBalanceSat), FfiConverterUint32INSTANCE.Lower(channelExpiryBlocks), FfiConverterBoolINSTANCE.Lower(announceChannel), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Lsps1OrderStatus
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterLsps1OrderStatusINSTANCE.Lift(_uniffiRV), nil
	}
}
func (object *Lsps1Liquidity) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterLsps1Liquidity struct{}

var FfiConverterLsps1LiquidityINSTANCE = FfiConverterLsps1Liquidity{}

func (c FfiConverterLsps1Liquidity) Lift(pointer unsafe.Pointer) *Lsps1Liquidity {
	result := &Lsps1Liquidity{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_lsps1liquidity(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_lsps1liquidity(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Lsps1Liquidity).Destroy)
	return result
}

func (c FfiConverterLsps1Liquidity) Read(reader io.Reader) *Lsps1Liquidity {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterLsps1Liquidity) Lower(value *Lsps1Liquidity) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Lsps1Liquidity")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterLsps1Liquidity) Write(writer io.Writer, value *Lsps1Liquidity) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerLsps1Liquidity struct{}

func (_ FfiDestroyerLsps1Liquidity) Destroy(value *Lsps1Liquidity) {
	value.Destroy()
}

type LogWriter interface {
	Log(record LogRecord)
}
type LogWriterImpl struct {
	ffiObject FfiObject
}

func (_self *LogWriterImpl) Log(record LogRecord) {
	_pointer := _self.ffiObject.incrementPointer("LogWriter")
	defer _self.ffiObject.decrementPointer()
	rustCall(func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_logwriter_log(
			_pointer, FfiConverterLogRecordINSTANCE.Lower(record), _uniffiStatus)
		return false
	})
}
func (object *LogWriterImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterLogWriter struct {
	handleMap *concurrentHandleMap[LogWriter]
}

var FfiConverterLogWriterINSTANCE = FfiConverterLogWriter{
	handleMap: newConcurrentHandleMap[LogWriter](),
}

func (c FfiConverterLogWriter) Lift(pointer unsafe.Pointer) LogWriter {
	result := &LogWriterImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_logwriter(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_logwriter(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*LogWriterImpl).Destroy)
	return result
}

func (c FfiConverterLogWriter) Read(reader io.Reader) LogWriter {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterLogWriter) Lower(value LogWriter) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterLogWriter) Write(writer io.Writer, value LogWriter) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerLogWriter struct{}

func (_ FfiDestroyerLogWriter) Destroy(value LogWriter) {
	if val, ok := value.(*LogWriterImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *LogWriterImpl")
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

//export ldk_node_cgo_dispatchCallbackInterfaceLogWriterMethod0
func ldk_node_cgo_dispatchCallbackInterfaceLogWriterMethod0(uniffiHandle C.uint64_t, record C.RustBuffer, uniffiOutReturn *C.void, callStatus *C.RustCallStatus) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterLogWriterINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	uniffiObj.Log(
		FfiConverterLogRecordINSTANCE.Lift(GoRustBuffer{
			inner: record,
		}),
	)

}

var UniffiVTableCallbackInterfaceLogWriterINSTANCE = C.UniffiVTableCallbackInterfaceLogWriter{
	log: (C.UniffiCallbackInterfaceLogWriterMethod0)(C.ldk_node_cgo_dispatchCallbackInterfaceLogWriterMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.ldk_node_cgo_dispatchCallbackInterfaceLogWriterFree),
}

//export ldk_node_cgo_dispatchCallbackInterfaceLogWriterFree
func ldk_node_cgo_dispatchCallbackInterfaceLogWriterFree(handle C.uint64_t) {
	FfiConverterLogWriterINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterLogWriter) register() {
	C.uniffi_ldk_node_fn_init_callback_vtable_logwriter(&UniffiVTableCallbackInterfaceLogWriterINSTANCE)
}

// Represents the network as nodes and channels between them.
type NetworkGraphInterface interface {
	// Returns information on a channel with the given id.
	Channel(shortChannelId uint64) *ChannelInfo
	// Returns the list of channels in the graph
	ListChannels() []uint64
	// Returns the list of nodes in the graph
	ListNodes() []NodeId
	// Returns information on a node with the given id.
	Node(nodeId NodeId) *NodeInfo
}

// Represents the network as nodes and channels between them.
type NetworkGraph struct {
	ffiObject FfiObject
}

// Returns information on a channel with the given id.
func (_self *NetworkGraph) Channel(shortChannelId uint64) *ChannelInfo {
	_pointer := _self.ffiObject.incrementPointer("*NetworkGraph")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalChannelInfoINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_networkgraph_channel(
				_pointer, FfiConverterUint64INSTANCE.Lower(shortChannelId), _uniffiStatus),
		}
	}))
}

// Returns the list of channels in the graph
func (_self *NetworkGraph) ListChannels() []uint64 {
	_pointer := _self.ffiObject.incrementPointer("*NetworkGraph")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_networkgraph_list_channels(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns the list of nodes in the graph
func (_self *NetworkGraph) ListNodes() []NodeId {
	_pointer := _self.ffiObject.incrementPointer("*NetworkGraph")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceTypeNodeIdINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_networkgraph_list_nodes(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns information on a node with the given id.
func (_self *NetworkGraph) Node(nodeId NodeId) *NodeInfo {
	_pointer := _self.ffiObject.incrementPointer("*NetworkGraph")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalNodeInfoINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_networkgraph_node(
				_pointer, FfiConverterTypeNodeIdINSTANCE.Lower(nodeId), _uniffiStatus),
		}
	}))
}
func (object *NetworkGraph) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNetworkGraph struct{}

var FfiConverterNetworkGraphINSTANCE = FfiConverterNetworkGraph{}

func (c FfiConverterNetworkGraph) Lift(pointer unsafe.Pointer) *NetworkGraph {
	result := &NetworkGraph{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_networkgraph(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_networkgraph(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*NetworkGraph).Destroy)
	return result
}

func (c FfiConverterNetworkGraph) Read(reader io.Reader) *NetworkGraph {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNetworkGraph) Lower(value *NetworkGraph) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*NetworkGraph")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNetworkGraph) Write(writer io.Writer, value *NetworkGraph) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNetworkGraph struct{}

func (_ FfiDestroyerNetworkGraph) Destroy(value *NetworkGraph) {
	value.Destroy()
}

type NodeInterface interface {
	AnnouncementAddresses() *[]SocketAddress
	Bolt11Payment() *Bolt11Payment
	Bolt12Payment() *Bolt12Payment
	CloseChannel(userChannelId UserChannelId, counterpartyNodeId PublicKey) error
	Config() Config
	Connect(nodeId PublicKey, address SocketAddress, persist bool) error
	Disconnect(nodeId PublicKey) error
	EventHandled() error
	ExportPathfindingScores() ([]byte, error)
	ForceCloseChannel(userChannelId UserChannelId, counterpartyNodeId PublicKey, reason *string) error
	ListBalances() BalanceDetails
	ListChannels() []ChannelDetails
	ListPayments() []PaymentDetails
	ListPeers() []PeerDetails
	ListeningAddresses() *[]SocketAddress
	LnurlAuth(lnurl string) error
	Lsps1Liquidity() *Lsps1Liquidity
	NetworkGraph() *NetworkGraph
	NextEvent() *Event
	NextEventAsync() Event
	NodeAlias() *NodeAlias
	NodeId() PublicKey
	OnchainPayment() *OnchainPayment
	OpenAnnouncedChannel(nodeId PublicKey, address SocketAddress, channelAmountSats uint64, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error)
	OpenAnnouncedChannelWithAll(nodeId PublicKey, address SocketAddress, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error)
	OpenChannel(nodeId PublicKey, address SocketAddress, channelAmountSats uint64, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error)
	OpenChannelWithAll(nodeId PublicKey, address SocketAddress, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error)
	Payment(paymentId PaymentId) *PaymentDetails
	RemovePayment(paymentId PaymentId) error
	SignMessage(msg []uint8) string
	SpliceIn(userChannelId UserChannelId, counterpartyNodeId PublicKey, spliceAmountSats uint64) error
	SpliceInWithAll(userChannelId UserChannelId, counterpartyNodeId PublicKey) error
	SpliceOut(userChannelId UserChannelId, counterpartyNodeId PublicKey, address Address, spliceAmountSats uint64) error
	SpontaneousPayment() *SpontaneousPayment
	Start() error
	Status() NodeStatus
	Stop() error
	SyncWallets() error
	UnifiedPayment() *UnifiedPayment
	UpdateChannelConfig(userChannelId UserChannelId, counterpartyNodeId PublicKey, channelConfig ChannelConfig) error
	VerifySignature(msg []uint8, sig string, pkey PublicKey) bool
	WaitNextEvent() Event
}
type Node struct {
	ffiObject FfiObject
}

func (_self *Node) AnnouncementAddresses() *[]SocketAddress {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalSequenceTypeSocketAddressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_announcement_addresses(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) Bolt11Payment() *Bolt11Payment {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBolt11PaymentINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_node_bolt11_payment(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Node) Bolt12Payment() *Bolt12Payment {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBolt12PaymentINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_node_bolt12_payment(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Node) CloseChannel(userChannelId UserChannelId, counterpartyNodeId PublicKey) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_close_channel(
			_pointer, FfiConverterTypeUserChannelIdINSTANCE.Lower(userChannelId), FfiConverterTypePublicKeyINSTANCE.Lower(counterpartyNodeId), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) Config() Config {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterConfigINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_config(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) Connect(nodeId PublicKey, address SocketAddress, persist bool) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_connect(
			_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypeSocketAddressINSTANCE.Lower(address), FfiConverterBoolINSTANCE.Lower(persist), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) Disconnect(nodeId PublicKey) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_disconnect(
			_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) EventHandled() error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_event_handled(
			_pointer, _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) ExportPathfindingScores() ([]byte, error) {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_export_pathfinding_scores(
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

func (_self *Node) ForceCloseChannel(userChannelId UserChannelId, counterpartyNodeId PublicKey, reason *string) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_force_close_channel(
			_pointer, FfiConverterTypeUserChannelIdINSTANCE.Lower(userChannelId), FfiConverterTypePublicKeyINSTANCE.Lower(counterpartyNodeId), FfiConverterOptionalStringINSTANCE.Lower(reason), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) ListBalances() BalanceDetails {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBalanceDetailsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_list_balances(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) ListChannels() []ChannelDetails {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceChannelDetailsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_list_channels(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) ListPayments() []PaymentDetails {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequencePaymentDetailsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_list_payments(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) ListPeers() []PeerDetails {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequencePeerDetailsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_list_peers(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) ListeningAddresses() *[]SocketAddress {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalSequenceTypeSocketAddressINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_listening_addresses(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) LnurlAuth(lnurl string) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_lnurl_auth(
			_pointer, FfiConverterStringINSTANCE.Lower(lnurl), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) Lsps1Liquidity() *Lsps1Liquidity {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterLsps1LiquidityINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_node_lsps1_liquidity(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Node) NetworkGraph() *NetworkGraph {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNetworkGraphINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_node_network_graph(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Node) NextEvent() *Event {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalEventINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_next_event(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) NextEventAsync() Event {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	res, _ := uniffiRustCallAsync[error](
		nil,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_ldk_node_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) Event {
			return FfiConverterEventINSTANCE.Lift(ffi)
		},
		C.uniffi_ldk_node_fn_method_node_next_event_async(
			_pointer),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_ldk_node_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_ldk_node_rust_future_free_rust_buffer(handle)
		},
	)

	return res
}

func (_self *Node) NodeAlias() *NodeAlias {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalTypeNodeAliasINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_node_alias(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) NodeId() PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_node_id(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) OnchainPayment() *OnchainPayment {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOnchainPaymentINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_node_onchain_payment(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Node) OpenAnnouncedChannel(nodeId PublicKey, address SocketAddress, channelAmountSats uint64, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_open_announced_channel(
				_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypeSocketAddressINSTANCE.Lower(address), FfiConverterUint64INSTANCE.Lower(channelAmountSats), FfiConverterOptionalUint64INSTANCE.Lower(pushToCounterpartyMsat), FfiConverterOptionalChannelConfigINSTANCE.Lower(channelConfig), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue UserChannelId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeUserChannelIdINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Node) OpenAnnouncedChannelWithAll(nodeId PublicKey, address SocketAddress, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_open_announced_channel_with_all(
				_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypeSocketAddressINSTANCE.Lower(address), FfiConverterOptionalUint64INSTANCE.Lower(pushToCounterpartyMsat), FfiConverterOptionalChannelConfigINSTANCE.Lower(channelConfig), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue UserChannelId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeUserChannelIdINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Node) OpenChannel(nodeId PublicKey, address SocketAddress, channelAmountSats uint64, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_open_channel(
				_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypeSocketAddressINSTANCE.Lower(address), FfiConverterUint64INSTANCE.Lower(channelAmountSats), FfiConverterOptionalUint64INSTANCE.Lower(pushToCounterpartyMsat), FfiConverterOptionalChannelConfigINSTANCE.Lower(channelConfig), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue UserChannelId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeUserChannelIdINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Node) OpenChannelWithAll(nodeId PublicKey, address SocketAddress, pushToCounterpartyMsat *uint64, channelConfig *ChannelConfig) (UserChannelId, error) {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_open_channel_with_all(
				_pointer, FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypeSocketAddressINSTANCE.Lower(address), FfiConverterOptionalUint64INSTANCE.Lower(pushToCounterpartyMsat), FfiConverterOptionalChannelConfigINSTANCE.Lower(channelConfig), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue UserChannelId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeUserChannelIdINSTANCE.Lift(_uniffiRV), nil
	}
}

func (_self *Node) Payment(paymentId PaymentId) *PaymentDetails {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalPaymentDetailsINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_payment(
				_pointer, FfiConverterTypePaymentIdINSTANCE.Lower(paymentId), _uniffiStatus),
		}
	}))
}

func (_self *Node) RemovePayment(paymentId PaymentId) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_remove_payment(
			_pointer, FfiConverterTypePaymentIdINSTANCE.Lower(paymentId), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) SignMessage(msg []uint8) string {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_sign_message(
				_pointer, FfiConverterSequenceUint8INSTANCE.Lower(msg), _uniffiStatus),
		}
	}))
}

func (_self *Node) SpliceIn(userChannelId UserChannelId, counterpartyNodeId PublicKey, spliceAmountSats uint64) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_splice_in(
			_pointer, FfiConverterTypeUserChannelIdINSTANCE.Lower(userChannelId), FfiConverterTypePublicKeyINSTANCE.Lower(counterpartyNodeId), FfiConverterUint64INSTANCE.Lower(spliceAmountSats), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) SpliceInWithAll(userChannelId UserChannelId, counterpartyNodeId PublicKey) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_splice_in_with_all(
			_pointer, FfiConverterTypeUserChannelIdINSTANCE.Lower(userChannelId), FfiConverterTypePublicKeyINSTANCE.Lower(counterpartyNodeId), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) SpliceOut(userChannelId UserChannelId, counterpartyNodeId PublicKey, address Address, spliceAmountSats uint64) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_splice_out(
			_pointer, FfiConverterTypeUserChannelIdINSTANCE.Lower(userChannelId), FfiConverterTypePublicKeyINSTANCE.Lower(counterpartyNodeId), FfiConverterTypeAddressINSTANCE.Lower(address), FfiConverterUint64INSTANCE.Lower(spliceAmountSats), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) SpontaneousPayment() *SpontaneousPayment {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSpontaneousPaymentINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_node_spontaneous_payment(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Node) Start() error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_start(
			_pointer, _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) Status() NodeStatus {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterNodeStatusINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_status(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Node) Stop() error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_stop(
			_pointer, _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) SyncWallets() error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_sync_wallets(
			_pointer, _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) UnifiedPayment() *UnifiedPayment {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUnifiedPaymentINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_method_node_unified_payment(
			_pointer, _uniffiStatus)
	}))
}

func (_self *Node) UpdateChannelConfig(userChannelId UserChannelId, counterpartyNodeId PublicKey, channelConfig ChannelConfig) error {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_node_update_channel_config(
			_pointer, FfiConverterTypeUserChannelIdINSTANCE.Lower(userChannelId), FfiConverterTypePublicKeyINSTANCE.Lower(counterpartyNodeId), FfiConverterChannelConfigINSTANCE.Lower(channelConfig), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

func (_self *Node) VerifySignature(msg []uint8, sig string, pkey PublicKey) bool {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_node_verify_signature(
			_pointer, FfiConverterSequenceUint8INSTANCE.Lower(msg), FfiConverterStringINSTANCE.Lower(sig), FfiConverterTypePublicKeyINSTANCE.Lower(pkey), _uniffiStatus)
	}))
}

func (_self *Node) WaitNextEvent() Event {
	_pointer := _self.ffiObject.incrementPointer("*Node")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterEventINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_node_wait_next_event(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *Node) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNode struct{}

var FfiConverterNodeINSTANCE = FfiConverterNode{}

func (c FfiConverterNode) Lift(pointer unsafe.Pointer) *Node {
	result := &Node{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_node(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_node(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Node).Destroy)
	return result
}

func (c FfiConverterNode) Read(reader io.Reader) *Node {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNode) Lower(value *Node) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Node")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNode) Write(writer io.Writer, value *Node) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNode struct{}

func (_ FfiDestroyerNode) Destroy(value *Node) {
	value.Destroy()
}

// The node entropy, i.e., the main secret from which all other secrets of the [`Node`] are
// derived.
//
// [`Node`]: crate::Node
type NodeEntropyInterface interface {
}

// The node entropy, i.e., the main secret from which all other secrets of the [`Node`] are
// derived.
//
// [`Node`]: crate::Node
type NodeEntropy struct {
	ffiObject FfiObject
}

// Configures the [`Node`] instance to source its wallet entropy from a [BIP 39] mnemonic.
//
// [BIP 39]: https://github.com/bitcoin/bips/blob/master/bip-0039.mediawiki
// [`Node`]: crate::Node
func NodeEntropyFromBip39Mnemonic(mnemonic Mnemonic, passphrase *string) *NodeEntropy {
	return FfiConverterNodeEntropyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_nodeentropy_from_bip39_mnemonic(FfiConverterTypeMnemonicINSTANCE.Lower(mnemonic), FfiConverterOptionalStringINSTANCE.Lower(passphrase), _uniffiStatus)
	}))
}

// Configures the [`Node`] instance to source its wallet entropy from the given
// [`WALLET_KEYS_SEED_LEN`] seed bytes.
//
// Will return an error if the length of the given `Vec` is not exactly
// [`WALLET_KEYS_SEED_LEN`].
//
// [`Node`]: crate::Node
func NodeEntropyFromSeedBytes(seedBytes []byte) (*NodeEntropy, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[EntropyError](FfiConverterEntropyError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_nodeentropy_from_seed_bytes(FfiConverterBytesINSTANCE.Lower(seedBytes), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *NodeEntropy
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeEntropyINSTANCE.Lift(_uniffiRV), nil
	}
}

// Configures the [`Node`] instance to source its wallet entropy from a seed file on disk.
//
// If the given file does not exist a new random seed file will be generated and
// stored at the given location.
//
// [`Node`]: crate::Node
func NodeEntropyFromSeedPath(seedPath string) (*NodeEntropy, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[EntropyError](FfiConverterEntropyError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_nodeentropy_from_seed_path(FfiConverterStringINSTANCE.Lower(seedPath), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *NodeEntropy
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterNodeEntropyINSTANCE.Lift(_uniffiRV), nil
	}
}

func (object *NodeEntropy) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterNodeEntropy struct{}

var FfiConverterNodeEntropyINSTANCE = FfiConverterNodeEntropy{}

func (c FfiConverterNodeEntropy) Lift(pointer unsafe.Pointer) *NodeEntropy {
	result := &NodeEntropy{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_nodeentropy(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_nodeentropy(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*NodeEntropy).Destroy)
	return result
}

func (c FfiConverterNodeEntropy) Read(reader io.Reader) *NodeEntropy {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterNodeEntropy) Lower(value *NodeEntropy) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*NodeEntropy")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterNodeEntropy) Write(writer io.Writer, value *NodeEntropy) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerNodeEntropy struct{}

func (_ FfiDestroyerNodeEntropy) Destroy(value *NodeEntropy) {
	value.Destroy()
}

// An `Offer` is a potentially long-lived proposal for payment of a good or service.
//
// An offer is a precursor to an [`InvoiceRequest`]. A merchant publishes an offer from which a
// customer may request an [`Bolt12Invoice`] for a specific quantity and using an amount sufficient
// to cover that quantity (i.e., at least `quantity * amount`). See [`Offer::amount`].
//
// Offers may be denominated in currency other than bitcoin but are ultimately paid using the
// latter.
//
// Through the use of [`BlindedMessagePath`]s, offers provide recipient privacy.
//
// [`InvoiceRequest`]: lightning::offers::invoice_request::InvoiceRequest
// [`Bolt12Invoice`]: lightning::offers::invoice::Bolt12Invoice
// [`Offer`]: lightning::offers::Offer:amount
type OfferInterface interface {
	// Seconds since the Unix epoch when an invoice should no longer be requested.
	//
	// If `None`, the offer does not expire.
	AbsoluteExpirySeconds() *uint64
	// The minimum amount required for a successful payment of a single item.
	Amount() *OfferAmount
	// The chains that may be used when paying a requested invoice (e.g., bitcoin mainnet).
	//
	// Payments must be denominated in units of the minimal lightning-payable unit (e.g., msats)
	// for the selected chain.
	Chains() []Network
	// Returns whether a quantity is expected in an [`InvoiceRequest`] for the offer.
	//
	// [`InvoiceRequest`]: lightning::offers::invoice_request::InvoiceRequest
	ExpectsQuantity() bool
	// Returns the id of the offer.
	Id() OfferId
	// Whether the offer has expired.
	IsExpired() bool
	// Returns whether the given quantity is valid for the offer.
	IsValidQuantity(quantity uint64) bool
	// The issuer of the offer, possibly beginning with `user@domain` or `domain`.
	//
	// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
	Issuer() *string
	// The public key corresponding to the key used by the recipient to sign invoices.
	// - If [`Offer::paths`] is empty, MUST be `Some` and contain the recipient's node id for
	// sending an [`InvoiceRequest`].
	// - If [`Offer::paths`] is not empty, MAY be `Some` and contain a transient id.
	// - If `None`, the signing pubkey will be the final blinded node id from the
	// [`BlindedMessagePath`] in [`Offer::paths`] used to send the [`InvoiceRequest`].
	//
	// See also [`Bolt12Invoice::signing_pubkey`].
	//
	// [`InvoiceRequest`]: lightning::offers::invoice_request::InvoiceRequest
	// [`Bolt12Invoice::signing_pubkey`]: lightning::offers::invoice::Bolt12Invoice::signing_pubkey
	IssuerSigningPubkey() *PublicKey
	// Opaque bytes set by the originator.
	//
	// Useful for authentication and validating fields since it is reflected in `invoice_request`
	// messages along with all the other fields from the `offer`.
	Metadata() *[]byte
	// A complete description of the purpose of the payment.
	//
	// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
	OfferDescription() *string
	// Returns whether the given chain is supported by the offer.
	SupportsChain(chain Network) bool
}

// An `Offer` is a potentially long-lived proposal for payment of a good or service.
//
// An offer is a precursor to an [`InvoiceRequest`]. A merchant publishes an offer from which a
// customer may request an [`Bolt12Invoice`] for a specific quantity and using an amount sufficient
// to cover that quantity (i.e., at least `quantity * amount`). See [`Offer::amount`].
//
// Offers may be denominated in currency other than bitcoin but are ultimately paid using the
// latter.
//
// Through the use of [`BlindedMessagePath`]s, offers provide recipient privacy.
//
// [`InvoiceRequest`]: lightning::offers::invoice_request::InvoiceRequest
// [`Bolt12Invoice`]: lightning::offers::invoice::Bolt12Invoice
// [`Offer`]: lightning::offers::Offer:amount
type Offer struct {
	ffiObject FfiObject
}

func OfferFromStr(offerStr string) (*Offer, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_offer_from_str(FfiConverterStringINSTANCE.Lower(offerStr), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Offer
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterOfferINSTANCE.Lift(_uniffiRV), nil
	}
}

// Seconds since the Unix epoch when an invoice should no longer be requested.
//
// If `None`, the offer does not expire.
func (_self *Offer) AbsoluteExpirySeconds() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_absolute_expiry_seconds(
				_pointer, _uniffiStatus),
		}
	}))
}

// The minimum amount required for a successful payment of a single item.
func (_self *Offer) Amount() *OfferAmount {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalOfferAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_amount(
				_pointer, _uniffiStatus),
		}
	}))
}

// The chains that may be used when paying a requested invoice (e.g., bitcoin mainnet).
//
// Payments must be denominated in units of the minimal lightning-payable unit (e.g., msats)
// for the selected chain.
func (_self *Offer) Chains() []Network {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterSequenceNetworkINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_chains(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns whether a quantity is expected in an [`InvoiceRequest`] for the offer.
//
// [`InvoiceRequest`]: lightning::offers::invoice_request::InvoiceRequest
func (_self *Offer) ExpectsQuantity() bool {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_offer_expects_quantity(
			_pointer, _uniffiStatus)
	}))
}

// Returns the id of the offer.
func (_self *Offer) Id() OfferId {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypeOfferIdINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_id(
				_pointer, _uniffiStatus),
		}
	}))
}

// Whether the offer has expired.
func (_self *Offer) IsExpired() bool {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_offer_is_expired(
			_pointer, _uniffiStatus)
	}))
}

// Returns whether the given quantity is valid for the offer.
func (_self *Offer) IsValidQuantity(quantity uint64) bool {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_offer_is_valid_quantity(
			_pointer, FfiConverterUint64INSTANCE.Lower(quantity), _uniffiStatus)
	}))
}

// The issuer of the offer, possibly beginning with `user@domain` or `domain`.
//
// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
func (_self *Offer) Issuer() *string {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_issuer(
				_pointer, _uniffiStatus),
		}
	}))
}

// The public key corresponding to the key used by the recipient to sign invoices.
// - If [`Offer::paths`] is empty, MUST be `Some` and contain the recipient's node id for
// sending an [`InvoiceRequest`].
// - If [`Offer::paths`] is not empty, MAY be `Some` and contain a transient id.
// - If `None`, the signing pubkey will be the final blinded node id from the
// [`BlindedMessagePath`] in [`Offer::paths`] used to send the [`InvoiceRequest`].
//
// See also [`Bolt12Invoice::signing_pubkey`].
//
// [`InvoiceRequest`]: lightning::offers::invoice_request::InvoiceRequest
// [`Bolt12Invoice::signing_pubkey`]: lightning::offers::invoice::Bolt12Invoice::signing_pubkey
func (_self *Offer) IssuerSigningPubkey() *PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalTypePublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_issuer_signing_pubkey(
				_pointer, _uniffiStatus),
		}
	}))
}

// Opaque bytes set by the originator.
//
// Useful for authentication and validating fields since it is reflected in `invoice_request`
// messages along with all the other fields from the `offer`.
func (_self *Offer) Metadata() *[]byte {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_metadata(
				_pointer, _uniffiStatus),
		}
	}))
}

// A complete description of the purpose of the payment.
//
// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
func (_self *Offer) OfferDescription() *string {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_offer_description(
				_pointer, _uniffiStatus),
		}
	}))
}

// Returns whether the given chain is supported by the offer.
func (_self *Offer) SupportsChain(chain Network) bool {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_offer_supports_chain(
			_pointer, FfiConverterNetworkINSTANCE.Lower(chain), _uniffiStatus)
	}))
}

func (_self *Offer) DebugString() string {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_uniffi_trait_debug(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Offer) String() string {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_offer_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Offer) Eq(other *Offer) bool {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_offer_uniffi_trait_eq_eq(
			_pointer, FfiConverterOfferINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (_self *Offer) Ne(other *Offer) bool {
	_pointer := _self.ffiObject.incrementPointer("*Offer")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_offer_uniffi_trait_eq_ne(
			_pointer, FfiConverterOfferINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (object *Offer) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterOffer struct{}

var FfiConverterOfferINSTANCE = FfiConverterOffer{}

func (c FfiConverterOffer) Lift(pointer unsafe.Pointer) *Offer {
	result := &Offer{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_offer(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_offer(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Offer).Destroy)
	return result
}

func (c FfiConverterOffer) Read(reader io.Reader) *Offer {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterOffer) Lower(value *Offer) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Offer")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterOffer) Write(writer io.Writer, value *Offer) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerOffer struct{}

func (_ FfiDestroyerOffer) Destroy(value *Offer) {
	value.Destroy()
}

// A payment handler allowing to send and receive on-chain payments.
//
// Should be retrieved by calling [`Node::onchain_payment`].
//
// [`Node::onchain_payment`]: crate::Node::onchain_payment
type OnchainPaymentInterface interface {
	// Attempt to bump the fee of an unconfirmed transaction using Replace-by-Fee (RBF).
	//
	// This creates a new transaction that replaces the original one, increasing the fee by the
	// specified increment to improve its chances of confirmation.
	//
	// The new transaction will have the same outputs as the original but with a
	// higher fee, resulting in faster confirmation potential.
	//
	// Returns the [`Txid`] of the new replacement transaction if successful.
	BumpFeeRbf(paymentId PaymentId, feeRate **FeeRate) (Txid, error)
	// Retrieve a new on-chain/funding address.
	NewAddress() (Address, error)
	// Send an on-chain payment to the given address, draining the available funds.
	//
	// This is useful if you have closed all channels and want to migrate funds to another
	// on-chain wallet.
	//
	// Please note that if `retain_reserves` is set to `false` this will **not** retain any on-chain reserves, which might be potentially
	// dangerous if you have open Anchor channels for which you can't trust the counterparty to
	// spend the Anchor output after channel closure. If `retain_reserves` is set to `true`, this
	// will try to send all spendable onchain funds, i.e.,
	// [`BalanceDetails::spendable_onchain_balance_sats`].
	//
	// If `fee_rate` is set it will be used on the resulting transaction. Otherwise a reasonable
	// we'll retrieve an estimate from the configured chain source.
	//
	// [`BalanceDetails::spendable_onchain_balance_sats`]: crate::balance::BalanceDetails::spendable_onchain_balance_sats
	SendAllToAddress(address Address, retainReserves bool, feeRate **FeeRate) (Txid, error)
	// Send an on-chain payment to the given address.
	//
	// This will respect any on-chain reserve we need to keep, i.e., won't allow to cut into
	// [`BalanceDetails::total_anchor_channels_reserve_sats`].
	//
	// If `fee_rate` is set it will be used on the resulting transaction. Otherwise we'll retrieve
	// a reasonable estimate from the configured chain source.
	//
	// [`BalanceDetails::total_anchor_channels_reserve_sats`]: crate::BalanceDetails::total_anchor_channels_reserve_sats
	SendToAddress(address Address, amountSats uint64, feeRate **FeeRate) (Txid, error)
}

// A payment handler allowing to send and receive on-chain payments.
//
// Should be retrieved by calling [`Node::onchain_payment`].
//
// [`Node::onchain_payment`]: crate::Node::onchain_payment
type OnchainPayment struct {
	ffiObject FfiObject
}

// Attempt to bump the fee of an unconfirmed transaction using Replace-by-Fee (RBF).
//
// This creates a new transaction that replaces the original one, increasing the fee by the
// specified increment to improve its chances of confirmation.
//
// The new transaction will have the same outputs as the original but with a
// higher fee, resulting in faster confirmation potential.
//
// Returns the [`Txid`] of the new replacement transaction if successful.
func (_self *OnchainPayment) BumpFeeRbf(paymentId PaymentId, feeRate **FeeRate) (Txid, error) {
	_pointer := _self.ffiObject.incrementPointer("*OnchainPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_onchainpayment_bump_fee_rbf(
				_pointer, FfiConverterTypePaymentIdINSTANCE.Lower(paymentId), FfiConverterOptionalFeeRateINSTANCE.Lower(feeRate), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Txid
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeTxidINSTANCE.Lift(_uniffiRV), nil
	}
}

// Retrieve a new on-chain/funding address.
func (_self *OnchainPayment) NewAddress() (Address, error) {
	_pointer := _self.ffiObject.incrementPointer("*OnchainPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_onchainpayment_new_address(
				_pointer, _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Address
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeAddressINSTANCE.Lift(_uniffiRV), nil
	}
}

// Send an on-chain payment to the given address, draining the available funds.
//
// This is useful if you have closed all channels and want to migrate funds to another
// on-chain wallet.
//
// Please note that if `retain_reserves` is set to `false` this will **not** retain any on-chain reserves, which might be potentially
// dangerous if you have open Anchor channels for which you can't trust the counterparty to
// spend the Anchor output after channel closure. If `retain_reserves` is set to `true`, this
// will try to send all spendable onchain funds, i.e.,
// [`BalanceDetails::spendable_onchain_balance_sats`].
//
// If `fee_rate` is set it will be used on the resulting transaction. Otherwise a reasonable
// we'll retrieve an estimate from the configured chain source.
//
// [`BalanceDetails::spendable_onchain_balance_sats`]: crate::balance::BalanceDetails::spendable_onchain_balance_sats
func (_self *OnchainPayment) SendAllToAddress(address Address, retainReserves bool, feeRate **FeeRate) (Txid, error) {
	_pointer := _self.ffiObject.incrementPointer("*OnchainPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_onchainpayment_send_all_to_address(
				_pointer, FfiConverterTypeAddressINSTANCE.Lower(address), FfiConverterBoolINSTANCE.Lower(retainReserves), FfiConverterOptionalFeeRateINSTANCE.Lower(feeRate), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Txid
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeTxidINSTANCE.Lift(_uniffiRV), nil
	}
}

// Send an on-chain payment to the given address.
//
// This will respect any on-chain reserve we need to keep, i.e., won't allow to cut into
// [`BalanceDetails::total_anchor_channels_reserve_sats`].
//
// If `fee_rate` is set it will be used on the resulting transaction. Otherwise we'll retrieve
// a reasonable estimate from the configured chain source.
//
// [`BalanceDetails::total_anchor_channels_reserve_sats`]: crate::BalanceDetails::total_anchor_channels_reserve_sats
func (_self *OnchainPayment) SendToAddress(address Address, amountSats uint64, feeRate **FeeRate) (Txid, error) {
	_pointer := _self.ffiObject.incrementPointer("*OnchainPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_onchainpayment_send_to_address(
				_pointer, FfiConverterTypeAddressINSTANCE.Lower(address), FfiConverterUint64INSTANCE.Lower(amountSats), FfiConverterOptionalFeeRateINSTANCE.Lower(feeRate), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue Txid
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypeTxidINSTANCE.Lift(_uniffiRV), nil
	}
}
func (object *OnchainPayment) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterOnchainPayment struct{}

var FfiConverterOnchainPaymentINSTANCE = FfiConverterOnchainPayment{}

func (c FfiConverterOnchainPayment) Lift(pointer unsafe.Pointer) *OnchainPayment {
	result := &OnchainPayment{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_onchainpayment(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_onchainpayment(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*OnchainPayment).Destroy)
	return result
}

func (c FfiConverterOnchainPayment) Read(reader io.Reader) *OnchainPayment {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterOnchainPayment) Lower(value *OnchainPayment) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*OnchainPayment")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterOnchainPayment) Write(writer io.Writer, value *OnchainPayment) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerOnchainPayment struct{}

func (_ FfiDestroyerOnchainPayment) Destroy(value *OnchainPayment) {
	value.Destroy()
}

// A `Refund` is a request to send an [`Bolt12Invoice`] without a preceding [`Offer`].
//
// Typically, after an invoice is paid, the recipient may publish a refund allowing the sender to
// recoup their funds. A refund may be used more generally as an "offer for money", such as with a
// bitcoin ATM.
//
// [`Bolt12Invoice`]: lightning::offers::invoice::Bolt12Invoice
// [`Offer`]: lightning::offers::offer::Offer
type RefundInterface interface {
	// Seconds since the Unix epoch when an invoice should no longer be sent.
	//
	// If `None`, the refund does not expire.
	AbsoluteExpirySeconds() *uint64
	// The amount to refund in msats (i.e., the minimum lightning-payable unit for [`chain`]).
	//
	// [`chain`]: Self::chain
	AmountMsats() uint64
	// A chain that the refund is valid for.
	Chain() *Network
	// Whether the refund has expired.
	IsExpired() bool
	// The issuer of the refund, possibly beginning with `user@domain` or `domain`.
	//
	// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
	Issuer() *string
	// An unpredictable series of bytes, typically containing information about the derivation of
	// [`payer_signing_pubkey`].
	//
	// [`payer_signing_pubkey`]: Self::payer_signing_pubkey
	PayerMetadata() []byte
	// Payer provided note to include in the invoice.
	PayerNote() *string
	// A public node id to send to in the case where there are no [`paths`].
	//
	// Otherwise, a possibly transient pubkey.
	//
	// [`paths`]: lightning::offers::refund::Refund::paths
	PayerSigningPubkey() PublicKey
	// The quantity of an item that refund is for.
	Quantity() *uint64
	// A complete description of the purpose of the refund.
	//
	// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
	RefundDescription() string
}

// A `Refund` is a request to send an [`Bolt12Invoice`] without a preceding [`Offer`].
//
// Typically, after an invoice is paid, the recipient may publish a refund allowing the sender to
// recoup their funds. A refund may be used more generally as an "offer for money", such as with a
// bitcoin ATM.
//
// [`Bolt12Invoice`]: lightning::offers::invoice::Bolt12Invoice
// [`Offer`]: lightning::offers::offer::Offer
type Refund struct {
	ffiObject FfiObject
}

func RefundFromStr(refundStr string) (*Refund, error) {
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) unsafe.Pointer {
		return C.uniffi_ldk_node_fn_constructor_refund_from_str(FfiConverterStringINSTANCE.Lower(refundStr), _uniffiStatus)
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue *Refund
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterRefundINSTANCE.Lift(_uniffiRV), nil
	}
}

// Seconds since the Unix epoch when an invoice should no longer be sent.
//
// If `None`, the refund does not expire.
func (_self *Refund) AbsoluteExpirySeconds() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_absolute_expiry_seconds(
				_pointer, _uniffiStatus),
		}
	}))
}

// The amount to refund in msats (i.e., the minimum lightning-payable unit for [`chain`]).
//
// [`chain`]: Self::chain
func (_self *Refund) AmountMsats() uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.uint64_t {
		return C.uniffi_ldk_node_fn_method_refund_amount_msats(
			_pointer, _uniffiStatus)
	}))
}

// A chain that the refund is valid for.
func (_self *Refund) Chain() *Network {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalNetworkINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_chain(
				_pointer, _uniffiStatus),
		}
	}))
}

// Whether the refund has expired.
func (_self *Refund) IsExpired() bool {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_refund_is_expired(
			_pointer, _uniffiStatus)
	}))
}

// The issuer of the refund, possibly beginning with `user@domain` or `domain`.
//
// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
func (_self *Refund) Issuer() *string {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_issuer(
				_pointer, _uniffiStatus),
		}
	}))
}

// An unpredictable series of bytes, typically containing information about the derivation of
// [`payer_signing_pubkey`].
//
// [`payer_signing_pubkey`]: Self::payer_signing_pubkey
func (_self *Refund) PayerMetadata() []byte {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBytesINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_payer_metadata(
				_pointer, _uniffiStatus),
		}
	}))
}

// Payer provided note to include in the invoice.
func (_self *Refund) PayerNote() *string {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_payer_note(
				_pointer, _uniffiStatus),
		}
	}))
}

// A public node id to send to in the case where there are no [`paths`].
//
// Otherwise, a possibly transient pubkey.
//
// [`paths`]: lightning::offers::refund::Refund::paths
func (_self *Refund) PayerSigningPubkey() PublicKey {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterTypePublicKeyINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_payer_signing_pubkey(
				_pointer, _uniffiStatus),
		}
	}))
}

// The quantity of an item that refund is for.
func (_self *Refund) Quantity() *uint64 {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalUint64INSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_quantity(
				_pointer, _uniffiStatus),
		}
	}))
}

// A complete description of the purpose of the refund.
//
// Intended to be displayed to the user but with the caveat that it has not been verified in any way.
func (_self *Refund) RefundDescription() string {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_refund_description(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Refund) DebugString() string {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_uniffi_trait_debug(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Refund) String() string {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterStringINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_refund_uniffi_trait_display(
				_pointer, _uniffiStatus),
		}
	}))
}

func (_self *Refund) Eq(other *Refund) bool {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_refund_uniffi_trait_eq_eq(
			_pointer, FfiConverterRefundINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (_self *Refund) Ne(other *Refund) bool {
	_pointer := _self.ffiObject.incrementPointer("*Refund")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterBoolINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) C.int8_t {
		return C.uniffi_ldk_node_fn_method_refund_uniffi_trait_eq_ne(
			_pointer, FfiConverterRefundINSTANCE.Lower(other), _uniffiStatus)
	}))
}

func (object *Refund) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterRefund struct{}

var FfiConverterRefundINSTANCE = FfiConverterRefund{}

func (c FfiConverterRefund) Lift(pointer unsafe.Pointer) *Refund {
	result := &Refund{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_refund(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_refund(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*Refund).Destroy)
	return result
}

func (c FfiConverterRefund) Read(reader io.Reader) *Refund {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterRefund) Lower(value *Refund) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*Refund")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterRefund) Write(writer io.Writer, value *Refund) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerRefund struct{}

func (_ FfiDestroyerRefund) Destroy(value *Refund) {
	value.Destroy()
}

// A payment handler allowing to send spontaneous ("keysend") payments.
//
// Should be retrieved by calling [`Node::spontaneous_payment`].
//
// [`Node::spontaneous_payment`]: crate::Node::spontaneous_payment
type SpontaneousPaymentInterface interface {
	// Send a spontaneous aka. "keysend", payment.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	Send(amountMsat uint64, nodeId PublicKey, routeParameters *RouteParametersConfig) (PaymentId, error)
	// Sends payment probes over all paths of a route that would be used to pay the given
	// amount to the given `node_id`.
	//
	// See [`Bolt11Payment::send_probes`] for more information.
	//
	// [`Bolt11Payment::send_probes`]: crate::payment::Bolt11Payment
	SendProbes(amountMsat uint64, nodeId PublicKey) error
	// Send a spontaneous payment including a list of custom TLVs.
	SendWithCustomTlvs(amountMsat uint64, nodeId PublicKey, routeParameters *RouteParametersConfig, customTlvs []CustomTlvRecord) (PaymentId, error)
	// Send a spontaneous payment with custom preimage
	SendWithPreimage(amountMsat uint64, nodeId PublicKey, preimage PaymentPreimage, routeParameters *RouteParametersConfig) (PaymentId, error)
	// Send a spontaneous payment with custom preimage including a list of custom TLVs.
	SendWithPreimageAndCustomTlvs(amountMsat uint64, nodeId PublicKey, customTlvs []CustomTlvRecord, preimage PaymentPreimage, routeParameters *RouteParametersConfig) (PaymentId, error)
}

// A payment handler allowing to send spontaneous ("keysend") payments.
//
// Should be retrieved by calling [`Node::spontaneous_payment`].
//
// [`Node::spontaneous_payment`]: crate::Node::spontaneous_payment
type SpontaneousPayment struct {
	ffiObject FfiObject
}

// Send a spontaneous aka. "keysend", payment.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
func (_self *SpontaneousPayment) Send(amountMsat uint64, nodeId PublicKey, routeParameters *RouteParametersConfig) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*SpontaneousPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_spontaneouspayment_send(
				_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}

// Sends payment probes over all paths of a route that would be used to pay the given
// amount to the given `node_id`.
//
// See [`Bolt11Payment::send_probes`] for more information.
//
// [`Bolt11Payment::send_probes`]: crate::payment::Bolt11Payment
func (_self *SpontaneousPayment) SendProbes(amountMsat uint64, nodeId PublicKey) error {
	_pointer := _self.ffiObject.incrementPointer("*SpontaneousPayment")
	defer _self.ffiObject.decrementPointer()
	_, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) bool {
		C.uniffi_ldk_node_fn_method_spontaneouspayment_send_probes(
			_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), _uniffiStatus)
		return false
	})
	return _uniffiErr.AsError()
}

// Send a spontaneous payment including a list of custom TLVs.
func (_self *SpontaneousPayment) SendWithCustomTlvs(amountMsat uint64, nodeId PublicKey, routeParameters *RouteParametersConfig, customTlvs []CustomTlvRecord) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*SpontaneousPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_spontaneouspayment_send_with_custom_tlvs(
				_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), FfiConverterSequenceCustomTlvRecordINSTANCE.Lower(customTlvs), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}

// Send a spontaneous payment with custom preimage
func (_self *SpontaneousPayment) SendWithPreimage(amountMsat uint64, nodeId PublicKey, preimage PaymentPreimage, routeParameters *RouteParametersConfig) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*SpontaneousPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_spontaneouspayment_send_with_preimage(
				_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterTypePaymentPreimageINSTANCE.Lower(preimage), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}

// Send a spontaneous payment with custom preimage including a list of custom TLVs.
func (_self *SpontaneousPayment) SendWithPreimageAndCustomTlvs(amountMsat uint64, nodeId PublicKey, customTlvs []CustomTlvRecord, preimage PaymentPreimage, routeParameters *RouteParametersConfig) (PaymentId, error) {
	_pointer := _self.ffiObject.incrementPointer("*SpontaneousPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_spontaneouspayment_send_with_preimage_and_custom_tlvs(
				_pointer, FfiConverterUint64INSTANCE.Lower(amountMsat), FfiConverterTypePublicKeyINSTANCE.Lower(nodeId), FfiConverterSequenceCustomTlvRecordINSTANCE.Lower(customTlvs), FfiConverterTypePaymentPreimageINSTANCE.Lower(preimage), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue PaymentId
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterTypePaymentIdINSTANCE.Lift(_uniffiRV), nil
	}
}
func (object *SpontaneousPayment) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterSpontaneousPayment struct{}

var FfiConverterSpontaneousPaymentINSTANCE = FfiConverterSpontaneousPayment{}

func (c FfiConverterSpontaneousPayment) Lift(pointer unsafe.Pointer) *SpontaneousPayment {
	result := &SpontaneousPayment{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_spontaneouspayment(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_spontaneouspayment(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*SpontaneousPayment).Destroy)
	return result
}

func (c FfiConverterSpontaneousPayment) Read(reader io.Reader) *SpontaneousPayment {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterSpontaneousPayment) Lower(value *SpontaneousPayment) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*SpontaneousPayment")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterSpontaneousPayment) Write(writer io.Writer, value *SpontaneousPayment) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerSpontaneousPayment struct{}

func (_ FfiDestroyerSpontaneousPayment) Destroy(value *SpontaneousPayment) {
	value.Destroy()
}

// A static invoice used for async payments.
//
// Static invoices are a special type of BOLT12 invoice where proof of payment is not possible,
// as the payment hash is not derived from a preimage known only to the recipient.
type StaticInvoiceInterface interface {
	// The amount for a successful payment of the invoice, if specified.
	Amount() *OfferAmount
}

// A static invoice used for async payments.
//
// Static invoices are a special type of BOLT12 invoice where proof of payment is not possible,
// as the payment hash is not derived from a preimage known only to the recipient.
type StaticInvoice struct {
	ffiObject FfiObject
}

// The amount for a successful payment of the invoice, if specified.
func (_self *StaticInvoice) Amount() *OfferAmount {
	_pointer := _self.ffiObject.incrementPointer("*StaticInvoice")
	defer _self.ffiObject.decrementPointer()
	return FfiConverterOptionalOfferAmountINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_staticinvoice_amount(
				_pointer, _uniffiStatus),
		}
	}))
}
func (object *StaticInvoice) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterStaticInvoice struct{}

var FfiConverterStaticInvoiceINSTANCE = FfiConverterStaticInvoice{}

func (c FfiConverterStaticInvoice) Lift(pointer unsafe.Pointer) *StaticInvoice {
	result := &StaticInvoice{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_staticinvoice(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_staticinvoice(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*StaticInvoice).Destroy)
	return result
}

func (c FfiConverterStaticInvoice) Read(reader io.Reader) *StaticInvoice {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterStaticInvoice) Lower(value *StaticInvoice) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*StaticInvoice")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterStaticInvoice) Write(writer io.Writer, value *StaticInvoice) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerStaticInvoice struct{}

func (_ FfiDestroyerStaticInvoice) Destroy(value *StaticInvoice) {
	value.Destroy()
}

// A payment handler that supports creating and paying to [BIP 21] URIs with on-chain, [BOLT 11],
// and [BOLT 12] payment options.
//
// Also supports sending payments to [BIP 353] Human-Readable Names.
//
// Should be retrieved by calling [`Node::unified_payment`]
//
// [BIP 21]: https://github.com/bitcoin/bips/blob/master/bip-0021.mediawiki
// [BIP 353]: https://github.com/bitcoin/bips/blob/master/bip-0353.mediawiki
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
// [`Node::unified_payment`]: crate::Node::unified_payment
type UnifiedPaymentInterface interface {
	// Generates a URI with an on-chain address, [BOLT 11] invoice and [BOLT 12] offer.
	//
	// The URI allows users to send the payment request allowing the wallet to decide
	// which payment method to use. This enables a fallback mechanism: older wallets
	// can always pay using the provided on-chain address, while newer wallets will
	// typically opt to use the provided BOLT11 invoice or BOLT12 offer.
	//
	// The URI will always include an on-chain address. A BOLT11 invoice will be included
	// unless invoice generation fails, while a BOLT12 offer will only be included when
	// the node has suitable channels for routing.
	//
	// # Parameters
	// - `amount_sats`: The amount to be received, specified in satoshis.
	// - `description`: A description or note associated with the payment.
	// This message is visible to the payer and can provide context or details about the payment.
	// - `expiry_sec`: The expiration time for the payment, specified in seconds.
	//
	// Returns a payable URI that can be used to request and receive a payment of the amount
	// given. Failure to generate the on-chain address will result in an error return
	// (`Error::WalletOperationFailed`), while failures in invoice or offer generation will
	// result in those components being omitted from the URI.
	//
	// The generated URI can then be given to a QR code library.
	//
	// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
	// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
	Receive(amountSats uint64, description string, expirySec uint32) (string, error)
	// Sends a payment given a [BIP 21] URI or [BIP 353] Human-Readable Name.
	//
	// This method parses the provided URI string and attempts to send the payment. If the URI
	// has an offer and or invoice, it will try to pay the offer first followed by the invoice.
	// If they both fail, the on-chain payment will be paid.
	//
	// Returns a [`UnifiedPaymentResult`] indicating the outcome of the payment. If an error
	// occurs, an `Error` is returned detailing the issue encountered.
	//
	// If `route_parameters` are provided they will override the default as well as the
	// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
	//
	// [BIP 21]: https://github.com/bitcoin/bips/blob/master/bip-0021.mediawiki
	// [BIP 353]: https://github.com/bitcoin/bips/blob/master/bip-0353.mediawiki
	Send(uriStr string, amountMsat *uint64, routeParameters *RouteParametersConfig) (UnifiedPaymentResult, error)
}

// A payment handler that supports creating and paying to [BIP 21] URIs with on-chain, [BOLT 11],
// and [BOLT 12] payment options.
//
// Also supports sending payments to [BIP 353] Human-Readable Names.
//
// Should be retrieved by calling [`Node::unified_payment`]
//
// [BIP 21]: https://github.com/bitcoin/bips/blob/master/bip-0021.mediawiki
// [BIP 353]: https://github.com/bitcoin/bips/blob/master/bip-0353.mediawiki
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
// [`Node::unified_payment`]: crate::Node::unified_payment
type UnifiedPayment struct {
	ffiObject FfiObject
}

// Generates a URI with an on-chain address, [BOLT 11] invoice and [BOLT 12] offer.
//
// The URI allows users to send the payment request allowing the wallet to decide
// which payment method to use. This enables a fallback mechanism: older wallets
// can always pay using the provided on-chain address, while newer wallets will
// typically opt to use the provided BOLT11 invoice or BOLT12 offer.
//
// The URI will always include an on-chain address. A BOLT11 invoice will be included
// unless invoice generation fails, while a BOLT12 offer will only be included when
// the node has suitable channels for routing.
//
// # Parameters
// - `amount_sats`: The amount to be received, specified in satoshis.
// - `description`: A description or note associated with the payment.
// This message is visible to the payer and can provide context or details about the payment.
// - `expiry_sec`: The expiration time for the payment, specified in seconds.
//
// Returns a payable URI that can be used to request and receive a payment of the amount
// given. Failure to generate the on-chain address will result in an error return
// (`Error::WalletOperationFailed`), while failures in invoice or offer generation will
// result in those components being omitted from the URI.
//
// The generated URI can then be given to a QR code library.
//
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
func (_self *UnifiedPayment) Receive(amountSats uint64, description string, expirySec uint32) (string, error) {
	_pointer := _self.ffiObject.incrementPointer("*UnifiedPayment")
	defer _self.ffiObject.decrementPointer()
	_uniffiRV, _uniffiErr := rustCallWithError[NodeError](FfiConverterNodeError{}, func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_method_unifiedpayment_receive(
				_pointer, FfiConverterUint64INSTANCE.Lower(amountSats), FfiConverterStringINSTANCE.Lower(description), FfiConverterUint32INSTANCE.Lower(expirySec), _uniffiStatus),
		}
	})
	if _uniffiErr != nil {
		var _uniffiDefaultValue string
		return _uniffiDefaultValue, _uniffiErr
	} else {
		return FfiConverterStringINSTANCE.Lift(_uniffiRV), nil
	}
}

// Sends a payment given a [BIP 21] URI or [BIP 353] Human-Readable Name.
//
// This method parses the provided URI string and attempts to send the payment. If the URI
// has an offer and or invoice, it will try to pay the offer first followed by the invoice.
// If they both fail, the on-chain payment will be paid.
//
// Returns a [`UnifiedPaymentResult`] indicating the outcome of the payment. If an error
// occurs, an `Error` is returned detailing the issue encountered.
//
// If `route_parameters` are provided they will override the default as well as the
// node-wide parameters configured via [`Config::route_parameters`] on a per-field basis.
//
// [BIP 21]: https://github.com/bitcoin/bips/blob/master/bip-0021.mediawiki
// [BIP 353]: https://github.com/bitcoin/bips/blob/master/bip-0353.mediawiki
func (_self *UnifiedPayment) Send(uriStr string, amountMsat *uint64, routeParameters *RouteParametersConfig) (UnifiedPaymentResult, error) {
	_pointer := _self.ffiObject.incrementPointer("*UnifiedPayment")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[NodeError](
		FfiConverterNodeErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_ldk_node_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) UnifiedPaymentResult {
			return FfiConverterUnifiedPaymentResultINSTANCE.Lift(ffi)
		},
		C.uniffi_ldk_node_fn_method_unifiedpayment_send(
			_pointer, FfiConverterStringINSTANCE.Lower(uriStr), FfiConverterOptionalUint64INSTANCE.Lower(amountMsat), FfiConverterOptionalRouteParametersConfigINSTANCE.Lower(routeParameters)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_ldk_node_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_ldk_node_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}
func (object *UnifiedPayment) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterUnifiedPayment struct{}

var FfiConverterUnifiedPaymentINSTANCE = FfiConverterUnifiedPayment{}

func (c FfiConverterUnifiedPayment) Lift(pointer unsafe.Pointer) *UnifiedPayment {
	result := &UnifiedPayment{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_unifiedpayment(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_unifiedpayment(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*UnifiedPayment).Destroy)
	return result
}

func (c FfiConverterUnifiedPayment) Read(reader io.Reader) *UnifiedPayment {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterUnifiedPayment) Lower(value *UnifiedPayment) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := value.ffiObject.incrementPointer("*UnifiedPayment")
	defer value.ffiObject.decrementPointer()
	return pointer

}

func (c FfiConverterUnifiedPayment) Write(writer io.Writer, value *UnifiedPayment) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerUnifiedPayment struct{}

func (_ FfiDestroyerUnifiedPayment) Destroy(value *UnifiedPayment) {
	value.Destroy()
}

type VssHeaderProvider interface {
	GetHeaders(request []uint8) (map[string]string, error)
}
type VssHeaderProviderImpl struct {
	ffiObject FfiObject
}

func (_self *VssHeaderProviderImpl) GetHeaders(request []uint8) (map[string]string, error) {
	_pointer := _self.ffiObject.incrementPointer("VssHeaderProvider")
	defer _self.ffiObject.decrementPointer()
	res, err := uniffiRustCallAsync[VssHeaderProviderError](
		FfiConverterVssHeaderProviderErrorINSTANCE,
		// completeFn
		func(handle C.uint64_t, status *C.RustCallStatus) RustBufferI {
			res := C.ffi_ldk_node_rust_future_complete_rust_buffer(handle, status)
			return GoRustBuffer{
				inner: res,
			}
		},
		// liftFn
		func(ffi RustBufferI) map[string]string {
			return FfiConverterMapStringStringINSTANCE.Lift(ffi)
		},
		C.uniffi_ldk_node_fn_method_vssheaderprovider_get_headers(
			_pointer, FfiConverterSequenceUint8INSTANCE.Lower(request)),
		// pollFn
		func(handle C.uint64_t, continuation C.UniffiRustFutureContinuationCallback, data C.uint64_t) {
			C.ffi_ldk_node_rust_future_poll_rust_buffer(handle, continuation, data)
		},
		// freeFn
		func(handle C.uint64_t) {
			C.ffi_ldk_node_rust_future_free_rust_buffer(handle)
		},
	)

	if err == nil {
		return res, nil
	}

	return res, err
}
func (object *VssHeaderProviderImpl) Destroy() {
	runtime.SetFinalizer(object, nil)
	object.ffiObject.destroy()
}

type FfiConverterVssHeaderProvider struct {
	handleMap *concurrentHandleMap[VssHeaderProvider]
}

var FfiConverterVssHeaderProviderINSTANCE = FfiConverterVssHeaderProvider{
	handleMap: newConcurrentHandleMap[VssHeaderProvider](),
}

func (c FfiConverterVssHeaderProvider) Lift(pointer unsafe.Pointer) VssHeaderProvider {
	result := &VssHeaderProviderImpl{
		newFfiObject(
			pointer,
			func(pointer unsafe.Pointer, status *C.RustCallStatus) unsafe.Pointer {
				return C.uniffi_ldk_node_fn_clone_vssheaderprovider(pointer, status)
			},
			func(pointer unsafe.Pointer, status *C.RustCallStatus) {
				C.uniffi_ldk_node_fn_free_vssheaderprovider(pointer, status)
			},
		),
	}
	runtime.SetFinalizer(result, (*VssHeaderProviderImpl).Destroy)
	return result
}

func (c FfiConverterVssHeaderProvider) Read(reader io.Reader) VssHeaderProvider {
	return c.Lift(unsafe.Pointer(uintptr(readUint64(reader))))
}

func (c FfiConverterVssHeaderProvider) Lower(value VssHeaderProvider) unsafe.Pointer {
	// TODO: this is bad - all synchronization from ObjectRuntime.go is discarded here,
	// because the pointer will be decremented immediately after this function returns,
	// and someone will be left holding onto a non-locked pointer.
	pointer := unsafe.Pointer(uintptr(c.handleMap.insert(value)))
	return pointer

}

func (c FfiConverterVssHeaderProvider) Write(writer io.Writer, value VssHeaderProvider) {
	writeUint64(writer, uint64(uintptr(c.Lower(value))))
}

type FfiDestroyerVssHeaderProvider struct{}

func (_ FfiDestroyerVssHeaderProvider) Destroy(value VssHeaderProvider) {
	if val, ok := value.(*VssHeaderProviderImpl); ok {
		val.Destroy()
	} else {
		panic("Expected *VssHeaderProviderImpl")
	}
}

//export ldk_node_cgo_dispatchCallbackInterfaceVssHeaderProviderMethod0
func ldk_node_cgo_dispatchCallbackInterfaceVssHeaderProviderMethod0(uniffiHandle C.uint64_t, request C.RustBuffer, uniffiFutureCallback C.UniffiForeignFutureCompleteRustBuffer, uniffiCallbackData C.uint64_t, uniffiOutReturn *C.UniffiForeignFuture) {
	handle := uint64(uniffiHandle)
	uniffiObj, ok := FfiConverterVssHeaderProviderINSTANCE.handleMap.tryGet(handle)
	if !ok {
		panic(fmt.Errorf("no callback in handle map: %d", handle))
	}

	result := make(chan C.UniffiForeignFutureStructRustBuffer, 1)
	cancel := make(chan struct{}, 1)
	guardHandle := cgo.NewHandle(cancel)
	*uniffiOutReturn = C.UniffiForeignFuture{
		handle: C.uint64_t(guardHandle),
		free:   C.UniffiForeignFutureFree(C.ldk_node_uniffiFreeGorutine),
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
			uniffiObj.GetHeaders(
				FfiConverterSequenceUint8INSTANCE.Lift(GoRustBuffer{
					inner: request,
				}),
			)

		if err != nil {
			var actualError *VssHeaderProviderError
			if errors.As(err, &actualError) {
				*callStatus = C.RustCallStatus{
					code:     C.int8_t(uniffiCallbackResultError),
					errorBuf: FfiConverterVssHeaderProviderErrorINSTANCE.Lower(actualError),
				}
			} else {
				*callStatus = C.RustCallStatus{
					code: C.int8_t(uniffiCallbackUnexpectedResultError),
				}
			}
			return
		}

		*uniffiOutReturn = FfiConverterMapStringStringINSTANCE.Lower(res)
	}()
}

var UniffiVTableCallbackInterfaceVssHeaderProviderINSTANCE = C.UniffiVTableCallbackInterfaceVssHeaderProvider{
	getHeaders: (C.UniffiCallbackInterfaceVssHeaderProviderMethod0)(C.ldk_node_cgo_dispatchCallbackInterfaceVssHeaderProviderMethod0),

	uniffiFree: (C.UniffiCallbackInterfaceFree)(C.ldk_node_cgo_dispatchCallbackInterfaceVssHeaderProviderFree),
}

//export ldk_node_cgo_dispatchCallbackInterfaceVssHeaderProviderFree
func ldk_node_cgo_dispatchCallbackInterfaceVssHeaderProviderFree(handle C.uint64_t) {
	FfiConverterVssHeaderProviderINSTANCE.handleMap.remove(uint64(handle))
}

func (c FfiConverterVssHeaderProvider) register() {
	C.uniffi_ldk_node_fn_init_callback_vtable_vssheaderprovider(&UniffiVTableCallbackInterfaceVssHeaderProviderINSTANCE)
}

// Configuration options pertaining to 'Anchor' channels, i.e., channels for which the
// `option_anchors_zero_fee_htlc_tx` channel type is negotiated.
//
// Prior to the introduction of Anchor channels, the on-chain fees paying for the transactions
// issued on channel closure were pre-determined and locked-in at the time of the channel
// opening. This required to estimate what fee rate would be sufficient to still have the
// closing transactions be spendable on-chain (i.e., not be considered dust). This legacy
// design of pre-anchor channels proved inadequate in the unpredictable, often turbulent, fee
// markets we experience today.
//
// In contrast, Anchor channels allow to determine an adequate fee rate *at the time of channel
// closure*, making them much more robust in the face of fee spikes. In turn, they require to
// maintain a reserve of on-chain funds to have the channel closure transactions confirmed
// on-chain, at least if the channel counterparty can't be trusted to do this for us.
//
// See [BOLT 3] for more technical details on Anchor channels.
//
// ### Defaults
//
// | Parameter                  | Value  |
// |----------------------------|--------|
// | `trusted_peers_no_reserve` | []     |
// | `per_channel_reserve_sats` | 25000  |
//
// [BOLT 3]: https://github.com/lightning/bolts/blob/master/03-transactions.md#htlc-timeout-and-htlc-success-transactions
type AnchorChannelsConfig struct {
	// A list of peers that we trust to get the required channel closing transactions confirmed
	// on-chain.
	//
	// Channels with these peers won't count towards the retained on-chain reserve and we won't
	// take any action to get the required channel closing transactions confirmed ourselves.
	//
	// **Note:** Trusting the channel counterparty to take the necessary actions to get the
	// required Anchor spending transactions confirmed on-chain is potentially insecure
	// as the channel may not be closed if they refuse to do so.
	TrustedPeersNoReserve []PublicKey
	// The amount of satoshis per anchors-negotiated channel with an untrusted peer that we keep
	// as an emergency reserve in our on-chain wallet.
	//
	// This allows for having the required Anchor output spending and HTLC transactions confirmed
	// when the channel is closed.
	//
	// If the channel peer is not marked as trusted via
	// [`AnchorChannelsConfig::trusted_peers_no_reserve`], we will always try to spend the Anchor
	// outputs with *any* on-chain funds available, i.e., the total reserve value as well as any
	// spendable funds available in the on-chain wallet. Therefore, this per-channel multiplier is
	// really an emergency reserve that we maintain at all time to reduce the risk of
	// insufficient funds at time of a channel closure. To this end, we will refuse to open
	// outbound or accept inbound channels if we don't have sufficient on-chain funds available to
	// cover the additional reserve requirement.
	//
	// **Note:** Depending on the fee market at the time of closure, this reserve amount might or
	// might not suffice to successfully spend the Anchor output and have the HTLC transactions
	// confirmed on-chain, i.e., you may want to adjust this value accordingly.
	PerChannelReserveSats uint64
}

func (r *AnchorChannelsConfig) Destroy() {
	FfiDestroyerSequenceTypePublicKey{}.Destroy(r.TrustedPeersNoReserve)
	FfiDestroyerUint64{}.Destroy(r.PerChannelReserveSats)
}

type FfiConverterAnchorChannelsConfig struct{}

var FfiConverterAnchorChannelsConfigINSTANCE = FfiConverterAnchorChannelsConfig{}

func (c FfiConverterAnchorChannelsConfig) Lift(rb RustBufferI) AnchorChannelsConfig {
	return LiftFromRustBuffer[AnchorChannelsConfig](c, rb)
}

func (c FfiConverterAnchorChannelsConfig) Read(reader io.Reader) AnchorChannelsConfig {
	return AnchorChannelsConfig{
		FfiConverterSequenceTypePublicKeyINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterAnchorChannelsConfig) Lower(value AnchorChannelsConfig) C.RustBuffer {
	return LowerIntoRustBuffer[AnchorChannelsConfig](c, value)
}

func (c FfiConverterAnchorChannelsConfig) LowerExternal(value AnchorChannelsConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[AnchorChannelsConfig](c, value))
}

func (c FfiConverterAnchorChannelsConfig) Write(writer io.Writer, value AnchorChannelsConfig) {
	FfiConverterSequenceTypePublicKeyINSTANCE.Write(writer, value.TrustedPeersNoReserve)
	FfiConverterUint64INSTANCE.Write(writer, value.PerChannelReserveSats)
}

type FfiDestroyerAnchorChannelsConfig struct{}

func (_ FfiDestroyerAnchorChannelsConfig) Destroy(value AnchorChannelsConfig) {
	value.Destroy()
}

// Options related to background syncing the Lightning and on-chain wallets.
//
// ### Defaults
//
// | Parameter                              | Value              |
// |----------------------------------------|--------------------|
// | `onchain_wallet_sync_interval_secs`    | 80                 |
// | `lightning_wallet_sync_interval_secs`  | 30                 |
// | `fee_rate_cache_update_interval_secs`  | 600                |
type BackgroundSyncConfig struct {
	// The time in-between background sync attempts of the onchain wallet, in seconds.
	//
	// **Note:** A minimum of 10 seconds is enforced when background syncing is enabled.
	OnchainWalletSyncIntervalSecs uint64
	// The time in-between background sync attempts of the LDK wallet, in seconds.
	//
	// **Note:** A minimum of 10 seconds is enforced when background syncing is enabled.
	LightningWalletSyncIntervalSecs uint64
	// The time in-between background update attempts to our fee rate cache, in seconds.
	//
	// **Note:** A minimum of 10 seconds is enforced when background syncing is enabled.
	FeeRateCacheUpdateIntervalSecs uint64
}

func (r *BackgroundSyncConfig) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.OnchainWalletSyncIntervalSecs)
	FfiDestroyerUint64{}.Destroy(r.LightningWalletSyncIntervalSecs)
	FfiDestroyerUint64{}.Destroy(r.FeeRateCacheUpdateIntervalSecs)
}

type FfiConverterBackgroundSyncConfig struct{}

var FfiConverterBackgroundSyncConfigINSTANCE = FfiConverterBackgroundSyncConfig{}

func (c FfiConverterBackgroundSyncConfig) Lift(rb RustBufferI) BackgroundSyncConfig {
	return LiftFromRustBuffer[BackgroundSyncConfig](c, rb)
}

func (c FfiConverterBackgroundSyncConfig) Read(reader io.Reader) BackgroundSyncConfig {
	return BackgroundSyncConfig{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterBackgroundSyncConfig) Lower(value BackgroundSyncConfig) C.RustBuffer {
	return LowerIntoRustBuffer[BackgroundSyncConfig](c, value)
}

func (c FfiConverterBackgroundSyncConfig) LowerExternal(value BackgroundSyncConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[BackgroundSyncConfig](c, value))
}

func (c FfiConverterBackgroundSyncConfig) Write(writer io.Writer, value BackgroundSyncConfig) {
	FfiConverterUint64INSTANCE.Write(writer, value.OnchainWalletSyncIntervalSecs)
	FfiConverterUint64INSTANCE.Write(writer, value.LightningWalletSyncIntervalSecs)
	FfiConverterUint64INSTANCE.Write(writer, value.FeeRateCacheUpdateIntervalSecs)
}

type FfiDestroyerBackgroundSyncConfig struct{}

func (_ FfiDestroyerBackgroundSyncConfig) Destroy(value BackgroundSyncConfig) {
	value.Destroy()
}

// Details of the known available balances returned by [`Node::list_balances`].
//
// [`Node::list_balances`]: crate::Node::list_balances
type BalanceDetails struct {
	// The total balance of our on-chain wallet.
	TotalOnchainBalanceSats uint64
	// The currently spendable balance of our on-chain wallet.
	//
	// This includes any sufficiently confirmed funds, minus
	// [`total_anchor_channels_reserve_sats`].
	//
	// [`total_anchor_channels_reserve_sats`]: Self::total_anchor_channels_reserve_sats
	SpendableOnchainBalanceSats uint64
	// The share of our total balance that we retain as an emergency reserve to (hopefully) be
	// able to spend the Anchor outputs when one of our channels is closed.
	TotalAnchorChannelsReserveSats uint64
	// The total balance that we would be able to claim across all our Lightning channels.
	//
	// Note this excludes balances that we are unsure if we are able to claim (e.g., as we are
	// waiting for a preimage or for a timeout to expire). These balances will however be included
	// as [`MaybePreimageClaimableHTLC`] and
	// [`MaybeTimeoutClaimableHTLC`] in [`lightning_balances`].
	//
	// [`MaybePreimageClaimableHTLC`]: LightningBalance::MaybePreimageClaimableHTLC
	// [`MaybeTimeoutClaimableHTLC`]: LightningBalance::MaybeTimeoutClaimableHTLC
	// [`lightning_balances`]: Self::lightning_balances
	TotalLightningBalanceSats uint64
	// A detailed list of all known Lightning balances that would be claimable on channel closure.
	//
	// Note that less than the listed amounts are spendable over lightning as further reserve
	// restrictions apply. Please refer to [`ChannelDetails::outbound_capacity_msat`] and
	// [`ChannelDetails::next_outbound_htlc_limit_msat`] as returned by [`Node::list_channels`]
	// for a better approximation of the spendable amounts.
	//
	// [`ChannelDetails::outbound_capacity_msat`]: crate::ChannelDetails::outbound_capacity_msat
	// [`ChannelDetails::next_outbound_htlc_limit_msat`]: crate::ChannelDetails::next_outbound_htlc_limit_msat
	// [`Node::list_channels`]: crate::Node::list_channels
	LightningBalances []LightningBalance
	// A detailed list of balances currently being swept from the Lightning to the on-chain
	// wallet.
	//
	// These are balances resulting from channel closures that may have been encumbered by a
	// delay, but are now being claimed and useable once sufficiently confirmed on-chain.
	//
	// Note that, depending on the sync status of the wallets, swept balances listed here might or
	// might not already be accounted for in [`total_onchain_balance_sats`].
	//
	// [`total_onchain_balance_sats`]: Self::total_onchain_balance_sats
	PendingBalancesFromChannelClosures []PendingSweepBalance
}

func (r *BalanceDetails) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.TotalOnchainBalanceSats)
	FfiDestroyerUint64{}.Destroy(r.SpendableOnchainBalanceSats)
	FfiDestroyerUint64{}.Destroy(r.TotalAnchorChannelsReserveSats)
	FfiDestroyerUint64{}.Destroy(r.TotalLightningBalanceSats)
	FfiDestroyerSequenceLightningBalance{}.Destroy(r.LightningBalances)
	FfiDestroyerSequencePendingSweepBalance{}.Destroy(r.PendingBalancesFromChannelClosures)
}

type FfiConverterBalanceDetails struct{}

var FfiConverterBalanceDetailsINSTANCE = FfiConverterBalanceDetails{}

func (c FfiConverterBalanceDetails) Lift(rb RustBufferI) BalanceDetails {
	return LiftFromRustBuffer[BalanceDetails](c, rb)
}

func (c FfiConverterBalanceDetails) Read(reader io.Reader) BalanceDetails {
	return BalanceDetails{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterSequenceLightningBalanceINSTANCE.Read(reader),
		FfiConverterSequencePendingSweepBalanceINSTANCE.Read(reader),
	}
}

func (c FfiConverterBalanceDetails) Lower(value BalanceDetails) C.RustBuffer {
	return LowerIntoRustBuffer[BalanceDetails](c, value)
}

func (c FfiConverterBalanceDetails) LowerExternal(value BalanceDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[BalanceDetails](c, value))
}

func (c FfiConverterBalanceDetails) Write(writer io.Writer, value BalanceDetails) {
	FfiConverterUint64INSTANCE.Write(writer, value.TotalOnchainBalanceSats)
	FfiConverterUint64INSTANCE.Write(writer, value.SpendableOnchainBalanceSats)
	FfiConverterUint64INSTANCE.Write(writer, value.TotalAnchorChannelsReserveSats)
	FfiConverterUint64INSTANCE.Write(writer, value.TotalLightningBalanceSats)
	FfiConverterSequenceLightningBalanceINSTANCE.Write(writer, value.LightningBalances)
	FfiConverterSequencePendingSweepBalanceINSTANCE.Write(writer, value.PendingBalancesFromChannelClosures)
}

type FfiDestroyerBalanceDetails struct{}

func (_ FfiDestroyerBalanceDetails) Destroy(value BalanceDetails) {
	value.Destroy()
}

type BestBlock struct {
	BlockHash BlockHash
	Height    uint32
}

func (r *BestBlock) Destroy() {
	FfiDestroyerTypeBlockHash{}.Destroy(r.BlockHash)
	FfiDestroyerUint32{}.Destroy(r.Height)
}

type FfiConverterBestBlock struct{}

var FfiConverterBestBlockINSTANCE = FfiConverterBestBlock{}

func (c FfiConverterBestBlock) Lift(rb RustBufferI) BestBlock {
	return LiftFromRustBuffer[BestBlock](c, rb)
}

func (c FfiConverterBestBlock) Read(reader io.Reader) BestBlock {
	return BestBlock{
		FfiConverterTypeBlockHashINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
	}
}

func (c FfiConverterBestBlock) Lower(value BestBlock) C.RustBuffer {
	return LowerIntoRustBuffer[BestBlock](c, value)
}

func (c FfiConverterBestBlock) LowerExternal(value BestBlock) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[BestBlock](c, value))
}

func (c FfiConverterBestBlock) Write(writer io.Writer, value BestBlock) {
	FfiConverterTypeBlockHashINSTANCE.Write(writer, value.BlockHash)
	FfiConverterUint32INSTANCE.Write(writer, value.Height)
}

type FfiDestroyerBestBlock struct{}

func (_ FfiDestroyerBestBlock) Destroy(value BestBlock) {
	value.Destroy()
}

// Options which apply on a per-channel basis and may change at runtime or based on negotiation
// with our counterparty.
type ChannelConfig struct {
	// Amount (in millionths of a satoshi) charged per satoshi for payments forwarded outbound
	// over the channel.
	// This may be allowed to change at runtime in a later update, however doing so must result in
	// update messages sent to notify all nodes of our updated relay fee.
	//
	// Please refer to [`LdkChannelConfig`] for further details.
	ForwardingFeeProportionalMillionths uint32
	// Amount (in milli-satoshi) charged for payments forwarded outbound over the channel, in
	// excess of [`ChannelConfig::forwarding_fee_proportional_millionths`].
	// This may be allowed to change at runtime in a later update, however doing so must result in
	// update messages sent to notify all nodes of our updated relay fee.
	//
	// Please refer to [`LdkChannelConfig`] for further details.
	ForwardingFeeBaseMsat uint32
	// The difference in the CLTV value between incoming HTLCs and an outbound HTLC forwarded over
	// the channel this config applies to.
	//
	// Please refer to [`LdkChannelConfig`] for further details.
	CltvExpiryDelta uint16
	// Limit our total exposure to potential loss to on-chain fees on close, including in-flight
	// HTLCs which are burned to fees as they are too small to claim on-chain and fees on
	// commitment transaction(s) broadcasted by our counterparty in excess of our own fee estimate.
	//
	// Please refer to [`LdkChannelConfig`] for further details.
	MaxDustHtlcExposure MaxDustHtlcExposure
	// The additional fee we're willing to pay to avoid waiting for the counterparty's
	// `to_self_delay` to reclaim funds.
	//
	// Please refer to [`LdkChannelConfig`] for further details.
	ForceCloseAvoidanceMaxFeeSatoshis uint64
	// If set, allows this channel's counterparty to skim an additional fee off this node's inbound
	// HTLCs. Useful for liquidity providers to offload on-chain channel costs to end users.
	//
	// Please refer to [`LdkChannelConfig`] for further details.
	AcceptUnderpayingHtlcs bool
}

func (r *ChannelConfig) Destroy() {
	FfiDestroyerUint32{}.Destroy(r.ForwardingFeeProportionalMillionths)
	FfiDestroyerUint32{}.Destroy(r.ForwardingFeeBaseMsat)
	FfiDestroyerUint16{}.Destroy(r.CltvExpiryDelta)
	FfiDestroyerMaxDustHtlcExposure{}.Destroy(r.MaxDustHtlcExposure)
	FfiDestroyerUint64{}.Destroy(r.ForceCloseAvoidanceMaxFeeSatoshis)
	FfiDestroyerBool{}.Destroy(r.AcceptUnderpayingHtlcs)
}

type FfiConverterChannelConfig struct{}

var FfiConverterChannelConfigINSTANCE = FfiConverterChannelConfig{}

func (c FfiConverterChannelConfig) Lift(rb RustBufferI) ChannelConfig {
	return LiftFromRustBuffer[ChannelConfig](c, rb)
}

func (c FfiConverterChannelConfig) Read(reader io.Reader) ChannelConfig {
	return ChannelConfig{
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint16INSTANCE.Read(reader),
		FfiConverterMaxDustHtlcExposureINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterChannelConfig) Lower(value ChannelConfig) C.RustBuffer {
	return LowerIntoRustBuffer[ChannelConfig](c, value)
}

func (c FfiConverterChannelConfig) LowerExternal(value ChannelConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[ChannelConfig](c, value))
}

func (c FfiConverterChannelConfig) Write(writer io.Writer, value ChannelConfig) {
	FfiConverterUint32INSTANCE.Write(writer, value.ForwardingFeeProportionalMillionths)
	FfiConverterUint32INSTANCE.Write(writer, value.ForwardingFeeBaseMsat)
	FfiConverterUint16INSTANCE.Write(writer, value.CltvExpiryDelta)
	FfiConverterMaxDustHtlcExposureINSTANCE.Write(writer, value.MaxDustHtlcExposure)
	FfiConverterUint64INSTANCE.Write(writer, value.ForceCloseAvoidanceMaxFeeSatoshis)
	FfiConverterBoolINSTANCE.Write(writer, value.AcceptUnderpayingHtlcs)
}

type FfiDestroyerChannelConfig struct{}

func (_ FfiDestroyerChannelConfig) Destroy(value ChannelConfig) {
	value.Destroy()
}

// Details of a channel as returned by [`Node::list_channels`].
//
// When a channel is spliced, most fields continue to refer to the original pre-splice channel
// state until the splice transaction reaches sufficient confirmations to be locked (and we
// exchange `splice_locked` messages with our peer). See individual fields for details.
//
// [`Node::list_channels`]: crate::Node::list_channels
type ChannelDetails struct {
	// The channel's ID (prior to initial channel setup this is a random 32 bytes, thereafter it
	// is derived from channel funding or key material).
	//
	// Note that this means this value is *not* persistent - it can change once during the
	// lifetime of the channel.
	ChannelId ChannelId
	// The node ID of our the channel's counterparty.
	CounterpartyNodeId PublicKey
	// The channel's funding transaction output, if we've negotiated the funding transaction with
	// our counterparty already.
	//
	// When a channel is spliced, this continues to refer to the original pre-splice channel
	// state until the splice transaction reaches sufficient confirmations to be locked (and we
	// exchange `splice_locked` messages with our peer).
	FundingTxo *OutPoint
	// The witness script that is used to lock the channel's funding output to commitment transactions.
	//
	// This field will be `None` if we have not negotiated the funding transaction with our
	// counterparty already.
	//
	// When a channel is spliced, this continues to refer to the original pre-splice channel
	// state until the splice transaction reaches sufficient confirmations to be locked (and we
	// exchange `splice_locked` messages with our peer).
	FundingRedeemScript *ScriptBuf
	// The position of the funding transaction in the chain. None if the funding transaction has
	// not yet been confirmed and the channel fully opened.
	//
	// Note that if [`inbound_scid_alias`] is set, it will be used for invoices and inbound
	// payments instead of this.
	//
	// For channels with [`confirmations_required`] set to `Some(0)`, [`outbound_scid_alias`] may
	// be used in place of this in outbound routes.
	//
	// When a channel is spliced, this continues to refer to the original pre-splice channel state
	// until the splice transaction reaches sufficient confirmations to be locked (and we exchange
	// `splice_locked` messages with our peer).
	//
	// [`inbound_scid_alias`]: Self::inbound_scid_alias
	// [`outbound_scid_alias`]: Self::outbound_scid_alias
	// [`confirmations_required`]: Self::confirmations_required
	ShortChannelId *uint64
	// An optional [`short_channel_id`] alias for this channel, randomly generated by us and
	// usable in place of [`short_channel_id`] to reference the channel in outbound routes when
	// the channel has not yet been confirmed (as long as [`confirmations_required`] is
	// `Some(0)`).
	//
	// This will be `None` as long as the channel is not available for routing outbound payments.
	//
	// When a channel is spliced, this continues to refer to the original pre-splice channel
	// state until the splice transaction reaches sufficient confirmations to be locked (and we
	// exchange `splice_locked` messages with our peer).
	//
	// [`short_channel_id`]: Self::short_channel_id
	// [`confirmations_required`]: Self::confirmations_required
	OutboundScidAlias *uint64
	// An optional [`short_channel_id`] alias for this channel, randomly generated by our
	// counterparty and usable in place of [`short_channel_id`] in invoice route hints. Our
	// counterparty will recognize the alias provided here in place of the [`short_channel_id`]
	// when they see a payment to be routed to us.
	//
	// Our counterparty may choose to rotate this value at any time, though will always recognize
	// previous values for inbound payment forwarding.
	//
	// [`short_channel_id`]: Self::short_channel_id
	InboundScidAlias *uint64
	// The value, in satoshis, of this channel as it appears in the funding output.
	//
	// When a channel is spliced, this continues to refer to the original pre-splice channel
	// state until the splice transaction reaches sufficient confirmations to be locked (and we
	// exchange `splice_locked` messages with our peer).
	ChannelValueSats uint64
	// The value, in satoshis, that must always be held as a reserve in the channel for us. This
	// value ensures that if we broadcast a revoked state, our counterparty can punish us by
	// claiming at least this value on chain.
	//
	// This value is not included in [`outbound_capacity_msat`] as it can never be spent.
	//
	// This value will be `None` for outbound channels until the counterparty accepts the channel.
	//
	// [`outbound_capacity_msat`]: Self::outbound_capacity_msat
	UnspendablePunishmentReserve *uint64
	// The local `user_channel_id` of this channel.
	UserChannelId UserChannelId
	// The currently negotiated fee rate denominated in satoshi per 1000 weight units,
	// which is applied to commitment and HTLC transactions.
	FeerateSatPer1000Weight uint32
	// The available outbound capacity for sending HTLCs to the remote peer.
	//
	// The amount does not include any pending HTLCs which are not yet resolved (and, thus, whose
	// balance is not available for inclusion in new outbound HTLCs). This further does not include
	// any pending outgoing HTLCs which are awaiting some other resolution to be sent.
	OutboundCapacityMsat uint64
	// The available inbound capacity for receiving HTLCs from the remote peer.
	//
	// The amount does not include any pending HTLCs which are not yet resolved
	// (and, thus, whose balance is not available for inclusion in new inbound HTLCs). This further
	// does not include any pending incoming HTLCs which are awaiting some other resolution to be
	// sent.
	InboundCapacityMsat uint64
	// The number of required confirmations on the funding transactions before the funding is
	// considered "locked". The amount is selected by the channel fundee.
	//
	// The value will be `None` for outbound channels until the counterparty accepts the channel.
	ConfirmationsRequired *uint32
	// The current number of confirmations on the funding transaction.
	Confirmations *uint32
	// Returns `true` if the channel was initiated (and therefore funded) by us.
	IsOutbound bool
	// Returns `true` if both parties have exchanged `channel_ready` messages, and the channel is
	// not currently being shut down. Both parties exchange `channel_ready` messages upon
	// independently verifying that the required confirmations count provided by
	// `confirmations_required` has been reached.
	IsChannelReady bool
	// Returns `true` if the channel (a) `channel_ready` messages have been exchanged, (b) the
	// peer is connected, and (c) the channel is not currently negotiating shutdown.
	//
	// This is a strict superset of `is_channel_ready`.
	IsUsable bool
	// Returns `true` if this channel is (or will be) publicly-announced
	IsAnnounced bool
	// The difference in the CLTV value between incoming HTLCs and an outbound HTLC forwarded over
	// the channel.
	CltvExpiryDelta *uint16
	// The value, in satoshis, that must always be held in the channel for our counterparty. This
	// value ensures that if our counterparty broadcasts a revoked state, we can punish them by
	// claiming at least this value on chain.
	//
	// This value is not included in [`inbound_capacity_msat`] as it can never be spent.
	//
	// [`inbound_capacity_msat`]: ChannelDetails::inbound_capacity_msat
	CounterpartyUnspendablePunishmentReserve uint64
	// The smallest value HTLC (in msat) the remote peer will accept, for this channel.
	//
	// This field is only `None` before we have received either the `OpenChannel` or
	// `AcceptChannel` message from the remote peer.
	CounterpartyOutboundHtlcMinimumMsat *uint64
	// The largest value HTLC (in msat) the remote peer currently will accept, for this channel.
	CounterpartyOutboundHtlcMaximumMsat *uint64
	// Base routing fee in millisatoshis.
	CounterpartyForwardingInfoFeeBaseMsat *uint32
	// Proportional fee, in millionths of a satoshi the channel will charge per transferred satoshi.
	CounterpartyForwardingInfoFeeProportionalMillionths *uint32
	// The minimum difference in CLTV expiry between an ingoing HTLC and its outgoing counterpart,
	// such that the outgoing HTLC is forwardable to this counterparty.
	CounterpartyForwardingInfoCltvExpiryDelta *uint16
	// The available outbound capacity for sending a single HTLC to the remote peer. This is
	// similar to [`ChannelDetails::outbound_capacity_msat`] but it may be further restricted by
	// the current state and per-HTLC limit(s). This is intended for use when routing, allowing us
	// to use a limit as close as possible to the HTLC limit we can currently send.
	//
	// See also [`ChannelDetails::next_outbound_htlc_minimum_msat`] and
	// [`ChannelDetails::outbound_capacity_msat`].
	NextOutboundHtlcLimitMsat uint64
	// The minimum value for sending a single HTLC to the remote peer. This is the equivalent of
	// [`ChannelDetails::next_outbound_htlc_limit_msat`] but represents a lower-bound, rather than
	// an upper-bound. This is intended for use when routing, allowing us to ensure we pick a
	// route which is valid.
	NextOutboundHtlcMinimumMsat uint64
	// The number of blocks (after our commitment transaction confirms) that we will need to wait
	// until we can claim our funds after we force-close the channel. During this time our
	// counterparty is allowed to punish us if we broadcasted a stale state. If our counterparty
	// force-closes the channel and broadcasts a commitment transaction we do not have to wait any
	// time to claim our non-HTLC-encumbered funds.
	//
	// This value will be `None` for outbound channels until the counterparty accepts the channel.
	ForceCloseSpendDelay *uint16
	// The smallest value HTLC (in msat) we will accept, for this channel.
	InboundHtlcMinimumMsat uint64
	// The largest value HTLC (in msat) we currently will accept, for this channel.
	InboundHtlcMaximumMsat *uint64
	// Set of configurable parameters that affect channel operation.
	Config ChannelConfig
}

func (r *ChannelDetails) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(r.ChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(r.CounterpartyNodeId)
	FfiDestroyerOptionalOutPoint{}.Destroy(r.FundingTxo)
	FfiDestroyerOptionalTypeScriptBuf{}.Destroy(r.FundingRedeemScript)
	FfiDestroyerOptionalUint64{}.Destroy(r.ShortChannelId)
	FfiDestroyerOptionalUint64{}.Destroy(r.OutboundScidAlias)
	FfiDestroyerOptionalUint64{}.Destroy(r.InboundScidAlias)
	FfiDestroyerUint64{}.Destroy(r.ChannelValueSats)
	FfiDestroyerOptionalUint64{}.Destroy(r.UnspendablePunishmentReserve)
	FfiDestroyerTypeUserChannelId{}.Destroy(r.UserChannelId)
	FfiDestroyerUint32{}.Destroy(r.FeerateSatPer1000Weight)
	FfiDestroyerUint64{}.Destroy(r.OutboundCapacityMsat)
	FfiDestroyerUint64{}.Destroy(r.InboundCapacityMsat)
	FfiDestroyerOptionalUint32{}.Destroy(r.ConfirmationsRequired)
	FfiDestroyerOptionalUint32{}.Destroy(r.Confirmations)
	FfiDestroyerBool{}.Destroy(r.IsOutbound)
	FfiDestroyerBool{}.Destroy(r.IsChannelReady)
	FfiDestroyerBool{}.Destroy(r.IsUsable)
	FfiDestroyerBool{}.Destroy(r.IsAnnounced)
	FfiDestroyerOptionalUint16{}.Destroy(r.CltvExpiryDelta)
	FfiDestroyerUint64{}.Destroy(r.CounterpartyUnspendablePunishmentReserve)
	FfiDestroyerOptionalUint64{}.Destroy(r.CounterpartyOutboundHtlcMinimumMsat)
	FfiDestroyerOptionalUint64{}.Destroy(r.CounterpartyOutboundHtlcMaximumMsat)
	FfiDestroyerOptionalUint32{}.Destroy(r.CounterpartyForwardingInfoFeeBaseMsat)
	FfiDestroyerOptionalUint32{}.Destroy(r.CounterpartyForwardingInfoFeeProportionalMillionths)
	FfiDestroyerOptionalUint16{}.Destroy(r.CounterpartyForwardingInfoCltvExpiryDelta)
	FfiDestroyerUint64{}.Destroy(r.NextOutboundHtlcLimitMsat)
	FfiDestroyerUint64{}.Destroy(r.NextOutboundHtlcMinimumMsat)
	FfiDestroyerOptionalUint16{}.Destroy(r.ForceCloseSpendDelay)
	FfiDestroyerUint64{}.Destroy(r.InboundHtlcMinimumMsat)
	FfiDestroyerOptionalUint64{}.Destroy(r.InboundHtlcMaximumMsat)
	FfiDestroyerChannelConfig{}.Destroy(r.Config)
}

type FfiConverterChannelDetails struct{}

var FfiConverterChannelDetailsINSTANCE = FfiConverterChannelDetails{}

func (c FfiConverterChannelDetails) Lift(rb RustBufferI) ChannelDetails {
	return LiftFromRustBuffer[ChannelDetails](c, rb)
}

func (c FfiConverterChannelDetails) Read(reader io.Reader) ChannelDetails {
	return ChannelDetails{
		FfiConverterTypeChannelIdINSTANCE.Read(reader),
		FfiConverterTypePublicKeyINSTANCE.Read(reader),
		FfiConverterOptionalOutPointINSTANCE.Read(reader),
		FfiConverterOptionalTypeScriptBufINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterTypeUserChannelIdINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint32INSTANCE.Read(reader),
		FfiConverterOptionalUint32INSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterOptionalUint16INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint32INSTANCE.Read(reader),
		FfiConverterOptionalUint32INSTANCE.Read(reader),
		FfiConverterOptionalUint16INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint16INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterChannelConfigINSTANCE.Read(reader),
	}
}

func (c FfiConverterChannelDetails) Lower(value ChannelDetails) C.RustBuffer {
	return LowerIntoRustBuffer[ChannelDetails](c, value)
}

func (c FfiConverterChannelDetails) LowerExternal(value ChannelDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[ChannelDetails](c, value))
}

func (c FfiConverterChannelDetails) Write(writer io.Writer, value ChannelDetails) {
	FfiConverterTypeChannelIdINSTANCE.Write(writer, value.ChannelId)
	FfiConverterTypePublicKeyINSTANCE.Write(writer, value.CounterpartyNodeId)
	FfiConverterOptionalOutPointINSTANCE.Write(writer, value.FundingTxo)
	FfiConverterOptionalTypeScriptBufINSTANCE.Write(writer, value.FundingRedeemScript)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.ShortChannelId)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.OutboundScidAlias)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.InboundScidAlias)
	FfiConverterUint64INSTANCE.Write(writer, value.ChannelValueSats)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.UnspendablePunishmentReserve)
	FfiConverterTypeUserChannelIdINSTANCE.Write(writer, value.UserChannelId)
	FfiConverterUint32INSTANCE.Write(writer, value.FeerateSatPer1000Weight)
	FfiConverterUint64INSTANCE.Write(writer, value.OutboundCapacityMsat)
	FfiConverterUint64INSTANCE.Write(writer, value.InboundCapacityMsat)
	FfiConverterOptionalUint32INSTANCE.Write(writer, value.ConfirmationsRequired)
	FfiConverterOptionalUint32INSTANCE.Write(writer, value.Confirmations)
	FfiConverterBoolINSTANCE.Write(writer, value.IsOutbound)
	FfiConverterBoolINSTANCE.Write(writer, value.IsChannelReady)
	FfiConverterBoolINSTANCE.Write(writer, value.IsUsable)
	FfiConverterBoolINSTANCE.Write(writer, value.IsAnnounced)
	FfiConverterOptionalUint16INSTANCE.Write(writer, value.CltvExpiryDelta)
	FfiConverterUint64INSTANCE.Write(writer, value.CounterpartyUnspendablePunishmentReserve)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.CounterpartyOutboundHtlcMinimumMsat)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.CounterpartyOutboundHtlcMaximumMsat)
	FfiConverterOptionalUint32INSTANCE.Write(writer, value.CounterpartyForwardingInfoFeeBaseMsat)
	FfiConverterOptionalUint32INSTANCE.Write(writer, value.CounterpartyForwardingInfoFeeProportionalMillionths)
	FfiConverterOptionalUint16INSTANCE.Write(writer, value.CounterpartyForwardingInfoCltvExpiryDelta)
	FfiConverterUint64INSTANCE.Write(writer, value.NextOutboundHtlcLimitMsat)
	FfiConverterUint64INSTANCE.Write(writer, value.NextOutboundHtlcMinimumMsat)
	FfiConverterOptionalUint16INSTANCE.Write(writer, value.ForceCloseSpendDelay)
	FfiConverterUint64INSTANCE.Write(writer, value.InboundHtlcMinimumMsat)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.InboundHtlcMaximumMsat)
	FfiConverterChannelConfigINSTANCE.Write(writer, value.Config)
}

type FfiDestroyerChannelDetails struct{}

func (_ FfiDestroyerChannelDetails) Destroy(value ChannelDetails) {
	value.Destroy()
}

// Details about a channel (both directions).
//
// Received within a channel announcement.
//
// This is a simplified version of LDK's `ChannelInfo` for bindings.
type ChannelInfo struct {
	// Source node of the first direction of a channel
	NodeOne NodeId
	// Details about the first direction of a channel
	OneToTwo *ChannelUpdateInfo
	// Source node of the second direction of a channel
	NodeTwo NodeId
	// Details about the second direction of a channel
	TwoToOne *ChannelUpdateInfo
	// The channel capacity as seen on-chain, if chain lookup is available.
	CapacitySats *uint64
}

func (r *ChannelInfo) Destroy() {
	FfiDestroyerTypeNodeId{}.Destroy(r.NodeOne)
	FfiDestroyerOptionalChannelUpdateInfo{}.Destroy(r.OneToTwo)
	FfiDestroyerTypeNodeId{}.Destroy(r.NodeTwo)
	FfiDestroyerOptionalChannelUpdateInfo{}.Destroy(r.TwoToOne)
	FfiDestroyerOptionalUint64{}.Destroy(r.CapacitySats)
}

type FfiConverterChannelInfo struct{}

var FfiConverterChannelInfoINSTANCE = FfiConverterChannelInfo{}

func (c FfiConverterChannelInfo) Lift(rb RustBufferI) ChannelInfo {
	return LiftFromRustBuffer[ChannelInfo](c, rb)
}

func (c FfiConverterChannelInfo) Read(reader io.Reader) ChannelInfo {
	return ChannelInfo{
		FfiConverterTypeNodeIdINSTANCE.Read(reader),
		FfiConverterOptionalChannelUpdateInfoINSTANCE.Read(reader),
		FfiConverterTypeNodeIdINSTANCE.Read(reader),
		FfiConverterOptionalChannelUpdateInfoINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterChannelInfo) Lower(value ChannelInfo) C.RustBuffer {
	return LowerIntoRustBuffer[ChannelInfo](c, value)
}

func (c FfiConverterChannelInfo) LowerExternal(value ChannelInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[ChannelInfo](c, value))
}

func (c FfiConverterChannelInfo) Write(writer io.Writer, value ChannelInfo) {
	FfiConverterTypeNodeIdINSTANCE.Write(writer, value.NodeOne)
	FfiConverterOptionalChannelUpdateInfoINSTANCE.Write(writer, value.OneToTwo)
	FfiConverterTypeNodeIdINSTANCE.Write(writer, value.NodeTwo)
	FfiConverterOptionalChannelUpdateInfoINSTANCE.Write(writer, value.TwoToOne)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.CapacitySats)
}

type FfiDestroyerChannelInfo struct{}

func (_ FfiDestroyerChannelInfo) Destroy(value ChannelInfo) {
	value.Destroy()
}

// Details about one direction of a channel as received within a `ChannelUpdate`.
//
// This is a simplified version of LDK's `ChannelUpdateInfo` for bindings.
type ChannelUpdateInfo struct {
	// When the last update to the channel direction was issued.
	// Value is opaque, as set in the announcement.
	LastUpdate uint32
	// Whether the channel can be currently used for payments (in this one direction).
	Enabled bool
	// The difference in CLTV values that you must have when routing through this channel.
	CltvExpiryDelta uint16
	// The minimum value, which must be relayed to the next hop via the channel
	HtlcMinimumMsat uint64
	// The maximum value which may be relayed to the next hop via the channel.
	HtlcMaximumMsat uint64
	// Fees charged when the channel is used for routing
	Fees RoutingFees
}

func (r *ChannelUpdateInfo) Destroy() {
	FfiDestroyerUint32{}.Destroy(r.LastUpdate)
	FfiDestroyerBool{}.Destroy(r.Enabled)
	FfiDestroyerUint16{}.Destroy(r.CltvExpiryDelta)
	FfiDestroyerUint64{}.Destroy(r.HtlcMinimumMsat)
	FfiDestroyerUint64{}.Destroy(r.HtlcMaximumMsat)
	FfiDestroyerRoutingFees{}.Destroy(r.Fees)
}

type FfiConverterChannelUpdateInfo struct{}

var FfiConverterChannelUpdateInfoINSTANCE = FfiConverterChannelUpdateInfo{}

func (c FfiConverterChannelUpdateInfo) Lift(rb RustBufferI) ChannelUpdateInfo {
	return LiftFromRustBuffer[ChannelUpdateInfo](c, rb)
}

func (c FfiConverterChannelUpdateInfo) Read(reader io.Reader) ChannelUpdateInfo {
	return ChannelUpdateInfo{
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterUint16INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterRoutingFeesINSTANCE.Read(reader),
	}
}

func (c FfiConverterChannelUpdateInfo) Lower(value ChannelUpdateInfo) C.RustBuffer {
	return LowerIntoRustBuffer[ChannelUpdateInfo](c, value)
}

func (c FfiConverterChannelUpdateInfo) LowerExternal(value ChannelUpdateInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[ChannelUpdateInfo](c, value))
}

func (c FfiConverterChannelUpdateInfo) Write(writer io.Writer, value ChannelUpdateInfo) {
	FfiConverterUint32INSTANCE.Write(writer, value.LastUpdate)
	FfiConverterBoolINSTANCE.Write(writer, value.Enabled)
	FfiConverterUint16INSTANCE.Write(writer, value.CltvExpiryDelta)
	FfiConverterUint64INSTANCE.Write(writer, value.HtlcMinimumMsat)
	FfiConverterUint64INSTANCE.Write(writer, value.HtlcMaximumMsat)
	FfiConverterRoutingFeesINSTANCE.Write(writer, value.Fees)
}

type FfiDestroyerChannelUpdateInfo struct{}

func (_ FfiDestroyerChannelUpdateInfo) Destroy(value ChannelUpdateInfo) {
	value.Destroy()
}

// Represents the configuration of an [`Node`] instance.
//
// ### Defaults
//
// | Parameter                              | Value              |
// |----------------------------------------|--------------------|
// | `storage_dir_path`                     | /tmp/ldk_node/     |
// | `network`                              | Bitcoin            |
// | `listening_addresses`                  | None               |
// | `announcement_addresses`               | None               |
// | `node_alias`                           | None               |
// | `trusted_peers_0conf`                  | []                 |
// | `probing_liquidity_limit_multiplier`   | 3                  |
// | `anchor_channels_config`               | Some(..)           |
// | `route_parameters`                     | None               |
// | `tor_config`                           | None               |
//
// See [`AnchorChannelsConfig`] and [`RouteParametersConfig`] for more information regarding their
// respective default values.
//
// [`Node`]: crate::Node
type Config struct {
	// The path where the underlying LDK and BDK persist their data.
	StorageDirPath string
	// The used Bitcoin network.
	Network Network
	// The addresses on which the node will listen for incoming connections.
	//
	// **Note**: We will only allow opening and accepting public channels if the `node_alias` and the
	// `listening_addresses` are set.
	ListeningAddresses *[]SocketAddress
	// The addresses which the node will announce to the gossip network that it accepts connections on.
	//
	// **Note**: If unset, the [`listening_addresses`] will be used as the list of addresses to announce.
	//
	// [`listening_addresses`]: Config::listening_addresses
	AnnouncementAddresses *[]SocketAddress
	// The node alias that will be used when broadcasting announcements to the gossip network.
	//
	// The provided alias must be a valid UTF-8 string and no longer than 32 bytes in total.
	//
	// **Note**: We will only allow opening and accepting public channels if the `node_alias` and the
	// `listening_addresses` are set.
	NodeAlias *NodeAlias
	// A list of peers that we allow to establish zero confirmation channels to us.
	//
	// **Note:** Allowing payments via zero-confirmation channels is potentially insecure if the
	// funding transaction ends up never being confirmed on-chain. Zero-confirmation channels
	// should therefore only be accepted from trusted peers.
	TrustedPeers0conf []PublicKey
	// The liquidity factor by which we filter the outgoing channels used for sending probes.
	//
	// Channels with available liquidity less than the required amount times this value won't be
	// used to send pre-flight probes.
	ProbingLiquidityLimitMultiplier uint64
	// Configuration options pertaining to Anchor channels, i.e., channels for which the
	// `option_anchors_zero_fee_htlc_tx` channel type is negotiated.
	//
	// Please refer to [`AnchorChannelsConfig`] for further information on Anchor channels.
	//
	// If set to `Some`, we'll try to open new channels with Anchors enabled, i.e., new channels
	// will be negotiated with the `option_anchors_zero_fee_htlc_tx` channel type if supported by
	// the counterparty. Note that this won't prevent us from opening non-Anchor channels if the
	// counterparty doesn't support `option_anchors_zero_fee_htlc_tx`. If set to `None`, new
	// channels will be negotiated with the legacy `option_static_remotekey` channel type only.
	//
	// **Note:** If set to `None` *after* some Anchor channels have already been
	// opened, no dedicated emergency on-chain reserve will be maintained for these channels,
	// which can be dangerous if only insufficient funds are available at the time of channel
	// closure. We *will* however still try to get the Anchor spending transactions confirmed
	// on-chain with the funds available.
	AnchorChannelsConfig *AnchorChannelsConfig
	// Configuration options for payment routing and pathfinding.
	//
	// Setting the [`RouteParametersConfig`] provides flexibility to customize how payments are routed,
	// including setting limits on routing fees, CLTV expiry, and channel utilization.
	//
	// **Note:** If unset, default parameters will be used, and you will be able to override the
	// parameters on a per-payment basis in the corresponding method calls.
	RouteParameters *RouteParametersConfig
	// Configuration options for enabling peer connections via the Tor network.
	//
	// Setting [`TorConfig`] enables connecting to peers with OnionV3 addresses. No other connections
	// are routed via Tor. Please refer to [`TorConfig`] for further information.
	//
	// **Note**: If unset, connecting to peer OnionV3 addresses will fail.
	TorConfig *TorConfig
}

func (r *Config) Destroy() {
	FfiDestroyerString{}.Destroy(r.StorageDirPath)
	FfiDestroyerNetwork{}.Destroy(r.Network)
	FfiDestroyerOptionalSequenceTypeSocketAddress{}.Destroy(r.ListeningAddresses)
	FfiDestroyerOptionalSequenceTypeSocketAddress{}.Destroy(r.AnnouncementAddresses)
	FfiDestroyerOptionalTypeNodeAlias{}.Destroy(r.NodeAlias)
	FfiDestroyerSequenceTypePublicKey{}.Destroy(r.TrustedPeers0conf)
	FfiDestroyerUint64{}.Destroy(r.ProbingLiquidityLimitMultiplier)
	FfiDestroyerOptionalAnchorChannelsConfig{}.Destroy(r.AnchorChannelsConfig)
	FfiDestroyerOptionalRouteParametersConfig{}.Destroy(r.RouteParameters)
	FfiDestroyerOptionalTorConfig{}.Destroy(r.TorConfig)
}

type FfiConverterConfig struct{}

var FfiConverterConfigINSTANCE = FfiConverterConfig{}

func (c FfiConverterConfig) Lift(rb RustBufferI) Config {
	return LiftFromRustBuffer[Config](c, rb)
}

func (c FfiConverterConfig) Read(reader io.Reader) Config {
	return Config{
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterNetworkINSTANCE.Read(reader),
		FfiConverterOptionalSequenceTypeSocketAddressINSTANCE.Read(reader),
		FfiConverterOptionalSequenceTypeSocketAddressINSTANCE.Read(reader),
		FfiConverterOptionalTypeNodeAliasINSTANCE.Read(reader),
		FfiConverterSequenceTypePublicKeyINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterOptionalAnchorChannelsConfigINSTANCE.Read(reader),
		FfiConverterOptionalRouteParametersConfigINSTANCE.Read(reader),
		FfiConverterOptionalTorConfigINSTANCE.Read(reader),
	}
}

func (c FfiConverterConfig) Lower(value Config) C.RustBuffer {
	return LowerIntoRustBuffer[Config](c, value)
}

func (c FfiConverterConfig) LowerExternal(value Config) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Config](c, value))
}

func (c FfiConverterConfig) Write(writer io.Writer, value Config) {
	FfiConverterStringINSTANCE.Write(writer, value.StorageDirPath)
	FfiConverterNetworkINSTANCE.Write(writer, value.Network)
	FfiConverterOptionalSequenceTypeSocketAddressINSTANCE.Write(writer, value.ListeningAddresses)
	FfiConverterOptionalSequenceTypeSocketAddressINSTANCE.Write(writer, value.AnnouncementAddresses)
	FfiConverterOptionalTypeNodeAliasINSTANCE.Write(writer, value.NodeAlias)
	FfiConverterSequenceTypePublicKeyINSTANCE.Write(writer, value.TrustedPeers0conf)
	FfiConverterUint64INSTANCE.Write(writer, value.ProbingLiquidityLimitMultiplier)
	FfiConverterOptionalAnchorChannelsConfigINSTANCE.Write(writer, value.AnchorChannelsConfig)
	FfiConverterOptionalRouteParametersConfigINSTANCE.Write(writer, value.RouteParameters)
	FfiConverterOptionalTorConfigINSTANCE.Write(writer, value.TorConfig)
}

type FfiDestroyerConfig struct{}

func (_ FfiDestroyerConfig) Destroy(value Config) {
	value.Destroy()
}

// Custom TLV entry.
type CustomTlvRecord struct {
	// Type number.
	TypeNum uint64
	// Serialized value.
	Value []byte
}

func (r *CustomTlvRecord) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.TypeNum)
	FfiDestroyerBytes{}.Destroy(r.Value)
}

type FfiConverterCustomTlvRecord struct{}

var FfiConverterCustomTlvRecordINSTANCE = FfiConverterCustomTlvRecord{}

func (c FfiConverterCustomTlvRecord) Lift(rb RustBufferI) CustomTlvRecord {
	return LiftFromRustBuffer[CustomTlvRecord](c, rb)
}

func (c FfiConverterCustomTlvRecord) Read(reader io.Reader) CustomTlvRecord {
	return CustomTlvRecord{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBytesINSTANCE.Read(reader),
	}
}

func (c FfiConverterCustomTlvRecord) Lower(value CustomTlvRecord) C.RustBuffer {
	return LowerIntoRustBuffer[CustomTlvRecord](c, value)
}

func (c FfiConverterCustomTlvRecord) LowerExternal(value CustomTlvRecord) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[CustomTlvRecord](c, value))
}

func (c FfiConverterCustomTlvRecord) Write(writer io.Writer, value CustomTlvRecord) {
	FfiConverterUint64INSTANCE.Write(writer, value.TypeNum)
	FfiConverterBytesINSTANCE.Write(writer, value.Value)
}

type FfiDestroyerCustomTlvRecord struct{}

func (_ FfiDestroyerCustomTlvRecord) Destroy(value CustomTlvRecord) {
	value.Destroy()
}

// Configuration for syncing with an Electrum backend.
//
// Background syncing is enabled by default, using the default values specified in
// [`BackgroundSyncConfig`].
type ElectrumSyncConfig struct {
	// Background sync configuration.
	//
	// If set to `None`, background syncing will be disabled. Users will need to manually
	// sync via [`Node::sync_wallets`] for the wallets and fee rate updates.
	//
	// [`Node::sync_wallets`]: crate::Node::sync_wallets
	BackgroundSyncConfig *BackgroundSyncConfig
	// Sync timeouts configuration.
	TimeoutsConfig SyncTimeoutsConfig
}

func (r *ElectrumSyncConfig) Destroy() {
	FfiDestroyerOptionalBackgroundSyncConfig{}.Destroy(r.BackgroundSyncConfig)
	FfiDestroyerSyncTimeoutsConfig{}.Destroy(r.TimeoutsConfig)
}

type FfiConverterElectrumSyncConfig struct{}

var FfiConverterElectrumSyncConfigINSTANCE = FfiConverterElectrumSyncConfig{}

func (c FfiConverterElectrumSyncConfig) Lift(rb RustBufferI) ElectrumSyncConfig {
	return LiftFromRustBuffer[ElectrumSyncConfig](c, rb)
}

func (c FfiConverterElectrumSyncConfig) Read(reader io.Reader) ElectrumSyncConfig {
	return ElectrumSyncConfig{
		FfiConverterOptionalBackgroundSyncConfigINSTANCE.Read(reader),
		FfiConverterSyncTimeoutsConfigINSTANCE.Read(reader),
	}
}

func (c FfiConverterElectrumSyncConfig) Lower(value ElectrumSyncConfig) C.RustBuffer {
	return LowerIntoRustBuffer[ElectrumSyncConfig](c, value)
}

func (c FfiConverterElectrumSyncConfig) LowerExternal(value ElectrumSyncConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[ElectrumSyncConfig](c, value))
}

func (c FfiConverterElectrumSyncConfig) Write(writer io.Writer, value ElectrumSyncConfig) {
	FfiConverterOptionalBackgroundSyncConfigINSTANCE.Write(writer, value.BackgroundSyncConfig)
	FfiConverterSyncTimeoutsConfigINSTANCE.Write(writer, value.TimeoutsConfig)
}

type FfiDestroyerElectrumSyncConfig struct{}

func (_ FfiDestroyerElectrumSyncConfig) Destroy(value ElectrumSyncConfig) {
	value.Destroy()
}

// Configuration for syncing with an Esplora backend.
//
// Background syncing is enabled by default, using the default values specified in
// [`BackgroundSyncConfig`].
type EsploraSyncConfig struct {
	// Background sync configuration.
	//
	// If set to `None`, background syncing will be disabled. Users will need to manually
	// sync via [`Node::sync_wallets`] for the wallets and fee rate updates.
	//
	// [`Node::sync_wallets`]: crate::Node::sync_wallets
	BackgroundSyncConfig *BackgroundSyncConfig
	// Sync timeouts configuration.
	TimeoutsConfig SyncTimeoutsConfig
}

func (r *EsploraSyncConfig) Destroy() {
	FfiDestroyerOptionalBackgroundSyncConfig{}.Destroy(r.BackgroundSyncConfig)
	FfiDestroyerSyncTimeoutsConfig{}.Destroy(r.TimeoutsConfig)
}

type FfiConverterEsploraSyncConfig struct{}

var FfiConverterEsploraSyncConfigINSTANCE = FfiConverterEsploraSyncConfig{}

func (c FfiConverterEsploraSyncConfig) Lift(rb RustBufferI) EsploraSyncConfig {
	return LiftFromRustBuffer[EsploraSyncConfig](c, rb)
}

func (c FfiConverterEsploraSyncConfig) Read(reader io.Reader) EsploraSyncConfig {
	return EsploraSyncConfig{
		FfiConverterOptionalBackgroundSyncConfigINSTANCE.Read(reader),
		FfiConverterSyncTimeoutsConfigINSTANCE.Read(reader),
	}
}

func (c FfiConverterEsploraSyncConfig) Lower(value EsploraSyncConfig) C.RustBuffer {
	return LowerIntoRustBuffer[EsploraSyncConfig](c, value)
}

func (c FfiConverterEsploraSyncConfig) LowerExternal(value EsploraSyncConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[EsploraSyncConfig](c, value))
}

func (c FfiConverterEsploraSyncConfig) Write(writer io.Writer, value EsploraSyncConfig) {
	FfiConverterOptionalBackgroundSyncConfigINSTANCE.Write(writer, value.BackgroundSyncConfig)
	FfiConverterSyncTimeoutsConfigINSTANCE.Write(writer, value.TimeoutsConfig)
}

type FfiDestroyerEsploraSyncConfig struct{}

func (_ FfiDestroyerEsploraSyncConfig) Destroy(value EsploraSyncConfig) {
	value.Destroy()
}

// Limits applying to how much fee we allow an LSP to deduct from the payment amount.
//
// See [`LdkChannelConfig::accept_underpaying_htlcs`] for more information.
//
// [`LdkChannelConfig::accept_underpaying_htlcs`]: lightning::util::config::ChannelConfig::accept_underpaying_htlcs
type LspFeeLimits struct {
	// The maximal total amount we allow any configured LSP withhold from us when forwarding the
	// payment.
	MaxTotalOpeningFeeMsat *uint64
	// The maximal proportional fee, in parts-per-million millisatoshi, we allow any configured
	// LSP withhold from us when forwarding the payment.
	MaxProportionalOpeningFeePpmMsat *uint64
}

func (r *LspFeeLimits) Destroy() {
	FfiDestroyerOptionalUint64{}.Destroy(r.MaxTotalOpeningFeeMsat)
	FfiDestroyerOptionalUint64{}.Destroy(r.MaxProportionalOpeningFeePpmMsat)
}

type FfiConverterLspFeeLimits struct{}

var FfiConverterLspFeeLimitsINSTANCE = FfiConverterLspFeeLimits{}

func (c FfiConverterLspFeeLimits) Lift(rb RustBufferI) LspFeeLimits {
	return LiftFromRustBuffer[LspFeeLimits](c, rb)
}

func (c FfiConverterLspFeeLimits) Read(reader io.Reader) LspFeeLimits {
	return LspFeeLimits{
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterLspFeeLimits) Lower(value LspFeeLimits) C.RustBuffer {
	return LowerIntoRustBuffer[LspFeeLimits](c, value)
}

func (c FfiConverterLspFeeLimits) LowerExternal(value LspFeeLimits) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[LspFeeLimits](c, value))
}

func (c FfiConverterLspFeeLimits) Write(writer io.Writer, value LspFeeLimits) {
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.MaxTotalOpeningFeeMsat)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.MaxProportionalOpeningFeePpmMsat)
}

type FfiDestroyerLspFeeLimits struct{}

func (_ FfiDestroyerLspFeeLimits) Destroy(value LspFeeLimits) {
	value.Destroy()
}

// A Lightning payment using BOLT 11.
type Lsps1Bolt11PaymentInfo struct {
	// Indicates the current state of the payment.
	State Lsps1PaymentState
	// The datetime when the payment option expires.
	ExpiresAt LSPSDateTime
	// The total fee the LSP will charge to open this channel in satoshi.
	FeeTotalSat uint64
	// The amount the client needs to pay to have the requested channel openend.
	OrderTotalSat uint64
	// A BOLT11 invoice the client can pay to have to channel opened.
	Invoice *Bolt11Invoice
}

func (r *Lsps1Bolt11PaymentInfo) Destroy() {
	FfiDestroyerLsps1PaymentState{}.Destroy(r.State)
	FfiDestroyerTypeLSPSDateTime{}.Destroy(r.ExpiresAt)
	FfiDestroyerUint64{}.Destroy(r.FeeTotalSat)
	FfiDestroyerUint64{}.Destroy(r.OrderTotalSat)
	FfiDestroyerBolt11Invoice{}.Destroy(r.Invoice)
}

type FfiConverterLsps1Bolt11PaymentInfo struct{}

var FfiConverterLsps1Bolt11PaymentInfoINSTANCE = FfiConverterLsps1Bolt11PaymentInfo{}

func (c FfiConverterLsps1Bolt11PaymentInfo) Lift(rb RustBufferI) Lsps1Bolt11PaymentInfo {
	return LiftFromRustBuffer[Lsps1Bolt11PaymentInfo](c, rb)
}

func (c FfiConverterLsps1Bolt11PaymentInfo) Read(reader io.Reader) Lsps1Bolt11PaymentInfo {
	return Lsps1Bolt11PaymentInfo{
		FfiConverterLsps1PaymentStateINSTANCE.Read(reader),
		FfiConverterTypeLSPSDateTimeINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBolt11InvoiceINSTANCE.Read(reader),
	}
}

func (c FfiConverterLsps1Bolt11PaymentInfo) Lower(value Lsps1Bolt11PaymentInfo) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps1Bolt11PaymentInfo](c, value)
}

func (c FfiConverterLsps1Bolt11PaymentInfo) LowerExternal(value Lsps1Bolt11PaymentInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps1Bolt11PaymentInfo](c, value))
}

func (c FfiConverterLsps1Bolt11PaymentInfo) Write(writer io.Writer, value Lsps1Bolt11PaymentInfo) {
	FfiConverterLsps1PaymentStateINSTANCE.Write(writer, value.State)
	FfiConverterTypeLSPSDateTimeINSTANCE.Write(writer, value.ExpiresAt)
	FfiConverterUint64INSTANCE.Write(writer, value.FeeTotalSat)
	FfiConverterUint64INSTANCE.Write(writer, value.OrderTotalSat)
	FfiConverterBolt11InvoiceINSTANCE.Write(writer, value.Invoice)
}

type FfiDestroyerLsps1Bolt11PaymentInfo struct{}

func (_ FfiDestroyerLsps1Bolt11PaymentInfo) Destroy(value Lsps1Bolt11PaymentInfo) {
	value.Destroy()
}

type Lsps1ChannelInfo struct {
	FundedAt        LSPSDateTime
	FundingOutpoint OutPoint
	ExpiresAt       LSPSDateTime
}

func (r *Lsps1ChannelInfo) Destroy() {
	FfiDestroyerTypeLSPSDateTime{}.Destroy(r.FundedAt)
	FfiDestroyerOutPoint{}.Destroy(r.FundingOutpoint)
	FfiDestroyerTypeLSPSDateTime{}.Destroy(r.ExpiresAt)
}

type FfiConverterLsps1ChannelInfo struct{}

var FfiConverterLsps1ChannelInfoINSTANCE = FfiConverterLsps1ChannelInfo{}

func (c FfiConverterLsps1ChannelInfo) Lift(rb RustBufferI) Lsps1ChannelInfo {
	return LiftFromRustBuffer[Lsps1ChannelInfo](c, rb)
}

func (c FfiConverterLsps1ChannelInfo) Read(reader io.Reader) Lsps1ChannelInfo {
	return Lsps1ChannelInfo{
		FfiConverterTypeLSPSDateTimeINSTANCE.Read(reader),
		FfiConverterOutPointINSTANCE.Read(reader),
		FfiConverterTypeLSPSDateTimeINSTANCE.Read(reader),
	}
}

func (c FfiConverterLsps1ChannelInfo) Lower(value Lsps1ChannelInfo) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps1ChannelInfo](c, value)
}

func (c FfiConverterLsps1ChannelInfo) LowerExternal(value Lsps1ChannelInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps1ChannelInfo](c, value))
}

func (c FfiConverterLsps1ChannelInfo) Write(writer io.Writer, value Lsps1ChannelInfo) {
	FfiConverterTypeLSPSDateTimeINSTANCE.Write(writer, value.FundedAt)
	FfiConverterOutPointINSTANCE.Write(writer, value.FundingOutpoint)
	FfiConverterTypeLSPSDateTimeINSTANCE.Write(writer, value.ExpiresAt)
}

type FfiDestroyerLsps1ChannelInfo struct{}

func (_ FfiDestroyerLsps1ChannelInfo) Destroy(value Lsps1ChannelInfo) {
	value.Destroy()
}

// An onchain payment.
type Lsps1OnchainPaymentInfo struct {
	// Indicates the current state of the payment.
	State Lsps1PaymentState
	// The datetime when the payment option expires.
	ExpiresAt LSPSDateTime
	// The total fee the LSP will charge to open this channel in satoshi.
	FeeTotalSat uint64
	// The amount the client needs to pay to have the requested channel opened.
	OrderTotalSat uint64
	// An on-chain address the client can send [`Self::order_total_sat`] to have the channel
	// opened.
	Address Address
	// The minimum number of block confirmations that are required for the on-chain payment to be
	// considered confirmed.
	MinOnchainPaymentConfirmations *uint16
	// The minimum fee rate for the on-chain payment in case the client wants the payment to be
	// confirmed without a confirmation.
	MinFeeFor0conf *FeeRate
	// The address where the LSP will send the funds if the order fails.
	RefundOnchainAddress *Address
}

func (r *Lsps1OnchainPaymentInfo) Destroy() {
	FfiDestroyerLsps1PaymentState{}.Destroy(r.State)
	FfiDestroyerTypeLSPSDateTime{}.Destroy(r.ExpiresAt)
	FfiDestroyerUint64{}.Destroy(r.FeeTotalSat)
	FfiDestroyerUint64{}.Destroy(r.OrderTotalSat)
	FfiDestroyerTypeAddress{}.Destroy(r.Address)
	FfiDestroyerOptionalUint16{}.Destroy(r.MinOnchainPaymentConfirmations)
	FfiDestroyerFeeRate{}.Destroy(r.MinFeeFor0conf)
	FfiDestroyerOptionalTypeAddress{}.Destroy(r.RefundOnchainAddress)
}

type FfiConverterLsps1OnchainPaymentInfo struct{}

var FfiConverterLsps1OnchainPaymentInfoINSTANCE = FfiConverterLsps1OnchainPaymentInfo{}

func (c FfiConverterLsps1OnchainPaymentInfo) Lift(rb RustBufferI) Lsps1OnchainPaymentInfo {
	return LiftFromRustBuffer[Lsps1OnchainPaymentInfo](c, rb)
}

func (c FfiConverterLsps1OnchainPaymentInfo) Read(reader io.Reader) Lsps1OnchainPaymentInfo {
	return Lsps1OnchainPaymentInfo{
		FfiConverterLsps1PaymentStateINSTANCE.Read(reader),
		FfiConverterTypeLSPSDateTimeINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterTypeAddressINSTANCE.Read(reader),
		FfiConverterOptionalUint16INSTANCE.Read(reader),
		FfiConverterFeeRateINSTANCE.Read(reader),
		FfiConverterOptionalTypeAddressINSTANCE.Read(reader),
	}
}

func (c FfiConverterLsps1OnchainPaymentInfo) Lower(value Lsps1OnchainPaymentInfo) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps1OnchainPaymentInfo](c, value)
}

func (c FfiConverterLsps1OnchainPaymentInfo) LowerExternal(value Lsps1OnchainPaymentInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps1OnchainPaymentInfo](c, value))
}

func (c FfiConverterLsps1OnchainPaymentInfo) Write(writer io.Writer, value Lsps1OnchainPaymentInfo) {
	FfiConverterLsps1PaymentStateINSTANCE.Write(writer, value.State)
	FfiConverterTypeLSPSDateTimeINSTANCE.Write(writer, value.ExpiresAt)
	FfiConverterUint64INSTANCE.Write(writer, value.FeeTotalSat)
	FfiConverterUint64INSTANCE.Write(writer, value.OrderTotalSat)
	FfiConverterTypeAddressINSTANCE.Write(writer, value.Address)
	FfiConverterOptionalUint16INSTANCE.Write(writer, value.MinOnchainPaymentConfirmations)
	FfiConverterFeeRateINSTANCE.Write(writer, value.MinFeeFor0conf)
	FfiConverterOptionalTypeAddressINSTANCE.Write(writer, value.RefundOnchainAddress)
}

type FfiDestroyerLsps1OnchainPaymentInfo struct{}

func (_ FfiDestroyerLsps1OnchainPaymentInfo) Destroy(value Lsps1OnchainPaymentInfo) {
	value.Destroy()
}

type Lsps1OrderParams struct {
	LspBalanceSat                uint64
	ClientBalanceSat             uint64
	RequiredChannelConfirmations uint16
	FundingConfirmsWithinBlocks  uint16
	ChannelExpiryBlocks          uint32
	Token                        *string
	AnnounceChannel              bool
}

func (r *Lsps1OrderParams) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.LspBalanceSat)
	FfiDestroyerUint64{}.Destroy(r.ClientBalanceSat)
	FfiDestroyerUint16{}.Destroy(r.RequiredChannelConfirmations)
	FfiDestroyerUint16{}.Destroy(r.FundingConfirmsWithinBlocks)
	FfiDestroyerUint32{}.Destroy(r.ChannelExpiryBlocks)
	FfiDestroyerOptionalString{}.Destroy(r.Token)
	FfiDestroyerBool{}.Destroy(r.AnnounceChannel)
}

type FfiConverterLsps1OrderParams struct{}

var FfiConverterLsps1OrderParamsINSTANCE = FfiConverterLsps1OrderParams{}

func (c FfiConverterLsps1OrderParams) Lift(rb RustBufferI) Lsps1OrderParams {
	return LiftFromRustBuffer[Lsps1OrderParams](c, rb)
}

func (c FfiConverterLsps1OrderParams) Read(reader io.Reader) Lsps1OrderParams {
	return Lsps1OrderParams{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint16INSTANCE.Read(reader),
		FfiConverterUint16INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterLsps1OrderParams) Lower(value Lsps1OrderParams) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps1OrderParams](c, value)
}

func (c FfiConverterLsps1OrderParams) LowerExternal(value Lsps1OrderParams) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps1OrderParams](c, value))
}

func (c FfiConverterLsps1OrderParams) Write(writer io.Writer, value Lsps1OrderParams) {
	FfiConverterUint64INSTANCE.Write(writer, value.LspBalanceSat)
	FfiConverterUint64INSTANCE.Write(writer, value.ClientBalanceSat)
	FfiConverterUint16INSTANCE.Write(writer, value.RequiredChannelConfirmations)
	FfiConverterUint16INSTANCE.Write(writer, value.FundingConfirmsWithinBlocks)
	FfiConverterUint32INSTANCE.Write(writer, value.ChannelExpiryBlocks)
	FfiConverterOptionalStringINSTANCE.Write(writer, value.Token)
	FfiConverterBoolINSTANCE.Write(writer, value.AnnounceChannel)
}

type FfiDestroyerLsps1OrderParams struct{}

func (_ FfiDestroyerLsps1OrderParams) Destroy(value Lsps1OrderParams) {
	value.Destroy()
}

type Lsps1OrderStatus struct {
	OrderId        LSPS1OrderId
	OrderParams    Lsps1OrderParams
	PaymentOptions Lsps1PaymentInfo
	ChannelState   *Lsps1ChannelInfo
}

func (r *Lsps1OrderStatus) Destroy() {
	FfiDestroyerTypeLSPS1OrderId{}.Destroy(r.OrderId)
	FfiDestroyerLsps1OrderParams{}.Destroy(r.OrderParams)
	FfiDestroyerLsps1PaymentInfo{}.Destroy(r.PaymentOptions)
	FfiDestroyerOptionalLsps1ChannelInfo{}.Destroy(r.ChannelState)
}

type FfiConverterLsps1OrderStatus struct{}

var FfiConverterLsps1OrderStatusINSTANCE = FfiConverterLsps1OrderStatus{}

func (c FfiConverterLsps1OrderStatus) Lift(rb RustBufferI) Lsps1OrderStatus {
	return LiftFromRustBuffer[Lsps1OrderStatus](c, rb)
}

func (c FfiConverterLsps1OrderStatus) Read(reader io.Reader) Lsps1OrderStatus {
	return Lsps1OrderStatus{
		FfiConverterTypeLSPS1OrderIdINSTANCE.Read(reader),
		FfiConverterLsps1OrderParamsINSTANCE.Read(reader),
		FfiConverterLsps1PaymentInfoINSTANCE.Read(reader),
		FfiConverterOptionalLsps1ChannelInfoINSTANCE.Read(reader),
	}
}

func (c FfiConverterLsps1OrderStatus) Lower(value Lsps1OrderStatus) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps1OrderStatus](c, value)
}

func (c FfiConverterLsps1OrderStatus) LowerExternal(value Lsps1OrderStatus) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps1OrderStatus](c, value))
}

func (c FfiConverterLsps1OrderStatus) Write(writer io.Writer, value Lsps1OrderStatus) {
	FfiConverterTypeLSPS1OrderIdINSTANCE.Write(writer, value.OrderId)
	FfiConverterLsps1OrderParamsINSTANCE.Write(writer, value.OrderParams)
	FfiConverterLsps1PaymentInfoINSTANCE.Write(writer, value.PaymentOptions)
	FfiConverterOptionalLsps1ChannelInfoINSTANCE.Write(writer, value.ChannelState)
}

type FfiDestroyerLsps1OrderStatus struct{}

func (_ FfiDestroyerLsps1OrderStatus) Destroy(value Lsps1OrderStatus) {
	value.Destroy()
}

type Lsps1PaymentInfo struct {
	// A Lightning payment using BOLT 11.
	Bolt11 *Lsps1Bolt11PaymentInfo
	// An onchain payment.
	Onchain *Lsps1OnchainPaymentInfo
}

func (r *Lsps1PaymentInfo) Destroy() {
	FfiDestroyerOptionalLsps1Bolt11PaymentInfo{}.Destroy(r.Bolt11)
	FfiDestroyerOptionalLsps1OnchainPaymentInfo{}.Destroy(r.Onchain)
}

type FfiConverterLsps1PaymentInfo struct{}

var FfiConverterLsps1PaymentInfoINSTANCE = FfiConverterLsps1PaymentInfo{}

func (c FfiConverterLsps1PaymentInfo) Lift(rb RustBufferI) Lsps1PaymentInfo {
	return LiftFromRustBuffer[Lsps1PaymentInfo](c, rb)
}

func (c FfiConverterLsps1PaymentInfo) Read(reader io.Reader) Lsps1PaymentInfo {
	return Lsps1PaymentInfo{
		FfiConverterOptionalLsps1Bolt11PaymentInfoINSTANCE.Read(reader),
		FfiConverterOptionalLsps1OnchainPaymentInfoINSTANCE.Read(reader),
	}
}

func (c FfiConverterLsps1PaymentInfo) Lower(value Lsps1PaymentInfo) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps1PaymentInfo](c, value)
}

func (c FfiConverterLsps1PaymentInfo) LowerExternal(value Lsps1PaymentInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps1PaymentInfo](c, value))
}

func (c FfiConverterLsps1PaymentInfo) Write(writer io.Writer, value Lsps1PaymentInfo) {
	FfiConverterOptionalLsps1Bolt11PaymentInfoINSTANCE.Write(writer, value.Bolt11)
	FfiConverterOptionalLsps1OnchainPaymentInfoINSTANCE.Write(writer, value.Onchain)
}

type FfiDestroyerLsps1PaymentInfo struct{}

func (_ FfiDestroyerLsps1PaymentInfo) Destroy(value Lsps1PaymentInfo) {
	value.Destroy()
}

// Represents the configuration of the LSPS2 service.
//
// See [bLIP-52 / LSPS2] for more information.
//
// [bLIP-52 / LSPS2]: https://github.com/lightning/blips/blob/master/blip-0052.md
type Lsps2ServiceConfig struct {
	// A token we may require to be sent by the clients.
	//
	// If set, only requests matching this token will be accepted.
	RequireToken *string
	// Indicates whether the LSPS service will be announced via the gossip network.
	AdvertiseService bool
	// The fee we withhold for the channel open from the initial payment.
	//
	// This fee is proportional to the client-requested amount, in parts-per-million.
	ChannelOpeningFeePpm uint32
	// The proportional overprovisioning for the channel.
	//
	// This determines, in parts-per-million, how much value we'll provision on top of the amount
	// we need to forward the payment to the client.
	//
	// For example, setting this to `100_000` will result in a channel being opened that is 10%
	// larger than then the to-be-forwarded amount (i.e., client-requested amount minus the
	// channel opening fee fee).
	ChannelOverProvisioningPpm uint32
	// The minimum fee required for opening a channel.
	MinChannelOpeningFeeMsat uint64
	// The minimum number of blocks after confirmation we promise to keep the channel open.
	MinChannelLifetime uint32
	// The maximum number of blocks that the client is allowed to set its `to_self_delay` parameter.
	MaxClientToSelfDelay uint32
	// The minimum payment size that we will accept when opening a channel.
	MinPaymentSizeMsat uint64
	// The maximum payment size that we will accept when opening a channel.
	MaxPaymentSizeMsat uint64
	// Use the 'client-trusts-LSP' trust model.
	//
	// When set, the service will delay *broadcasting* the JIT channel's funding transaction until
	// the client claimed sufficient HTLC parts to pay for the channel open.
	//
	// Note this will render the flow incompatible with clients utilizing the 'LSP-trust-client'
	// trust model, i.e., in turn delay *claiming* any HTLCs until they see the funding
	// transaction in the mempool.
	//
	// Please refer to [`bLIP-52`] for more information.
	//
	// [`bLIP-52`]: https://github.com/lightning/blips/blob/master/blip-0052.md#trust-models
	ClientTrustsLsp bool
}

func (r *Lsps2ServiceConfig) Destroy() {
	FfiDestroyerOptionalString{}.Destroy(r.RequireToken)
	FfiDestroyerBool{}.Destroy(r.AdvertiseService)
	FfiDestroyerUint32{}.Destroy(r.ChannelOpeningFeePpm)
	FfiDestroyerUint32{}.Destroy(r.ChannelOverProvisioningPpm)
	FfiDestroyerUint64{}.Destroy(r.MinChannelOpeningFeeMsat)
	FfiDestroyerUint32{}.Destroy(r.MinChannelLifetime)
	FfiDestroyerUint32{}.Destroy(r.MaxClientToSelfDelay)
	FfiDestroyerUint64{}.Destroy(r.MinPaymentSizeMsat)
	FfiDestroyerUint64{}.Destroy(r.MaxPaymentSizeMsat)
	FfiDestroyerBool{}.Destroy(r.ClientTrustsLsp)
}

type FfiConverterLsps2ServiceConfig struct{}

var FfiConverterLsps2ServiceConfigINSTANCE = FfiConverterLsps2ServiceConfig{}

func (c FfiConverterLsps2ServiceConfig) Lift(rb RustBufferI) Lsps2ServiceConfig {
	return LiftFromRustBuffer[Lsps2ServiceConfig](c, rb)
}

func (c FfiConverterLsps2ServiceConfig) Read(reader io.Reader) Lsps2ServiceConfig {
	return Lsps2ServiceConfig{
		FfiConverterOptionalStringINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterLsps2ServiceConfig) Lower(value Lsps2ServiceConfig) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps2ServiceConfig](c, value)
}

func (c FfiConverterLsps2ServiceConfig) LowerExternal(value Lsps2ServiceConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps2ServiceConfig](c, value))
}

func (c FfiConverterLsps2ServiceConfig) Write(writer io.Writer, value Lsps2ServiceConfig) {
	FfiConverterOptionalStringINSTANCE.Write(writer, value.RequireToken)
	FfiConverterBoolINSTANCE.Write(writer, value.AdvertiseService)
	FfiConverterUint32INSTANCE.Write(writer, value.ChannelOpeningFeePpm)
	FfiConverterUint32INSTANCE.Write(writer, value.ChannelOverProvisioningPpm)
	FfiConverterUint64INSTANCE.Write(writer, value.MinChannelOpeningFeeMsat)
	FfiConverterUint32INSTANCE.Write(writer, value.MinChannelLifetime)
	FfiConverterUint32INSTANCE.Write(writer, value.MaxClientToSelfDelay)
	FfiConverterUint64INSTANCE.Write(writer, value.MinPaymentSizeMsat)
	FfiConverterUint64INSTANCE.Write(writer, value.MaxPaymentSizeMsat)
	FfiConverterBoolINSTANCE.Write(writer, value.ClientTrustsLsp)
}

type FfiDestroyerLsps2ServiceConfig struct{}

func (_ FfiDestroyerLsps2ServiceConfig) Destroy(value Lsps2ServiceConfig) {
	value.Destroy()
}

// A unit of logging output with metadata to enable filtering `module_path`,
// `file`, and `line` to inform on log's source.
//
// This version is used when the `uniffi` feature is enabled.
// It is similar to the non-`uniffi` version, but it omits the lifetime parameter
// for the `LogRecord`, as the Uniffi-exposed interface cannot handle lifetimes.
type LogRecord struct {
	// The verbosity level of the message.
	Level LogLevel
	// The message body.
	Args string
	// The module path of the message.
	ModulePath string
	// The line containing the message.
	Line uint32
	// The node id of the peer pertaining to the logged record.
	PeerId *PublicKey
	// The channel id of the channel pertaining to the logged record.
	ChannelId *ChannelId
	// The payment hash pertaining to the logged record.
	PaymentHash *PaymentHash
}

func (r *LogRecord) Destroy() {
	FfiDestroyerLogLevel{}.Destroy(r.Level)
	FfiDestroyerString{}.Destroy(r.Args)
	FfiDestroyerString{}.Destroy(r.ModulePath)
	FfiDestroyerUint32{}.Destroy(r.Line)
	FfiDestroyerOptionalTypePublicKey{}.Destroy(r.PeerId)
	FfiDestroyerOptionalTypeChannelId{}.Destroy(r.ChannelId)
	FfiDestroyerOptionalTypePaymentHash{}.Destroy(r.PaymentHash)
}

type FfiConverterLogRecord struct{}

var FfiConverterLogRecordINSTANCE = FfiConverterLogRecord{}

func (c FfiConverterLogRecord) Lift(rb RustBufferI) LogRecord {
	return LiftFromRustBuffer[LogRecord](c, rb)
}

func (c FfiConverterLogRecord) Read(reader io.Reader) LogRecord {
	return LogRecord{
		FfiConverterLogLevelINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterOptionalTypePublicKeyINSTANCE.Read(reader),
		FfiConverterOptionalTypeChannelIdINSTANCE.Read(reader),
		FfiConverterOptionalTypePaymentHashINSTANCE.Read(reader),
	}
}

func (c FfiConverterLogRecord) Lower(value LogRecord) C.RustBuffer {
	return LowerIntoRustBuffer[LogRecord](c, value)
}

func (c FfiConverterLogRecord) LowerExternal(value LogRecord) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[LogRecord](c, value))
}

func (c FfiConverterLogRecord) Write(writer io.Writer, value LogRecord) {
	FfiConverterLogLevelINSTANCE.Write(writer, value.Level)
	FfiConverterStringINSTANCE.Write(writer, value.Args)
	FfiConverterStringINSTANCE.Write(writer, value.ModulePath)
	FfiConverterUint32INSTANCE.Write(writer, value.Line)
	FfiConverterOptionalTypePublicKeyINSTANCE.Write(writer, value.PeerId)
	FfiConverterOptionalTypeChannelIdINSTANCE.Write(writer, value.ChannelId)
	FfiConverterOptionalTypePaymentHashINSTANCE.Write(writer, value.PaymentHash)
}

type FfiDestroyerLogRecord struct{}

func (_ FfiDestroyerLogRecord) Destroy(value LogRecord) {
	value.Destroy()
}

// Information received in the latest node_announcement from this node.
//
// This is a simplified version of LDK's `NodeAnnouncementInfo` for bindings.
type NodeAnnouncementInfo struct {
	// When the last known update to the node state was issued.
	// Value is opaque, as set in the announcement.
	LastUpdate uint32
	// Moniker assigned to the node.
	// May be invalid or malicious (eg control chars),
	// should not be exposed to the user.
	Alias string
	// List of addresses on which this node is reachable
	Addresses []SocketAddress
}

func (r *NodeAnnouncementInfo) Destroy() {
	FfiDestroyerUint32{}.Destroy(r.LastUpdate)
	FfiDestroyerString{}.Destroy(r.Alias)
	FfiDestroyerSequenceTypeSocketAddress{}.Destroy(r.Addresses)
}

type FfiConverterNodeAnnouncementInfo struct{}

var FfiConverterNodeAnnouncementInfoINSTANCE = FfiConverterNodeAnnouncementInfo{}

func (c FfiConverterNodeAnnouncementInfo) Lift(rb RustBufferI) NodeAnnouncementInfo {
	return LiftFromRustBuffer[NodeAnnouncementInfo](c, rb)
}

func (c FfiConverterNodeAnnouncementInfo) Read(reader io.Reader) NodeAnnouncementInfo {
	return NodeAnnouncementInfo{
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterStringINSTANCE.Read(reader),
		FfiConverterSequenceTypeSocketAddressINSTANCE.Read(reader),
	}
}

func (c FfiConverterNodeAnnouncementInfo) Lower(value NodeAnnouncementInfo) C.RustBuffer {
	return LowerIntoRustBuffer[NodeAnnouncementInfo](c, value)
}

func (c FfiConverterNodeAnnouncementInfo) LowerExternal(value NodeAnnouncementInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[NodeAnnouncementInfo](c, value))
}

func (c FfiConverterNodeAnnouncementInfo) Write(writer io.Writer, value NodeAnnouncementInfo) {
	FfiConverterUint32INSTANCE.Write(writer, value.LastUpdate)
	FfiConverterStringINSTANCE.Write(writer, value.Alias)
	FfiConverterSequenceTypeSocketAddressINSTANCE.Write(writer, value.Addresses)
}

type FfiDestroyerNodeAnnouncementInfo struct{}

func (_ FfiDestroyerNodeAnnouncementInfo) Destroy(value NodeAnnouncementInfo) {
	value.Destroy()
}

// Details about a node in the network, known from the network announcement.
//
// This is a simplified version of LDK's `NodeInfo` for bindings.
type NodeInfo struct {
	// All valid channels a node has announced
	Channels []uint64
	// More information about a node from node_announcement.
	// Optional because we store a Node entry after learning about it from
	// a channel announcement, but before receiving a node announcement.
	AnnouncementInfo *NodeAnnouncementInfo
}

func (r *NodeInfo) Destroy() {
	FfiDestroyerSequenceUint64{}.Destroy(r.Channels)
	FfiDestroyerOptionalNodeAnnouncementInfo{}.Destroy(r.AnnouncementInfo)
}

type FfiConverterNodeInfo struct{}

var FfiConverterNodeInfoINSTANCE = FfiConverterNodeInfo{}

func (c FfiConverterNodeInfo) Lift(rb RustBufferI) NodeInfo {
	return LiftFromRustBuffer[NodeInfo](c, rb)
}

func (c FfiConverterNodeInfo) Read(reader io.Reader) NodeInfo {
	return NodeInfo{
		FfiConverterSequenceUint64INSTANCE.Read(reader),
		FfiConverterOptionalNodeAnnouncementInfoINSTANCE.Read(reader),
	}
}

func (c FfiConverterNodeInfo) Lower(value NodeInfo) C.RustBuffer {
	return LowerIntoRustBuffer[NodeInfo](c, value)
}

func (c FfiConverterNodeInfo) LowerExternal(value NodeInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[NodeInfo](c, value))
}

func (c FfiConverterNodeInfo) Write(writer io.Writer, value NodeInfo) {
	FfiConverterSequenceUint64INSTANCE.Write(writer, value.Channels)
	FfiConverterOptionalNodeAnnouncementInfoINSTANCE.Write(writer, value.AnnouncementInfo)
}

type FfiDestroyerNodeInfo struct{}

func (_ FfiDestroyerNodeInfo) Destroy(value NodeInfo) {
	value.Destroy()
}

// Represents the status of the [`Node`].
type NodeStatus struct {
	// Indicates whether the [`Node`] is running.
	IsRunning bool
	// The best block to which our Lightning wallet is currently synced.
	CurrentBestBlock BestBlock
	// The timestamp, in seconds since start of the UNIX epoch, when we last successfully synced
	// our Lightning wallet to the chain tip.
	//
	// Will be `None` if the wallet hasn't been synced yet.
	LatestLightningWalletSyncTimestamp *uint64
	// The timestamp, in seconds since start of the UNIX epoch, when we last successfully synced
	// our on-chain wallet to the chain tip.
	//
	// Will be `None` if the wallet hasn't been synced yet.
	LatestOnchainWalletSyncTimestamp *uint64
	// The timestamp, in seconds since start of the UNIX epoch, when we last successfully update
	// our fee rate cache.
	//
	// Will be `None` if the cache hasn't been updated yet.
	LatestFeeRateCacheUpdateTimestamp *uint64
	// The timestamp, in seconds since start of the UNIX epoch, when the last rapid gossip sync
	// (RGS) snapshot we successfully applied was generated.
	//
	// Will be `None` if RGS isn't configured or the snapshot hasn't been updated yet.
	LatestRgsSnapshotTimestamp *uint64
	// The timestamp, in seconds since start of the UNIX epoch, when we last successfully merged external scores.
	LatestPathfindingScoresSyncTimestamp *uint64
	// The timestamp, in seconds since start of the UNIX epoch, when we last broadcasted a node
	// announcement.
	//
	// Will be `None` if we have no public channels or we haven't broadcasted yet.
	LatestNodeAnnouncementBroadcastTimestamp *uint64
}

func (r *NodeStatus) Destroy() {
	FfiDestroyerBool{}.Destroy(r.IsRunning)
	FfiDestroyerBestBlock{}.Destroy(r.CurrentBestBlock)
	FfiDestroyerOptionalUint64{}.Destroy(r.LatestLightningWalletSyncTimestamp)
	FfiDestroyerOptionalUint64{}.Destroy(r.LatestOnchainWalletSyncTimestamp)
	FfiDestroyerOptionalUint64{}.Destroy(r.LatestFeeRateCacheUpdateTimestamp)
	FfiDestroyerOptionalUint64{}.Destroy(r.LatestRgsSnapshotTimestamp)
	FfiDestroyerOptionalUint64{}.Destroy(r.LatestPathfindingScoresSyncTimestamp)
	FfiDestroyerOptionalUint64{}.Destroy(r.LatestNodeAnnouncementBroadcastTimestamp)
}

type FfiConverterNodeStatus struct{}

var FfiConverterNodeStatusINSTANCE = FfiConverterNodeStatus{}

func (c FfiConverterNodeStatus) Lift(rb RustBufferI) NodeStatus {
	return LiftFromRustBuffer[NodeStatus](c, rb)
}

func (c FfiConverterNodeStatus) Read(reader io.Reader) NodeStatus {
	return NodeStatus{
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBestBlockINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterNodeStatus) Lower(value NodeStatus) C.RustBuffer {
	return LowerIntoRustBuffer[NodeStatus](c, value)
}

func (c FfiConverterNodeStatus) LowerExternal(value NodeStatus) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[NodeStatus](c, value))
}

func (c FfiConverterNodeStatus) Write(writer io.Writer, value NodeStatus) {
	FfiConverterBoolINSTANCE.Write(writer, value.IsRunning)
	FfiConverterBestBlockINSTANCE.Write(writer, value.CurrentBestBlock)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.LatestLightningWalletSyncTimestamp)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.LatestOnchainWalletSyncTimestamp)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.LatestFeeRateCacheUpdateTimestamp)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.LatestRgsSnapshotTimestamp)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.LatestPathfindingScoresSyncTimestamp)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.LatestNodeAnnouncementBroadcastTimestamp)
}

type FfiDestroyerNodeStatus struct{}

func (_ FfiDestroyerNodeStatus) Destroy(value NodeStatus) {
	value.Destroy()
}

type OutPoint struct {
	Txid Txid
	Vout uint32
}

func (r *OutPoint) Destroy() {
	FfiDestroyerTypeTxid{}.Destroy(r.Txid)
	FfiDestroyerUint32{}.Destroy(r.Vout)
}

type FfiConverterOutPoint struct{}

var FfiConverterOutPointINSTANCE = FfiConverterOutPoint{}

func (c FfiConverterOutPoint) Lift(rb RustBufferI) OutPoint {
	return LiftFromRustBuffer[OutPoint](c, rb)
}

func (c FfiConverterOutPoint) Read(reader io.Reader) OutPoint {
	return OutPoint{
		FfiConverterTypeTxidINSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
	}
}

func (c FfiConverterOutPoint) Lower(value OutPoint) C.RustBuffer {
	return LowerIntoRustBuffer[OutPoint](c, value)
}

func (c FfiConverterOutPoint) LowerExternal(value OutPoint) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[OutPoint](c, value))
}

func (c FfiConverterOutPoint) Write(writer io.Writer, value OutPoint) {
	FfiConverterTypeTxidINSTANCE.Write(writer, value.Txid)
	FfiConverterUint32INSTANCE.Write(writer, value.Vout)
}

type FfiDestroyerOutPoint struct{}

func (_ FfiDestroyerOutPoint) Destroy(value OutPoint) {
	value.Destroy()
}

// Represents a payment.
type PaymentDetails struct {
	// The identifier of this payment.
	Id PaymentId
	// The kind of the payment.
	Kind PaymentKind
	// The amount transferred.
	//
	// Will be `None` for variable-amount payments until we receive them.
	AmountMsat *uint64
	// The fees that were paid for this payment.
	//
	// For Lightning payments, this will only be updated for outbound payments once they
	// succeeded.
	//
	// Will be `None` for Lightning payments made with LDK Node v0.4.x and earlier.
	FeePaidMsat *uint64
	// The direction of the payment.
	Direction PaymentDirection
	// The status of the payment.
	Status PaymentStatus
	// The timestamp, in seconds since start of the UNIX epoch, when this entry was last updated.
	LatestUpdateTimestamp uint64
}

func (r *PaymentDetails) Destroy() {
	FfiDestroyerTypePaymentId{}.Destroy(r.Id)
	FfiDestroyerPaymentKind{}.Destroy(r.Kind)
	FfiDestroyerOptionalUint64{}.Destroy(r.AmountMsat)
	FfiDestroyerOptionalUint64{}.Destroy(r.FeePaidMsat)
	FfiDestroyerPaymentDirection{}.Destroy(r.Direction)
	FfiDestroyerPaymentStatus{}.Destroy(r.Status)
	FfiDestroyerUint64{}.Destroy(r.LatestUpdateTimestamp)
}

type FfiConverterPaymentDetails struct{}

var FfiConverterPaymentDetailsINSTANCE = FfiConverterPaymentDetails{}

func (c FfiConverterPaymentDetails) Lift(rb RustBufferI) PaymentDetails {
	return LiftFromRustBuffer[PaymentDetails](c, rb)
}

func (c FfiConverterPaymentDetails) Read(reader io.Reader) PaymentDetails {
	return PaymentDetails{
		FfiConverterTypePaymentIdINSTANCE.Read(reader),
		FfiConverterPaymentKindINSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterPaymentDirectionINSTANCE.Read(reader),
		FfiConverterPaymentStatusINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterPaymentDetails) Lower(value PaymentDetails) C.RustBuffer {
	return LowerIntoRustBuffer[PaymentDetails](c, value)
}

func (c FfiConverterPaymentDetails) LowerExternal(value PaymentDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PaymentDetails](c, value))
}

func (c FfiConverterPaymentDetails) Write(writer io.Writer, value PaymentDetails) {
	FfiConverterTypePaymentIdINSTANCE.Write(writer, value.Id)
	FfiConverterPaymentKindINSTANCE.Write(writer, value.Kind)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.AmountMsat)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.FeePaidMsat)
	FfiConverterPaymentDirectionINSTANCE.Write(writer, value.Direction)
	FfiConverterPaymentStatusINSTANCE.Write(writer, value.Status)
	FfiConverterUint64INSTANCE.Write(writer, value.LatestUpdateTimestamp)
}

type FfiDestroyerPaymentDetails struct{}

func (_ FfiDestroyerPaymentDetails) Destroy(value PaymentDetails) {
	value.Destroy()
}

// Details of a known Lightning peer as returned by [`Node::list_peers`].
//
// [`Node::list_peers`]: crate::Node::list_peers
type PeerDetails struct {
	// The node ID of the peer.
	NodeId PublicKey
	// The network address of the peer.
	Address SocketAddress
	// Indicates whether we'll try to reconnect to this peer after restarts.
	IsPersisted bool
	// Indicates whether we currently have an active connection with the peer.
	IsConnected bool
}

func (r *PeerDetails) Destroy() {
	FfiDestroyerTypePublicKey{}.Destroy(r.NodeId)
	FfiDestroyerTypeSocketAddress{}.Destroy(r.Address)
	FfiDestroyerBool{}.Destroy(r.IsPersisted)
	FfiDestroyerBool{}.Destroy(r.IsConnected)
}

type FfiConverterPeerDetails struct{}

var FfiConverterPeerDetailsINSTANCE = FfiConverterPeerDetails{}

func (c FfiConverterPeerDetails) Lift(rb RustBufferI) PeerDetails {
	return LiftFromRustBuffer[PeerDetails](c, rb)
}

func (c FfiConverterPeerDetails) Read(reader io.Reader) PeerDetails {
	return PeerDetails{
		FfiConverterTypePublicKeyINSTANCE.Read(reader),
		FfiConverterTypeSocketAddressINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
		FfiConverterBoolINSTANCE.Read(reader),
	}
}

func (c FfiConverterPeerDetails) Lower(value PeerDetails) C.RustBuffer {
	return LowerIntoRustBuffer[PeerDetails](c, value)
}

func (c FfiConverterPeerDetails) LowerExternal(value PeerDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PeerDetails](c, value))
}

func (c FfiConverterPeerDetails) Write(writer io.Writer, value PeerDetails) {
	FfiConverterTypePublicKeyINSTANCE.Write(writer, value.NodeId)
	FfiConverterTypeSocketAddressINSTANCE.Write(writer, value.Address)
	FfiConverterBoolINSTANCE.Write(writer, value.IsPersisted)
	FfiConverterBoolINSTANCE.Write(writer, value.IsConnected)
}

type FfiDestroyerPeerDetails struct{}

func (_ FfiDestroyerPeerDetails) Destroy(value PeerDetails) {
	value.Destroy()
}

// A channel descriptor for a hop along a payment path.
//
// While this generally comes from BOLT 11's `r` field, this struct includes more fields than are
// available in BOLT 11.
type RouteHintHop struct {
	// The node_id of the non-target end of the route
	SrcNodeId PublicKey
	// The short_channel_id of this channel
	ShortChannelId uint64
	// The fees which must be paid to use this channel
	Fees RoutingFees
	// The difference in CLTV values between this node and the next node.
	CltvExpiryDelta uint16
	// The minimum value, in msat, which must be relayed to the next hop.
	HtlcMinimumMsat *uint64
	// The maximum value in msat available for routing with a single HTLC.
	HtlcMaximumMsat *uint64
}

func (r *RouteHintHop) Destroy() {
	FfiDestroyerTypePublicKey{}.Destroy(r.SrcNodeId)
	FfiDestroyerUint64{}.Destroy(r.ShortChannelId)
	FfiDestroyerRoutingFees{}.Destroy(r.Fees)
	FfiDestroyerUint16{}.Destroy(r.CltvExpiryDelta)
	FfiDestroyerOptionalUint64{}.Destroy(r.HtlcMinimumMsat)
	FfiDestroyerOptionalUint64{}.Destroy(r.HtlcMaximumMsat)
}

type FfiConverterRouteHintHop struct{}

var FfiConverterRouteHintHopINSTANCE = FfiConverterRouteHintHop{}

func (c FfiConverterRouteHintHop) Lift(rb RustBufferI) RouteHintHop {
	return LiftFromRustBuffer[RouteHintHop](c, rb)
}

func (c FfiConverterRouteHintHop) Read(reader io.Reader) RouteHintHop {
	return RouteHintHop{
		FfiConverterTypePublicKeyINSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterRoutingFeesINSTANCE.Read(reader),
		FfiConverterUint16INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterOptionalUint64INSTANCE.Read(reader),
	}
}

func (c FfiConverterRouteHintHop) Lower(value RouteHintHop) C.RustBuffer {
	return LowerIntoRustBuffer[RouteHintHop](c, value)
}

func (c FfiConverterRouteHintHop) LowerExternal(value RouteHintHop) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[RouteHintHop](c, value))
}

func (c FfiConverterRouteHintHop) Write(writer io.Writer, value RouteHintHop) {
	FfiConverterTypePublicKeyINSTANCE.Write(writer, value.SrcNodeId)
	FfiConverterUint64INSTANCE.Write(writer, value.ShortChannelId)
	FfiConverterRoutingFeesINSTANCE.Write(writer, value.Fees)
	FfiConverterUint16INSTANCE.Write(writer, value.CltvExpiryDelta)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.HtlcMinimumMsat)
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.HtlcMaximumMsat)
}

type FfiDestroyerRouteHintHop struct{}

func (_ FfiDestroyerRouteHintHop) Destroy(value RouteHintHop) {
	value.Destroy()
}

type RouteParametersConfig struct {
	MaxTotalRoutingFeeMsat          *uint64
	MaxTotalCltvExpiryDelta         uint32
	MaxPathCount                    uint8
	MaxChannelSaturationPowerOfHalf uint8
}

func (r *RouteParametersConfig) Destroy() {
	FfiDestroyerOptionalUint64{}.Destroy(r.MaxTotalRoutingFeeMsat)
	FfiDestroyerUint32{}.Destroy(r.MaxTotalCltvExpiryDelta)
	FfiDestroyerUint8{}.Destroy(r.MaxPathCount)
	FfiDestroyerUint8{}.Destroy(r.MaxChannelSaturationPowerOfHalf)
}

type FfiConverterRouteParametersConfig struct{}

var FfiConverterRouteParametersConfigINSTANCE = FfiConverterRouteParametersConfig{}

func (c FfiConverterRouteParametersConfig) Lift(rb RustBufferI) RouteParametersConfig {
	return LiftFromRustBuffer[RouteParametersConfig](c, rb)
}

func (c FfiConverterRouteParametersConfig) Read(reader io.Reader) RouteParametersConfig {
	return RouteParametersConfig{
		FfiConverterOptionalUint64INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint8INSTANCE.Read(reader),
		FfiConverterUint8INSTANCE.Read(reader),
	}
}

func (c FfiConverterRouteParametersConfig) Lower(value RouteParametersConfig) C.RustBuffer {
	return LowerIntoRustBuffer[RouteParametersConfig](c, value)
}

func (c FfiConverterRouteParametersConfig) LowerExternal(value RouteParametersConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[RouteParametersConfig](c, value))
}

func (c FfiConverterRouteParametersConfig) Write(writer io.Writer, value RouteParametersConfig) {
	FfiConverterOptionalUint64INSTANCE.Write(writer, value.MaxTotalRoutingFeeMsat)
	FfiConverterUint32INSTANCE.Write(writer, value.MaxTotalCltvExpiryDelta)
	FfiConverterUint8INSTANCE.Write(writer, value.MaxPathCount)
	FfiConverterUint8INSTANCE.Write(writer, value.MaxChannelSaturationPowerOfHalf)
}

type FfiDestroyerRouteParametersConfig struct{}

func (_ FfiDestroyerRouteParametersConfig) Destroy(value RouteParametersConfig) {
	value.Destroy()
}

type RoutingFees struct {
	BaseMsat               uint32
	ProportionalMillionths uint32
}

func (r *RoutingFees) Destroy() {
	FfiDestroyerUint32{}.Destroy(r.BaseMsat)
	FfiDestroyerUint32{}.Destroy(r.ProportionalMillionths)
}

type FfiConverterRoutingFees struct{}

var FfiConverterRoutingFeesINSTANCE = FfiConverterRoutingFees{}

func (c FfiConverterRoutingFees) Lift(rb RustBufferI) RoutingFees {
	return LiftFromRustBuffer[RoutingFees](c, rb)
}

func (c FfiConverterRoutingFees) Read(reader io.Reader) RoutingFees {
	return RoutingFees{
		FfiConverterUint32INSTANCE.Read(reader),
		FfiConverterUint32INSTANCE.Read(reader),
	}
}

func (c FfiConverterRoutingFees) Lower(value RoutingFees) C.RustBuffer {
	return LowerIntoRustBuffer[RoutingFees](c, value)
}

func (c FfiConverterRoutingFees) LowerExternal(value RoutingFees) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[RoutingFees](c, value))
}

func (c FfiConverterRoutingFees) Write(writer io.Writer, value RoutingFees) {
	FfiConverterUint32INSTANCE.Write(writer, value.BaseMsat)
	FfiConverterUint32INSTANCE.Write(writer, value.ProportionalMillionths)
}

type FfiDestroyerRoutingFees struct{}

func (_ FfiDestroyerRoutingFees) Destroy(value RoutingFees) {
	value.Destroy()
}

// Timeout-related parameters for syncing the Lightning and on-chain wallets.
//
// ### Defaults
//
// | Parameter                              | Value              |
// |----------------------------------------|--------------------|
// | `onchain_wallet_sync_timeout_secs`     | 60                 |
// | `lightning_wallet_sync_timeout_secs`   | 30                 |
// | `fee_rate_cache_update_timeout_secs`   | 10                 |
// | `tx_broadcast_timeout_secs`            | 10                 |
// | `per_request_timeout_secs`             | 10                 |
type SyncTimeoutsConfig struct {
	// The timeout after which we abort syncing the onchain wallet.
	OnchainWalletSyncTimeoutSecs uint64
	// The timeout after which we abort syncing the LDK wallet.
	LightningWalletSyncTimeoutSecs uint64
	// The timeout after which we abort updating the fee rate cache.
	FeeRateCacheUpdateTimeoutSecs uint64
	// The timeout after which we abort broadcasting a transaction.
	TxBroadcastTimeoutSecs uint64
	// The per-request timeout after which we abort a single Electrum or Esplora API request.
	PerRequestTimeoutSecs uint8
}

func (r *SyncTimeoutsConfig) Destroy() {
	FfiDestroyerUint64{}.Destroy(r.OnchainWalletSyncTimeoutSecs)
	FfiDestroyerUint64{}.Destroy(r.LightningWalletSyncTimeoutSecs)
	FfiDestroyerUint64{}.Destroy(r.FeeRateCacheUpdateTimeoutSecs)
	FfiDestroyerUint64{}.Destroy(r.TxBroadcastTimeoutSecs)
	FfiDestroyerUint8{}.Destroy(r.PerRequestTimeoutSecs)
}

type FfiConverterSyncTimeoutsConfig struct{}

var FfiConverterSyncTimeoutsConfigINSTANCE = FfiConverterSyncTimeoutsConfig{}

func (c FfiConverterSyncTimeoutsConfig) Lift(rb RustBufferI) SyncTimeoutsConfig {
	return LiftFromRustBuffer[SyncTimeoutsConfig](c, rb)
}

func (c FfiConverterSyncTimeoutsConfig) Read(reader io.Reader) SyncTimeoutsConfig {
	return SyncTimeoutsConfig{
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint64INSTANCE.Read(reader),
		FfiConverterUint8INSTANCE.Read(reader),
	}
}

func (c FfiConverterSyncTimeoutsConfig) Lower(value SyncTimeoutsConfig) C.RustBuffer {
	return LowerIntoRustBuffer[SyncTimeoutsConfig](c, value)
}

func (c FfiConverterSyncTimeoutsConfig) LowerExternal(value SyncTimeoutsConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[SyncTimeoutsConfig](c, value))
}

func (c FfiConverterSyncTimeoutsConfig) Write(writer io.Writer, value SyncTimeoutsConfig) {
	FfiConverterUint64INSTANCE.Write(writer, value.OnchainWalletSyncTimeoutSecs)
	FfiConverterUint64INSTANCE.Write(writer, value.LightningWalletSyncTimeoutSecs)
	FfiConverterUint64INSTANCE.Write(writer, value.FeeRateCacheUpdateTimeoutSecs)
	FfiConverterUint64INSTANCE.Write(writer, value.TxBroadcastTimeoutSecs)
	FfiConverterUint8INSTANCE.Write(writer, value.PerRequestTimeoutSecs)
}

type FfiDestroyerSyncTimeoutsConfig struct{}

func (_ FfiDestroyerSyncTimeoutsConfig) Destroy(value SyncTimeoutsConfig) {
	value.Destroy()
}

// Configuration for connecting to peers via the Tor Network.
type TorConfig struct {
	// Tor daemon SOCKS proxy address. Only connections to OnionV3 peers will be made
	// via this proxy; other connections (IPv4 peers, Electrum server) will not be
	// routed over Tor.
	ProxyAddress SocketAddress
}

func (r *TorConfig) Destroy() {
	FfiDestroyerTypeSocketAddress{}.Destroy(r.ProxyAddress)
}

type FfiConverterTorConfig struct{}

var FfiConverterTorConfigINSTANCE = FfiConverterTorConfig{}

func (c FfiConverterTorConfig) Lift(rb RustBufferI) TorConfig {
	return LiftFromRustBuffer[TorConfig](c, rb)
}

func (c FfiConverterTorConfig) Read(reader io.Reader) TorConfig {
	return TorConfig{
		FfiConverterTypeSocketAddressINSTANCE.Read(reader),
	}
}

func (c FfiConverterTorConfig) Lower(value TorConfig) C.RustBuffer {
	return LowerIntoRustBuffer[TorConfig](c, value)
}

func (c FfiConverterTorConfig) LowerExternal(value TorConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[TorConfig](c, value))
}

func (c FfiConverterTorConfig) Write(writer io.Writer, value TorConfig) {
	FfiConverterTypeSocketAddressINSTANCE.Write(writer, value.ProxyAddress)
}

type FfiDestroyerTorConfig struct{}

func (_ FfiDestroyerTorConfig) Destroy(value TorConfig) {
	value.Destroy()
}

// The role of the node in an asynchronous payments context.
//
// See <https://github.com/lightning/bolts/pull/1149> for more information about the async payments protocol.
type AsyncPaymentsRole uint

const (
	// Node acts a client in an async payments context. This means that if possible, it will instruct its peers to hold
	// HTLCs for it, so that it can go offline.
	AsyncPaymentsRoleClient AsyncPaymentsRole = 1
	// Node acts as a server in an async payments context. This means that it will hold async payments HTLCs and onion
	// messages for its peers.
	AsyncPaymentsRoleServer AsyncPaymentsRole = 2
)

type FfiConverterAsyncPaymentsRole struct{}

var FfiConverterAsyncPaymentsRoleINSTANCE = FfiConverterAsyncPaymentsRole{}

func (c FfiConverterAsyncPaymentsRole) Lift(rb RustBufferI) AsyncPaymentsRole {
	return LiftFromRustBuffer[AsyncPaymentsRole](c, rb)
}

func (c FfiConverterAsyncPaymentsRole) Lower(value AsyncPaymentsRole) C.RustBuffer {
	return LowerIntoRustBuffer[AsyncPaymentsRole](c, value)
}

func (c FfiConverterAsyncPaymentsRole) LowerExternal(value AsyncPaymentsRole) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[AsyncPaymentsRole](c, value))
}
func (FfiConverterAsyncPaymentsRole) Read(reader io.Reader) AsyncPaymentsRole {
	id := readInt32(reader)
	return AsyncPaymentsRole(id)
}

func (FfiConverterAsyncPaymentsRole) Write(writer io.Writer, value AsyncPaymentsRole) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerAsyncPaymentsRole struct{}

func (_ FfiDestroyerAsyncPaymentsRole) Destroy(value AsyncPaymentsRole) {
}

type BalanceSource uint

const (
	BalanceSourceHolderForceClosed       BalanceSource = 1
	BalanceSourceCounterpartyForceClosed BalanceSource = 2
	BalanceSourceCoopClose               BalanceSource = 3
	BalanceSourceHtlc                    BalanceSource = 4
)

type FfiConverterBalanceSource struct{}

var FfiConverterBalanceSourceINSTANCE = FfiConverterBalanceSource{}

func (c FfiConverterBalanceSource) Lift(rb RustBufferI) BalanceSource {
	return LiftFromRustBuffer[BalanceSource](c, rb)
}

func (c FfiConverterBalanceSource) Lower(value BalanceSource) C.RustBuffer {
	return LowerIntoRustBuffer[BalanceSource](c, value)
}

func (c FfiConverterBalanceSource) LowerExternal(value BalanceSource) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[BalanceSource](c, value))
}
func (FfiConverterBalanceSource) Read(reader io.Reader) BalanceSource {
	id := readInt32(reader)
	return BalanceSource(id)
}

func (FfiConverterBalanceSource) Write(writer io.Writer, value BalanceSource) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerBalanceSource struct{}

func (_ FfiDestroyerBalanceSource) Destroy(value BalanceSource) {
}

// Represents the description of an invoice which has to be either a directly included string or
// a hash of a description provided out of band.
type Bolt11InvoiceDescription interface {
	Destroy()
}

// Contains a full description.
type Bolt11InvoiceDescriptionDirect struct {
	Description string
}

func (e Bolt11InvoiceDescriptionDirect) Destroy() {
	FfiDestroyerString{}.Destroy(e.Description)
}

// Contains a hash.
type Bolt11InvoiceDescriptionHash struct {
	Hash string
}

func (e Bolt11InvoiceDescriptionHash) Destroy() {
	FfiDestroyerString{}.Destroy(e.Hash)
}

type FfiConverterBolt11InvoiceDescription struct{}

var FfiConverterBolt11InvoiceDescriptionINSTANCE = FfiConverterBolt11InvoiceDescription{}

func (c FfiConverterBolt11InvoiceDescription) Lift(rb RustBufferI) Bolt11InvoiceDescription {
	return LiftFromRustBuffer[Bolt11InvoiceDescription](c, rb)
}

func (c FfiConverterBolt11InvoiceDescription) Lower(value Bolt11InvoiceDescription) C.RustBuffer {
	return LowerIntoRustBuffer[Bolt11InvoiceDescription](c, value)
}

func (c FfiConverterBolt11InvoiceDescription) LowerExternal(value Bolt11InvoiceDescription) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Bolt11InvoiceDescription](c, value))
}
func (FfiConverterBolt11InvoiceDescription) Read(reader io.Reader) Bolt11InvoiceDescription {
	id := readInt32(reader)
	switch id {
	case 1:
		return Bolt11InvoiceDescriptionDirect{
			FfiConverterStringINSTANCE.Read(reader),
		}
	case 2:
		return Bolt11InvoiceDescriptionHash{
			FfiConverterStringINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterBolt11InvoiceDescription.Read()", id))
	}
}

func (FfiConverterBolt11InvoiceDescription) Write(writer io.Writer, value Bolt11InvoiceDescription) {
	switch variant_value := value.(type) {
	case Bolt11InvoiceDescriptionDirect:
		writeInt32(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Description)
	case Bolt11InvoiceDescriptionHash:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Hash)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterBolt11InvoiceDescription.Write", value))
	}
}

type FfiDestroyerBolt11InvoiceDescription struct{}

func (_ FfiDestroyerBolt11InvoiceDescription) Destroy(value Bolt11InvoiceDescription) {
	value.Destroy()
}

// An error encountered during building a [`Node`].
//
// [`Node`]: crate::Node
type BuildError struct {
	err error
}

// Convience method to turn *BuildError into error
// Avoiding treating nil pointer as non nil error interface
func (err *BuildError) AsError() error {
	if err == nil {
		return nil
	} else {
		return err
	}
}

func (err BuildError) Error() string {
	return fmt.Sprintf("BuildError: %s", err.err.Error())
}

func (err BuildError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var ErrBuildErrorInvalidSystemTime = fmt.Errorf("BuildErrorInvalidSystemTime")
var ErrBuildErrorInvalidChannelMonitor = fmt.Errorf("BuildErrorInvalidChannelMonitor")
var ErrBuildErrorInvalidListeningAddresses = fmt.Errorf("BuildErrorInvalidListeningAddresses")
var ErrBuildErrorInvalidAnnouncementAddresses = fmt.Errorf("BuildErrorInvalidAnnouncementAddresses")
var ErrBuildErrorInvalidTorProxyAddress = fmt.Errorf("BuildErrorInvalidTorProxyAddress")
var ErrBuildErrorInvalidNodeAlias = fmt.Errorf("BuildErrorInvalidNodeAlias")
var ErrBuildErrorRuntimeSetupFailed = fmt.Errorf("BuildErrorRuntimeSetupFailed")
var ErrBuildErrorReadFailed = fmt.Errorf("BuildErrorReadFailed")
var ErrBuildErrorWriteFailed = fmt.Errorf("BuildErrorWriteFailed")
var ErrBuildErrorStoragePathAccessFailed = fmt.Errorf("BuildErrorStoragePathAccessFailed")
var ErrBuildErrorKvStoreSetupFailed = fmt.Errorf("BuildErrorKvStoreSetupFailed")
var ErrBuildErrorWalletSetupFailed = fmt.Errorf("BuildErrorWalletSetupFailed")
var ErrBuildErrorLoggerSetupFailed = fmt.Errorf("BuildErrorLoggerSetupFailed")
var ErrBuildErrorNetworkMismatch = fmt.Errorf("BuildErrorNetworkMismatch")
var ErrBuildErrorAsyncPaymentsConfigMismatch = fmt.Errorf("BuildErrorAsyncPaymentsConfigMismatch")

// Variant structs
// The current system time is invalid, clocks might have gone backwards.
type BuildErrorInvalidSystemTime struct {
}

// The current system time is invalid, clocks might have gone backwards.
func NewBuildErrorInvalidSystemTime() *BuildError {
	return &BuildError{err: &BuildErrorInvalidSystemTime{}}
}

func (e BuildErrorInvalidSystemTime) destroy() {
}

func (err BuildErrorInvalidSystemTime) Error() string {
	return fmt.Sprint("InvalidSystemTime")
}

func (self BuildErrorInvalidSystemTime) Is(target error) bool {
	return target == ErrBuildErrorInvalidSystemTime
}

// The a read channel monitor is invalid.
type BuildErrorInvalidChannelMonitor struct {
}

// The a read channel monitor is invalid.
func NewBuildErrorInvalidChannelMonitor() *BuildError {
	return &BuildError{err: &BuildErrorInvalidChannelMonitor{}}
}

func (e BuildErrorInvalidChannelMonitor) destroy() {
}

func (err BuildErrorInvalidChannelMonitor) Error() string {
	return fmt.Sprint("InvalidChannelMonitor")
}

func (self BuildErrorInvalidChannelMonitor) Is(target error) bool {
	return target == ErrBuildErrorInvalidChannelMonitor
}

// The given listening addresses are invalid, e.g. too many were passed.
type BuildErrorInvalidListeningAddresses struct {
}

// The given listening addresses are invalid, e.g. too many were passed.
func NewBuildErrorInvalidListeningAddresses() *BuildError {
	return &BuildError{err: &BuildErrorInvalidListeningAddresses{}}
}

func (e BuildErrorInvalidListeningAddresses) destroy() {
}

func (err BuildErrorInvalidListeningAddresses) Error() string {
	return fmt.Sprint("InvalidListeningAddresses")
}

func (self BuildErrorInvalidListeningAddresses) Is(target error) bool {
	return target == ErrBuildErrorInvalidListeningAddresses
}

// The given announcement addresses are invalid, e.g. too many were passed.
type BuildErrorInvalidAnnouncementAddresses struct {
}

// The given announcement addresses are invalid, e.g. too many were passed.
func NewBuildErrorInvalidAnnouncementAddresses() *BuildError {
	return &BuildError{err: &BuildErrorInvalidAnnouncementAddresses{}}
}

func (e BuildErrorInvalidAnnouncementAddresses) destroy() {
}

func (err BuildErrorInvalidAnnouncementAddresses) Error() string {
	return fmt.Sprint("InvalidAnnouncementAddresses")
}

func (self BuildErrorInvalidAnnouncementAddresses) Is(target error) bool {
	return target == ErrBuildErrorInvalidAnnouncementAddresses
}

// The given tor proxy address is invalid, e.g. an onion address was passed.
type BuildErrorInvalidTorProxyAddress struct {
}

// The given tor proxy address is invalid, e.g. an onion address was passed.
func NewBuildErrorInvalidTorProxyAddress() *BuildError {
	return &BuildError{err: &BuildErrorInvalidTorProxyAddress{}}
}

func (e BuildErrorInvalidTorProxyAddress) destroy() {
}

func (err BuildErrorInvalidTorProxyAddress) Error() string {
	return fmt.Sprint("InvalidTorProxyAddress")
}

func (self BuildErrorInvalidTorProxyAddress) Is(target error) bool {
	return target == ErrBuildErrorInvalidTorProxyAddress
}

// The provided alias is invalid.
type BuildErrorInvalidNodeAlias struct {
}

// The provided alias is invalid.
func NewBuildErrorInvalidNodeAlias() *BuildError {
	return &BuildError{err: &BuildErrorInvalidNodeAlias{}}
}

func (e BuildErrorInvalidNodeAlias) destroy() {
}

func (err BuildErrorInvalidNodeAlias) Error() string {
	return fmt.Sprint("InvalidNodeAlias")
}

func (self BuildErrorInvalidNodeAlias) Is(target error) bool {
	return target == ErrBuildErrorInvalidNodeAlias
}

// An attempt to setup a runtime has failed.
type BuildErrorRuntimeSetupFailed struct {
}

// An attempt to setup a runtime has failed.
func NewBuildErrorRuntimeSetupFailed() *BuildError {
	return &BuildError{err: &BuildErrorRuntimeSetupFailed{}}
}

func (e BuildErrorRuntimeSetupFailed) destroy() {
}

func (err BuildErrorRuntimeSetupFailed) Error() string {
	return fmt.Sprint("RuntimeSetupFailed")
}

func (self BuildErrorRuntimeSetupFailed) Is(target error) bool {
	return target == ErrBuildErrorRuntimeSetupFailed
}

// We failed to read data from the [`KVStore`].
//
// [`KVStore`]: lightning::util::persist::KVStoreSync
type BuildErrorReadFailed struct {
}

// We failed to read data from the [`KVStore`].
//
// [`KVStore`]: lightning::util::persist::KVStoreSync
func NewBuildErrorReadFailed() *BuildError {
	return &BuildError{err: &BuildErrorReadFailed{}}
}

func (e BuildErrorReadFailed) destroy() {
}

func (err BuildErrorReadFailed) Error() string {
	return fmt.Sprint("ReadFailed")
}

func (self BuildErrorReadFailed) Is(target error) bool {
	return target == ErrBuildErrorReadFailed
}

// We failed to write data to the [`KVStore`].
//
// [`KVStore`]: lightning::util::persist::KVStoreSync
type BuildErrorWriteFailed struct {
}

// We failed to write data to the [`KVStore`].
//
// [`KVStore`]: lightning::util::persist::KVStoreSync
func NewBuildErrorWriteFailed() *BuildError {
	return &BuildError{err: &BuildErrorWriteFailed{}}
}

func (e BuildErrorWriteFailed) destroy() {
}

func (err BuildErrorWriteFailed) Error() string {
	return fmt.Sprint("WriteFailed")
}

func (self BuildErrorWriteFailed) Is(target error) bool {
	return target == ErrBuildErrorWriteFailed
}

// We failed to access the given `storage_dir_path`.
type BuildErrorStoragePathAccessFailed struct {
}

// We failed to access the given `storage_dir_path`.
func NewBuildErrorStoragePathAccessFailed() *BuildError {
	return &BuildError{err: &BuildErrorStoragePathAccessFailed{}}
}

func (e BuildErrorStoragePathAccessFailed) destroy() {
}

func (err BuildErrorStoragePathAccessFailed) Error() string {
	return fmt.Sprint("StoragePathAccessFailed")
}

func (self BuildErrorStoragePathAccessFailed) Is(target error) bool {
	return target == ErrBuildErrorStoragePathAccessFailed
}

// We failed to setup our [`KVStore`].
//
// [`KVStore`]: lightning::util::persist::KVStoreSync
type BuildErrorKvStoreSetupFailed struct {
}

// We failed to setup our [`KVStore`].
//
// [`KVStore`]: lightning::util::persist::KVStoreSync
func NewBuildErrorKvStoreSetupFailed() *BuildError {
	return &BuildError{err: &BuildErrorKvStoreSetupFailed{}}
}

func (e BuildErrorKvStoreSetupFailed) destroy() {
}

func (err BuildErrorKvStoreSetupFailed) Error() string {
	return fmt.Sprint("KvStoreSetupFailed")
}

func (self BuildErrorKvStoreSetupFailed) Is(target error) bool {
	return target == ErrBuildErrorKvStoreSetupFailed
}

// We failed to setup the onchain wallet.
type BuildErrorWalletSetupFailed struct {
}

// We failed to setup the onchain wallet.
func NewBuildErrorWalletSetupFailed() *BuildError {
	return &BuildError{err: &BuildErrorWalletSetupFailed{}}
}

func (e BuildErrorWalletSetupFailed) destroy() {
}

func (err BuildErrorWalletSetupFailed) Error() string {
	return fmt.Sprint("WalletSetupFailed")
}

func (self BuildErrorWalletSetupFailed) Is(target error) bool {
	return target == ErrBuildErrorWalletSetupFailed
}

// We failed to setup the logger.
type BuildErrorLoggerSetupFailed struct {
}

// We failed to setup the logger.
func NewBuildErrorLoggerSetupFailed() *BuildError {
	return &BuildError{err: &BuildErrorLoggerSetupFailed{}}
}

func (e BuildErrorLoggerSetupFailed) destroy() {
}

func (err BuildErrorLoggerSetupFailed) Error() string {
	return fmt.Sprint("LoggerSetupFailed")
}

func (self BuildErrorLoggerSetupFailed) Is(target error) bool {
	return target == ErrBuildErrorLoggerSetupFailed
}

// The given network does not match the node's previously configured network.
type BuildErrorNetworkMismatch struct {
}

// The given network does not match the node's previously configured network.
func NewBuildErrorNetworkMismatch() *BuildError {
	return &BuildError{err: &BuildErrorNetworkMismatch{}}
}

func (e BuildErrorNetworkMismatch) destroy() {
}

func (err BuildErrorNetworkMismatch) Error() string {
	return fmt.Sprint("NetworkMismatch")
}

func (self BuildErrorNetworkMismatch) Is(target error) bool {
	return target == ErrBuildErrorNetworkMismatch
}

// The role of the node in an asynchronous payments context is not compatible with the current configuration.
type BuildErrorAsyncPaymentsConfigMismatch struct {
}

// The role of the node in an asynchronous payments context is not compatible with the current configuration.
func NewBuildErrorAsyncPaymentsConfigMismatch() *BuildError {
	return &BuildError{err: &BuildErrorAsyncPaymentsConfigMismatch{}}
}

func (e BuildErrorAsyncPaymentsConfigMismatch) destroy() {
}

func (err BuildErrorAsyncPaymentsConfigMismatch) Error() string {
	return fmt.Sprint("AsyncPaymentsConfigMismatch")
}

func (self BuildErrorAsyncPaymentsConfigMismatch) Is(target error) bool {
	return target == ErrBuildErrorAsyncPaymentsConfigMismatch
}

type FfiConverterBuildError struct{}

var FfiConverterBuildErrorINSTANCE = FfiConverterBuildError{}

func (c FfiConverterBuildError) Lift(eb RustBufferI) *BuildError {
	return LiftFromRustBuffer[*BuildError](c, eb)
}

func (c FfiConverterBuildError) Lower(value *BuildError) C.RustBuffer {
	return LowerIntoRustBuffer[*BuildError](c, value)
}

func (c FfiConverterBuildError) LowerExternal(value *BuildError) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*BuildError](c, value))
}

func (c FfiConverterBuildError) Read(reader io.Reader) *BuildError {
	errorID := readUint32(reader)

	switch errorID {
	case 1:
		return &BuildError{&BuildErrorInvalidSystemTime{}}
	case 2:
		return &BuildError{&BuildErrorInvalidChannelMonitor{}}
	case 3:
		return &BuildError{&BuildErrorInvalidListeningAddresses{}}
	case 4:
		return &BuildError{&BuildErrorInvalidAnnouncementAddresses{}}
	case 5:
		return &BuildError{&BuildErrorInvalidTorProxyAddress{}}
	case 6:
		return &BuildError{&BuildErrorInvalidNodeAlias{}}
	case 7:
		return &BuildError{&BuildErrorRuntimeSetupFailed{}}
	case 8:
		return &BuildError{&BuildErrorReadFailed{}}
	case 9:
		return &BuildError{&BuildErrorWriteFailed{}}
	case 10:
		return &BuildError{&BuildErrorStoragePathAccessFailed{}}
	case 11:
		return &BuildError{&BuildErrorKvStoreSetupFailed{}}
	case 12:
		return &BuildError{&BuildErrorWalletSetupFailed{}}
	case 13:
		return &BuildError{&BuildErrorLoggerSetupFailed{}}
	case 14:
		return &BuildError{&BuildErrorNetworkMismatch{}}
	case 15:
		return &BuildError{&BuildErrorAsyncPaymentsConfigMismatch{}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterBuildError.Read()", errorID))
	}
}

func (c FfiConverterBuildError) Write(writer io.Writer, value *BuildError) {
	switch variantValue := value.err.(type) {
	case *BuildErrorInvalidSystemTime:
		writeInt32(writer, 1)
	case *BuildErrorInvalidChannelMonitor:
		writeInt32(writer, 2)
	case *BuildErrorInvalidListeningAddresses:
		writeInt32(writer, 3)
	case *BuildErrorInvalidAnnouncementAddresses:
		writeInt32(writer, 4)
	case *BuildErrorInvalidTorProxyAddress:
		writeInt32(writer, 5)
	case *BuildErrorInvalidNodeAlias:
		writeInt32(writer, 6)
	case *BuildErrorRuntimeSetupFailed:
		writeInt32(writer, 7)
	case *BuildErrorReadFailed:
		writeInt32(writer, 8)
	case *BuildErrorWriteFailed:
		writeInt32(writer, 9)
	case *BuildErrorStoragePathAccessFailed:
		writeInt32(writer, 10)
	case *BuildErrorKvStoreSetupFailed:
		writeInt32(writer, 11)
	case *BuildErrorWalletSetupFailed:
		writeInt32(writer, 12)
	case *BuildErrorLoggerSetupFailed:
		writeInt32(writer, 13)
	case *BuildErrorNetworkMismatch:
		writeInt32(writer, 14)
	case *BuildErrorAsyncPaymentsConfigMismatch:
		writeInt32(writer, 15)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterBuildError.Write", value))
	}
}

type FfiDestroyerBuildError struct{}

func (_ FfiDestroyerBuildError) Destroy(value *BuildError) {
	switch variantValue := value.err.(type) {
	case BuildErrorInvalidSystemTime:
		variantValue.destroy()
	case BuildErrorInvalidChannelMonitor:
		variantValue.destroy()
	case BuildErrorInvalidListeningAddresses:
		variantValue.destroy()
	case BuildErrorInvalidAnnouncementAddresses:
		variantValue.destroy()
	case BuildErrorInvalidTorProxyAddress:
		variantValue.destroy()
	case BuildErrorInvalidNodeAlias:
		variantValue.destroy()
	case BuildErrorRuntimeSetupFailed:
		variantValue.destroy()
	case BuildErrorReadFailed:
		variantValue.destroy()
	case BuildErrorWriteFailed:
		variantValue.destroy()
	case BuildErrorStoragePathAccessFailed:
		variantValue.destroy()
	case BuildErrorKvStoreSetupFailed:
		variantValue.destroy()
	case BuildErrorWalletSetupFailed:
		variantValue.destroy()
	case BuildErrorLoggerSetupFailed:
		variantValue.destroy()
	case BuildErrorNetworkMismatch:
		variantValue.destroy()
	case BuildErrorAsyncPaymentsConfigMismatch:
		variantValue.destroy()
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiDestroyerBuildError.Destroy", value))
	}
}

// The reason the channel was closed. See individual variants for more details.
type ClosureReason interface {
	Destroy()
}

// Closure generated from receiving a peer error message.
//
// Our counterparty may have broadcasted their latest commitment state, and we have
// as well.
type ClosureReasonCounterpartyForceClosed struct {
	PeerMsg UntrustedString
}

func (e ClosureReasonCounterpartyForceClosed) Destroy() {
	FfiDestroyerTypeUntrustedString{}.Destroy(e.PeerMsg)
}

// Closure generated from a force close initiated by us.
type ClosureReasonHolderForceClosed struct {
	BroadcastedLatestTxn *bool
	Message              string
}

func (e ClosureReasonHolderForceClosed) Destroy() {
	FfiDestroyerOptionalBool{}.Destroy(e.BroadcastedLatestTxn)
	FfiDestroyerString{}.Destroy(e.Message)
}

// The channel was closed after negotiating a cooperative close and we've now broadcasted
// the cooperative close transaction. Note the shutdown may have been initiated by us.
type ClosureReasonLegacyCooperativeClosure struct {
}

func (e ClosureReasonLegacyCooperativeClosure) Destroy() {
}

// The channel was closed after negotiating a cooperative close and we've now broadcasted
// the cooperative close transaction. This indicates that the shutdown was initiated by our
// counterparty.
//
// In rare cases where we initiated closure immediately prior to shutting down without
// persisting, this value may be provided for channels we initiated closure for.
type ClosureReasonCounterpartyInitiatedCooperativeClosure struct {
}

func (e ClosureReasonCounterpartyInitiatedCooperativeClosure) Destroy() {
}

// The channel was closed after negotiating a cooperative close and we've now broadcasted
// the cooperative close transaction. This indicates that the shutdown was initiated by us.
type ClosureReasonLocallyInitiatedCooperativeClosure struct {
}

func (e ClosureReasonLocallyInitiatedCooperativeClosure) Destroy() {
}

// A commitment transaction was confirmed on chain, closing the channel. Most likely this
// commitment transaction came from our counterparty, but it may also have come from
// a copy of our own channel monitor.
type ClosureReasonCommitmentTxConfirmed struct {
}

func (e ClosureReasonCommitmentTxConfirmed) Destroy() {
}

// The funding transaction failed to confirm in a timely manner on an inbound channel or the
// counterparty failed to fund the channel in a timely manner.
type ClosureReasonFundingTimedOut struct {
}

func (e ClosureReasonFundingTimedOut) Destroy() {
}

// Closure generated from processing an event, likely a HTLC forward/relay/reception.
type ClosureReasonProcessingError struct {
	Err string
}

func (e ClosureReasonProcessingError) Destroy() {
	FfiDestroyerString{}.Destroy(e.Err)
}

// The peer disconnected prior to funding completing. In this case the spec mandates that we
// forget the channel entirely - we can attempt again if the peer reconnects.
type ClosureReasonDisconnectedPeer struct {
}

func (e ClosureReasonDisconnectedPeer) Destroy() {
}

// Closure generated during deserialization if the channel monitor is newer than
// the channel manager deserialized.
type ClosureReasonOutdatedChannelManager struct {
}

func (e ClosureReasonOutdatedChannelManager) Destroy() {
}

// The counterparty requested a cooperative close of a channel that had not been funded yet.
// The channel has been immediately closed.
type ClosureReasonCounterpartyCoopClosedUnfundedChannel struct {
}

func (e ClosureReasonCounterpartyCoopClosedUnfundedChannel) Destroy() {
}

// We requested a cooperative close of a channel that had not been funded yet.
// The channel has been immediately closed.
type ClosureReasonLocallyCoopClosedUnfundedChannel struct {
}

func (e ClosureReasonLocallyCoopClosedUnfundedChannel) Destroy() {
}

// Another channel in the same funding batch closed before the funding transaction
// was ready to be broadcast.
type ClosureReasonFundingBatchClosure struct {
}

func (e ClosureReasonFundingBatchClosure) Destroy() {
}

// One of our HTLCs timed out in a channel, causing us to force close the channel.
type ClosureReasonHtlCsTimedOut struct {
	PaymentHash *PaymentHash
}

func (e ClosureReasonHtlCsTimedOut) Destroy() {
	FfiDestroyerOptionalTypePaymentHash{}.Destroy(e.PaymentHash)
}

// Our peer provided a feerate which violated our required minimum.
type ClosureReasonPeerFeerateTooLow struct {
	PeerFeerateSatPerKw     uint32
	RequiredFeerateSatPerKw uint32
}

func (e ClosureReasonPeerFeerateTooLow) Destroy() {
	FfiDestroyerUint32{}.Destroy(e.PeerFeerateSatPerKw)
	FfiDestroyerUint32{}.Destroy(e.RequiredFeerateSatPerKw)
}

type FfiConverterClosureReason struct{}

var FfiConverterClosureReasonINSTANCE = FfiConverterClosureReason{}

func (c FfiConverterClosureReason) Lift(rb RustBufferI) ClosureReason {
	return LiftFromRustBuffer[ClosureReason](c, rb)
}

func (c FfiConverterClosureReason) Lower(value ClosureReason) C.RustBuffer {
	return LowerIntoRustBuffer[ClosureReason](c, value)
}

func (c FfiConverterClosureReason) LowerExternal(value ClosureReason) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[ClosureReason](c, value))
}
func (FfiConverterClosureReason) Read(reader io.Reader) ClosureReason {
	id := readInt32(reader)
	switch id {
	case 1:
		return ClosureReasonCounterpartyForceClosed{
			FfiConverterTypeUntrustedStringINSTANCE.Read(reader),
		}
	case 2:
		return ClosureReasonHolderForceClosed{
			FfiConverterOptionalBoolINSTANCE.Read(reader),
			FfiConverterStringINSTANCE.Read(reader),
		}
	case 3:
		return ClosureReasonLegacyCooperativeClosure{}
	case 4:
		return ClosureReasonCounterpartyInitiatedCooperativeClosure{}
	case 5:
		return ClosureReasonLocallyInitiatedCooperativeClosure{}
	case 6:
		return ClosureReasonCommitmentTxConfirmed{}
	case 7:
		return ClosureReasonFundingTimedOut{}
	case 8:
		return ClosureReasonProcessingError{
			FfiConverterStringINSTANCE.Read(reader),
		}
	case 9:
		return ClosureReasonDisconnectedPeer{}
	case 10:
		return ClosureReasonOutdatedChannelManager{}
	case 11:
		return ClosureReasonCounterpartyCoopClosedUnfundedChannel{}
	case 12:
		return ClosureReasonLocallyCoopClosedUnfundedChannel{}
	case 13:
		return ClosureReasonFundingBatchClosure{}
	case 14:
		return ClosureReasonHtlCsTimedOut{
			FfiConverterOptionalTypePaymentHashINSTANCE.Read(reader),
		}
	case 15:
		return ClosureReasonPeerFeerateTooLow{
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterClosureReason.Read()", id))
	}
}

func (FfiConverterClosureReason) Write(writer io.Writer, value ClosureReason) {
	switch variant_value := value.(type) {
	case ClosureReasonCounterpartyForceClosed:
		writeInt32(writer, 1)
		FfiConverterTypeUntrustedStringINSTANCE.Write(writer, variant_value.PeerMsg)
	case ClosureReasonHolderForceClosed:
		writeInt32(writer, 2)
		FfiConverterOptionalBoolINSTANCE.Write(writer, variant_value.BroadcastedLatestTxn)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Message)
	case ClosureReasonLegacyCooperativeClosure:
		writeInt32(writer, 3)
	case ClosureReasonCounterpartyInitiatedCooperativeClosure:
		writeInt32(writer, 4)
	case ClosureReasonLocallyInitiatedCooperativeClosure:
		writeInt32(writer, 5)
	case ClosureReasonCommitmentTxConfirmed:
		writeInt32(writer, 6)
	case ClosureReasonFundingTimedOut:
		writeInt32(writer, 7)
	case ClosureReasonProcessingError:
		writeInt32(writer, 8)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Err)
	case ClosureReasonDisconnectedPeer:
		writeInt32(writer, 9)
	case ClosureReasonOutdatedChannelManager:
		writeInt32(writer, 10)
	case ClosureReasonCounterpartyCoopClosedUnfundedChannel:
		writeInt32(writer, 11)
	case ClosureReasonLocallyCoopClosedUnfundedChannel:
		writeInt32(writer, 12)
	case ClosureReasonFundingBatchClosure:
		writeInt32(writer, 13)
	case ClosureReasonHtlCsTimedOut:
		writeInt32(writer, 14)
		FfiConverterOptionalTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
	case ClosureReasonPeerFeerateTooLow:
		writeInt32(writer, 15)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.PeerFeerateSatPerKw)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.RequiredFeerateSatPerKw)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterClosureReason.Write", value))
	}
}

type FfiDestroyerClosureReason struct{}

func (_ FfiDestroyerClosureReason) Destroy(value ClosureReason) {
	value.Destroy()
}

// Represents the confirmation status of a transaction.
type ConfirmationStatus interface {
	Destroy()
}

// The transaction is confirmed in the best chain.
type ConfirmationStatusConfirmed struct {
	BlockHash BlockHash
	Height    uint32
	Timestamp uint64
}

func (e ConfirmationStatusConfirmed) Destroy() {
	FfiDestroyerTypeBlockHash{}.Destroy(e.BlockHash)
	FfiDestroyerUint32{}.Destroy(e.Height)
	FfiDestroyerUint64{}.Destroy(e.Timestamp)
}

// The transaction is unconfirmed.
type ConfirmationStatusUnconfirmed struct {
}

func (e ConfirmationStatusUnconfirmed) Destroy() {
}

type FfiConverterConfirmationStatus struct{}

var FfiConverterConfirmationStatusINSTANCE = FfiConverterConfirmationStatus{}

func (c FfiConverterConfirmationStatus) Lift(rb RustBufferI) ConfirmationStatus {
	return LiftFromRustBuffer[ConfirmationStatus](c, rb)
}

func (c FfiConverterConfirmationStatus) Lower(value ConfirmationStatus) C.RustBuffer {
	return LowerIntoRustBuffer[ConfirmationStatus](c, value)
}

func (c FfiConverterConfirmationStatus) LowerExternal(value ConfirmationStatus) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[ConfirmationStatus](c, value))
}
func (FfiConverterConfirmationStatus) Read(reader io.Reader) ConfirmationStatus {
	id := readInt32(reader)
	switch id {
	case 1:
		return ConfirmationStatusConfirmed{
			FfiConverterTypeBlockHashINSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
		}
	case 2:
		return ConfirmationStatusUnconfirmed{}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterConfirmationStatus.Read()", id))
	}
}

func (FfiConverterConfirmationStatus) Write(writer io.Writer, value ConfirmationStatus) {
	switch variant_value := value.(type) {
	case ConfirmationStatusConfirmed:
		writeInt32(writer, 1)
		FfiConverterTypeBlockHashINSTANCE.Write(writer, variant_value.BlockHash)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.Height)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.Timestamp)
	case ConfirmationStatusUnconfirmed:
		writeInt32(writer, 2)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterConfirmationStatus.Write", value))
	}
}

type FfiDestroyerConfirmationStatus struct{}

func (_ FfiDestroyerConfirmationStatus) Destroy(value ConfirmationStatus) {
	value.Destroy()
}

type Currency uint

const (
	CurrencyBitcoin        Currency = 1
	CurrencyBitcoinTestnet Currency = 2
	CurrencyRegtest        Currency = 3
	CurrencySimnet         Currency = 4
	CurrencySignet         Currency = 5
)

type FfiConverterCurrency struct{}

var FfiConverterCurrencyINSTANCE = FfiConverterCurrency{}

func (c FfiConverterCurrency) Lift(rb RustBufferI) Currency {
	return LiftFromRustBuffer[Currency](c, rb)
}

func (c FfiConverterCurrency) Lower(value Currency) C.RustBuffer {
	return LowerIntoRustBuffer[Currency](c, value)
}

func (c FfiConverterCurrency) LowerExternal(value Currency) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Currency](c, value))
}
func (FfiConverterCurrency) Read(reader io.Reader) Currency {
	id := readInt32(reader)
	return Currency(id)
}

func (FfiConverterCurrency) Write(writer io.Writer, value Currency) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerCurrency struct{}

func (_ FfiDestroyerCurrency) Destroy(value Currency) {
}

// An error that could arise during [`NodeEntropy`] construction.
type EntropyError struct {
	err error
}

// Convience method to turn *EntropyError into error
// Avoiding treating nil pointer as non nil error interface
func (err *EntropyError) AsError() error {
	if err == nil {
		return nil
	} else {
		return err
	}
}

func (err EntropyError) Error() string {
	return fmt.Sprintf("EntropyError: %s", err.err.Error())
}

func (err EntropyError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var ErrEntropyErrorInvalidSeedBytes = fmt.Errorf("EntropyErrorInvalidSeedBytes")
var ErrEntropyErrorInvalidSeedFile = fmt.Errorf("EntropyErrorInvalidSeedFile")

// Variant structs
// The given seed bytes are invalid, e.g., have invalid length.
type EntropyErrorInvalidSeedBytes struct {
}

// The given seed bytes are invalid, e.g., have invalid length.
func NewEntropyErrorInvalidSeedBytes() *EntropyError {
	return &EntropyError{err: &EntropyErrorInvalidSeedBytes{}}
}

func (e EntropyErrorInvalidSeedBytes) destroy() {
}

func (err EntropyErrorInvalidSeedBytes) Error() string {
	return fmt.Sprint("InvalidSeedBytes")
}

func (self EntropyErrorInvalidSeedBytes) Is(target error) bool {
	return target == ErrEntropyErrorInvalidSeedBytes
}

// The given seed file is invalid, e.g., has invalid length, or could not be read.
type EntropyErrorInvalidSeedFile struct {
}

// The given seed file is invalid, e.g., has invalid length, or could not be read.
func NewEntropyErrorInvalidSeedFile() *EntropyError {
	return &EntropyError{err: &EntropyErrorInvalidSeedFile{}}
}

func (e EntropyErrorInvalidSeedFile) destroy() {
}

func (err EntropyErrorInvalidSeedFile) Error() string {
	return fmt.Sprint("InvalidSeedFile")
}

func (self EntropyErrorInvalidSeedFile) Is(target error) bool {
	return target == ErrEntropyErrorInvalidSeedFile
}

type FfiConverterEntropyError struct{}

var FfiConverterEntropyErrorINSTANCE = FfiConverterEntropyError{}

func (c FfiConverterEntropyError) Lift(eb RustBufferI) *EntropyError {
	return LiftFromRustBuffer[*EntropyError](c, eb)
}

func (c FfiConverterEntropyError) Lower(value *EntropyError) C.RustBuffer {
	return LowerIntoRustBuffer[*EntropyError](c, value)
}

func (c FfiConverterEntropyError) LowerExternal(value *EntropyError) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*EntropyError](c, value))
}

func (c FfiConverterEntropyError) Read(reader io.Reader) *EntropyError {
	errorID := readUint32(reader)

	switch errorID {
	case 1:
		return &EntropyError{&EntropyErrorInvalidSeedBytes{}}
	case 2:
		return &EntropyError{&EntropyErrorInvalidSeedFile{}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterEntropyError.Read()", errorID))
	}
}

func (c FfiConverterEntropyError) Write(writer io.Writer, value *EntropyError) {
	switch variantValue := value.err.(type) {
	case *EntropyErrorInvalidSeedBytes:
		writeInt32(writer, 1)
	case *EntropyErrorInvalidSeedFile:
		writeInt32(writer, 2)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterEntropyError.Write", value))
	}
}

type FfiDestroyerEntropyError struct{}

func (_ FfiDestroyerEntropyError) Destroy(value *EntropyError) {
	switch variantValue := value.err.(type) {
	case EntropyErrorInvalidSeedBytes:
		variantValue.destroy()
	case EntropyErrorInvalidSeedFile:
		variantValue.destroy()
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiDestroyerEntropyError.Destroy", value))
	}
}

// An event emitted by [`Node`], which should be handled by the user.
//
// [`Node`]: [`crate::Node`]
type Event interface {
	Destroy()
}

// A sent payment was successful.
type EventPaymentSuccessful struct {
	PaymentId       *PaymentId
	PaymentHash     PaymentHash
	PaymentPreimage *PaymentPreimage
	FeePaidMsat     *uint64
	Bolt12Invoice   *PaidBolt12Invoice
}

func (e EventPaymentSuccessful) Destroy() {
	FfiDestroyerOptionalTypePaymentId{}.Destroy(e.PaymentId)
	FfiDestroyerTypePaymentHash{}.Destroy(e.PaymentHash)
	FfiDestroyerOptionalTypePaymentPreimage{}.Destroy(e.PaymentPreimage)
	FfiDestroyerOptionalUint64{}.Destroy(e.FeePaidMsat)
	FfiDestroyerOptionalPaidBolt12Invoice{}.Destroy(e.Bolt12Invoice)
}

// A sent payment has failed.
type EventPaymentFailed struct {
	PaymentId   *PaymentId
	PaymentHash *PaymentHash
	Reason      *PaymentFailureReason
}

func (e EventPaymentFailed) Destroy() {
	FfiDestroyerOptionalTypePaymentId{}.Destroy(e.PaymentId)
	FfiDestroyerOptionalTypePaymentHash{}.Destroy(e.PaymentHash)
	FfiDestroyerOptionalPaymentFailureReason{}.Destroy(e.Reason)
}

// A payment has been received.
type EventPaymentReceived struct {
	PaymentId     *PaymentId
	PaymentHash   PaymentHash
	AmountMsat    uint64
	CustomRecords []CustomTlvRecord
}

func (e EventPaymentReceived) Destroy() {
	FfiDestroyerOptionalTypePaymentId{}.Destroy(e.PaymentId)
	FfiDestroyerTypePaymentHash{}.Destroy(e.PaymentHash)
	FfiDestroyerUint64{}.Destroy(e.AmountMsat)
	FfiDestroyerSequenceCustomTlvRecord{}.Destroy(e.CustomRecords)
}

// A payment has been forwarded.
type EventPaymentForwarded struct {
	PrevChannelId               ChannelId
	NextChannelId               ChannelId
	PrevUserChannelId           *UserChannelId
	NextUserChannelId           *UserChannelId
	PrevNodeId                  *PublicKey
	NextNodeId                  *PublicKey
	TotalFeeEarnedMsat          *uint64
	SkimmedFeeMsat              *uint64
	ClaimFromOnchainTx          bool
	OutboundAmountForwardedMsat *uint64
}

func (e EventPaymentForwarded) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.PrevChannelId)
	FfiDestroyerTypeChannelId{}.Destroy(e.NextChannelId)
	FfiDestroyerOptionalTypeUserChannelId{}.Destroy(e.PrevUserChannelId)
	FfiDestroyerOptionalTypeUserChannelId{}.Destroy(e.NextUserChannelId)
	FfiDestroyerOptionalTypePublicKey{}.Destroy(e.PrevNodeId)
	FfiDestroyerOptionalTypePublicKey{}.Destroy(e.NextNodeId)
	FfiDestroyerOptionalUint64{}.Destroy(e.TotalFeeEarnedMsat)
	FfiDestroyerOptionalUint64{}.Destroy(e.SkimmedFeeMsat)
	FfiDestroyerBool{}.Destroy(e.ClaimFromOnchainTx)
	FfiDestroyerOptionalUint64{}.Destroy(e.OutboundAmountForwardedMsat)
}

// A payment for a previously-registered payment hash has been received.
//
// This needs to be manually claimed by supplying the correct preimage to [`claim_for_hash`].
//
// If the provided parameters don't match the expectations or the preimage can't be
// retrieved in time, should be failed-back via [`fail_for_hash`].
//
// Note claiming will necessarily fail after the `claim_deadline` has been reached.
//
// [`claim_for_hash`]: crate::payment::Bolt11Payment::claim_for_hash
// [`fail_for_hash`]: crate::payment::Bolt11Payment::fail_for_hash
type EventPaymentClaimable struct {
	PaymentId           PaymentId
	PaymentHash         PaymentHash
	ClaimableAmountMsat uint64
	ClaimDeadline       *uint32
	CustomRecords       []CustomTlvRecord
}

func (e EventPaymentClaimable) Destroy() {
	FfiDestroyerTypePaymentId{}.Destroy(e.PaymentId)
	FfiDestroyerTypePaymentHash{}.Destroy(e.PaymentHash)
	FfiDestroyerUint64{}.Destroy(e.ClaimableAmountMsat)
	FfiDestroyerOptionalUint32{}.Destroy(e.ClaimDeadline)
	FfiDestroyerSequenceCustomTlvRecord{}.Destroy(e.CustomRecords)
}

// A channel has been created and is pending confirmation on-chain.
type EventChannelPending struct {
	ChannelId                ChannelId
	UserChannelId            UserChannelId
	FormerTemporaryChannelId ChannelId
	CounterpartyNodeId       PublicKey
	FundingTxo               OutPoint
}

func (e EventChannelPending) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypeUserChannelId{}.Destroy(e.UserChannelId)
	FfiDestroyerTypeChannelId{}.Destroy(e.FormerTemporaryChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerOutPoint{}.Destroy(e.FundingTxo)
}

// A channel is ready to be used.
//
// This event is emitted when:
// - A new channel has been established and is ready for use
// - An existing channel has been spliced and is ready with the new funding output
type EventChannelReady struct {
	ChannelId          ChannelId
	UserChannelId      UserChannelId
	CounterpartyNodeId *PublicKey
	FundingTxo         *OutPoint
}

func (e EventChannelReady) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypeUserChannelId{}.Destroy(e.UserChannelId)
	FfiDestroyerOptionalTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerOptionalOutPoint{}.Destroy(e.FundingTxo)
}

// A channel has been closed.
type EventChannelClosed struct {
	ChannelId          ChannelId
	UserChannelId      UserChannelId
	CounterpartyNodeId *PublicKey
	Reason             *ClosureReason
}

func (e EventChannelClosed) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypeUserChannelId{}.Destroy(e.UserChannelId)
	FfiDestroyerOptionalTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerOptionalClosureReason{}.Destroy(e.Reason)
}

// A channel splice is pending confirmation on-chain.
type EventSplicePending struct {
	ChannelId          ChannelId
	UserChannelId      UserChannelId
	CounterpartyNodeId PublicKey
	NewFundingTxo      OutPoint
}

func (e EventSplicePending) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypeUserChannelId{}.Destroy(e.UserChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerOutPoint{}.Destroy(e.NewFundingTxo)
}

// A channel splice has failed.
type EventSpliceFailed struct {
	ChannelId           ChannelId
	UserChannelId       UserChannelId
	CounterpartyNodeId  PublicKey
	AbandonedFundingTxo *OutPoint
}

func (e EventSpliceFailed) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypeUserChannelId{}.Destroy(e.UserChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerOptionalOutPoint{}.Destroy(e.AbandonedFundingTxo)
}

type FfiConverterEvent struct{}

var FfiConverterEventINSTANCE = FfiConverterEvent{}

func (c FfiConverterEvent) Lift(rb RustBufferI) Event {
	return LiftFromRustBuffer[Event](c, rb)
}

func (c FfiConverterEvent) Lower(value Event) C.RustBuffer {
	return LowerIntoRustBuffer[Event](c, value)
}

func (c FfiConverterEvent) LowerExternal(value Event) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Event](c, value))
}
func (FfiConverterEvent) Read(reader io.Reader) Event {
	id := readInt32(reader)
	switch id {
	case 1:
		return EventPaymentSuccessful{
			FfiConverterOptionalTypePaymentIdINSTANCE.Read(reader),
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentPreimageINSTANCE.Read(reader),
			FfiConverterOptionalUint64INSTANCE.Read(reader),
			FfiConverterOptionalPaidBolt12InvoiceINSTANCE.Read(reader),
		}
	case 2:
		return EventPaymentFailed{
			FfiConverterOptionalTypePaymentIdINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentHashINSTANCE.Read(reader),
			FfiConverterOptionalPaymentFailureReasonINSTANCE.Read(reader),
		}
	case 3:
		return EventPaymentReceived{
			FfiConverterOptionalTypePaymentIdINSTANCE.Read(reader),
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterSequenceCustomTlvRecordINSTANCE.Read(reader),
		}
	case 4:
		return EventPaymentForwarded{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterOptionalTypeUserChannelIdINSTANCE.Read(reader),
			FfiConverterOptionalTypeUserChannelIdINSTANCE.Read(reader),
			FfiConverterOptionalTypePublicKeyINSTANCE.Read(reader),
			FfiConverterOptionalTypePublicKeyINSTANCE.Read(reader),
			FfiConverterOptionalUint64INSTANCE.Read(reader),
			FfiConverterOptionalUint64INSTANCE.Read(reader),
			FfiConverterBoolINSTANCE.Read(reader),
			FfiConverterOptionalUint64INSTANCE.Read(reader),
		}
	case 5:
		return EventPaymentClaimable{
			FfiConverterTypePaymentIdINSTANCE.Read(reader),
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterOptionalUint32INSTANCE.Read(reader),
			FfiConverterSequenceCustomTlvRecordINSTANCE.Read(reader),
		}
	case 6:
		return EventChannelPending{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypeUserChannelIdINSTANCE.Read(reader),
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterOutPointINSTANCE.Read(reader),
		}
	case 7:
		return EventChannelReady{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypeUserChannelIdINSTANCE.Read(reader),
			FfiConverterOptionalTypePublicKeyINSTANCE.Read(reader),
			FfiConverterOptionalOutPointINSTANCE.Read(reader),
		}
	case 8:
		return EventChannelClosed{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypeUserChannelIdINSTANCE.Read(reader),
			FfiConverterOptionalTypePublicKeyINSTANCE.Read(reader),
			FfiConverterOptionalClosureReasonINSTANCE.Read(reader),
		}
	case 9:
		return EventSplicePending{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypeUserChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterOutPointINSTANCE.Read(reader),
		}
	case 10:
		return EventSpliceFailed{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypeUserChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterOptionalOutPointINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterEvent.Read()", id))
	}
}

func (FfiConverterEvent) Write(writer io.Writer, value Event) {
	switch variant_value := value.(type) {
	case EventPaymentSuccessful:
		writeInt32(writer, 1)
		FfiConverterOptionalTypePaymentIdINSTANCE.Write(writer, variant_value.PaymentId)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
		FfiConverterOptionalTypePaymentPreimageINSTANCE.Write(writer, variant_value.PaymentPreimage)
		FfiConverterOptionalUint64INSTANCE.Write(writer, variant_value.FeePaidMsat)
		FfiConverterOptionalPaidBolt12InvoiceINSTANCE.Write(writer, variant_value.Bolt12Invoice)
	case EventPaymentFailed:
		writeInt32(writer, 2)
		FfiConverterOptionalTypePaymentIdINSTANCE.Write(writer, variant_value.PaymentId)
		FfiConverterOptionalTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
		FfiConverterOptionalPaymentFailureReasonINSTANCE.Write(writer, variant_value.Reason)
	case EventPaymentReceived:
		writeInt32(writer, 3)
		FfiConverterOptionalTypePaymentIdINSTANCE.Write(writer, variant_value.PaymentId)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountMsat)
		FfiConverterSequenceCustomTlvRecordINSTANCE.Write(writer, variant_value.CustomRecords)
	case EventPaymentForwarded:
		writeInt32(writer, 4)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.PrevChannelId)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.NextChannelId)
		FfiConverterOptionalTypeUserChannelIdINSTANCE.Write(writer, variant_value.PrevUserChannelId)
		FfiConverterOptionalTypeUserChannelIdINSTANCE.Write(writer, variant_value.NextUserChannelId)
		FfiConverterOptionalTypePublicKeyINSTANCE.Write(writer, variant_value.PrevNodeId)
		FfiConverterOptionalTypePublicKeyINSTANCE.Write(writer, variant_value.NextNodeId)
		FfiConverterOptionalUint64INSTANCE.Write(writer, variant_value.TotalFeeEarnedMsat)
		FfiConverterOptionalUint64INSTANCE.Write(writer, variant_value.SkimmedFeeMsat)
		FfiConverterBoolINSTANCE.Write(writer, variant_value.ClaimFromOnchainTx)
		FfiConverterOptionalUint64INSTANCE.Write(writer, variant_value.OutboundAmountForwardedMsat)
	case EventPaymentClaimable:
		writeInt32(writer, 5)
		FfiConverterTypePaymentIdINSTANCE.Write(writer, variant_value.PaymentId)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.ClaimableAmountMsat)
		FfiConverterOptionalUint32INSTANCE.Write(writer, variant_value.ClaimDeadline)
		FfiConverterSequenceCustomTlvRecordINSTANCE.Write(writer, variant_value.CustomRecords)
	case EventChannelPending:
		writeInt32(writer, 6)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypeUserChannelIdINSTANCE.Write(writer, variant_value.UserChannelId)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.FormerTemporaryChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterOutPointINSTANCE.Write(writer, variant_value.FundingTxo)
	case EventChannelReady:
		writeInt32(writer, 7)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypeUserChannelIdINSTANCE.Write(writer, variant_value.UserChannelId)
		FfiConverterOptionalTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterOptionalOutPointINSTANCE.Write(writer, variant_value.FundingTxo)
	case EventChannelClosed:
		writeInt32(writer, 8)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypeUserChannelIdINSTANCE.Write(writer, variant_value.UserChannelId)
		FfiConverterOptionalTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterOptionalClosureReasonINSTANCE.Write(writer, variant_value.Reason)
	case EventSplicePending:
		writeInt32(writer, 9)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypeUserChannelIdINSTANCE.Write(writer, variant_value.UserChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterOutPointINSTANCE.Write(writer, variant_value.NewFundingTxo)
	case EventSpliceFailed:
		writeInt32(writer, 10)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypeUserChannelIdINSTANCE.Write(writer, variant_value.UserChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterOptionalOutPointINSTANCE.Write(writer, variant_value.AbandonedFundingTxo)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterEvent.Write", value))
	}
}

type FfiDestroyerEvent struct{}

func (_ FfiDestroyerEvent) Destroy(value Event) {
	value.Destroy()
}

type Lsps1PaymentState uint

const (
	Lsps1PaymentStateExpectPayment Lsps1PaymentState = 1
	Lsps1PaymentStatePaid          Lsps1PaymentState = 2
	Lsps1PaymentStateRefunded      Lsps1PaymentState = 3
)

type FfiConverterLsps1PaymentState struct{}

var FfiConverterLsps1PaymentStateINSTANCE = FfiConverterLsps1PaymentState{}

func (c FfiConverterLsps1PaymentState) Lift(rb RustBufferI) Lsps1PaymentState {
	return LiftFromRustBuffer[Lsps1PaymentState](c, rb)
}

func (c FfiConverterLsps1PaymentState) Lower(value Lsps1PaymentState) C.RustBuffer {
	return LowerIntoRustBuffer[Lsps1PaymentState](c, value)
}

func (c FfiConverterLsps1PaymentState) LowerExternal(value Lsps1PaymentState) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Lsps1PaymentState](c, value))
}
func (FfiConverterLsps1PaymentState) Read(reader io.Reader) Lsps1PaymentState {
	id := readInt32(reader)
	return Lsps1PaymentState(id)
}

func (FfiConverterLsps1PaymentState) Write(writer io.Writer, value Lsps1PaymentState) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerLsps1PaymentState struct{}

func (_ FfiDestroyerLsps1PaymentState) Destroy(value Lsps1PaymentState) {
}

// Details about the status of a known Lightning balance.
type LightningBalance interface {
	Destroy()
}

// The channel is not yet closed (or the commitment or closing transaction has not yet
// appeared in a block). The given balance is claimable (less on-chain fees) if the channel is
// force-closed now. Values do not take into account any pending splices and are only based
// on the confirmed state of the channel.
type LightningBalanceClaimableOnChannelClose struct {
	ChannelId                        ChannelId
	CounterpartyNodeId               PublicKey
	AmountSatoshis                   uint64
	TransactionFeeSatoshis           uint64
	OutboundPaymentHtlcRoundedMsat   uint64
	OutboundForwardedHtlcRoundedMsat uint64
	InboundClaimingHtlcRoundedMsat   uint64
	InboundHtlcRoundedMsat           uint64
}

func (e LightningBalanceClaimableOnChannelClose) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
	FfiDestroyerUint64{}.Destroy(e.TransactionFeeSatoshis)
	FfiDestroyerUint64{}.Destroy(e.OutboundPaymentHtlcRoundedMsat)
	FfiDestroyerUint64{}.Destroy(e.OutboundForwardedHtlcRoundedMsat)
	FfiDestroyerUint64{}.Destroy(e.InboundClaimingHtlcRoundedMsat)
	FfiDestroyerUint64{}.Destroy(e.InboundHtlcRoundedMsat)
}

// The channel has been closed, and the given balance is ours but awaiting confirmations until
// we consider it spendable.
type LightningBalanceClaimableAwaitingConfirmations struct {
	ChannelId          ChannelId
	CounterpartyNodeId PublicKey
	AmountSatoshis     uint64
	ConfirmationHeight uint32
	Source             BalanceSource
}

func (e LightningBalanceClaimableAwaitingConfirmations) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
	FfiDestroyerUint32{}.Destroy(e.ConfirmationHeight)
	FfiDestroyerBalanceSource{}.Destroy(e.Source)
}

// The channel has been closed, and the given balance should be ours but awaiting spending
// transaction confirmation. If the spending transaction does not confirm in time, it is
// possible our counterparty can take the funds by broadcasting an HTLC timeout on-chain.
//
// Once the spending transaction confirms, before it has reached enough confirmations to be
// considered safe from chain reorganizations, the balance will instead be provided via
// [`LightningBalance::ClaimableAwaitingConfirmations`].
type LightningBalanceContentiousClaimable struct {
	ChannelId          ChannelId
	CounterpartyNodeId PublicKey
	AmountSatoshis     uint64
	TimeoutHeight      uint32
	PaymentHash        PaymentHash
	PaymentPreimage    PaymentPreimage
}

func (e LightningBalanceContentiousClaimable) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
	FfiDestroyerUint32{}.Destroy(e.TimeoutHeight)
	FfiDestroyerTypePaymentHash{}.Destroy(e.PaymentHash)
	FfiDestroyerTypePaymentPreimage{}.Destroy(e.PaymentPreimage)
}

// HTLCs which we sent to our counterparty which are claimable after a timeout (less on-chain
// fees) if the counterparty does not know the preimage for the HTLCs. These are somewhat
// likely to be claimed by our counterparty before we do.
type LightningBalanceMaybeTimeoutClaimableHtlc struct {
	ChannelId          ChannelId
	CounterpartyNodeId PublicKey
	AmountSatoshis     uint64
	ClaimableHeight    uint32
	PaymentHash        PaymentHash
	OutboundPayment    bool
}

func (e LightningBalanceMaybeTimeoutClaimableHtlc) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
	FfiDestroyerUint32{}.Destroy(e.ClaimableHeight)
	FfiDestroyerTypePaymentHash{}.Destroy(e.PaymentHash)
	FfiDestroyerBool{}.Destroy(e.OutboundPayment)
}

// HTLCs which we received from our counterparty which are claimable with a preimage which we
// do not currently have. This will only be claimable if we receive the preimage from the node
// to which we forwarded this HTLC before the timeout.
type LightningBalanceMaybePreimageClaimableHtlc struct {
	ChannelId          ChannelId
	CounterpartyNodeId PublicKey
	AmountSatoshis     uint64
	ExpiryHeight       uint32
	PaymentHash        PaymentHash
}

func (e LightningBalanceMaybePreimageClaimableHtlc) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
	FfiDestroyerUint32{}.Destroy(e.ExpiryHeight)
	FfiDestroyerTypePaymentHash{}.Destroy(e.PaymentHash)
}

// The channel has been closed, and our counterparty broadcasted a revoked commitment
// transaction.
//
// Thus, we're able to claim all outputs in the commitment transaction, one of which has the
// following amount.
type LightningBalanceCounterpartyRevokedOutputClaimable struct {
	ChannelId          ChannelId
	CounterpartyNodeId PublicKey
	AmountSatoshis     uint64
}

func (e LightningBalanceCounterpartyRevokedOutputClaimable) Destroy() {
	FfiDestroyerTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypePublicKey{}.Destroy(e.CounterpartyNodeId)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
}

type FfiConverterLightningBalance struct{}

var FfiConverterLightningBalanceINSTANCE = FfiConverterLightningBalance{}

func (c FfiConverterLightningBalance) Lift(rb RustBufferI) LightningBalance {
	return LiftFromRustBuffer[LightningBalance](c, rb)
}

func (c FfiConverterLightningBalance) Lower(value LightningBalance) C.RustBuffer {
	return LowerIntoRustBuffer[LightningBalance](c, value)
}

func (c FfiConverterLightningBalance) LowerExternal(value LightningBalance) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[LightningBalance](c, value))
}
func (FfiConverterLightningBalance) Read(reader io.Reader) LightningBalance {
	id := readInt32(reader)
	switch id {
	case 1:
		return LightningBalanceClaimableOnChannelClose{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
		}
	case 2:
		return LightningBalanceClaimableAwaitingConfirmations{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterBalanceSourceINSTANCE.Read(reader),
		}
	case 3:
		return LightningBalanceContentiousClaimable{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterTypePaymentPreimageINSTANCE.Read(reader),
		}
	case 4:
		return LightningBalanceMaybeTimeoutClaimableHtlc{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterBoolINSTANCE.Read(reader),
		}
	case 5:
		return LightningBalanceMaybePreimageClaimableHtlc{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
		}
	case 6:
		return LightningBalanceCounterpartyRevokedOutputClaimable{
			FfiConverterTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypePublicKeyINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterLightningBalance.Read()", id))
	}
}

func (FfiConverterLightningBalance) Write(writer io.Writer, value LightningBalance) {
	switch variant_value := value.(type) {
	case LightningBalanceClaimableOnChannelClose:
		writeInt32(writer, 1)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.TransactionFeeSatoshis)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.OutboundPaymentHtlcRoundedMsat)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.OutboundForwardedHtlcRoundedMsat)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.InboundClaimingHtlcRoundedMsat)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.InboundHtlcRoundedMsat)
	case LightningBalanceClaimableAwaitingConfirmations:
		writeInt32(writer, 2)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.ConfirmationHeight)
		FfiConverterBalanceSourceINSTANCE.Write(writer, variant_value.Source)
	case LightningBalanceContentiousClaimable:
		writeInt32(writer, 3)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.TimeoutHeight)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
		FfiConverterTypePaymentPreimageINSTANCE.Write(writer, variant_value.PaymentPreimage)
	case LightningBalanceMaybeTimeoutClaimableHtlc:
		writeInt32(writer, 4)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.ClaimableHeight)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
		FfiConverterBoolINSTANCE.Write(writer, variant_value.OutboundPayment)
	case LightningBalanceMaybePreimageClaimableHtlc:
		writeInt32(writer, 5)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.ExpiryHeight)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.PaymentHash)
	case LightningBalanceCounterpartyRevokedOutputClaimable:
		writeInt32(writer, 6)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, variant_value.CounterpartyNodeId)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterLightningBalance.Write", value))
	}
}

type FfiDestroyerLightningBalance struct{}

func (_ FfiDestroyerLightningBalance) Destroy(value LightningBalance) {
	value.Destroy()
}

type LogLevel uint

const (
	LogLevelGossip LogLevel = 1
	LogLevelTrace  LogLevel = 2
	LogLevelDebug  LogLevel = 3
	LogLevelInfo   LogLevel = 4
	LogLevelWarn   LogLevel = 5
	LogLevelError  LogLevel = 6
)

type FfiConverterLogLevel struct{}

var FfiConverterLogLevelINSTANCE = FfiConverterLogLevel{}

func (c FfiConverterLogLevel) Lift(rb RustBufferI) LogLevel {
	return LiftFromRustBuffer[LogLevel](c, rb)
}

func (c FfiConverterLogLevel) Lower(value LogLevel) C.RustBuffer {
	return LowerIntoRustBuffer[LogLevel](c, value)
}

func (c FfiConverterLogLevel) LowerExternal(value LogLevel) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[LogLevel](c, value))
}
func (FfiConverterLogLevel) Read(reader io.Reader) LogLevel {
	id := readInt32(reader)
	return LogLevel(id)
}

func (FfiConverterLogLevel) Write(writer io.Writer, value LogLevel) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerLogLevel struct{}

func (_ FfiDestroyerLogLevel) Destroy(value LogLevel) {
}

// Options for how to set the max dust exposure allowed on a channel.
//
// See [`LdkChannelConfig::max_dust_htlc_exposure`] for details.
type MaxDustHtlcExposure interface {
	Destroy()
}

// This sets a fixed limit on the total dust exposure in millisatoshis.
//
// Please refer to [`LdkMaxDustHTLCExposure`] for further details.
type MaxDustHtlcExposureFixedLimit struct {
	LimitMsat uint64
}

func (e MaxDustHtlcExposureFixedLimit) Destroy() {
	FfiDestroyerUint64{}.Destroy(e.LimitMsat)
}

// This sets a multiplier on the feerate to determine the maximum allowed dust exposure.
//
// Please refer to [`LdkMaxDustHTLCExposure`] for further details.
type MaxDustHtlcExposureFeeRateMultiplier struct {
	Multiplier uint64
}

func (e MaxDustHtlcExposureFeeRateMultiplier) Destroy() {
	FfiDestroyerUint64{}.Destroy(e.Multiplier)
}

type FfiConverterMaxDustHtlcExposure struct{}

var FfiConverterMaxDustHtlcExposureINSTANCE = FfiConverterMaxDustHtlcExposure{}

func (c FfiConverterMaxDustHtlcExposure) Lift(rb RustBufferI) MaxDustHtlcExposure {
	return LiftFromRustBuffer[MaxDustHtlcExposure](c, rb)
}

func (c FfiConverterMaxDustHtlcExposure) Lower(value MaxDustHtlcExposure) C.RustBuffer {
	return LowerIntoRustBuffer[MaxDustHtlcExposure](c, value)
}

func (c FfiConverterMaxDustHtlcExposure) LowerExternal(value MaxDustHtlcExposure) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[MaxDustHtlcExposure](c, value))
}
func (FfiConverterMaxDustHtlcExposure) Read(reader io.Reader) MaxDustHtlcExposure {
	id := readInt32(reader)
	switch id {
	case 1:
		return MaxDustHtlcExposureFixedLimit{
			FfiConverterUint64INSTANCE.Read(reader),
		}
	case 2:
		return MaxDustHtlcExposureFeeRateMultiplier{
			FfiConverterUint64INSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterMaxDustHtlcExposure.Read()", id))
	}
}

func (FfiConverterMaxDustHtlcExposure) Write(writer io.Writer, value MaxDustHtlcExposure) {
	switch variant_value := value.(type) {
	case MaxDustHtlcExposureFixedLimit:
		writeInt32(writer, 1)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.LimitMsat)
	case MaxDustHtlcExposureFeeRateMultiplier:
		writeInt32(writer, 2)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.Multiplier)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterMaxDustHtlcExposure.Write", value))
	}
}

type FfiDestroyerMaxDustHtlcExposure struct{}

func (_ FfiDestroyerMaxDustHtlcExposure) Destroy(value MaxDustHtlcExposure) {
	value.Destroy()
}

type Network uint

const (
	NetworkBitcoin Network = 1
	NetworkTestnet Network = 2
	NetworkSignet  Network = 3
	NetworkRegtest Network = 4
)

type FfiConverterNetwork struct{}

var FfiConverterNetworkINSTANCE = FfiConverterNetwork{}

func (c FfiConverterNetwork) Lift(rb RustBufferI) Network {
	return LiftFromRustBuffer[Network](c, rb)
}

func (c FfiConverterNetwork) Lower(value Network) C.RustBuffer {
	return LowerIntoRustBuffer[Network](c, value)
}

func (c FfiConverterNetwork) LowerExternal(value Network) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[Network](c, value))
}
func (FfiConverterNetwork) Read(reader io.Reader) Network {
	id := readInt32(reader)
	return Network(id)
}

func (FfiConverterNetwork) Write(writer io.Writer, value Network) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerNetwork struct{}

func (_ FfiDestroyerNetwork) Destroy(value Network) {
}

type NodeError struct {
	err error
}

// Convience method to turn *NodeError into error
// Avoiding treating nil pointer as non nil error interface
func (err *NodeError) AsError() error {
	if err == nil {
		return nil
	} else {
		return err
	}
}

func (err NodeError) Error() string {
	return fmt.Sprintf("NodeError: %s", err.err.Error())
}

func (err NodeError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var ErrNodeErrorAlreadyRunning = fmt.Errorf("NodeErrorAlreadyRunning")
var ErrNodeErrorNotRunning = fmt.Errorf("NodeErrorNotRunning")
var ErrNodeErrorOnchainTxCreationFailed = fmt.Errorf("NodeErrorOnchainTxCreationFailed")
var ErrNodeErrorConnectionFailed = fmt.Errorf("NodeErrorConnectionFailed")
var ErrNodeErrorInvoiceCreationFailed = fmt.Errorf("NodeErrorInvoiceCreationFailed")
var ErrNodeErrorInvoiceRequestCreationFailed = fmt.Errorf("NodeErrorInvoiceRequestCreationFailed")
var ErrNodeErrorOfferCreationFailed = fmt.Errorf("NodeErrorOfferCreationFailed")
var ErrNodeErrorRefundCreationFailed = fmt.Errorf("NodeErrorRefundCreationFailed")
var ErrNodeErrorPaymentSendingFailed = fmt.Errorf("NodeErrorPaymentSendingFailed")
var ErrNodeErrorInvalidCustomTlvs = fmt.Errorf("NodeErrorInvalidCustomTlvs")
var ErrNodeErrorProbeSendingFailed = fmt.Errorf("NodeErrorProbeSendingFailed")
var ErrNodeErrorChannelCreationFailed = fmt.Errorf("NodeErrorChannelCreationFailed")
var ErrNodeErrorChannelClosingFailed = fmt.Errorf("NodeErrorChannelClosingFailed")
var ErrNodeErrorChannelSplicingFailed = fmt.Errorf("NodeErrorChannelSplicingFailed")
var ErrNodeErrorChannelConfigUpdateFailed = fmt.Errorf("NodeErrorChannelConfigUpdateFailed")
var ErrNodeErrorPersistenceFailed = fmt.Errorf("NodeErrorPersistenceFailed")
var ErrNodeErrorFeerateEstimationUpdateFailed = fmt.Errorf("NodeErrorFeerateEstimationUpdateFailed")
var ErrNodeErrorFeerateEstimationUpdateTimeout = fmt.Errorf("NodeErrorFeerateEstimationUpdateTimeout")
var ErrNodeErrorWalletOperationFailed = fmt.Errorf("NodeErrorWalletOperationFailed")
var ErrNodeErrorWalletOperationTimeout = fmt.Errorf("NodeErrorWalletOperationTimeout")
var ErrNodeErrorOnchainTxSigningFailed = fmt.Errorf("NodeErrorOnchainTxSigningFailed")
var ErrNodeErrorTxSyncFailed = fmt.Errorf("NodeErrorTxSyncFailed")
var ErrNodeErrorTxSyncTimeout = fmt.Errorf("NodeErrorTxSyncTimeout")
var ErrNodeErrorGossipUpdateFailed = fmt.Errorf("NodeErrorGossipUpdateFailed")
var ErrNodeErrorGossipUpdateTimeout = fmt.Errorf("NodeErrorGossipUpdateTimeout")
var ErrNodeErrorLiquidityRequestFailed = fmt.Errorf("NodeErrorLiquidityRequestFailed")
var ErrNodeErrorUriParameterParsingFailed = fmt.Errorf("NodeErrorUriParameterParsingFailed")
var ErrNodeErrorInvalidAddress = fmt.Errorf("NodeErrorInvalidAddress")
var ErrNodeErrorInvalidSocketAddress = fmt.Errorf("NodeErrorInvalidSocketAddress")
var ErrNodeErrorInvalidPublicKey = fmt.Errorf("NodeErrorInvalidPublicKey")
var ErrNodeErrorInvalidSecretKey = fmt.Errorf("NodeErrorInvalidSecretKey")
var ErrNodeErrorInvalidOfferId = fmt.Errorf("NodeErrorInvalidOfferId")
var ErrNodeErrorInvalidNodeId = fmt.Errorf("NodeErrorInvalidNodeId")
var ErrNodeErrorInvalidPaymentId = fmt.Errorf("NodeErrorInvalidPaymentId")
var ErrNodeErrorInvalidPaymentHash = fmt.Errorf("NodeErrorInvalidPaymentHash")
var ErrNodeErrorInvalidPaymentPreimage = fmt.Errorf("NodeErrorInvalidPaymentPreimage")
var ErrNodeErrorInvalidPaymentSecret = fmt.Errorf("NodeErrorInvalidPaymentSecret")
var ErrNodeErrorInvalidAmount = fmt.Errorf("NodeErrorInvalidAmount")
var ErrNodeErrorInvalidInvoice = fmt.Errorf("NodeErrorInvalidInvoice")
var ErrNodeErrorInvalidOffer = fmt.Errorf("NodeErrorInvalidOffer")
var ErrNodeErrorInvalidRefund = fmt.Errorf("NodeErrorInvalidRefund")
var ErrNodeErrorInvalidChannelId = fmt.Errorf("NodeErrorInvalidChannelId")
var ErrNodeErrorInvalidNetwork = fmt.Errorf("NodeErrorInvalidNetwork")
var ErrNodeErrorInvalidUri = fmt.Errorf("NodeErrorInvalidUri")
var ErrNodeErrorInvalidQuantity = fmt.Errorf("NodeErrorInvalidQuantity")
var ErrNodeErrorInvalidNodeAlias = fmt.Errorf("NodeErrorInvalidNodeAlias")
var ErrNodeErrorInvalidDateTime = fmt.Errorf("NodeErrorInvalidDateTime")
var ErrNodeErrorInvalidFeeRate = fmt.Errorf("NodeErrorInvalidFeeRate")
var ErrNodeErrorInvalidScriptPubKey = fmt.Errorf("NodeErrorInvalidScriptPubKey")
var ErrNodeErrorDuplicatePayment = fmt.Errorf("NodeErrorDuplicatePayment")
var ErrNodeErrorUnsupportedCurrency = fmt.Errorf("NodeErrorUnsupportedCurrency")
var ErrNodeErrorInsufficientFunds = fmt.Errorf("NodeErrorInsufficientFunds")
var ErrNodeErrorLiquiditySourceUnavailable = fmt.Errorf("NodeErrorLiquiditySourceUnavailable")
var ErrNodeErrorLiquidityFeeTooHigh = fmt.Errorf("NodeErrorLiquidityFeeTooHigh")
var ErrNodeErrorInvalidBlindedPaths = fmt.Errorf("NodeErrorInvalidBlindedPaths")
var ErrNodeErrorAsyncPaymentServicesDisabled = fmt.Errorf("NodeErrorAsyncPaymentServicesDisabled")
var ErrNodeErrorHrnParsingFailed = fmt.Errorf("NodeErrorHrnParsingFailed")
var ErrNodeErrorLnurlAuthFailed = fmt.Errorf("NodeErrorLnurlAuthFailed")
var ErrNodeErrorLnurlAuthTimeout = fmt.Errorf("NodeErrorLnurlAuthTimeout")
var ErrNodeErrorInvalidLnurl = fmt.Errorf("NodeErrorInvalidLnurl")

// Variant structs
type NodeErrorAlreadyRunning struct {
	message string
}

func NewNodeErrorAlreadyRunning() *NodeError {
	return &NodeError{err: &NodeErrorAlreadyRunning{}}
}

func (e NodeErrorAlreadyRunning) destroy() {
}

func (err NodeErrorAlreadyRunning) Error() string {
	return fmt.Sprintf("AlreadyRunning: %s", err.message)
}

func (self NodeErrorAlreadyRunning) Is(target error) bool {
	return target == ErrNodeErrorAlreadyRunning
}

type NodeErrorNotRunning struct {
	message string
}

func NewNodeErrorNotRunning() *NodeError {
	return &NodeError{err: &NodeErrorNotRunning{}}
}

func (e NodeErrorNotRunning) destroy() {
}

func (err NodeErrorNotRunning) Error() string {
	return fmt.Sprintf("NotRunning: %s", err.message)
}

func (self NodeErrorNotRunning) Is(target error) bool {
	return target == ErrNodeErrorNotRunning
}

type NodeErrorOnchainTxCreationFailed struct {
	message string
}

func NewNodeErrorOnchainTxCreationFailed() *NodeError {
	return &NodeError{err: &NodeErrorOnchainTxCreationFailed{}}
}

func (e NodeErrorOnchainTxCreationFailed) destroy() {
}

func (err NodeErrorOnchainTxCreationFailed) Error() string {
	return fmt.Sprintf("OnchainTxCreationFailed: %s", err.message)
}

func (self NodeErrorOnchainTxCreationFailed) Is(target error) bool {
	return target == ErrNodeErrorOnchainTxCreationFailed
}

type NodeErrorConnectionFailed struct {
	message string
}

func NewNodeErrorConnectionFailed() *NodeError {
	return &NodeError{err: &NodeErrorConnectionFailed{}}
}

func (e NodeErrorConnectionFailed) destroy() {
}

func (err NodeErrorConnectionFailed) Error() string {
	return fmt.Sprintf("ConnectionFailed: %s", err.message)
}

func (self NodeErrorConnectionFailed) Is(target error) bool {
	return target == ErrNodeErrorConnectionFailed
}

type NodeErrorInvoiceCreationFailed struct {
	message string
}

func NewNodeErrorInvoiceCreationFailed() *NodeError {
	return &NodeError{err: &NodeErrorInvoiceCreationFailed{}}
}

func (e NodeErrorInvoiceCreationFailed) destroy() {
}

func (err NodeErrorInvoiceCreationFailed) Error() string {
	return fmt.Sprintf("InvoiceCreationFailed: %s", err.message)
}

func (self NodeErrorInvoiceCreationFailed) Is(target error) bool {
	return target == ErrNodeErrorInvoiceCreationFailed
}

type NodeErrorInvoiceRequestCreationFailed struct {
	message string
}

func NewNodeErrorInvoiceRequestCreationFailed() *NodeError {
	return &NodeError{err: &NodeErrorInvoiceRequestCreationFailed{}}
}

func (e NodeErrorInvoiceRequestCreationFailed) destroy() {
}

func (err NodeErrorInvoiceRequestCreationFailed) Error() string {
	return fmt.Sprintf("InvoiceRequestCreationFailed: %s", err.message)
}

func (self NodeErrorInvoiceRequestCreationFailed) Is(target error) bool {
	return target == ErrNodeErrorInvoiceRequestCreationFailed
}

type NodeErrorOfferCreationFailed struct {
	message string
}

func NewNodeErrorOfferCreationFailed() *NodeError {
	return &NodeError{err: &NodeErrorOfferCreationFailed{}}
}

func (e NodeErrorOfferCreationFailed) destroy() {
}

func (err NodeErrorOfferCreationFailed) Error() string {
	return fmt.Sprintf("OfferCreationFailed: %s", err.message)
}

func (self NodeErrorOfferCreationFailed) Is(target error) bool {
	return target == ErrNodeErrorOfferCreationFailed
}

type NodeErrorRefundCreationFailed struct {
	message string
}

func NewNodeErrorRefundCreationFailed() *NodeError {
	return &NodeError{err: &NodeErrorRefundCreationFailed{}}
}

func (e NodeErrorRefundCreationFailed) destroy() {
}

func (err NodeErrorRefundCreationFailed) Error() string {
	return fmt.Sprintf("RefundCreationFailed: %s", err.message)
}

func (self NodeErrorRefundCreationFailed) Is(target error) bool {
	return target == ErrNodeErrorRefundCreationFailed
}

type NodeErrorPaymentSendingFailed struct {
	message string
}

func NewNodeErrorPaymentSendingFailed() *NodeError {
	return &NodeError{err: &NodeErrorPaymentSendingFailed{}}
}

func (e NodeErrorPaymentSendingFailed) destroy() {
}

func (err NodeErrorPaymentSendingFailed) Error() string {
	return fmt.Sprintf("PaymentSendingFailed: %s", err.message)
}

func (self NodeErrorPaymentSendingFailed) Is(target error) bool {
	return target == ErrNodeErrorPaymentSendingFailed
}

type NodeErrorInvalidCustomTlvs struct {
	message string
}

func NewNodeErrorInvalidCustomTlvs() *NodeError {
	return &NodeError{err: &NodeErrorInvalidCustomTlvs{}}
}

func (e NodeErrorInvalidCustomTlvs) destroy() {
}

func (err NodeErrorInvalidCustomTlvs) Error() string {
	return fmt.Sprintf("InvalidCustomTlvs: %s", err.message)
}

func (self NodeErrorInvalidCustomTlvs) Is(target error) bool {
	return target == ErrNodeErrorInvalidCustomTlvs
}

type NodeErrorProbeSendingFailed struct {
	message string
}

func NewNodeErrorProbeSendingFailed() *NodeError {
	return &NodeError{err: &NodeErrorProbeSendingFailed{}}
}

func (e NodeErrorProbeSendingFailed) destroy() {
}

func (err NodeErrorProbeSendingFailed) Error() string {
	return fmt.Sprintf("ProbeSendingFailed: %s", err.message)
}

func (self NodeErrorProbeSendingFailed) Is(target error) bool {
	return target == ErrNodeErrorProbeSendingFailed
}

type NodeErrorChannelCreationFailed struct {
	message string
}

func NewNodeErrorChannelCreationFailed() *NodeError {
	return &NodeError{err: &NodeErrorChannelCreationFailed{}}
}

func (e NodeErrorChannelCreationFailed) destroy() {
}

func (err NodeErrorChannelCreationFailed) Error() string {
	return fmt.Sprintf("ChannelCreationFailed: %s", err.message)
}

func (self NodeErrorChannelCreationFailed) Is(target error) bool {
	return target == ErrNodeErrorChannelCreationFailed
}

type NodeErrorChannelClosingFailed struct {
	message string
}

func NewNodeErrorChannelClosingFailed() *NodeError {
	return &NodeError{err: &NodeErrorChannelClosingFailed{}}
}

func (e NodeErrorChannelClosingFailed) destroy() {
}

func (err NodeErrorChannelClosingFailed) Error() string {
	return fmt.Sprintf("ChannelClosingFailed: %s", err.message)
}

func (self NodeErrorChannelClosingFailed) Is(target error) bool {
	return target == ErrNodeErrorChannelClosingFailed
}

type NodeErrorChannelSplicingFailed struct {
	message string
}

func NewNodeErrorChannelSplicingFailed() *NodeError {
	return &NodeError{err: &NodeErrorChannelSplicingFailed{}}
}

func (e NodeErrorChannelSplicingFailed) destroy() {
}

func (err NodeErrorChannelSplicingFailed) Error() string {
	return fmt.Sprintf("ChannelSplicingFailed: %s", err.message)
}

func (self NodeErrorChannelSplicingFailed) Is(target error) bool {
	return target == ErrNodeErrorChannelSplicingFailed
}

type NodeErrorChannelConfigUpdateFailed struct {
	message string
}

func NewNodeErrorChannelConfigUpdateFailed() *NodeError {
	return &NodeError{err: &NodeErrorChannelConfigUpdateFailed{}}
}

func (e NodeErrorChannelConfigUpdateFailed) destroy() {
}

func (err NodeErrorChannelConfigUpdateFailed) Error() string {
	return fmt.Sprintf("ChannelConfigUpdateFailed: %s", err.message)
}

func (self NodeErrorChannelConfigUpdateFailed) Is(target error) bool {
	return target == ErrNodeErrorChannelConfigUpdateFailed
}

type NodeErrorPersistenceFailed struct {
	message string
}

func NewNodeErrorPersistenceFailed() *NodeError {
	return &NodeError{err: &NodeErrorPersistenceFailed{}}
}

func (e NodeErrorPersistenceFailed) destroy() {
}

func (err NodeErrorPersistenceFailed) Error() string {
	return fmt.Sprintf("PersistenceFailed: %s", err.message)
}

func (self NodeErrorPersistenceFailed) Is(target error) bool {
	return target == ErrNodeErrorPersistenceFailed
}

type NodeErrorFeerateEstimationUpdateFailed struct {
	message string
}

func NewNodeErrorFeerateEstimationUpdateFailed() *NodeError {
	return &NodeError{err: &NodeErrorFeerateEstimationUpdateFailed{}}
}

func (e NodeErrorFeerateEstimationUpdateFailed) destroy() {
}

func (err NodeErrorFeerateEstimationUpdateFailed) Error() string {
	return fmt.Sprintf("FeerateEstimationUpdateFailed: %s", err.message)
}

func (self NodeErrorFeerateEstimationUpdateFailed) Is(target error) bool {
	return target == ErrNodeErrorFeerateEstimationUpdateFailed
}

type NodeErrorFeerateEstimationUpdateTimeout struct {
	message string
}

func NewNodeErrorFeerateEstimationUpdateTimeout() *NodeError {
	return &NodeError{err: &NodeErrorFeerateEstimationUpdateTimeout{}}
}

func (e NodeErrorFeerateEstimationUpdateTimeout) destroy() {
}

func (err NodeErrorFeerateEstimationUpdateTimeout) Error() string {
	return fmt.Sprintf("FeerateEstimationUpdateTimeout: %s", err.message)
}

func (self NodeErrorFeerateEstimationUpdateTimeout) Is(target error) bool {
	return target == ErrNodeErrorFeerateEstimationUpdateTimeout
}

type NodeErrorWalletOperationFailed struct {
	message string
}

func NewNodeErrorWalletOperationFailed() *NodeError {
	return &NodeError{err: &NodeErrorWalletOperationFailed{}}
}

func (e NodeErrorWalletOperationFailed) destroy() {
}

func (err NodeErrorWalletOperationFailed) Error() string {
	return fmt.Sprintf("WalletOperationFailed: %s", err.message)
}

func (self NodeErrorWalletOperationFailed) Is(target error) bool {
	return target == ErrNodeErrorWalletOperationFailed
}

type NodeErrorWalletOperationTimeout struct {
	message string
}

func NewNodeErrorWalletOperationTimeout() *NodeError {
	return &NodeError{err: &NodeErrorWalletOperationTimeout{}}
}

func (e NodeErrorWalletOperationTimeout) destroy() {
}

func (err NodeErrorWalletOperationTimeout) Error() string {
	return fmt.Sprintf("WalletOperationTimeout: %s", err.message)
}

func (self NodeErrorWalletOperationTimeout) Is(target error) bool {
	return target == ErrNodeErrorWalletOperationTimeout
}

type NodeErrorOnchainTxSigningFailed struct {
	message string
}

func NewNodeErrorOnchainTxSigningFailed() *NodeError {
	return &NodeError{err: &NodeErrorOnchainTxSigningFailed{}}
}

func (e NodeErrorOnchainTxSigningFailed) destroy() {
}

func (err NodeErrorOnchainTxSigningFailed) Error() string {
	return fmt.Sprintf("OnchainTxSigningFailed: %s", err.message)
}

func (self NodeErrorOnchainTxSigningFailed) Is(target error) bool {
	return target == ErrNodeErrorOnchainTxSigningFailed
}

type NodeErrorTxSyncFailed struct {
	message string
}

func NewNodeErrorTxSyncFailed() *NodeError {
	return &NodeError{err: &NodeErrorTxSyncFailed{}}
}

func (e NodeErrorTxSyncFailed) destroy() {
}

func (err NodeErrorTxSyncFailed) Error() string {
	return fmt.Sprintf("TxSyncFailed: %s", err.message)
}

func (self NodeErrorTxSyncFailed) Is(target error) bool {
	return target == ErrNodeErrorTxSyncFailed
}

type NodeErrorTxSyncTimeout struct {
	message string
}

func NewNodeErrorTxSyncTimeout() *NodeError {
	return &NodeError{err: &NodeErrorTxSyncTimeout{}}
}

func (e NodeErrorTxSyncTimeout) destroy() {
}

func (err NodeErrorTxSyncTimeout) Error() string {
	return fmt.Sprintf("TxSyncTimeout: %s", err.message)
}

func (self NodeErrorTxSyncTimeout) Is(target error) bool {
	return target == ErrNodeErrorTxSyncTimeout
}

type NodeErrorGossipUpdateFailed struct {
	message string
}

func NewNodeErrorGossipUpdateFailed() *NodeError {
	return &NodeError{err: &NodeErrorGossipUpdateFailed{}}
}

func (e NodeErrorGossipUpdateFailed) destroy() {
}

func (err NodeErrorGossipUpdateFailed) Error() string {
	return fmt.Sprintf("GossipUpdateFailed: %s", err.message)
}

func (self NodeErrorGossipUpdateFailed) Is(target error) bool {
	return target == ErrNodeErrorGossipUpdateFailed
}

type NodeErrorGossipUpdateTimeout struct {
	message string
}

func NewNodeErrorGossipUpdateTimeout() *NodeError {
	return &NodeError{err: &NodeErrorGossipUpdateTimeout{}}
}

func (e NodeErrorGossipUpdateTimeout) destroy() {
}

func (err NodeErrorGossipUpdateTimeout) Error() string {
	return fmt.Sprintf("GossipUpdateTimeout: %s", err.message)
}

func (self NodeErrorGossipUpdateTimeout) Is(target error) bool {
	return target == ErrNodeErrorGossipUpdateTimeout
}

type NodeErrorLiquidityRequestFailed struct {
	message string
}

func NewNodeErrorLiquidityRequestFailed() *NodeError {
	return &NodeError{err: &NodeErrorLiquidityRequestFailed{}}
}

func (e NodeErrorLiquidityRequestFailed) destroy() {
}

func (err NodeErrorLiquidityRequestFailed) Error() string {
	return fmt.Sprintf("LiquidityRequestFailed: %s", err.message)
}

func (self NodeErrorLiquidityRequestFailed) Is(target error) bool {
	return target == ErrNodeErrorLiquidityRequestFailed
}

type NodeErrorUriParameterParsingFailed struct {
	message string
}

func NewNodeErrorUriParameterParsingFailed() *NodeError {
	return &NodeError{err: &NodeErrorUriParameterParsingFailed{}}
}

func (e NodeErrorUriParameterParsingFailed) destroy() {
}

func (err NodeErrorUriParameterParsingFailed) Error() string {
	return fmt.Sprintf("UriParameterParsingFailed: %s", err.message)
}

func (self NodeErrorUriParameterParsingFailed) Is(target error) bool {
	return target == ErrNodeErrorUriParameterParsingFailed
}

type NodeErrorInvalidAddress struct {
	message string
}

func NewNodeErrorInvalidAddress() *NodeError {
	return &NodeError{err: &NodeErrorInvalidAddress{}}
}

func (e NodeErrorInvalidAddress) destroy() {
}

func (err NodeErrorInvalidAddress) Error() string {
	return fmt.Sprintf("InvalidAddress: %s", err.message)
}

func (self NodeErrorInvalidAddress) Is(target error) bool {
	return target == ErrNodeErrorInvalidAddress
}

type NodeErrorInvalidSocketAddress struct {
	message string
}

func NewNodeErrorInvalidSocketAddress() *NodeError {
	return &NodeError{err: &NodeErrorInvalidSocketAddress{}}
}

func (e NodeErrorInvalidSocketAddress) destroy() {
}

func (err NodeErrorInvalidSocketAddress) Error() string {
	return fmt.Sprintf("InvalidSocketAddress: %s", err.message)
}

func (self NodeErrorInvalidSocketAddress) Is(target error) bool {
	return target == ErrNodeErrorInvalidSocketAddress
}

type NodeErrorInvalidPublicKey struct {
	message string
}

func NewNodeErrorInvalidPublicKey() *NodeError {
	return &NodeError{err: &NodeErrorInvalidPublicKey{}}
}

func (e NodeErrorInvalidPublicKey) destroy() {
}

func (err NodeErrorInvalidPublicKey) Error() string {
	return fmt.Sprintf("InvalidPublicKey: %s", err.message)
}

func (self NodeErrorInvalidPublicKey) Is(target error) bool {
	return target == ErrNodeErrorInvalidPublicKey
}

type NodeErrorInvalidSecretKey struct {
	message string
}

func NewNodeErrorInvalidSecretKey() *NodeError {
	return &NodeError{err: &NodeErrorInvalidSecretKey{}}
}

func (e NodeErrorInvalidSecretKey) destroy() {
}

func (err NodeErrorInvalidSecretKey) Error() string {
	return fmt.Sprintf("InvalidSecretKey: %s", err.message)
}

func (self NodeErrorInvalidSecretKey) Is(target error) bool {
	return target == ErrNodeErrorInvalidSecretKey
}

type NodeErrorInvalidOfferId struct {
	message string
}

func NewNodeErrorInvalidOfferId() *NodeError {
	return &NodeError{err: &NodeErrorInvalidOfferId{}}
}

func (e NodeErrorInvalidOfferId) destroy() {
}

func (err NodeErrorInvalidOfferId) Error() string {
	return fmt.Sprintf("InvalidOfferId: %s", err.message)
}

func (self NodeErrorInvalidOfferId) Is(target error) bool {
	return target == ErrNodeErrorInvalidOfferId
}

type NodeErrorInvalidNodeId struct {
	message string
}

func NewNodeErrorInvalidNodeId() *NodeError {
	return &NodeError{err: &NodeErrorInvalidNodeId{}}
}

func (e NodeErrorInvalidNodeId) destroy() {
}

func (err NodeErrorInvalidNodeId) Error() string {
	return fmt.Sprintf("InvalidNodeId: %s", err.message)
}

func (self NodeErrorInvalidNodeId) Is(target error) bool {
	return target == ErrNodeErrorInvalidNodeId
}

type NodeErrorInvalidPaymentId struct {
	message string
}

func NewNodeErrorInvalidPaymentId() *NodeError {
	return &NodeError{err: &NodeErrorInvalidPaymentId{}}
}

func (e NodeErrorInvalidPaymentId) destroy() {
}

func (err NodeErrorInvalidPaymentId) Error() string {
	return fmt.Sprintf("InvalidPaymentId: %s", err.message)
}

func (self NodeErrorInvalidPaymentId) Is(target error) bool {
	return target == ErrNodeErrorInvalidPaymentId
}

type NodeErrorInvalidPaymentHash struct {
	message string
}

func NewNodeErrorInvalidPaymentHash() *NodeError {
	return &NodeError{err: &NodeErrorInvalidPaymentHash{}}
}

func (e NodeErrorInvalidPaymentHash) destroy() {
}

func (err NodeErrorInvalidPaymentHash) Error() string {
	return fmt.Sprintf("InvalidPaymentHash: %s", err.message)
}

func (self NodeErrorInvalidPaymentHash) Is(target error) bool {
	return target == ErrNodeErrorInvalidPaymentHash
}

type NodeErrorInvalidPaymentPreimage struct {
	message string
}

func NewNodeErrorInvalidPaymentPreimage() *NodeError {
	return &NodeError{err: &NodeErrorInvalidPaymentPreimage{}}
}

func (e NodeErrorInvalidPaymentPreimage) destroy() {
}

func (err NodeErrorInvalidPaymentPreimage) Error() string {
	return fmt.Sprintf("InvalidPaymentPreimage: %s", err.message)
}

func (self NodeErrorInvalidPaymentPreimage) Is(target error) bool {
	return target == ErrNodeErrorInvalidPaymentPreimage
}

type NodeErrorInvalidPaymentSecret struct {
	message string
}

func NewNodeErrorInvalidPaymentSecret() *NodeError {
	return &NodeError{err: &NodeErrorInvalidPaymentSecret{}}
}

func (e NodeErrorInvalidPaymentSecret) destroy() {
}

func (err NodeErrorInvalidPaymentSecret) Error() string {
	return fmt.Sprintf("InvalidPaymentSecret: %s", err.message)
}

func (self NodeErrorInvalidPaymentSecret) Is(target error) bool {
	return target == ErrNodeErrorInvalidPaymentSecret
}

type NodeErrorInvalidAmount struct {
	message string
}

func NewNodeErrorInvalidAmount() *NodeError {
	return &NodeError{err: &NodeErrorInvalidAmount{}}
}

func (e NodeErrorInvalidAmount) destroy() {
}

func (err NodeErrorInvalidAmount) Error() string {
	return fmt.Sprintf("InvalidAmount: %s", err.message)
}

func (self NodeErrorInvalidAmount) Is(target error) bool {
	return target == ErrNodeErrorInvalidAmount
}

type NodeErrorInvalidInvoice struct {
	message string
}

func NewNodeErrorInvalidInvoice() *NodeError {
	return &NodeError{err: &NodeErrorInvalidInvoice{}}
}

func (e NodeErrorInvalidInvoice) destroy() {
}

func (err NodeErrorInvalidInvoice) Error() string {
	return fmt.Sprintf("InvalidInvoice: %s", err.message)
}

func (self NodeErrorInvalidInvoice) Is(target error) bool {
	return target == ErrNodeErrorInvalidInvoice
}

type NodeErrorInvalidOffer struct {
	message string
}

func NewNodeErrorInvalidOffer() *NodeError {
	return &NodeError{err: &NodeErrorInvalidOffer{}}
}

func (e NodeErrorInvalidOffer) destroy() {
}

func (err NodeErrorInvalidOffer) Error() string {
	return fmt.Sprintf("InvalidOffer: %s", err.message)
}

func (self NodeErrorInvalidOffer) Is(target error) bool {
	return target == ErrNodeErrorInvalidOffer
}

type NodeErrorInvalidRefund struct {
	message string
}

func NewNodeErrorInvalidRefund() *NodeError {
	return &NodeError{err: &NodeErrorInvalidRefund{}}
}

func (e NodeErrorInvalidRefund) destroy() {
}

func (err NodeErrorInvalidRefund) Error() string {
	return fmt.Sprintf("InvalidRefund: %s", err.message)
}

func (self NodeErrorInvalidRefund) Is(target error) bool {
	return target == ErrNodeErrorInvalidRefund
}

type NodeErrorInvalidChannelId struct {
	message string
}

func NewNodeErrorInvalidChannelId() *NodeError {
	return &NodeError{err: &NodeErrorInvalidChannelId{}}
}

func (e NodeErrorInvalidChannelId) destroy() {
}

func (err NodeErrorInvalidChannelId) Error() string {
	return fmt.Sprintf("InvalidChannelId: %s", err.message)
}

func (self NodeErrorInvalidChannelId) Is(target error) bool {
	return target == ErrNodeErrorInvalidChannelId
}

type NodeErrorInvalidNetwork struct {
	message string
}

func NewNodeErrorInvalidNetwork() *NodeError {
	return &NodeError{err: &NodeErrorInvalidNetwork{}}
}

func (e NodeErrorInvalidNetwork) destroy() {
}

func (err NodeErrorInvalidNetwork) Error() string {
	return fmt.Sprintf("InvalidNetwork: %s", err.message)
}

func (self NodeErrorInvalidNetwork) Is(target error) bool {
	return target == ErrNodeErrorInvalidNetwork
}

type NodeErrorInvalidUri struct {
	message string
}

func NewNodeErrorInvalidUri() *NodeError {
	return &NodeError{err: &NodeErrorInvalidUri{}}
}

func (e NodeErrorInvalidUri) destroy() {
}

func (err NodeErrorInvalidUri) Error() string {
	return fmt.Sprintf("InvalidUri: %s", err.message)
}

func (self NodeErrorInvalidUri) Is(target error) bool {
	return target == ErrNodeErrorInvalidUri
}

type NodeErrorInvalidQuantity struct {
	message string
}

func NewNodeErrorInvalidQuantity() *NodeError {
	return &NodeError{err: &NodeErrorInvalidQuantity{}}
}

func (e NodeErrorInvalidQuantity) destroy() {
}

func (err NodeErrorInvalidQuantity) Error() string {
	return fmt.Sprintf("InvalidQuantity: %s", err.message)
}

func (self NodeErrorInvalidQuantity) Is(target error) bool {
	return target == ErrNodeErrorInvalidQuantity
}

type NodeErrorInvalidNodeAlias struct {
	message string
}

func NewNodeErrorInvalidNodeAlias() *NodeError {
	return &NodeError{err: &NodeErrorInvalidNodeAlias{}}
}

func (e NodeErrorInvalidNodeAlias) destroy() {
}

func (err NodeErrorInvalidNodeAlias) Error() string {
	return fmt.Sprintf("InvalidNodeAlias: %s", err.message)
}

func (self NodeErrorInvalidNodeAlias) Is(target error) bool {
	return target == ErrNodeErrorInvalidNodeAlias
}

type NodeErrorInvalidDateTime struct {
	message string
}

func NewNodeErrorInvalidDateTime() *NodeError {
	return &NodeError{err: &NodeErrorInvalidDateTime{}}
}

func (e NodeErrorInvalidDateTime) destroy() {
}

func (err NodeErrorInvalidDateTime) Error() string {
	return fmt.Sprintf("InvalidDateTime: %s", err.message)
}

func (self NodeErrorInvalidDateTime) Is(target error) bool {
	return target == ErrNodeErrorInvalidDateTime
}

type NodeErrorInvalidFeeRate struct {
	message string
}

func NewNodeErrorInvalidFeeRate() *NodeError {
	return &NodeError{err: &NodeErrorInvalidFeeRate{}}
}

func (e NodeErrorInvalidFeeRate) destroy() {
}

func (err NodeErrorInvalidFeeRate) Error() string {
	return fmt.Sprintf("InvalidFeeRate: %s", err.message)
}

func (self NodeErrorInvalidFeeRate) Is(target error) bool {
	return target == ErrNodeErrorInvalidFeeRate
}

type NodeErrorInvalidScriptPubKey struct {
	message string
}

func NewNodeErrorInvalidScriptPubKey() *NodeError {
	return &NodeError{err: &NodeErrorInvalidScriptPubKey{}}
}

func (e NodeErrorInvalidScriptPubKey) destroy() {
}

func (err NodeErrorInvalidScriptPubKey) Error() string {
	return fmt.Sprintf("InvalidScriptPubKey: %s", err.message)
}

func (self NodeErrorInvalidScriptPubKey) Is(target error) bool {
	return target == ErrNodeErrorInvalidScriptPubKey
}

type NodeErrorDuplicatePayment struct {
	message string
}

func NewNodeErrorDuplicatePayment() *NodeError {
	return &NodeError{err: &NodeErrorDuplicatePayment{}}
}

func (e NodeErrorDuplicatePayment) destroy() {
}

func (err NodeErrorDuplicatePayment) Error() string {
	return fmt.Sprintf("DuplicatePayment: %s", err.message)
}

func (self NodeErrorDuplicatePayment) Is(target error) bool {
	return target == ErrNodeErrorDuplicatePayment
}

type NodeErrorUnsupportedCurrency struct {
	message string
}

func NewNodeErrorUnsupportedCurrency() *NodeError {
	return &NodeError{err: &NodeErrorUnsupportedCurrency{}}
}

func (e NodeErrorUnsupportedCurrency) destroy() {
}

func (err NodeErrorUnsupportedCurrency) Error() string {
	return fmt.Sprintf("UnsupportedCurrency: %s", err.message)
}

func (self NodeErrorUnsupportedCurrency) Is(target error) bool {
	return target == ErrNodeErrorUnsupportedCurrency
}

type NodeErrorInsufficientFunds struct {
	message string
}

func NewNodeErrorInsufficientFunds() *NodeError {
	return &NodeError{err: &NodeErrorInsufficientFunds{}}
}

func (e NodeErrorInsufficientFunds) destroy() {
}

func (err NodeErrorInsufficientFunds) Error() string {
	return fmt.Sprintf("InsufficientFunds: %s", err.message)
}

func (self NodeErrorInsufficientFunds) Is(target error) bool {
	return target == ErrNodeErrorInsufficientFunds
}

type NodeErrorLiquiditySourceUnavailable struct {
	message string
}

func NewNodeErrorLiquiditySourceUnavailable() *NodeError {
	return &NodeError{err: &NodeErrorLiquiditySourceUnavailable{}}
}

func (e NodeErrorLiquiditySourceUnavailable) destroy() {
}

func (err NodeErrorLiquiditySourceUnavailable) Error() string {
	return fmt.Sprintf("LiquiditySourceUnavailable: %s", err.message)
}

func (self NodeErrorLiquiditySourceUnavailable) Is(target error) bool {
	return target == ErrNodeErrorLiquiditySourceUnavailable
}

type NodeErrorLiquidityFeeTooHigh struct {
	message string
}

func NewNodeErrorLiquidityFeeTooHigh() *NodeError {
	return &NodeError{err: &NodeErrorLiquidityFeeTooHigh{}}
}

func (e NodeErrorLiquidityFeeTooHigh) destroy() {
}

func (err NodeErrorLiquidityFeeTooHigh) Error() string {
	return fmt.Sprintf("LiquidityFeeTooHigh: %s", err.message)
}

func (self NodeErrorLiquidityFeeTooHigh) Is(target error) bool {
	return target == ErrNodeErrorLiquidityFeeTooHigh
}

type NodeErrorInvalidBlindedPaths struct {
	message string
}

func NewNodeErrorInvalidBlindedPaths() *NodeError {
	return &NodeError{err: &NodeErrorInvalidBlindedPaths{}}
}

func (e NodeErrorInvalidBlindedPaths) destroy() {
}

func (err NodeErrorInvalidBlindedPaths) Error() string {
	return fmt.Sprintf("InvalidBlindedPaths: %s", err.message)
}

func (self NodeErrorInvalidBlindedPaths) Is(target error) bool {
	return target == ErrNodeErrorInvalidBlindedPaths
}

type NodeErrorAsyncPaymentServicesDisabled struct {
	message string
}

func NewNodeErrorAsyncPaymentServicesDisabled() *NodeError {
	return &NodeError{err: &NodeErrorAsyncPaymentServicesDisabled{}}
}

func (e NodeErrorAsyncPaymentServicesDisabled) destroy() {
}

func (err NodeErrorAsyncPaymentServicesDisabled) Error() string {
	return fmt.Sprintf("AsyncPaymentServicesDisabled: %s", err.message)
}

func (self NodeErrorAsyncPaymentServicesDisabled) Is(target error) bool {
	return target == ErrNodeErrorAsyncPaymentServicesDisabled
}

type NodeErrorHrnParsingFailed struct {
	message string
}

func NewNodeErrorHrnParsingFailed() *NodeError {
	return &NodeError{err: &NodeErrorHrnParsingFailed{}}
}

func (e NodeErrorHrnParsingFailed) destroy() {
}

func (err NodeErrorHrnParsingFailed) Error() string {
	return fmt.Sprintf("HrnParsingFailed: %s", err.message)
}

func (self NodeErrorHrnParsingFailed) Is(target error) bool {
	return target == ErrNodeErrorHrnParsingFailed
}

type NodeErrorLnurlAuthFailed struct {
	message string
}

func NewNodeErrorLnurlAuthFailed() *NodeError {
	return &NodeError{err: &NodeErrorLnurlAuthFailed{}}
}

func (e NodeErrorLnurlAuthFailed) destroy() {
}

func (err NodeErrorLnurlAuthFailed) Error() string {
	return fmt.Sprintf("LnurlAuthFailed: %s", err.message)
}

func (self NodeErrorLnurlAuthFailed) Is(target error) bool {
	return target == ErrNodeErrorLnurlAuthFailed
}

type NodeErrorLnurlAuthTimeout struct {
	message string
}

func NewNodeErrorLnurlAuthTimeout() *NodeError {
	return &NodeError{err: &NodeErrorLnurlAuthTimeout{}}
}

func (e NodeErrorLnurlAuthTimeout) destroy() {
}

func (err NodeErrorLnurlAuthTimeout) Error() string {
	return fmt.Sprintf("LnurlAuthTimeout: %s", err.message)
}

func (self NodeErrorLnurlAuthTimeout) Is(target error) bool {
	return target == ErrNodeErrorLnurlAuthTimeout
}

type NodeErrorInvalidLnurl struct {
	message string
}

func NewNodeErrorInvalidLnurl() *NodeError {
	return &NodeError{err: &NodeErrorInvalidLnurl{}}
}

func (e NodeErrorInvalidLnurl) destroy() {
}

func (err NodeErrorInvalidLnurl) Error() string {
	return fmt.Sprintf("InvalidLnurl: %s", err.message)
}

func (self NodeErrorInvalidLnurl) Is(target error) bool {
	return target == ErrNodeErrorInvalidLnurl
}

type FfiConverterNodeError struct{}

var FfiConverterNodeErrorINSTANCE = FfiConverterNodeError{}

func (c FfiConverterNodeError) Lift(eb RustBufferI) *NodeError {
	return LiftFromRustBuffer[*NodeError](c, eb)
}

func (c FfiConverterNodeError) Lower(value *NodeError) C.RustBuffer {
	return LowerIntoRustBuffer[*NodeError](c, value)
}

func (c FfiConverterNodeError) LowerExternal(value *NodeError) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*NodeError](c, value))
}

func (c FfiConverterNodeError) Read(reader io.Reader) *NodeError {
	errorID := readUint32(reader)

	message := FfiConverterStringINSTANCE.Read(reader)
	switch errorID {
	case 1:
		return &NodeError{&NodeErrorAlreadyRunning{message}}
	case 2:
		return &NodeError{&NodeErrorNotRunning{message}}
	case 3:
		return &NodeError{&NodeErrorOnchainTxCreationFailed{message}}
	case 4:
		return &NodeError{&NodeErrorConnectionFailed{message}}
	case 5:
		return &NodeError{&NodeErrorInvoiceCreationFailed{message}}
	case 6:
		return &NodeError{&NodeErrorInvoiceRequestCreationFailed{message}}
	case 7:
		return &NodeError{&NodeErrorOfferCreationFailed{message}}
	case 8:
		return &NodeError{&NodeErrorRefundCreationFailed{message}}
	case 9:
		return &NodeError{&NodeErrorPaymentSendingFailed{message}}
	case 10:
		return &NodeError{&NodeErrorInvalidCustomTlvs{message}}
	case 11:
		return &NodeError{&NodeErrorProbeSendingFailed{message}}
	case 12:
		return &NodeError{&NodeErrorChannelCreationFailed{message}}
	case 13:
		return &NodeError{&NodeErrorChannelClosingFailed{message}}
	case 14:
		return &NodeError{&NodeErrorChannelSplicingFailed{message}}
	case 15:
		return &NodeError{&NodeErrorChannelConfigUpdateFailed{message}}
	case 16:
		return &NodeError{&NodeErrorPersistenceFailed{message}}
	case 17:
		return &NodeError{&NodeErrorFeerateEstimationUpdateFailed{message}}
	case 18:
		return &NodeError{&NodeErrorFeerateEstimationUpdateTimeout{message}}
	case 19:
		return &NodeError{&NodeErrorWalletOperationFailed{message}}
	case 20:
		return &NodeError{&NodeErrorWalletOperationTimeout{message}}
	case 21:
		return &NodeError{&NodeErrorOnchainTxSigningFailed{message}}
	case 22:
		return &NodeError{&NodeErrorTxSyncFailed{message}}
	case 23:
		return &NodeError{&NodeErrorTxSyncTimeout{message}}
	case 24:
		return &NodeError{&NodeErrorGossipUpdateFailed{message}}
	case 25:
		return &NodeError{&NodeErrorGossipUpdateTimeout{message}}
	case 26:
		return &NodeError{&NodeErrorLiquidityRequestFailed{message}}
	case 27:
		return &NodeError{&NodeErrorUriParameterParsingFailed{message}}
	case 28:
		return &NodeError{&NodeErrorInvalidAddress{message}}
	case 29:
		return &NodeError{&NodeErrorInvalidSocketAddress{message}}
	case 30:
		return &NodeError{&NodeErrorInvalidPublicKey{message}}
	case 31:
		return &NodeError{&NodeErrorInvalidSecretKey{message}}
	case 32:
		return &NodeError{&NodeErrorInvalidOfferId{message}}
	case 33:
		return &NodeError{&NodeErrorInvalidNodeId{message}}
	case 34:
		return &NodeError{&NodeErrorInvalidPaymentId{message}}
	case 35:
		return &NodeError{&NodeErrorInvalidPaymentHash{message}}
	case 36:
		return &NodeError{&NodeErrorInvalidPaymentPreimage{message}}
	case 37:
		return &NodeError{&NodeErrorInvalidPaymentSecret{message}}
	case 38:
		return &NodeError{&NodeErrorInvalidAmount{message}}
	case 39:
		return &NodeError{&NodeErrorInvalidInvoice{message}}
	case 40:
		return &NodeError{&NodeErrorInvalidOffer{message}}
	case 41:
		return &NodeError{&NodeErrorInvalidRefund{message}}
	case 42:
		return &NodeError{&NodeErrorInvalidChannelId{message}}
	case 43:
		return &NodeError{&NodeErrorInvalidNetwork{message}}
	case 44:
		return &NodeError{&NodeErrorInvalidUri{message}}
	case 45:
		return &NodeError{&NodeErrorInvalidQuantity{message}}
	case 46:
		return &NodeError{&NodeErrorInvalidNodeAlias{message}}
	case 47:
		return &NodeError{&NodeErrorInvalidDateTime{message}}
	case 48:
		return &NodeError{&NodeErrorInvalidFeeRate{message}}
	case 49:
		return &NodeError{&NodeErrorInvalidScriptPubKey{message}}
	case 50:
		return &NodeError{&NodeErrorDuplicatePayment{message}}
	case 51:
		return &NodeError{&NodeErrorUnsupportedCurrency{message}}
	case 52:
		return &NodeError{&NodeErrorInsufficientFunds{message}}
	case 53:
		return &NodeError{&NodeErrorLiquiditySourceUnavailable{message}}
	case 54:
		return &NodeError{&NodeErrorLiquidityFeeTooHigh{message}}
	case 55:
		return &NodeError{&NodeErrorInvalidBlindedPaths{message}}
	case 56:
		return &NodeError{&NodeErrorAsyncPaymentServicesDisabled{message}}
	case 57:
		return &NodeError{&NodeErrorHrnParsingFailed{message}}
	case 58:
		return &NodeError{&NodeErrorLnurlAuthFailed{message}}
	case 59:
		return &NodeError{&NodeErrorLnurlAuthTimeout{message}}
	case 60:
		return &NodeError{&NodeErrorInvalidLnurl{message}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterNodeError.Read()", errorID))
	}

}

func (c FfiConverterNodeError) Write(writer io.Writer, value *NodeError) {
	switch variantValue := value.err.(type) {
	case *NodeErrorAlreadyRunning:
		writeInt32(writer, 1)
	case *NodeErrorNotRunning:
		writeInt32(writer, 2)
	case *NodeErrorOnchainTxCreationFailed:
		writeInt32(writer, 3)
	case *NodeErrorConnectionFailed:
		writeInt32(writer, 4)
	case *NodeErrorInvoiceCreationFailed:
		writeInt32(writer, 5)
	case *NodeErrorInvoiceRequestCreationFailed:
		writeInt32(writer, 6)
	case *NodeErrorOfferCreationFailed:
		writeInt32(writer, 7)
	case *NodeErrorRefundCreationFailed:
		writeInt32(writer, 8)
	case *NodeErrorPaymentSendingFailed:
		writeInt32(writer, 9)
	case *NodeErrorInvalidCustomTlvs:
		writeInt32(writer, 10)
	case *NodeErrorProbeSendingFailed:
		writeInt32(writer, 11)
	case *NodeErrorChannelCreationFailed:
		writeInt32(writer, 12)
	case *NodeErrorChannelClosingFailed:
		writeInt32(writer, 13)
	case *NodeErrorChannelSplicingFailed:
		writeInt32(writer, 14)
	case *NodeErrorChannelConfigUpdateFailed:
		writeInt32(writer, 15)
	case *NodeErrorPersistenceFailed:
		writeInt32(writer, 16)
	case *NodeErrorFeerateEstimationUpdateFailed:
		writeInt32(writer, 17)
	case *NodeErrorFeerateEstimationUpdateTimeout:
		writeInt32(writer, 18)
	case *NodeErrorWalletOperationFailed:
		writeInt32(writer, 19)
	case *NodeErrorWalletOperationTimeout:
		writeInt32(writer, 20)
	case *NodeErrorOnchainTxSigningFailed:
		writeInt32(writer, 21)
	case *NodeErrorTxSyncFailed:
		writeInt32(writer, 22)
	case *NodeErrorTxSyncTimeout:
		writeInt32(writer, 23)
	case *NodeErrorGossipUpdateFailed:
		writeInt32(writer, 24)
	case *NodeErrorGossipUpdateTimeout:
		writeInt32(writer, 25)
	case *NodeErrorLiquidityRequestFailed:
		writeInt32(writer, 26)
	case *NodeErrorUriParameterParsingFailed:
		writeInt32(writer, 27)
	case *NodeErrorInvalidAddress:
		writeInt32(writer, 28)
	case *NodeErrorInvalidSocketAddress:
		writeInt32(writer, 29)
	case *NodeErrorInvalidPublicKey:
		writeInt32(writer, 30)
	case *NodeErrorInvalidSecretKey:
		writeInt32(writer, 31)
	case *NodeErrorInvalidOfferId:
		writeInt32(writer, 32)
	case *NodeErrorInvalidNodeId:
		writeInt32(writer, 33)
	case *NodeErrorInvalidPaymentId:
		writeInt32(writer, 34)
	case *NodeErrorInvalidPaymentHash:
		writeInt32(writer, 35)
	case *NodeErrorInvalidPaymentPreimage:
		writeInt32(writer, 36)
	case *NodeErrorInvalidPaymentSecret:
		writeInt32(writer, 37)
	case *NodeErrorInvalidAmount:
		writeInt32(writer, 38)
	case *NodeErrorInvalidInvoice:
		writeInt32(writer, 39)
	case *NodeErrorInvalidOffer:
		writeInt32(writer, 40)
	case *NodeErrorInvalidRefund:
		writeInt32(writer, 41)
	case *NodeErrorInvalidChannelId:
		writeInt32(writer, 42)
	case *NodeErrorInvalidNetwork:
		writeInt32(writer, 43)
	case *NodeErrorInvalidUri:
		writeInt32(writer, 44)
	case *NodeErrorInvalidQuantity:
		writeInt32(writer, 45)
	case *NodeErrorInvalidNodeAlias:
		writeInt32(writer, 46)
	case *NodeErrorInvalidDateTime:
		writeInt32(writer, 47)
	case *NodeErrorInvalidFeeRate:
		writeInt32(writer, 48)
	case *NodeErrorInvalidScriptPubKey:
		writeInt32(writer, 49)
	case *NodeErrorDuplicatePayment:
		writeInt32(writer, 50)
	case *NodeErrorUnsupportedCurrency:
		writeInt32(writer, 51)
	case *NodeErrorInsufficientFunds:
		writeInt32(writer, 52)
	case *NodeErrorLiquiditySourceUnavailable:
		writeInt32(writer, 53)
	case *NodeErrorLiquidityFeeTooHigh:
		writeInt32(writer, 54)
	case *NodeErrorInvalidBlindedPaths:
		writeInt32(writer, 55)
	case *NodeErrorAsyncPaymentServicesDisabled:
		writeInt32(writer, 56)
	case *NodeErrorHrnParsingFailed:
		writeInt32(writer, 57)
	case *NodeErrorLnurlAuthFailed:
		writeInt32(writer, 58)
	case *NodeErrorLnurlAuthTimeout:
		writeInt32(writer, 59)
	case *NodeErrorInvalidLnurl:
		writeInt32(writer, 60)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterNodeError.Write", value))
	}
}

type FfiDestroyerNodeError struct{}

func (_ FfiDestroyerNodeError) Destroy(value *NodeError) {
	switch variantValue := value.err.(type) {
	case NodeErrorAlreadyRunning:
		variantValue.destroy()
	case NodeErrorNotRunning:
		variantValue.destroy()
	case NodeErrorOnchainTxCreationFailed:
		variantValue.destroy()
	case NodeErrorConnectionFailed:
		variantValue.destroy()
	case NodeErrorInvoiceCreationFailed:
		variantValue.destroy()
	case NodeErrorInvoiceRequestCreationFailed:
		variantValue.destroy()
	case NodeErrorOfferCreationFailed:
		variantValue.destroy()
	case NodeErrorRefundCreationFailed:
		variantValue.destroy()
	case NodeErrorPaymentSendingFailed:
		variantValue.destroy()
	case NodeErrorInvalidCustomTlvs:
		variantValue.destroy()
	case NodeErrorProbeSendingFailed:
		variantValue.destroy()
	case NodeErrorChannelCreationFailed:
		variantValue.destroy()
	case NodeErrorChannelClosingFailed:
		variantValue.destroy()
	case NodeErrorChannelSplicingFailed:
		variantValue.destroy()
	case NodeErrorChannelConfigUpdateFailed:
		variantValue.destroy()
	case NodeErrorPersistenceFailed:
		variantValue.destroy()
	case NodeErrorFeerateEstimationUpdateFailed:
		variantValue.destroy()
	case NodeErrorFeerateEstimationUpdateTimeout:
		variantValue.destroy()
	case NodeErrorWalletOperationFailed:
		variantValue.destroy()
	case NodeErrorWalletOperationTimeout:
		variantValue.destroy()
	case NodeErrorOnchainTxSigningFailed:
		variantValue.destroy()
	case NodeErrorTxSyncFailed:
		variantValue.destroy()
	case NodeErrorTxSyncTimeout:
		variantValue.destroy()
	case NodeErrorGossipUpdateFailed:
		variantValue.destroy()
	case NodeErrorGossipUpdateTimeout:
		variantValue.destroy()
	case NodeErrorLiquidityRequestFailed:
		variantValue.destroy()
	case NodeErrorUriParameterParsingFailed:
		variantValue.destroy()
	case NodeErrorInvalidAddress:
		variantValue.destroy()
	case NodeErrorInvalidSocketAddress:
		variantValue.destroy()
	case NodeErrorInvalidPublicKey:
		variantValue.destroy()
	case NodeErrorInvalidSecretKey:
		variantValue.destroy()
	case NodeErrorInvalidOfferId:
		variantValue.destroy()
	case NodeErrorInvalidNodeId:
		variantValue.destroy()
	case NodeErrorInvalidPaymentId:
		variantValue.destroy()
	case NodeErrorInvalidPaymentHash:
		variantValue.destroy()
	case NodeErrorInvalidPaymentPreimage:
		variantValue.destroy()
	case NodeErrorInvalidPaymentSecret:
		variantValue.destroy()
	case NodeErrorInvalidAmount:
		variantValue.destroy()
	case NodeErrorInvalidInvoice:
		variantValue.destroy()
	case NodeErrorInvalidOffer:
		variantValue.destroy()
	case NodeErrorInvalidRefund:
		variantValue.destroy()
	case NodeErrorInvalidChannelId:
		variantValue.destroy()
	case NodeErrorInvalidNetwork:
		variantValue.destroy()
	case NodeErrorInvalidUri:
		variantValue.destroy()
	case NodeErrorInvalidQuantity:
		variantValue.destroy()
	case NodeErrorInvalidNodeAlias:
		variantValue.destroy()
	case NodeErrorInvalidDateTime:
		variantValue.destroy()
	case NodeErrorInvalidFeeRate:
		variantValue.destroy()
	case NodeErrorInvalidScriptPubKey:
		variantValue.destroy()
	case NodeErrorDuplicatePayment:
		variantValue.destroy()
	case NodeErrorUnsupportedCurrency:
		variantValue.destroy()
	case NodeErrorInsufficientFunds:
		variantValue.destroy()
	case NodeErrorLiquiditySourceUnavailable:
		variantValue.destroy()
	case NodeErrorLiquidityFeeTooHigh:
		variantValue.destroy()
	case NodeErrorInvalidBlindedPaths:
		variantValue.destroy()
	case NodeErrorAsyncPaymentServicesDisabled:
		variantValue.destroy()
	case NodeErrorHrnParsingFailed:
		variantValue.destroy()
	case NodeErrorLnurlAuthFailed:
		variantValue.destroy()
	case NodeErrorLnurlAuthTimeout:
		variantValue.destroy()
	case NodeErrorInvalidLnurl:
		variantValue.destroy()
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiDestroyerNodeError.Destroy", value))
	}
}

type OfferAmount interface {
	Destroy()
}
type OfferAmountBitcoin struct {
	AmountMsats uint64
}

func (e OfferAmountBitcoin) Destroy() {
	FfiDestroyerUint64{}.Destroy(e.AmountMsats)
}

type OfferAmountCurrency struct {
	Iso4217Code string
	Amount      uint64
}

func (e OfferAmountCurrency) Destroy() {
	FfiDestroyerString{}.Destroy(e.Iso4217Code)
	FfiDestroyerUint64{}.Destroy(e.Amount)
}

type FfiConverterOfferAmount struct{}

var FfiConverterOfferAmountINSTANCE = FfiConverterOfferAmount{}

func (c FfiConverterOfferAmount) Lift(rb RustBufferI) OfferAmount {
	return LiftFromRustBuffer[OfferAmount](c, rb)
}

func (c FfiConverterOfferAmount) Lower(value OfferAmount) C.RustBuffer {
	return LowerIntoRustBuffer[OfferAmount](c, value)
}

func (c FfiConverterOfferAmount) LowerExternal(value OfferAmount) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[OfferAmount](c, value))
}
func (FfiConverterOfferAmount) Read(reader io.Reader) OfferAmount {
	id := readInt32(reader)
	switch id {
	case 1:
		return OfferAmountBitcoin{
			FfiConverterUint64INSTANCE.Read(reader),
		}
	case 2:
		return OfferAmountCurrency{
			FfiConverterStringINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterOfferAmount.Read()", id))
	}
}

func (FfiConverterOfferAmount) Write(writer io.Writer, value OfferAmount) {
	switch variant_value := value.(type) {
	case OfferAmountBitcoin:
		writeInt32(writer, 1)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountMsats)
	case OfferAmountCurrency:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variant_value.Iso4217Code)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.Amount)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterOfferAmount.Write", value))
	}
}

type FfiDestroyerOfferAmount struct{}

func (_ FfiDestroyerOfferAmount) Destroy(value OfferAmount) {
	value.Destroy()
}

// The BOLT12 invoice that was paid, surfaced in [`Event::PaymentSuccessful`].
//
// [`Event::PaymentSuccessful`]: crate::Event::PaymentSuccessful
type PaidBolt12Invoice interface {
	Destroy()
}

// The BOLT12 invoice, allowing the user to perform proof of payment.
type PaidBolt12InvoiceBolt12 struct {
	Field0 *Bolt12Invoice
}

func (e PaidBolt12InvoiceBolt12) Destroy() {
	FfiDestroyerBolt12Invoice{}.Destroy(e.Field0)
}

// The static invoice, used in async payments, where the user cannot perform proof of
// payment.
type PaidBolt12InvoiceStatic struct {
	Field0 *StaticInvoice
}

func (e PaidBolt12InvoiceStatic) Destroy() {
	FfiDestroyerStaticInvoice{}.Destroy(e.Field0)
}

type FfiConverterPaidBolt12Invoice struct{}

var FfiConverterPaidBolt12InvoiceINSTANCE = FfiConverterPaidBolt12Invoice{}

func (c FfiConverterPaidBolt12Invoice) Lift(rb RustBufferI) PaidBolt12Invoice {
	return LiftFromRustBuffer[PaidBolt12Invoice](c, rb)
}

func (c FfiConverterPaidBolt12Invoice) Lower(value PaidBolt12Invoice) C.RustBuffer {
	return LowerIntoRustBuffer[PaidBolt12Invoice](c, value)
}

func (c FfiConverterPaidBolt12Invoice) LowerExternal(value PaidBolt12Invoice) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PaidBolt12Invoice](c, value))
}
func (FfiConverterPaidBolt12Invoice) Read(reader io.Reader) PaidBolt12Invoice {
	id := readInt32(reader)
	switch id {
	case 1:
		return PaidBolt12InvoiceBolt12{
			FfiConverterBolt12InvoiceINSTANCE.Read(reader),
		}
	case 2:
		return PaidBolt12InvoiceStatic{
			FfiConverterStaticInvoiceINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterPaidBolt12Invoice.Read()", id))
	}
}

func (FfiConverterPaidBolt12Invoice) Write(writer io.Writer, value PaidBolt12Invoice) {
	switch variant_value := value.(type) {
	case PaidBolt12InvoiceBolt12:
		writeInt32(writer, 1)
		FfiConverterBolt12InvoiceINSTANCE.Write(writer, variant_value.Field0)
	case PaidBolt12InvoiceStatic:
		writeInt32(writer, 2)
		FfiConverterStaticInvoiceINSTANCE.Write(writer, variant_value.Field0)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterPaidBolt12Invoice.Write", value))
	}
}

type FfiDestroyerPaidBolt12Invoice struct{}

func (_ FfiDestroyerPaidBolt12Invoice) Destroy(value PaidBolt12Invoice) {
	value.Destroy()
}

// Represents the direction of a payment.
type PaymentDirection uint

const (
	// The payment is inbound.
	PaymentDirectionInbound PaymentDirection = 1
	// The payment is outbound.
	PaymentDirectionOutbound PaymentDirection = 2
)

type FfiConverterPaymentDirection struct{}

var FfiConverterPaymentDirectionINSTANCE = FfiConverterPaymentDirection{}

func (c FfiConverterPaymentDirection) Lift(rb RustBufferI) PaymentDirection {
	return LiftFromRustBuffer[PaymentDirection](c, rb)
}

func (c FfiConverterPaymentDirection) Lower(value PaymentDirection) C.RustBuffer {
	return LowerIntoRustBuffer[PaymentDirection](c, value)
}

func (c FfiConverterPaymentDirection) LowerExternal(value PaymentDirection) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PaymentDirection](c, value))
}
func (FfiConverterPaymentDirection) Read(reader io.Reader) PaymentDirection {
	id := readInt32(reader)
	return PaymentDirection(id)
}

func (FfiConverterPaymentDirection) Write(writer io.Writer, value PaymentDirection) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerPaymentDirection struct{}

func (_ FfiDestroyerPaymentDirection) Destroy(value PaymentDirection) {
}

type PaymentFailureReason uint

const (
	PaymentFailureReasonRecipientRejected         PaymentFailureReason = 1
	PaymentFailureReasonUserAbandoned             PaymentFailureReason = 2
	PaymentFailureReasonRetriesExhausted          PaymentFailureReason = 3
	PaymentFailureReasonPaymentExpired            PaymentFailureReason = 4
	PaymentFailureReasonRouteNotFound             PaymentFailureReason = 5
	PaymentFailureReasonUnexpectedError           PaymentFailureReason = 6
	PaymentFailureReasonUnknownRequiredFeatures   PaymentFailureReason = 7
	PaymentFailureReasonInvoiceRequestExpired     PaymentFailureReason = 8
	PaymentFailureReasonInvoiceRequestRejected    PaymentFailureReason = 9
	PaymentFailureReasonBlindedPathCreationFailed PaymentFailureReason = 10
)

type FfiConverterPaymentFailureReason struct{}

var FfiConverterPaymentFailureReasonINSTANCE = FfiConverterPaymentFailureReason{}

func (c FfiConverterPaymentFailureReason) Lift(rb RustBufferI) PaymentFailureReason {
	return LiftFromRustBuffer[PaymentFailureReason](c, rb)
}

func (c FfiConverterPaymentFailureReason) Lower(value PaymentFailureReason) C.RustBuffer {
	return LowerIntoRustBuffer[PaymentFailureReason](c, value)
}

func (c FfiConverterPaymentFailureReason) LowerExternal(value PaymentFailureReason) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PaymentFailureReason](c, value))
}
func (FfiConverterPaymentFailureReason) Read(reader io.Reader) PaymentFailureReason {
	id := readInt32(reader)
	return PaymentFailureReason(id)
}

func (FfiConverterPaymentFailureReason) Write(writer io.Writer, value PaymentFailureReason) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerPaymentFailureReason struct{}

func (_ FfiDestroyerPaymentFailureReason) Destroy(value PaymentFailureReason) {
}

// Represents the kind of a payment.
type PaymentKind interface {
	Destroy()
}

// An on-chain payment.
//
// Payments of this kind will be considered pending until the respective transaction has
// reached [`ANTI_REORG_DELAY`] confirmations on-chain.
//
// [`ANTI_REORG_DELAY`]: lightning::chain::channelmonitor::ANTI_REORG_DELAY
type PaymentKindOnchain struct {
	Txid   Txid
	Status ConfirmationStatus
}

func (e PaymentKindOnchain) Destroy() {
	FfiDestroyerTypeTxid{}.Destroy(e.Txid)
	FfiDestroyerConfirmationStatus{}.Destroy(e.Status)
}

// A [BOLT 11] payment.
//
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
type PaymentKindBolt11 struct {
	Hash     PaymentHash
	Preimage *PaymentPreimage
	Secret   *PaymentSecret
}

func (e PaymentKindBolt11) Destroy() {
	FfiDestroyerTypePaymentHash{}.Destroy(e.Hash)
	FfiDestroyerOptionalTypePaymentPreimage{}.Destroy(e.Preimage)
	FfiDestroyerOptionalTypePaymentSecret{}.Destroy(e.Secret)
}

// A [BOLT 11] payment intended to open an [bLIP-52 / LSPS 2] just-in-time channel.
//
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
//
// [bLIP-52 / LSPS2]: https://github.com/lightning/blips/blob/master/blip-0052.md
type PaymentKindBolt11Jit struct {
	Hash                       PaymentHash
	Preimage                   *PaymentPreimage
	Secret                     *PaymentSecret
	CounterpartySkimmedFeeMsat *uint64
	LspFeeLimits               LspFeeLimits
}

func (e PaymentKindBolt11Jit) Destroy() {
	FfiDestroyerTypePaymentHash{}.Destroy(e.Hash)
	FfiDestroyerOptionalTypePaymentPreimage{}.Destroy(e.Preimage)
	FfiDestroyerOptionalTypePaymentSecret{}.Destroy(e.Secret)
	FfiDestroyerOptionalUint64{}.Destroy(e.CounterpartySkimmedFeeMsat)
	FfiDestroyerLspFeeLimits{}.Destroy(e.LspFeeLimits)
}

// A [BOLT 12] 'offer' payment, i.e., a payment for an [`Offer`].
//
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
// [`Offer`]: crate::lightning::offers::offer::Offer
type PaymentKindBolt12Offer struct {
	Hash      *PaymentHash
	Preimage  *PaymentPreimage
	Secret    *PaymentSecret
	OfferId   OfferId
	PayerNote *UntrustedString
	Quantity  *uint64
}

func (e PaymentKindBolt12Offer) Destroy() {
	FfiDestroyerOptionalTypePaymentHash{}.Destroy(e.Hash)
	FfiDestroyerOptionalTypePaymentPreimage{}.Destroy(e.Preimage)
	FfiDestroyerOptionalTypePaymentSecret{}.Destroy(e.Secret)
	FfiDestroyerTypeOfferId{}.Destroy(e.OfferId)
	FfiDestroyerOptionalTypeUntrustedString{}.Destroy(e.PayerNote)
	FfiDestroyerOptionalUint64{}.Destroy(e.Quantity)
}

// A [BOLT 12] 'refund' payment, i.e., a payment for a [`Refund`].
//
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
// [`Refund`]: lightning::offers::refund::Refund
type PaymentKindBolt12Refund struct {
	Hash      *PaymentHash
	Preimage  *PaymentPreimage
	Secret    *PaymentSecret
	PayerNote *UntrustedString
	Quantity  *uint64
}

func (e PaymentKindBolt12Refund) Destroy() {
	FfiDestroyerOptionalTypePaymentHash{}.Destroy(e.Hash)
	FfiDestroyerOptionalTypePaymentPreimage{}.Destroy(e.Preimage)
	FfiDestroyerOptionalTypePaymentSecret{}.Destroy(e.Secret)
	FfiDestroyerOptionalTypeUntrustedString{}.Destroy(e.PayerNote)
	FfiDestroyerOptionalUint64{}.Destroy(e.Quantity)
}

// A spontaneous ("keysend") payment.
type PaymentKindSpontaneous struct {
	Hash     PaymentHash
	Preimage *PaymentPreimage
}

func (e PaymentKindSpontaneous) Destroy() {
	FfiDestroyerTypePaymentHash{}.Destroy(e.Hash)
	FfiDestroyerOptionalTypePaymentPreimage{}.Destroy(e.Preimage)
}

type FfiConverterPaymentKind struct{}

var FfiConverterPaymentKindINSTANCE = FfiConverterPaymentKind{}

func (c FfiConverterPaymentKind) Lift(rb RustBufferI) PaymentKind {
	return LiftFromRustBuffer[PaymentKind](c, rb)
}

func (c FfiConverterPaymentKind) Lower(value PaymentKind) C.RustBuffer {
	return LowerIntoRustBuffer[PaymentKind](c, value)
}

func (c FfiConverterPaymentKind) LowerExternal(value PaymentKind) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PaymentKind](c, value))
}
func (FfiConverterPaymentKind) Read(reader io.Reader) PaymentKind {
	id := readInt32(reader)
	switch id {
	case 1:
		return PaymentKindOnchain{
			FfiConverterTypeTxidINSTANCE.Read(reader),
			FfiConverterConfirmationStatusINSTANCE.Read(reader),
		}
	case 2:
		return PaymentKindBolt11{
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentPreimageINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentSecretINSTANCE.Read(reader),
		}
	case 3:
		return PaymentKindBolt11Jit{
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentPreimageINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentSecretINSTANCE.Read(reader),
			FfiConverterOptionalUint64INSTANCE.Read(reader),
			FfiConverterLspFeeLimitsINSTANCE.Read(reader),
		}
	case 4:
		return PaymentKindBolt12Offer{
			FfiConverterOptionalTypePaymentHashINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentPreimageINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentSecretINSTANCE.Read(reader),
			FfiConverterTypeOfferIdINSTANCE.Read(reader),
			FfiConverterOptionalTypeUntrustedStringINSTANCE.Read(reader),
			FfiConverterOptionalUint64INSTANCE.Read(reader),
		}
	case 5:
		return PaymentKindBolt12Refund{
			FfiConverterOptionalTypePaymentHashINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentPreimageINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentSecretINSTANCE.Read(reader),
			FfiConverterOptionalTypeUntrustedStringINSTANCE.Read(reader),
			FfiConverterOptionalUint64INSTANCE.Read(reader),
		}
	case 6:
		return PaymentKindSpontaneous{
			FfiConverterTypePaymentHashINSTANCE.Read(reader),
			FfiConverterOptionalTypePaymentPreimageINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterPaymentKind.Read()", id))
	}
}

func (FfiConverterPaymentKind) Write(writer io.Writer, value PaymentKind) {
	switch variant_value := value.(type) {
	case PaymentKindOnchain:
		writeInt32(writer, 1)
		FfiConverterTypeTxidINSTANCE.Write(writer, variant_value.Txid)
		FfiConverterConfirmationStatusINSTANCE.Write(writer, variant_value.Status)
	case PaymentKindBolt11:
		writeInt32(writer, 2)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.Hash)
		FfiConverterOptionalTypePaymentPreimageINSTANCE.Write(writer, variant_value.Preimage)
		FfiConverterOptionalTypePaymentSecretINSTANCE.Write(writer, variant_value.Secret)
	case PaymentKindBolt11Jit:
		writeInt32(writer, 3)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.Hash)
		FfiConverterOptionalTypePaymentPreimageINSTANCE.Write(writer, variant_value.Preimage)
		FfiConverterOptionalTypePaymentSecretINSTANCE.Write(writer, variant_value.Secret)
		FfiConverterOptionalUint64INSTANCE.Write(writer, variant_value.CounterpartySkimmedFeeMsat)
		FfiConverterLspFeeLimitsINSTANCE.Write(writer, variant_value.LspFeeLimits)
	case PaymentKindBolt12Offer:
		writeInt32(writer, 4)
		FfiConverterOptionalTypePaymentHashINSTANCE.Write(writer, variant_value.Hash)
		FfiConverterOptionalTypePaymentPreimageINSTANCE.Write(writer, variant_value.Preimage)
		FfiConverterOptionalTypePaymentSecretINSTANCE.Write(writer, variant_value.Secret)
		FfiConverterTypeOfferIdINSTANCE.Write(writer, variant_value.OfferId)
		FfiConverterOptionalTypeUntrustedStringINSTANCE.Write(writer, variant_value.PayerNote)
		FfiConverterOptionalUint64INSTANCE.Write(writer, variant_value.Quantity)
	case PaymentKindBolt12Refund:
		writeInt32(writer, 5)
		FfiConverterOptionalTypePaymentHashINSTANCE.Write(writer, variant_value.Hash)
		FfiConverterOptionalTypePaymentPreimageINSTANCE.Write(writer, variant_value.Preimage)
		FfiConverterOptionalTypePaymentSecretINSTANCE.Write(writer, variant_value.Secret)
		FfiConverterOptionalTypeUntrustedStringINSTANCE.Write(writer, variant_value.PayerNote)
		FfiConverterOptionalUint64INSTANCE.Write(writer, variant_value.Quantity)
	case PaymentKindSpontaneous:
		writeInt32(writer, 6)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, variant_value.Hash)
		FfiConverterOptionalTypePaymentPreimageINSTANCE.Write(writer, variant_value.Preimage)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterPaymentKind.Write", value))
	}
}

type FfiDestroyerPaymentKind struct{}

func (_ FfiDestroyerPaymentKind) Destroy(value PaymentKind) {
	value.Destroy()
}

// Represents the current status of a payment.
type PaymentStatus uint

const (
	// The payment is still pending.
	PaymentStatusPending PaymentStatus = 1
	// The payment succeeded.
	PaymentStatusSucceeded PaymentStatus = 2
	// The payment failed.
	PaymentStatusFailed PaymentStatus = 3
)

type FfiConverterPaymentStatus struct{}

var FfiConverterPaymentStatusINSTANCE = FfiConverterPaymentStatus{}

func (c FfiConverterPaymentStatus) Lift(rb RustBufferI) PaymentStatus {
	return LiftFromRustBuffer[PaymentStatus](c, rb)
}

func (c FfiConverterPaymentStatus) Lower(value PaymentStatus) C.RustBuffer {
	return LowerIntoRustBuffer[PaymentStatus](c, value)
}

func (c FfiConverterPaymentStatus) LowerExternal(value PaymentStatus) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PaymentStatus](c, value))
}
func (FfiConverterPaymentStatus) Read(reader io.Reader) PaymentStatus {
	id := readInt32(reader)
	return PaymentStatus(id)
}

func (FfiConverterPaymentStatus) Write(writer io.Writer, value PaymentStatus) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerPaymentStatus struct{}

func (_ FfiDestroyerPaymentStatus) Destroy(value PaymentStatus) {
}

// Details about the status of a known balance currently being swept to our on-chain wallet.
type PendingSweepBalance interface {
	Destroy()
}

// The spendable output is about to be swept, but a spending transaction has yet to be generated and
// broadcast.
type PendingSweepBalancePendingBroadcast struct {
	ChannelId      *ChannelId
	AmountSatoshis uint64
}

func (e PendingSweepBalancePendingBroadcast) Destroy() {
	FfiDestroyerOptionalTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
}

// A spending transaction has been generated and broadcast and is awaiting confirmation
// on-chain.
type PendingSweepBalanceBroadcastAwaitingConfirmation struct {
	ChannelId             *ChannelId
	LatestBroadcastHeight uint32
	LatestSpendingTxid    Txid
	AmountSatoshis        uint64
}

func (e PendingSweepBalanceBroadcastAwaitingConfirmation) Destroy() {
	FfiDestroyerOptionalTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerUint32{}.Destroy(e.LatestBroadcastHeight)
	FfiDestroyerTypeTxid{}.Destroy(e.LatestSpendingTxid)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
}

// A spending transaction has been confirmed on-chain and is awaiting threshold confirmations.
//
// It will be pruned after reaching [`PRUNE_DELAY_BLOCKS`] confirmations.
//
// [`PRUNE_DELAY_BLOCKS`]: lightning::util::sweep::PRUNE_DELAY_BLOCKS
type PendingSweepBalanceAwaitingThresholdConfirmations struct {
	ChannelId          *ChannelId
	LatestSpendingTxid Txid
	ConfirmationHash   BlockHash
	ConfirmationHeight uint32
	AmountSatoshis     uint64
}

func (e PendingSweepBalanceAwaitingThresholdConfirmations) Destroy() {
	FfiDestroyerOptionalTypeChannelId{}.Destroy(e.ChannelId)
	FfiDestroyerTypeTxid{}.Destroy(e.LatestSpendingTxid)
	FfiDestroyerTypeBlockHash{}.Destroy(e.ConfirmationHash)
	FfiDestroyerUint32{}.Destroy(e.ConfirmationHeight)
	FfiDestroyerUint64{}.Destroy(e.AmountSatoshis)
}

type FfiConverterPendingSweepBalance struct{}

var FfiConverterPendingSweepBalanceINSTANCE = FfiConverterPendingSweepBalance{}

func (c FfiConverterPendingSweepBalance) Lift(rb RustBufferI) PendingSweepBalance {
	return LiftFromRustBuffer[PendingSweepBalance](c, rb)
}

func (c FfiConverterPendingSweepBalance) Lower(value PendingSweepBalance) C.RustBuffer {
	return LowerIntoRustBuffer[PendingSweepBalance](c, value)
}

func (c FfiConverterPendingSweepBalance) LowerExternal(value PendingSweepBalance) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[PendingSweepBalance](c, value))
}
func (FfiConverterPendingSweepBalance) Read(reader io.Reader) PendingSweepBalance {
	id := readInt32(reader)
	switch id {
	case 1:
		return PendingSweepBalancePendingBroadcast{
			FfiConverterOptionalTypeChannelIdINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
		}
	case 2:
		return PendingSweepBalanceBroadcastAwaitingConfirmation{
			FfiConverterOptionalTypeChannelIdINSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterTypeTxidINSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
		}
	case 3:
		return PendingSweepBalanceAwaitingThresholdConfirmations{
			FfiConverterOptionalTypeChannelIdINSTANCE.Read(reader),
			FfiConverterTypeTxidINSTANCE.Read(reader),
			FfiConverterTypeBlockHashINSTANCE.Read(reader),
			FfiConverterUint32INSTANCE.Read(reader),
			FfiConverterUint64INSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterPendingSweepBalance.Read()", id))
	}
}

func (FfiConverterPendingSweepBalance) Write(writer io.Writer, value PendingSweepBalance) {
	switch variant_value := value.(type) {
	case PendingSweepBalancePendingBroadcast:
		writeInt32(writer, 1)
		FfiConverterOptionalTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
	case PendingSweepBalanceBroadcastAwaitingConfirmation:
		writeInt32(writer, 2)
		FfiConverterOptionalTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.LatestBroadcastHeight)
		FfiConverterTypeTxidINSTANCE.Write(writer, variant_value.LatestSpendingTxid)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
	case PendingSweepBalanceAwaitingThresholdConfirmations:
		writeInt32(writer, 3)
		FfiConverterOptionalTypeChannelIdINSTANCE.Write(writer, variant_value.ChannelId)
		FfiConverterTypeTxidINSTANCE.Write(writer, variant_value.LatestSpendingTxid)
		FfiConverterTypeBlockHashINSTANCE.Write(writer, variant_value.ConfirmationHash)
		FfiConverterUint32INSTANCE.Write(writer, variant_value.ConfirmationHeight)
		FfiConverterUint64INSTANCE.Write(writer, variant_value.AmountSatoshis)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterPendingSweepBalance.Write", value))
	}
}

type FfiDestroyerPendingSweepBalance struct{}

func (_ FfiDestroyerPendingSweepBalance) Destroy(value PendingSweepBalance) {
	value.Destroy()
}

// Represents the result of a payment made using a [BIP 21] URI or a [BIP 353] Human-Readable Name.
//
// After a successful on-chain transaction, the transaction ID ([`Txid`]) is returned.
// For BOLT11 and BOLT12 payments, the corresponding [`PaymentId`] is returned.
//
// [BIP 21]: https://github.com/bitcoin/bips/blob/master/bip-0021.mediawiki
// [BIP 353]: https://github.com/bitcoin/bips/blob/master/bip-0353.mediawiki
// [`PaymentId`]: lightning::ln::channelmanager::PaymentId
// [`Txid`]: bitcoin::hash_types::Txid
type UnifiedPaymentResult interface {
	Destroy()
}

// An on-chain payment.
type UnifiedPaymentResultOnchain struct {
	Txid Txid
}

func (e UnifiedPaymentResultOnchain) Destroy() {
	FfiDestroyerTypeTxid{}.Destroy(e.Txid)
}

// A [BOLT 11] payment.
//
// [BOLT 11]: https://github.com/lightning/bolts/blob/master/11-payment-encoding.md
type UnifiedPaymentResultBolt11 struct {
	PaymentId PaymentId
}

func (e UnifiedPaymentResultBolt11) Destroy() {
	FfiDestroyerTypePaymentId{}.Destroy(e.PaymentId)
}

// A [BOLT 12] offer payment, i.e., a payment for an [`Offer`].
//
// [BOLT 12]: https://github.com/lightning/bolts/blob/master/12-offer-encoding.md
// [`Offer`]: crate::lightning::offers::offer::Offer
type UnifiedPaymentResultBolt12 struct {
	PaymentId PaymentId
}

func (e UnifiedPaymentResultBolt12) Destroy() {
	FfiDestroyerTypePaymentId{}.Destroy(e.PaymentId)
}

type FfiConverterUnifiedPaymentResult struct{}

var FfiConverterUnifiedPaymentResultINSTANCE = FfiConverterUnifiedPaymentResult{}

func (c FfiConverterUnifiedPaymentResult) Lift(rb RustBufferI) UnifiedPaymentResult {
	return LiftFromRustBuffer[UnifiedPaymentResult](c, rb)
}

func (c FfiConverterUnifiedPaymentResult) Lower(value UnifiedPaymentResult) C.RustBuffer {
	return LowerIntoRustBuffer[UnifiedPaymentResult](c, value)
}

func (c FfiConverterUnifiedPaymentResult) LowerExternal(value UnifiedPaymentResult) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[UnifiedPaymentResult](c, value))
}
func (FfiConverterUnifiedPaymentResult) Read(reader io.Reader) UnifiedPaymentResult {
	id := readInt32(reader)
	switch id {
	case 1:
		return UnifiedPaymentResultOnchain{
			FfiConverterTypeTxidINSTANCE.Read(reader),
		}
	case 2:
		return UnifiedPaymentResultBolt11{
			FfiConverterTypePaymentIdINSTANCE.Read(reader),
		}
	case 3:
		return UnifiedPaymentResultBolt12{
			FfiConverterTypePaymentIdINSTANCE.Read(reader),
		}
	default:
		panic(fmt.Sprintf("invalid enum value %v in FfiConverterUnifiedPaymentResult.Read()", id))
	}
}

func (FfiConverterUnifiedPaymentResult) Write(writer io.Writer, value UnifiedPaymentResult) {
	switch variant_value := value.(type) {
	case UnifiedPaymentResultOnchain:
		writeInt32(writer, 1)
		FfiConverterTypeTxidINSTANCE.Write(writer, variant_value.Txid)
	case UnifiedPaymentResultBolt11:
		writeInt32(writer, 2)
		FfiConverterTypePaymentIdINSTANCE.Write(writer, variant_value.PaymentId)
	case UnifiedPaymentResultBolt12:
		writeInt32(writer, 3)
		FfiConverterTypePaymentIdINSTANCE.Write(writer, variant_value.PaymentId)
	default:
		_ = variant_value
		panic(fmt.Sprintf("invalid enum value `%v` in FfiConverterUnifiedPaymentResult.Write", value))
	}
}

type FfiDestroyerUnifiedPaymentResult struct{}

func (_ FfiDestroyerUnifiedPaymentResult) Destroy(value UnifiedPaymentResult) {
	value.Destroy()
}

// Errors around providing headers for each VSS request.
type VssHeaderProviderError struct {
	err error
}

// Convience method to turn *VssHeaderProviderError into error
// Avoiding treating nil pointer as non nil error interface
func (err *VssHeaderProviderError) AsError() error {
	if err == nil {
		return nil
	} else {
		return err
	}
}

func (err VssHeaderProviderError) Error() string {
	return fmt.Sprintf("VssHeaderProviderError: %s", err.err.Error())
}

func (err VssHeaderProviderError) Unwrap() error {
	return err.err
}

// Err* are used for checking error type with `errors.Is`
var ErrVssHeaderProviderErrorInvalidData = fmt.Errorf("VssHeaderProviderErrorInvalidData")
var ErrVssHeaderProviderErrorRequestError = fmt.Errorf("VssHeaderProviderErrorRequestError")
var ErrVssHeaderProviderErrorAuthorizationError = fmt.Errorf("VssHeaderProviderErrorAuthorizationError")
var ErrVssHeaderProviderErrorInternalError = fmt.Errorf("VssHeaderProviderErrorInternalError")

// Variant structs
// Invalid data was encountered.
type VssHeaderProviderErrorInvalidData struct {
	Error_ string
}

// Invalid data was encountered.
func NewVssHeaderProviderErrorInvalidData(
	error string,
) *VssHeaderProviderError {
	return &VssHeaderProviderError{err: &VssHeaderProviderErrorInvalidData{
		Error_: error}}
}

func (e VssHeaderProviderErrorInvalidData) destroy() {
	FfiDestroyerString{}.Destroy(e.Error_)
}

func (err VssHeaderProviderErrorInvalidData) Error() string {
	return fmt.Sprint("InvalidData",
		": ",

		"Error_=",
		err.Error_,
	)
}

func (self VssHeaderProviderErrorInvalidData) Is(target error) bool {
	return target == ErrVssHeaderProviderErrorInvalidData
}

// An external request failed.
type VssHeaderProviderErrorRequestError struct {
	Error_ string
}

// An external request failed.
func NewVssHeaderProviderErrorRequestError(
	error string,
) *VssHeaderProviderError {
	return &VssHeaderProviderError{err: &VssHeaderProviderErrorRequestError{
		Error_: error}}
}

func (e VssHeaderProviderErrorRequestError) destroy() {
	FfiDestroyerString{}.Destroy(e.Error_)
}

func (err VssHeaderProviderErrorRequestError) Error() string {
	return fmt.Sprint("RequestError",
		": ",

		"Error_=",
		err.Error_,
	)
}

func (self VssHeaderProviderErrorRequestError) Is(target error) bool {
	return target == ErrVssHeaderProviderErrorRequestError
}

// Authorization was refused.
type VssHeaderProviderErrorAuthorizationError struct {
	Error_ string
}

// Authorization was refused.
func NewVssHeaderProviderErrorAuthorizationError(
	error string,
) *VssHeaderProviderError {
	return &VssHeaderProviderError{err: &VssHeaderProviderErrorAuthorizationError{
		Error_: error}}
}

func (e VssHeaderProviderErrorAuthorizationError) destroy() {
	FfiDestroyerString{}.Destroy(e.Error_)
}

func (err VssHeaderProviderErrorAuthorizationError) Error() string {
	return fmt.Sprint("AuthorizationError",
		": ",

		"Error_=",
		err.Error_,
	)
}

func (self VssHeaderProviderErrorAuthorizationError) Is(target error) bool {
	return target == ErrVssHeaderProviderErrorAuthorizationError
}

// An application-level error occurred specific to the header provider functionality.
type VssHeaderProviderErrorInternalError struct {
	Error_ string
}

// An application-level error occurred specific to the header provider functionality.
func NewVssHeaderProviderErrorInternalError(
	error string,
) *VssHeaderProviderError {
	return &VssHeaderProviderError{err: &VssHeaderProviderErrorInternalError{
		Error_: error}}
}

func (e VssHeaderProviderErrorInternalError) destroy() {
	FfiDestroyerString{}.Destroy(e.Error_)
}

func (err VssHeaderProviderErrorInternalError) Error() string {
	return fmt.Sprint("InternalError",
		": ",

		"Error_=",
		err.Error_,
	)
}

func (self VssHeaderProviderErrorInternalError) Is(target error) bool {
	return target == ErrVssHeaderProviderErrorInternalError
}

type FfiConverterVssHeaderProviderError struct{}

var FfiConverterVssHeaderProviderErrorINSTANCE = FfiConverterVssHeaderProviderError{}

func (c FfiConverterVssHeaderProviderError) Lift(eb RustBufferI) *VssHeaderProviderError {
	return LiftFromRustBuffer[*VssHeaderProviderError](c, eb)
}

func (c FfiConverterVssHeaderProviderError) Lower(value *VssHeaderProviderError) C.RustBuffer {
	return LowerIntoRustBuffer[*VssHeaderProviderError](c, value)
}

func (c FfiConverterVssHeaderProviderError) LowerExternal(value *VssHeaderProviderError) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*VssHeaderProviderError](c, value))
}

func (c FfiConverterVssHeaderProviderError) Read(reader io.Reader) *VssHeaderProviderError {
	errorID := readUint32(reader)

	switch errorID {
	case 1:
		return &VssHeaderProviderError{&VssHeaderProviderErrorInvalidData{
			Error_: FfiConverterStringINSTANCE.Read(reader),
		}}
	case 2:
		return &VssHeaderProviderError{&VssHeaderProviderErrorRequestError{
			Error_: FfiConverterStringINSTANCE.Read(reader),
		}}
	case 3:
		return &VssHeaderProviderError{&VssHeaderProviderErrorAuthorizationError{
			Error_: FfiConverterStringINSTANCE.Read(reader),
		}}
	case 4:
		return &VssHeaderProviderError{&VssHeaderProviderErrorInternalError{
			Error_: FfiConverterStringINSTANCE.Read(reader),
		}}
	default:
		panic(fmt.Sprintf("Unknown error code %d in FfiConverterVssHeaderProviderError.Read()", errorID))
	}
}

func (c FfiConverterVssHeaderProviderError) Write(writer io.Writer, value *VssHeaderProviderError) {
	switch variantValue := value.err.(type) {
	case *VssHeaderProviderErrorInvalidData:
		writeInt32(writer, 1)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Error_)
	case *VssHeaderProviderErrorRequestError:
		writeInt32(writer, 2)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Error_)
	case *VssHeaderProviderErrorAuthorizationError:
		writeInt32(writer, 3)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Error_)
	case *VssHeaderProviderErrorInternalError:
		writeInt32(writer, 4)
		FfiConverterStringINSTANCE.Write(writer, variantValue.Error_)
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiConverterVssHeaderProviderError.Write", value))
	}
}

type FfiDestroyerVssHeaderProviderError struct{}

func (_ FfiDestroyerVssHeaderProviderError) Destroy(value *VssHeaderProviderError) {
	switch variantValue := value.err.(type) {
	case VssHeaderProviderErrorInvalidData:
		variantValue.destroy()
	case VssHeaderProviderErrorRequestError:
		variantValue.destroy()
	case VssHeaderProviderErrorAuthorizationError:
		variantValue.destroy()
	case VssHeaderProviderErrorInternalError:
		variantValue.destroy()
	default:
		_ = variantValue
		panic(fmt.Sprintf("invalid error value `%v` in FfiDestroyerVssHeaderProviderError.Destroy", value))
	}
}

// Supported BIP39 mnemonic word counts for entropy generation.
type WordCount uint

const (
	// 12-word mnemonic (128-bit entropy)
	WordCountWords12 WordCount = 1
	// 15-word mnemonic (160-bit entropy)
	WordCountWords15 WordCount = 2
	// 18-word mnemonic (192-bit entropy)
	WordCountWords18 WordCount = 3
	// 21-word mnemonic (224-bit entropy)
	WordCountWords21 WordCount = 4
	// 24-word mnemonic (256-bit entropy)
	WordCountWords24 WordCount = 5
)

type FfiConverterWordCount struct{}

var FfiConverterWordCountINSTANCE = FfiConverterWordCount{}

func (c FfiConverterWordCount) Lift(rb RustBufferI) WordCount {
	return LiftFromRustBuffer[WordCount](c, rb)
}

func (c FfiConverterWordCount) Lower(value WordCount) C.RustBuffer {
	return LowerIntoRustBuffer[WordCount](c, value)
}

func (c FfiConverterWordCount) LowerExternal(value WordCount) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[WordCount](c, value))
}
func (FfiConverterWordCount) Read(reader io.Reader) WordCount {
	id := readInt32(reader)
	return WordCount(id)
}

func (FfiConverterWordCount) Write(writer io.Writer, value WordCount) {
	writeInt32(writer, int32(value))
}

type FfiDestroyerWordCount struct{}

func (_ FfiDestroyerWordCount) Destroy(value WordCount) {
}

type FfiConverterOptionalUint16 struct{}

var FfiConverterOptionalUint16INSTANCE = FfiConverterOptionalUint16{}

func (c FfiConverterOptionalUint16) Lift(rb RustBufferI) *uint16 {
	return LiftFromRustBuffer[*uint16](c, rb)
}

func (_ FfiConverterOptionalUint16) Read(reader io.Reader) *uint16 {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterUint16INSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalUint16) Lower(value *uint16) C.RustBuffer {
	return LowerIntoRustBuffer[*uint16](c, value)
}

func (c FfiConverterOptionalUint16) LowerExternal(value *uint16) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*uint16](c, value))
}

func (_ FfiConverterOptionalUint16) Write(writer io.Writer, value *uint16) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterUint16INSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalUint16 struct{}

func (_ FfiDestroyerOptionalUint16) Destroy(value *uint16) {
	if value != nil {
		FfiDestroyerUint16{}.Destroy(*value)
	}
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

func (c FfiConverterOptionalUint32) LowerExternal(value *uint32) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*uint32](c, value))
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

func (c FfiConverterOptionalUint64) LowerExternal(value *uint64) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*uint64](c, value))
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

func (c FfiConverterOptionalBool) LowerExternal(value *bool) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*bool](c, value))
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

func (c FfiConverterOptionalString) LowerExternal(value *string) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*string](c, value))
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

type FfiConverterOptionalBytes struct{}

var FfiConverterOptionalBytesINSTANCE = FfiConverterOptionalBytes{}

func (c FfiConverterOptionalBytes) Lift(rb RustBufferI) *[]byte {
	return LiftFromRustBuffer[*[]byte](c, rb)
}

func (_ FfiConverterOptionalBytes) Read(reader io.Reader) *[]byte {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterBytesINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalBytes) Lower(value *[]byte) C.RustBuffer {
	return LowerIntoRustBuffer[*[]byte](c, value)
}

func (c FfiConverterOptionalBytes) LowerExternal(value *[]byte) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*[]byte](c, value))
}

func (_ FfiConverterOptionalBytes) Write(writer io.Writer, value *[]byte) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterBytesINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalBytes struct{}

func (_ FfiDestroyerOptionalBytes) Destroy(value *[]byte) {
	if value != nil {
		FfiDestroyerBytes{}.Destroy(*value)
	}
}

type FfiConverterOptionalFeeRate struct{}

var FfiConverterOptionalFeeRateINSTANCE = FfiConverterOptionalFeeRate{}

func (c FfiConverterOptionalFeeRate) Lift(rb RustBufferI) **FeeRate {
	return LiftFromRustBuffer[**FeeRate](c, rb)
}

func (_ FfiConverterOptionalFeeRate) Read(reader io.Reader) **FeeRate {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterFeeRateINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalFeeRate) Lower(value **FeeRate) C.RustBuffer {
	return LowerIntoRustBuffer[**FeeRate](c, value)
}

func (c FfiConverterOptionalFeeRate) LowerExternal(value **FeeRate) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[**FeeRate](c, value))
}

func (_ FfiConverterOptionalFeeRate) Write(writer io.Writer, value **FeeRate) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterFeeRateINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalFeeRate struct{}

func (_ FfiDestroyerOptionalFeeRate) Destroy(value **FeeRate) {
	if value != nil {
		FfiDestroyerFeeRate{}.Destroy(*value)
	}
}

type FfiConverterOptionalAnchorChannelsConfig struct{}

var FfiConverterOptionalAnchorChannelsConfigINSTANCE = FfiConverterOptionalAnchorChannelsConfig{}

func (c FfiConverterOptionalAnchorChannelsConfig) Lift(rb RustBufferI) *AnchorChannelsConfig {
	return LiftFromRustBuffer[*AnchorChannelsConfig](c, rb)
}

func (_ FfiConverterOptionalAnchorChannelsConfig) Read(reader io.Reader) *AnchorChannelsConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterAnchorChannelsConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalAnchorChannelsConfig) Lower(value *AnchorChannelsConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*AnchorChannelsConfig](c, value)
}

func (c FfiConverterOptionalAnchorChannelsConfig) LowerExternal(value *AnchorChannelsConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*AnchorChannelsConfig](c, value))
}

func (_ FfiConverterOptionalAnchorChannelsConfig) Write(writer io.Writer, value *AnchorChannelsConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterAnchorChannelsConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalAnchorChannelsConfig struct{}

func (_ FfiDestroyerOptionalAnchorChannelsConfig) Destroy(value *AnchorChannelsConfig) {
	if value != nil {
		FfiDestroyerAnchorChannelsConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalBackgroundSyncConfig struct{}

var FfiConverterOptionalBackgroundSyncConfigINSTANCE = FfiConverterOptionalBackgroundSyncConfig{}

func (c FfiConverterOptionalBackgroundSyncConfig) Lift(rb RustBufferI) *BackgroundSyncConfig {
	return LiftFromRustBuffer[*BackgroundSyncConfig](c, rb)
}

func (_ FfiConverterOptionalBackgroundSyncConfig) Read(reader io.Reader) *BackgroundSyncConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterBackgroundSyncConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalBackgroundSyncConfig) Lower(value *BackgroundSyncConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*BackgroundSyncConfig](c, value)
}

func (c FfiConverterOptionalBackgroundSyncConfig) LowerExternal(value *BackgroundSyncConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*BackgroundSyncConfig](c, value))
}

func (_ FfiConverterOptionalBackgroundSyncConfig) Write(writer io.Writer, value *BackgroundSyncConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterBackgroundSyncConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalBackgroundSyncConfig struct{}

func (_ FfiDestroyerOptionalBackgroundSyncConfig) Destroy(value *BackgroundSyncConfig) {
	if value != nil {
		FfiDestroyerBackgroundSyncConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalChannelConfig struct{}

var FfiConverterOptionalChannelConfigINSTANCE = FfiConverterOptionalChannelConfig{}

func (c FfiConverterOptionalChannelConfig) Lift(rb RustBufferI) *ChannelConfig {
	return LiftFromRustBuffer[*ChannelConfig](c, rb)
}

func (_ FfiConverterOptionalChannelConfig) Read(reader io.Reader) *ChannelConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterChannelConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalChannelConfig) Lower(value *ChannelConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*ChannelConfig](c, value)
}

func (c FfiConverterOptionalChannelConfig) LowerExternal(value *ChannelConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*ChannelConfig](c, value))
}

func (_ FfiConverterOptionalChannelConfig) Write(writer io.Writer, value *ChannelConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterChannelConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalChannelConfig struct{}

func (_ FfiDestroyerOptionalChannelConfig) Destroy(value *ChannelConfig) {
	if value != nil {
		FfiDestroyerChannelConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalChannelInfo struct{}

var FfiConverterOptionalChannelInfoINSTANCE = FfiConverterOptionalChannelInfo{}

func (c FfiConverterOptionalChannelInfo) Lift(rb RustBufferI) *ChannelInfo {
	return LiftFromRustBuffer[*ChannelInfo](c, rb)
}

func (_ FfiConverterOptionalChannelInfo) Read(reader io.Reader) *ChannelInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterChannelInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalChannelInfo) Lower(value *ChannelInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*ChannelInfo](c, value)
}

func (c FfiConverterOptionalChannelInfo) LowerExternal(value *ChannelInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*ChannelInfo](c, value))
}

func (_ FfiConverterOptionalChannelInfo) Write(writer io.Writer, value *ChannelInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterChannelInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalChannelInfo struct{}

func (_ FfiDestroyerOptionalChannelInfo) Destroy(value *ChannelInfo) {
	if value != nil {
		FfiDestroyerChannelInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalChannelUpdateInfo struct{}

var FfiConverterOptionalChannelUpdateInfoINSTANCE = FfiConverterOptionalChannelUpdateInfo{}

func (c FfiConverterOptionalChannelUpdateInfo) Lift(rb RustBufferI) *ChannelUpdateInfo {
	return LiftFromRustBuffer[*ChannelUpdateInfo](c, rb)
}

func (_ FfiConverterOptionalChannelUpdateInfo) Read(reader io.Reader) *ChannelUpdateInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterChannelUpdateInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalChannelUpdateInfo) Lower(value *ChannelUpdateInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*ChannelUpdateInfo](c, value)
}

func (c FfiConverterOptionalChannelUpdateInfo) LowerExternal(value *ChannelUpdateInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*ChannelUpdateInfo](c, value))
}

func (_ FfiConverterOptionalChannelUpdateInfo) Write(writer io.Writer, value *ChannelUpdateInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterChannelUpdateInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalChannelUpdateInfo struct{}

func (_ FfiDestroyerOptionalChannelUpdateInfo) Destroy(value *ChannelUpdateInfo) {
	if value != nil {
		FfiDestroyerChannelUpdateInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalElectrumSyncConfig struct{}

var FfiConverterOptionalElectrumSyncConfigINSTANCE = FfiConverterOptionalElectrumSyncConfig{}

func (c FfiConverterOptionalElectrumSyncConfig) Lift(rb RustBufferI) *ElectrumSyncConfig {
	return LiftFromRustBuffer[*ElectrumSyncConfig](c, rb)
}

func (_ FfiConverterOptionalElectrumSyncConfig) Read(reader io.Reader) *ElectrumSyncConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterElectrumSyncConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalElectrumSyncConfig) Lower(value *ElectrumSyncConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*ElectrumSyncConfig](c, value)
}

func (c FfiConverterOptionalElectrumSyncConfig) LowerExternal(value *ElectrumSyncConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*ElectrumSyncConfig](c, value))
}

func (_ FfiConverterOptionalElectrumSyncConfig) Write(writer io.Writer, value *ElectrumSyncConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterElectrumSyncConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalElectrumSyncConfig struct{}

func (_ FfiDestroyerOptionalElectrumSyncConfig) Destroy(value *ElectrumSyncConfig) {
	if value != nil {
		FfiDestroyerElectrumSyncConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalEsploraSyncConfig struct{}

var FfiConverterOptionalEsploraSyncConfigINSTANCE = FfiConverterOptionalEsploraSyncConfig{}

func (c FfiConverterOptionalEsploraSyncConfig) Lift(rb RustBufferI) *EsploraSyncConfig {
	return LiftFromRustBuffer[*EsploraSyncConfig](c, rb)
}

func (_ FfiConverterOptionalEsploraSyncConfig) Read(reader io.Reader) *EsploraSyncConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterEsploraSyncConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalEsploraSyncConfig) Lower(value *EsploraSyncConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*EsploraSyncConfig](c, value)
}

func (c FfiConverterOptionalEsploraSyncConfig) LowerExternal(value *EsploraSyncConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*EsploraSyncConfig](c, value))
}

func (_ FfiConverterOptionalEsploraSyncConfig) Write(writer io.Writer, value *EsploraSyncConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterEsploraSyncConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalEsploraSyncConfig struct{}

func (_ FfiDestroyerOptionalEsploraSyncConfig) Destroy(value *EsploraSyncConfig) {
	if value != nil {
		FfiDestroyerEsploraSyncConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalLsps1Bolt11PaymentInfo struct{}

var FfiConverterOptionalLsps1Bolt11PaymentInfoINSTANCE = FfiConverterOptionalLsps1Bolt11PaymentInfo{}

func (c FfiConverterOptionalLsps1Bolt11PaymentInfo) Lift(rb RustBufferI) *Lsps1Bolt11PaymentInfo {
	return LiftFromRustBuffer[*Lsps1Bolt11PaymentInfo](c, rb)
}

func (_ FfiConverterOptionalLsps1Bolt11PaymentInfo) Read(reader io.Reader) *Lsps1Bolt11PaymentInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterLsps1Bolt11PaymentInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalLsps1Bolt11PaymentInfo) Lower(value *Lsps1Bolt11PaymentInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*Lsps1Bolt11PaymentInfo](c, value)
}

func (c FfiConverterOptionalLsps1Bolt11PaymentInfo) LowerExternal(value *Lsps1Bolt11PaymentInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*Lsps1Bolt11PaymentInfo](c, value))
}

func (_ FfiConverterOptionalLsps1Bolt11PaymentInfo) Write(writer io.Writer, value *Lsps1Bolt11PaymentInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterLsps1Bolt11PaymentInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalLsps1Bolt11PaymentInfo struct{}

func (_ FfiDestroyerOptionalLsps1Bolt11PaymentInfo) Destroy(value *Lsps1Bolt11PaymentInfo) {
	if value != nil {
		FfiDestroyerLsps1Bolt11PaymentInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalLsps1ChannelInfo struct{}

var FfiConverterOptionalLsps1ChannelInfoINSTANCE = FfiConverterOptionalLsps1ChannelInfo{}

func (c FfiConverterOptionalLsps1ChannelInfo) Lift(rb RustBufferI) *Lsps1ChannelInfo {
	return LiftFromRustBuffer[*Lsps1ChannelInfo](c, rb)
}

func (_ FfiConverterOptionalLsps1ChannelInfo) Read(reader io.Reader) *Lsps1ChannelInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterLsps1ChannelInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalLsps1ChannelInfo) Lower(value *Lsps1ChannelInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*Lsps1ChannelInfo](c, value)
}

func (c FfiConverterOptionalLsps1ChannelInfo) LowerExternal(value *Lsps1ChannelInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*Lsps1ChannelInfo](c, value))
}

func (_ FfiConverterOptionalLsps1ChannelInfo) Write(writer io.Writer, value *Lsps1ChannelInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterLsps1ChannelInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalLsps1ChannelInfo struct{}

func (_ FfiDestroyerOptionalLsps1ChannelInfo) Destroy(value *Lsps1ChannelInfo) {
	if value != nil {
		FfiDestroyerLsps1ChannelInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalLsps1OnchainPaymentInfo struct{}

var FfiConverterOptionalLsps1OnchainPaymentInfoINSTANCE = FfiConverterOptionalLsps1OnchainPaymentInfo{}

func (c FfiConverterOptionalLsps1OnchainPaymentInfo) Lift(rb RustBufferI) *Lsps1OnchainPaymentInfo {
	return LiftFromRustBuffer[*Lsps1OnchainPaymentInfo](c, rb)
}

func (_ FfiConverterOptionalLsps1OnchainPaymentInfo) Read(reader io.Reader) *Lsps1OnchainPaymentInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterLsps1OnchainPaymentInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalLsps1OnchainPaymentInfo) Lower(value *Lsps1OnchainPaymentInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*Lsps1OnchainPaymentInfo](c, value)
}

func (c FfiConverterOptionalLsps1OnchainPaymentInfo) LowerExternal(value *Lsps1OnchainPaymentInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*Lsps1OnchainPaymentInfo](c, value))
}

func (_ FfiConverterOptionalLsps1OnchainPaymentInfo) Write(writer io.Writer, value *Lsps1OnchainPaymentInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterLsps1OnchainPaymentInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalLsps1OnchainPaymentInfo struct{}

func (_ FfiDestroyerOptionalLsps1OnchainPaymentInfo) Destroy(value *Lsps1OnchainPaymentInfo) {
	if value != nil {
		FfiDestroyerLsps1OnchainPaymentInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalNodeAnnouncementInfo struct{}

var FfiConverterOptionalNodeAnnouncementInfoINSTANCE = FfiConverterOptionalNodeAnnouncementInfo{}

func (c FfiConverterOptionalNodeAnnouncementInfo) Lift(rb RustBufferI) *NodeAnnouncementInfo {
	return LiftFromRustBuffer[*NodeAnnouncementInfo](c, rb)
}

func (_ FfiConverterOptionalNodeAnnouncementInfo) Read(reader io.Reader) *NodeAnnouncementInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterNodeAnnouncementInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalNodeAnnouncementInfo) Lower(value *NodeAnnouncementInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*NodeAnnouncementInfo](c, value)
}

func (c FfiConverterOptionalNodeAnnouncementInfo) LowerExternal(value *NodeAnnouncementInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*NodeAnnouncementInfo](c, value))
}

func (_ FfiConverterOptionalNodeAnnouncementInfo) Write(writer io.Writer, value *NodeAnnouncementInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterNodeAnnouncementInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalNodeAnnouncementInfo struct{}

func (_ FfiDestroyerOptionalNodeAnnouncementInfo) Destroy(value *NodeAnnouncementInfo) {
	if value != nil {
		FfiDestroyerNodeAnnouncementInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalNodeInfo struct{}

var FfiConverterOptionalNodeInfoINSTANCE = FfiConverterOptionalNodeInfo{}

func (c FfiConverterOptionalNodeInfo) Lift(rb RustBufferI) *NodeInfo {
	return LiftFromRustBuffer[*NodeInfo](c, rb)
}

func (_ FfiConverterOptionalNodeInfo) Read(reader io.Reader) *NodeInfo {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterNodeInfoINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalNodeInfo) Lower(value *NodeInfo) C.RustBuffer {
	return LowerIntoRustBuffer[*NodeInfo](c, value)
}

func (c FfiConverterOptionalNodeInfo) LowerExternal(value *NodeInfo) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*NodeInfo](c, value))
}

func (_ FfiConverterOptionalNodeInfo) Write(writer io.Writer, value *NodeInfo) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterNodeInfoINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalNodeInfo struct{}

func (_ FfiDestroyerOptionalNodeInfo) Destroy(value *NodeInfo) {
	if value != nil {
		FfiDestroyerNodeInfo{}.Destroy(*value)
	}
}

type FfiConverterOptionalOutPoint struct{}

var FfiConverterOptionalOutPointINSTANCE = FfiConverterOptionalOutPoint{}

func (c FfiConverterOptionalOutPoint) Lift(rb RustBufferI) *OutPoint {
	return LiftFromRustBuffer[*OutPoint](c, rb)
}

func (_ FfiConverterOptionalOutPoint) Read(reader io.Reader) *OutPoint {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterOutPointINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalOutPoint) Lower(value *OutPoint) C.RustBuffer {
	return LowerIntoRustBuffer[*OutPoint](c, value)
}

func (c FfiConverterOptionalOutPoint) LowerExternal(value *OutPoint) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*OutPoint](c, value))
}

func (_ FfiConverterOptionalOutPoint) Write(writer io.Writer, value *OutPoint) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterOutPointINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalOutPoint struct{}

func (_ FfiDestroyerOptionalOutPoint) Destroy(value *OutPoint) {
	if value != nil {
		FfiDestroyerOutPoint{}.Destroy(*value)
	}
}

type FfiConverterOptionalPaymentDetails struct{}

var FfiConverterOptionalPaymentDetailsINSTANCE = FfiConverterOptionalPaymentDetails{}

func (c FfiConverterOptionalPaymentDetails) Lift(rb RustBufferI) *PaymentDetails {
	return LiftFromRustBuffer[*PaymentDetails](c, rb)
}

func (_ FfiConverterOptionalPaymentDetails) Read(reader io.Reader) *PaymentDetails {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterPaymentDetailsINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalPaymentDetails) Lower(value *PaymentDetails) C.RustBuffer {
	return LowerIntoRustBuffer[*PaymentDetails](c, value)
}

func (c FfiConverterOptionalPaymentDetails) LowerExternal(value *PaymentDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PaymentDetails](c, value))
}

func (_ FfiConverterOptionalPaymentDetails) Write(writer io.Writer, value *PaymentDetails) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterPaymentDetailsINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalPaymentDetails struct{}

func (_ FfiDestroyerOptionalPaymentDetails) Destroy(value *PaymentDetails) {
	if value != nil {
		FfiDestroyerPaymentDetails{}.Destroy(*value)
	}
}

type FfiConverterOptionalRouteParametersConfig struct{}

var FfiConverterOptionalRouteParametersConfigINSTANCE = FfiConverterOptionalRouteParametersConfig{}

func (c FfiConverterOptionalRouteParametersConfig) Lift(rb RustBufferI) *RouteParametersConfig {
	return LiftFromRustBuffer[*RouteParametersConfig](c, rb)
}

func (_ FfiConverterOptionalRouteParametersConfig) Read(reader io.Reader) *RouteParametersConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterRouteParametersConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalRouteParametersConfig) Lower(value *RouteParametersConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*RouteParametersConfig](c, value)
}

func (c FfiConverterOptionalRouteParametersConfig) LowerExternal(value *RouteParametersConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*RouteParametersConfig](c, value))
}

func (_ FfiConverterOptionalRouteParametersConfig) Write(writer io.Writer, value *RouteParametersConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterRouteParametersConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalRouteParametersConfig struct{}

func (_ FfiDestroyerOptionalRouteParametersConfig) Destroy(value *RouteParametersConfig) {
	if value != nil {
		FfiDestroyerRouteParametersConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalTorConfig struct{}

var FfiConverterOptionalTorConfigINSTANCE = FfiConverterOptionalTorConfig{}

func (c FfiConverterOptionalTorConfig) Lift(rb RustBufferI) *TorConfig {
	return LiftFromRustBuffer[*TorConfig](c, rb)
}

func (_ FfiConverterOptionalTorConfig) Read(reader io.Reader) *TorConfig {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTorConfigINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTorConfig) Lower(value *TorConfig) C.RustBuffer {
	return LowerIntoRustBuffer[*TorConfig](c, value)
}

func (c FfiConverterOptionalTorConfig) LowerExternal(value *TorConfig) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*TorConfig](c, value))
}

func (_ FfiConverterOptionalTorConfig) Write(writer io.Writer, value *TorConfig) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTorConfigINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTorConfig struct{}

func (_ FfiDestroyerOptionalTorConfig) Destroy(value *TorConfig) {
	if value != nil {
		FfiDestroyerTorConfig{}.Destroy(*value)
	}
}

type FfiConverterOptionalAsyncPaymentsRole struct{}

var FfiConverterOptionalAsyncPaymentsRoleINSTANCE = FfiConverterOptionalAsyncPaymentsRole{}

func (c FfiConverterOptionalAsyncPaymentsRole) Lift(rb RustBufferI) *AsyncPaymentsRole {
	return LiftFromRustBuffer[*AsyncPaymentsRole](c, rb)
}

func (_ FfiConverterOptionalAsyncPaymentsRole) Read(reader io.Reader) *AsyncPaymentsRole {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterAsyncPaymentsRoleINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalAsyncPaymentsRole) Lower(value *AsyncPaymentsRole) C.RustBuffer {
	return LowerIntoRustBuffer[*AsyncPaymentsRole](c, value)
}

func (c FfiConverterOptionalAsyncPaymentsRole) LowerExternal(value *AsyncPaymentsRole) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*AsyncPaymentsRole](c, value))
}

func (_ FfiConverterOptionalAsyncPaymentsRole) Write(writer io.Writer, value *AsyncPaymentsRole) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterAsyncPaymentsRoleINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalAsyncPaymentsRole struct{}

func (_ FfiDestroyerOptionalAsyncPaymentsRole) Destroy(value *AsyncPaymentsRole) {
	if value != nil {
		FfiDestroyerAsyncPaymentsRole{}.Destroy(*value)
	}
}

type FfiConverterOptionalClosureReason struct{}

var FfiConverterOptionalClosureReasonINSTANCE = FfiConverterOptionalClosureReason{}

func (c FfiConverterOptionalClosureReason) Lift(rb RustBufferI) *ClosureReason {
	return LiftFromRustBuffer[*ClosureReason](c, rb)
}

func (_ FfiConverterOptionalClosureReason) Read(reader io.Reader) *ClosureReason {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterClosureReasonINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalClosureReason) Lower(value *ClosureReason) C.RustBuffer {
	return LowerIntoRustBuffer[*ClosureReason](c, value)
}

func (c FfiConverterOptionalClosureReason) LowerExternal(value *ClosureReason) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*ClosureReason](c, value))
}

func (_ FfiConverterOptionalClosureReason) Write(writer io.Writer, value *ClosureReason) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterClosureReasonINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalClosureReason struct{}

func (_ FfiDestroyerOptionalClosureReason) Destroy(value *ClosureReason) {
	if value != nil {
		FfiDestroyerClosureReason{}.Destroy(*value)
	}
}

type FfiConverterOptionalEvent struct{}

var FfiConverterOptionalEventINSTANCE = FfiConverterOptionalEvent{}

func (c FfiConverterOptionalEvent) Lift(rb RustBufferI) *Event {
	return LiftFromRustBuffer[*Event](c, rb)
}

func (_ FfiConverterOptionalEvent) Read(reader io.Reader) *Event {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterEventINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalEvent) Lower(value *Event) C.RustBuffer {
	return LowerIntoRustBuffer[*Event](c, value)
}

func (c FfiConverterOptionalEvent) LowerExternal(value *Event) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*Event](c, value))
}

func (_ FfiConverterOptionalEvent) Write(writer io.Writer, value *Event) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterEventINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalEvent struct{}

func (_ FfiDestroyerOptionalEvent) Destroy(value *Event) {
	if value != nil {
		FfiDestroyerEvent{}.Destroy(*value)
	}
}

type FfiConverterOptionalLogLevel struct{}

var FfiConverterOptionalLogLevelINSTANCE = FfiConverterOptionalLogLevel{}

func (c FfiConverterOptionalLogLevel) Lift(rb RustBufferI) *LogLevel {
	return LiftFromRustBuffer[*LogLevel](c, rb)
}

func (_ FfiConverterOptionalLogLevel) Read(reader io.Reader) *LogLevel {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterLogLevelINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalLogLevel) Lower(value *LogLevel) C.RustBuffer {
	return LowerIntoRustBuffer[*LogLevel](c, value)
}

func (c FfiConverterOptionalLogLevel) LowerExternal(value *LogLevel) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*LogLevel](c, value))
}

func (_ FfiConverterOptionalLogLevel) Write(writer io.Writer, value *LogLevel) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterLogLevelINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalLogLevel struct{}

func (_ FfiDestroyerOptionalLogLevel) Destroy(value *LogLevel) {
	if value != nil {
		FfiDestroyerLogLevel{}.Destroy(*value)
	}
}

type FfiConverterOptionalNetwork struct{}

var FfiConverterOptionalNetworkINSTANCE = FfiConverterOptionalNetwork{}

func (c FfiConverterOptionalNetwork) Lift(rb RustBufferI) *Network {
	return LiftFromRustBuffer[*Network](c, rb)
}

func (_ FfiConverterOptionalNetwork) Read(reader io.Reader) *Network {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterNetworkINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalNetwork) Lower(value *Network) C.RustBuffer {
	return LowerIntoRustBuffer[*Network](c, value)
}

func (c FfiConverterOptionalNetwork) LowerExternal(value *Network) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*Network](c, value))
}

func (_ FfiConverterOptionalNetwork) Write(writer io.Writer, value *Network) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterNetworkINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalNetwork struct{}

func (_ FfiDestroyerOptionalNetwork) Destroy(value *Network) {
	if value != nil {
		FfiDestroyerNetwork{}.Destroy(*value)
	}
}

type FfiConverterOptionalOfferAmount struct{}

var FfiConverterOptionalOfferAmountINSTANCE = FfiConverterOptionalOfferAmount{}

func (c FfiConverterOptionalOfferAmount) Lift(rb RustBufferI) *OfferAmount {
	return LiftFromRustBuffer[*OfferAmount](c, rb)
}

func (_ FfiConverterOptionalOfferAmount) Read(reader io.Reader) *OfferAmount {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterOfferAmountINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalOfferAmount) Lower(value *OfferAmount) C.RustBuffer {
	return LowerIntoRustBuffer[*OfferAmount](c, value)
}

func (c FfiConverterOptionalOfferAmount) LowerExternal(value *OfferAmount) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*OfferAmount](c, value))
}

func (_ FfiConverterOptionalOfferAmount) Write(writer io.Writer, value *OfferAmount) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterOfferAmountINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalOfferAmount struct{}

func (_ FfiDestroyerOptionalOfferAmount) Destroy(value *OfferAmount) {
	if value != nil {
		FfiDestroyerOfferAmount{}.Destroy(*value)
	}
}

type FfiConverterOptionalPaidBolt12Invoice struct{}

var FfiConverterOptionalPaidBolt12InvoiceINSTANCE = FfiConverterOptionalPaidBolt12Invoice{}

func (c FfiConverterOptionalPaidBolt12Invoice) Lift(rb RustBufferI) *PaidBolt12Invoice {
	return LiftFromRustBuffer[*PaidBolt12Invoice](c, rb)
}

func (_ FfiConverterOptionalPaidBolt12Invoice) Read(reader io.Reader) *PaidBolt12Invoice {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterPaidBolt12InvoiceINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalPaidBolt12Invoice) Lower(value *PaidBolt12Invoice) C.RustBuffer {
	return LowerIntoRustBuffer[*PaidBolt12Invoice](c, value)
}

func (c FfiConverterOptionalPaidBolt12Invoice) LowerExternal(value *PaidBolt12Invoice) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PaidBolt12Invoice](c, value))
}

func (_ FfiConverterOptionalPaidBolt12Invoice) Write(writer io.Writer, value *PaidBolt12Invoice) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterPaidBolt12InvoiceINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalPaidBolt12Invoice struct{}

func (_ FfiDestroyerOptionalPaidBolt12Invoice) Destroy(value *PaidBolt12Invoice) {
	if value != nil {
		FfiDestroyerPaidBolt12Invoice{}.Destroy(*value)
	}
}

type FfiConverterOptionalPaymentFailureReason struct{}

var FfiConverterOptionalPaymentFailureReasonINSTANCE = FfiConverterOptionalPaymentFailureReason{}

func (c FfiConverterOptionalPaymentFailureReason) Lift(rb RustBufferI) *PaymentFailureReason {
	return LiftFromRustBuffer[*PaymentFailureReason](c, rb)
}

func (_ FfiConverterOptionalPaymentFailureReason) Read(reader io.Reader) *PaymentFailureReason {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterPaymentFailureReasonINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalPaymentFailureReason) Lower(value *PaymentFailureReason) C.RustBuffer {
	return LowerIntoRustBuffer[*PaymentFailureReason](c, value)
}

func (c FfiConverterOptionalPaymentFailureReason) LowerExternal(value *PaymentFailureReason) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PaymentFailureReason](c, value))
}

func (_ FfiConverterOptionalPaymentFailureReason) Write(writer io.Writer, value *PaymentFailureReason) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterPaymentFailureReasonINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalPaymentFailureReason struct{}

func (_ FfiDestroyerOptionalPaymentFailureReason) Destroy(value *PaymentFailureReason) {
	if value != nil {
		FfiDestroyerPaymentFailureReason{}.Destroy(*value)
	}
}

type FfiConverterOptionalWordCount struct{}

var FfiConverterOptionalWordCountINSTANCE = FfiConverterOptionalWordCount{}

func (c FfiConverterOptionalWordCount) Lift(rb RustBufferI) *WordCount {
	return LiftFromRustBuffer[*WordCount](c, rb)
}

func (_ FfiConverterOptionalWordCount) Read(reader io.Reader) *WordCount {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterWordCountINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalWordCount) Lower(value *WordCount) C.RustBuffer {
	return LowerIntoRustBuffer[*WordCount](c, value)
}

func (c FfiConverterOptionalWordCount) LowerExternal(value *WordCount) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*WordCount](c, value))
}

func (_ FfiConverterOptionalWordCount) Write(writer io.Writer, value *WordCount) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterWordCountINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalWordCount struct{}

func (_ FfiDestroyerOptionalWordCount) Destroy(value *WordCount) {
	if value != nil {
		FfiDestroyerWordCount{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceBytes struct{}

var FfiConverterOptionalSequenceBytesINSTANCE = FfiConverterOptionalSequenceBytes{}

func (c FfiConverterOptionalSequenceBytes) Lift(rb RustBufferI) *[][]byte {
	return LiftFromRustBuffer[*[][]byte](c, rb)
}

func (_ FfiConverterOptionalSequenceBytes) Read(reader io.Reader) *[][]byte {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceBytesINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceBytes) Lower(value *[][]byte) C.RustBuffer {
	return LowerIntoRustBuffer[*[][]byte](c, value)
}

func (c FfiConverterOptionalSequenceBytes) LowerExternal(value *[][]byte) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*[][]byte](c, value))
}

func (_ FfiConverterOptionalSequenceBytes) Write(writer io.Writer, value *[][]byte) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceBytesINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceBytes struct{}

func (_ FfiDestroyerOptionalSequenceBytes) Destroy(value *[][]byte) {
	if value != nil {
		FfiDestroyerSequenceBytes{}.Destroy(*value)
	}
}

type FfiConverterOptionalSequenceTypeSocketAddress struct{}

var FfiConverterOptionalSequenceTypeSocketAddressINSTANCE = FfiConverterOptionalSequenceTypeSocketAddress{}

func (c FfiConverterOptionalSequenceTypeSocketAddress) Lift(rb RustBufferI) *[]SocketAddress {
	return LiftFromRustBuffer[*[]SocketAddress](c, rb)
}

func (_ FfiConverterOptionalSequenceTypeSocketAddress) Read(reader io.Reader) *[]SocketAddress {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterSequenceTypeSocketAddressINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalSequenceTypeSocketAddress) Lower(value *[]SocketAddress) C.RustBuffer {
	return LowerIntoRustBuffer[*[]SocketAddress](c, value)
}

func (c FfiConverterOptionalSequenceTypeSocketAddress) LowerExternal(value *[]SocketAddress) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*[]SocketAddress](c, value))
}

func (_ FfiConverterOptionalSequenceTypeSocketAddress) Write(writer io.Writer, value *[]SocketAddress) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterSequenceTypeSocketAddressINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalSequenceTypeSocketAddress struct{}

func (_ FfiDestroyerOptionalSequenceTypeSocketAddress) Destroy(value *[]SocketAddress) {
	if value != nil {
		FfiDestroyerSequenceTypeSocketAddress{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypeAddress struct{}

var FfiConverterOptionalTypeAddressINSTANCE = FfiConverterOptionalTypeAddress{}

func (c FfiConverterOptionalTypeAddress) Lift(rb RustBufferI) *Address {
	return LiftFromRustBuffer[*Address](c, rb)
}

func (_ FfiConverterOptionalTypeAddress) Read(reader io.Reader) *Address {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypeAddressINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypeAddress) Lower(value *Address) C.RustBuffer {
	return LowerIntoRustBuffer[*Address](c, value)
}

func (c FfiConverterOptionalTypeAddress) LowerExternal(value *Address) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*Address](c, value))
}

func (_ FfiConverterOptionalTypeAddress) Write(writer io.Writer, value *Address) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypeAddressINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypeAddress struct{}

func (_ FfiDestroyerOptionalTypeAddress) Destroy(value *Address) {
	if value != nil {
		FfiDestroyerTypeAddress{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypeChannelId struct{}

var FfiConverterOptionalTypeChannelIdINSTANCE = FfiConverterOptionalTypeChannelId{}

func (c FfiConverterOptionalTypeChannelId) Lift(rb RustBufferI) *ChannelId {
	return LiftFromRustBuffer[*ChannelId](c, rb)
}

func (_ FfiConverterOptionalTypeChannelId) Read(reader io.Reader) *ChannelId {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypeChannelIdINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypeChannelId) Lower(value *ChannelId) C.RustBuffer {
	return LowerIntoRustBuffer[*ChannelId](c, value)
}

func (c FfiConverterOptionalTypeChannelId) LowerExternal(value *ChannelId) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*ChannelId](c, value))
}

func (_ FfiConverterOptionalTypeChannelId) Write(writer io.Writer, value *ChannelId) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypeChannelIdINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypeChannelId struct{}

func (_ FfiDestroyerOptionalTypeChannelId) Destroy(value *ChannelId) {
	if value != nil {
		FfiDestroyerTypeChannelId{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypeNodeAlias struct{}

var FfiConverterOptionalTypeNodeAliasINSTANCE = FfiConverterOptionalTypeNodeAlias{}

func (c FfiConverterOptionalTypeNodeAlias) Lift(rb RustBufferI) *NodeAlias {
	return LiftFromRustBuffer[*NodeAlias](c, rb)
}

func (_ FfiConverterOptionalTypeNodeAlias) Read(reader io.Reader) *NodeAlias {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypeNodeAliasINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypeNodeAlias) Lower(value *NodeAlias) C.RustBuffer {
	return LowerIntoRustBuffer[*NodeAlias](c, value)
}

func (c FfiConverterOptionalTypeNodeAlias) LowerExternal(value *NodeAlias) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*NodeAlias](c, value))
}

func (_ FfiConverterOptionalTypeNodeAlias) Write(writer io.Writer, value *NodeAlias) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypeNodeAliasINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypeNodeAlias struct{}

func (_ FfiDestroyerOptionalTypeNodeAlias) Destroy(value *NodeAlias) {
	if value != nil {
		FfiDestroyerTypeNodeAlias{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypePaymentHash struct{}

var FfiConverterOptionalTypePaymentHashINSTANCE = FfiConverterOptionalTypePaymentHash{}

func (c FfiConverterOptionalTypePaymentHash) Lift(rb RustBufferI) *PaymentHash {
	return LiftFromRustBuffer[*PaymentHash](c, rb)
}

func (_ FfiConverterOptionalTypePaymentHash) Read(reader io.Reader) *PaymentHash {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypePaymentHashINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypePaymentHash) Lower(value *PaymentHash) C.RustBuffer {
	return LowerIntoRustBuffer[*PaymentHash](c, value)
}

func (c FfiConverterOptionalTypePaymentHash) LowerExternal(value *PaymentHash) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PaymentHash](c, value))
}

func (_ FfiConverterOptionalTypePaymentHash) Write(writer io.Writer, value *PaymentHash) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypePaymentHashINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypePaymentHash struct{}

func (_ FfiDestroyerOptionalTypePaymentHash) Destroy(value *PaymentHash) {
	if value != nil {
		FfiDestroyerTypePaymentHash{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypePaymentId struct{}

var FfiConverterOptionalTypePaymentIdINSTANCE = FfiConverterOptionalTypePaymentId{}

func (c FfiConverterOptionalTypePaymentId) Lift(rb RustBufferI) *PaymentId {
	return LiftFromRustBuffer[*PaymentId](c, rb)
}

func (_ FfiConverterOptionalTypePaymentId) Read(reader io.Reader) *PaymentId {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypePaymentIdINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypePaymentId) Lower(value *PaymentId) C.RustBuffer {
	return LowerIntoRustBuffer[*PaymentId](c, value)
}

func (c FfiConverterOptionalTypePaymentId) LowerExternal(value *PaymentId) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PaymentId](c, value))
}

func (_ FfiConverterOptionalTypePaymentId) Write(writer io.Writer, value *PaymentId) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypePaymentIdINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypePaymentId struct{}

func (_ FfiDestroyerOptionalTypePaymentId) Destroy(value *PaymentId) {
	if value != nil {
		FfiDestroyerTypePaymentId{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypePaymentPreimage struct{}

var FfiConverterOptionalTypePaymentPreimageINSTANCE = FfiConverterOptionalTypePaymentPreimage{}

func (c FfiConverterOptionalTypePaymentPreimage) Lift(rb RustBufferI) *PaymentPreimage {
	return LiftFromRustBuffer[*PaymentPreimage](c, rb)
}

func (_ FfiConverterOptionalTypePaymentPreimage) Read(reader io.Reader) *PaymentPreimage {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypePaymentPreimageINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypePaymentPreimage) Lower(value *PaymentPreimage) C.RustBuffer {
	return LowerIntoRustBuffer[*PaymentPreimage](c, value)
}

func (c FfiConverterOptionalTypePaymentPreimage) LowerExternal(value *PaymentPreimage) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PaymentPreimage](c, value))
}

func (_ FfiConverterOptionalTypePaymentPreimage) Write(writer io.Writer, value *PaymentPreimage) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypePaymentPreimageINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypePaymentPreimage struct{}

func (_ FfiDestroyerOptionalTypePaymentPreimage) Destroy(value *PaymentPreimage) {
	if value != nil {
		FfiDestroyerTypePaymentPreimage{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypePaymentSecret struct{}

var FfiConverterOptionalTypePaymentSecretINSTANCE = FfiConverterOptionalTypePaymentSecret{}

func (c FfiConverterOptionalTypePaymentSecret) Lift(rb RustBufferI) *PaymentSecret {
	return LiftFromRustBuffer[*PaymentSecret](c, rb)
}

func (_ FfiConverterOptionalTypePaymentSecret) Read(reader io.Reader) *PaymentSecret {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypePaymentSecretINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypePaymentSecret) Lower(value *PaymentSecret) C.RustBuffer {
	return LowerIntoRustBuffer[*PaymentSecret](c, value)
}

func (c FfiConverterOptionalTypePaymentSecret) LowerExternal(value *PaymentSecret) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PaymentSecret](c, value))
}

func (_ FfiConverterOptionalTypePaymentSecret) Write(writer io.Writer, value *PaymentSecret) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypePaymentSecretINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypePaymentSecret struct{}

func (_ FfiDestroyerOptionalTypePaymentSecret) Destroy(value *PaymentSecret) {
	if value != nil {
		FfiDestroyerTypePaymentSecret{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypePublicKey struct{}

var FfiConverterOptionalTypePublicKeyINSTANCE = FfiConverterOptionalTypePublicKey{}

func (c FfiConverterOptionalTypePublicKey) Lift(rb RustBufferI) *PublicKey {
	return LiftFromRustBuffer[*PublicKey](c, rb)
}

func (_ FfiConverterOptionalTypePublicKey) Read(reader io.Reader) *PublicKey {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypePublicKeyINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypePublicKey) Lower(value *PublicKey) C.RustBuffer {
	return LowerIntoRustBuffer[*PublicKey](c, value)
}

func (c FfiConverterOptionalTypePublicKey) LowerExternal(value *PublicKey) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*PublicKey](c, value))
}

func (_ FfiConverterOptionalTypePublicKey) Write(writer io.Writer, value *PublicKey) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypePublicKeyINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypePublicKey struct{}

func (_ FfiDestroyerOptionalTypePublicKey) Destroy(value *PublicKey) {
	if value != nil {
		FfiDestroyerTypePublicKey{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypeScriptBuf struct{}

var FfiConverterOptionalTypeScriptBufINSTANCE = FfiConverterOptionalTypeScriptBuf{}

func (c FfiConverterOptionalTypeScriptBuf) Lift(rb RustBufferI) *ScriptBuf {
	return LiftFromRustBuffer[*ScriptBuf](c, rb)
}

func (_ FfiConverterOptionalTypeScriptBuf) Read(reader io.Reader) *ScriptBuf {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypeScriptBufINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypeScriptBuf) Lower(value *ScriptBuf) C.RustBuffer {
	return LowerIntoRustBuffer[*ScriptBuf](c, value)
}

func (c FfiConverterOptionalTypeScriptBuf) LowerExternal(value *ScriptBuf) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*ScriptBuf](c, value))
}

func (_ FfiConverterOptionalTypeScriptBuf) Write(writer io.Writer, value *ScriptBuf) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypeScriptBufINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypeScriptBuf struct{}

func (_ FfiDestroyerOptionalTypeScriptBuf) Destroy(value *ScriptBuf) {
	if value != nil {
		FfiDestroyerTypeScriptBuf{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypeUntrustedString struct{}

var FfiConverterOptionalTypeUntrustedStringINSTANCE = FfiConverterOptionalTypeUntrustedString{}

func (c FfiConverterOptionalTypeUntrustedString) Lift(rb RustBufferI) *UntrustedString {
	return LiftFromRustBuffer[*UntrustedString](c, rb)
}

func (_ FfiConverterOptionalTypeUntrustedString) Read(reader io.Reader) *UntrustedString {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypeUntrustedStringINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypeUntrustedString) Lower(value *UntrustedString) C.RustBuffer {
	return LowerIntoRustBuffer[*UntrustedString](c, value)
}

func (c FfiConverterOptionalTypeUntrustedString) LowerExternal(value *UntrustedString) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*UntrustedString](c, value))
}

func (_ FfiConverterOptionalTypeUntrustedString) Write(writer io.Writer, value *UntrustedString) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypeUntrustedStringINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypeUntrustedString struct{}

func (_ FfiDestroyerOptionalTypeUntrustedString) Destroy(value *UntrustedString) {
	if value != nil {
		FfiDestroyerTypeUntrustedString{}.Destroy(*value)
	}
}

type FfiConverterOptionalTypeUserChannelId struct{}

var FfiConverterOptionalTypeUserChannelIdINSTANCE = FfiConverterOptionalTypeUserChannelId{}

func (c FfiConverterOptionalTypeUserChannelId) Lift(rb RustBufferI) *UserChannelId {
	return LiftFromRustBuffer[*UserChannelId](c, rb)
}

func (_ FfiConverterOptionalTypeUserChannelId) Read(reader io.Reader) *UserChannelId {
	if readInt8(reader) == 0 {
		return nil
	}
	temp := FfiConverterTypeUserChannelIdINSTANCE.Read(reader)
	return &temp
}

func (c FfiConverterOptionalTypeUserChannelId) Lower(value *UserChannelId) C.RustBuffer {
	return LowerIntoRustBuffer[*UserChannelId](c, value)
}

func (c FfiConverterOptionalTypeUserChannelId) LowerExternal(value *UserChannelId) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[*UserChannelId](c, value))
}

func (_ FfiConverterOptionalTypeUserChannelId) Write(writer io.Writer, value *UserChannelId) {
	if value == nil {
		writeInt8(writer, 0)
	} else {
		writeInt8(writer, 1)
		FfiConverterTypeUserChannelIdINSTANCE.Write(writer, *value)
	}
}

type FfiDestroyerOptionalTypeUserChannelId struct{}

func (_ FfiDestroyerOptionalTypeUserChannelId) Destroy(value *UserChannelId) {
	if value != nil {
		FfiDestroyerTypeUserChannelId{}.Destroy(*value)
	}
}

type FfiConverterSequenceUint8 struct{}

var FfiConverterSequenceUint8INSTANCE = FfiConverterSequenceUint8{}

func (c FfiConverterSequenceUint8) Lift(rb RustBufferI) []uint8 {
	return LiftFromRustBuffer[[]uint8](c, rb)
}

func (c FfiConverterSequenceUint8) Read(reader io.Reader) []uint8 {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]uint8, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterUint8INSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceUint8) Lower(value []uint8) C.RustBuffer {
	return LowerIntoRustBuffer[[]uint8](c, value)
}

func (c FfiConverterSequenceUint8) LowerExternal(value []uint8) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]uint8](c, value))
}

func (c FfiConverterSequenceUint8) Write(writer io.Writer, value []uint8) {
	if len(value) > math.MaxInt32 {
		panic("[]uint8 is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterUint8INSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceUint8 struct{}

func (FfiDestroyerSequenceUint8) Destroy(sequence []uint8) {
	for _, value := range sequence {
		FfiDestroyerUint8{}.Destroy(value)
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

func (c FfiConverterSequenceUint64) LowerExternal(value []uint64) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]uint64](c, value))
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

type FfiConverterSequenceBytes struct{}

var FfiConverterSequenceBytesINSTANCE = FfiConverterSequenceBytes{}

func (c FfiConverterSequenceBytes) Lift(rb RustBufferI) [][]byte {
	return LiftFromRustBuffer[[][]byte](c, rb)
}

func (c FfiConverterSequenceBytes) Read(reader io.Reader) [][]byte {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([][]byte, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterBytesINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceBytes) Lower(value [][]byte) C.RustBuffer {
	return LowerIntoRustBuffer[[][]byte](c, value)
}

func (c FfiConverterSequenceBytes) LowerExternal(value [][]byte) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[][]byte](c, value))
}

func (c FfiConverterSequenceBytes) Write(writer io.Writer, value [][]byte) {
	if len(value) > math.MaxInt32 {
		panic("[][]byte is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterBytesINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceBytes struct{}

func (FfiDestroyerSequenceBytes) Destroy(sequence [][]byte) {
	for _, value := range sequence {
		FfiDestroyerBytes{}.Destroy(value)
	}
}

type FfiConverterSequenceChannelDetails struct{}

var FfiConverterSequenceChannelDetailsINSTANCE = FfiConverterSequenceChannelDetails{}

func (c FfiConverterSequenceChannelDetails) Lift(rb RustBufferI) []ChannelDetails {
	return LiftFromRustBuffer[[]ChannelDetails](c, rb)
}

func (c FfiConverterSequenceChannelDetails) Read(reader io.Reader) []ChannelDetails {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]ChannelDetails, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterChannelDetailsINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceChannelDetails) Lower(value []ChannelDetails) C.RustBuffer {
	return LowerIntoRustBuffer[[]ChannelDetails](c, value)
}

func (c FfiConverterSequenceChannelDetails) LowerExternal(value []ChannelDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]ChannelDetails](c, value))
}

func (c FfiConverterSequenceChannelDetails) Write(writer io.Writer, value []ChannelDetails) {
	if len(value) > math.MaxInt32 {
		panic("[]ChannelDetails is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterChannelDetailsINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceChannelDetails struct{}

func (FfiDestroyerSequenceChannelDetails) Destroy(sequence []ChannelDetails) {
	for _, value := range sequence {
		FfiDestroyerChannelDetails{}.Destroy(value)
	}
}

type FfiConverterSequenceCustomTlvRecord struct{}

var FfiConverterSequenceCustomTlvRecordINSTANCE = FfiConverterSequenceCustomTlvRecord{}

func (c FfiConverterSequenceCustomTlvRecord) Lift(rb RustBufferI) []CustomTlvRecord {
	return LiftFromRustBuffer[[]CustomTlvRecord](c, rb)
}

func (c FfiConverterSequenceCustomTlvRecord) Read(reader io.Reader) []CustomTlvRecord {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]CustomTlvRecord, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterCustomTlvRecordINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceCustomTlvRecord) Lower(value []CustomTlvRecord) C.RustBuffer {
	return LowerIntoRustBuffer[[]CustomTlvRecord](c, value)
}

func (c FfiConverterSequenceCustomTlvRecord) LowerExternal(value []CustomTlvRecord) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]CustomTlvRecord](c, value))
}

func (c FfiConverterSequenceCustomTlvRecord) Write(writer io.Writer, value []CustomTlvRecord) {
	if len(value) > math.MaxInt32 {
		panic("[]CustomTlvRecord is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterCustomTlvRecordINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceCustomTlvRecord struct{}

func (FfiDestroyerSequenceCustomTlvRecord) Destroy(sequence []CustomTlvRecord) {
	for _, value := range sequence {
		FfiDestroyerCustomTlvRecord{}.Destroy(value)
	}
}

type FfiConverterSequencePaymentDetails struct{}

var FfiConverterSequencePaymentDetailsINSTANCE = FfiConverterSequencePaymentDetails{}

func (c FfiConverterSequencePaymentDetails) Lift(rb RustBufferI) []PaymentDetails {
	return LiftFromRustBuffer[[]PaymentDetails](c, rb)
}

func (c FfiConverterSequencePaymentDetails) Read(reader io.Reader) []PaymentDetails {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]PaymentDetails, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterPaymentDetailsINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequencePaymentDetails) Lower(value []PaymentDetails) C.RustBuffer {
	return LowerIntoRustBuffer[[]PaymentDetails](c, value)
}

func (c FfiConverterSequencePaymentDetails) LowerExternal(value []PaymentDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]PaymentDetails](c, value))
}

func (c FfiConverterSequencePaymentDetails) Write(writer io.Writer, value []PaymentDetails) {
	if len(value) > math.MaxInt32 {
		panic("[]PaymentDetails is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterPaymentDetailsINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequencePaymentDetails struct{}

func (FfiDestroyerSequencePaymentDetails) Destroy(sequence []PaymentDetails) {
	for _, value := range sequence {
		FfiDestroyerPaymentDetails{}.Destroy(value)
	}
}

type FfiConverterSequencePeerDetails struct{}

var FfiConverterSequencePeerDetailsINSTANCE = FfiConverterSequencePeerDetails{}

func (c FfiConverterSequencePeerDetails) Lift(rb RustBufferI) []PeerDetails {
	return LiftFromRustBuffer[[]PeerDetails](c, rb)
}

func (c FfiConverterSequencePeerDetails) Read(reader io.Reader) []PeerDetails {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]PeerDetails, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterPeerDetailsINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequencePeerDetails) Lower(value []PeerDetails) C.RustBuffer {
	return LowerIntoRustBuffer[[]PeerDetails](c, value)
}

func (c FfiConverterSequencePeerDetails) LowerExternal(value []PeerDetails) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]PeerDetails](c, value))
}

func (c FfiConverterSequencePeerDetails) Write(writer io.Writer, value []PeerDetails) {
	if len(value) > math.MaxInt32 {
		panic("[]PeerDetails is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterPeerDetailsINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequencePeerDetails struct{}

func (FfiDestroyerSequencePeerDetails) Destroy(sequence []PeerDetails) {
	for _, value := range sequence {
		FfiDestroyerPeerDetails{}.Destroy(value)
	}
}

type FfiConverterSequenceRouteHintHop struct{}

var FfiConverterSequenceRouteHintHopINSTANCE = FfiConverterSequenceRouteHintHop{}

func (c FfiConverterSequenceRouteHintHop) Lift(rb RustBufferI) []RouteHintHop {
	return LiftFromRustBuffer[[]RouteHintHop](c, rb)
}

func (c FfiConverterSequenceRouteHintHop) Read(reader io.Reader) []RouteHintHop {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]RouteHintHop, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterRouteHintHopINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceRouteHintHop) Lower(value []RouteHintHop) C.RustBuffer {
	return LowerIntoRustBuffer[[]RouteHintHop](c, value)
}

func (c FfiConverterSequenceRouteHintHop) LowerExternal(value []RouteHintHop) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]RouteHintHop](c, value))
}

func (c FfiConverterSequenceRouteHintHop) Write(writer io.Writer, value []RouteHintHop) {
	if len(value) > math.MaxInt32 {
		panic("[]RouteHintHop is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterRouteHintHopINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceRouteHintHop struct{}

func (FfiDestroyerSequenceRouteHintHop) Destroy(sequence []RouteHintHop) {
	for _, value := range sequence {
		FfiDestroyerRouteHintHop{}.Destroy(value)
	}
}

type FfiConverterSequenceLightningBalance struct{}

var FfiConverterSequenceLightningBalanceINSTANCE = FfiConverterSequenceLightningBalance{}

func (c FfiConverterSequenceLightningBalance) Lift(rb RustBufferI) []LightningBalance {
	return LiftFromRustBuffer[[]LightningBalance](c, rb)
}

func (c FfiConverterSequenceLightningBalance) Read(reader io.Reader) []LightningBalance {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]LightningBalance, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterLightningBalanceINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceLightningBalance) Lower(value []LightningBalance) C.RustBuffer {
	return LowerIntoRustBuffer[[]LightningBalance](c, value)
}

func (c FfiConverterSequenceLightningBalance) LowerExternal(value []LightningBalance) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]LightningBalance](c, value))
}

func (c FfiConverterSequenceLightningBalance) Write(writer io.Writer, value []LightningBalance) {
	if len(value) > math.MaxInt32 {
		panic("[]LightningBalance is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterLightningBalanceINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceLightningBalance struct{}

func (FfiDestroyerSequenceLightningBalance) Destroy(sequence []LightningBalance) {
	for _, value := range sequence {
		FfiDestroyerLightningBalance{}.Destroy(value)
	}
}

type FfiConverterSequenceNetwork struct{}

var FfiConverterSequenceNetworkINSTANCE = FfiConverterSequenceNetwork{}

func (c FfiConverterSequenceNetwork) Lift(rb RustBufferI) []Network {
	return LiftFromRustBuffer[[]Network](c, rb)
}

func (c FfiConverterSequenceNetwork) Read(reader io.Reader) []Network {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]Network, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterNetworkINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceNetwork) Lower(value []Network) C.RustBuffer {
	return LowerIntoRustBuffer[[]Network](c, value)
}

func (c FfiConverterSequenceNetwork) LowerExternal(value []Network) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]Network](c, value))
}

func (c FfiConverterSequenceNetwork) Write(writer io.Writer, value []Network) {
	if len(value) > math.MaxInt32 {
		panic("[]Network is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterNetworkINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceNetwork struct{}

func (FfiDestroyerSequenceNetwork) Destroy(sequence []Network) {
	for _, value := range sequence {
		FfiDestroyerNetwork{}.Destroy(value)
	}
}

type FfiConverterSequencePendingSweepBalance struct{}

var FfiConverterSequencePendingSweepBalanceINSTANCE = FfiConverterSequencePendingSweepBalance{}

func (c FfiConverterSequencePendingSweepBalance) Lift(rb RustBufferI) []PendingSweepBalance {
	return LiftFromRustBuffer[[]PendingSweepBalance](c, rb)
}

func (c FfiConverterSequencePendingSweepBalance) Read(reader io.Reader) []PendingSweepBalance {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]PendingSweepBalance, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterPendingSweepBalanceINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequencePendingSweepBalance) Lower(value []PendingSweepBalance) C.RustBuffer {
	return LowerIntoRustBuffer[[]PendingSweepBalance](c, value)
}

func (c FfiConverterSequencePendingSweepBalance) LowerExternal(value []PendingSweepBalance) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]PendingSweepBalance](c, value))
}

func (c FfiConverterSequencePendingSweepBalance) Write(writer io.Writer, value []PendingSweepBalance) {
	if len(value) > math.MaxInt32 {
		panic("[]PendingSweepBalance is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterPendingSweepBalanceINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequencePendingSweepBalance struct{}

func (FfiDestroyerSequencePendingSweepBalance) Destroy(sequence []PendingSweepBalance) {
	for _, value := range sequence {
		FfiDestroyerPendingSweepBalance{}.Destroy(value)
	}
}

type FfiConverterSequenceSequenceRouteHintHop struct{}

var FfiConverterSequenceSequenceRouteHintHopINSTANCE = FfiConverterSequenceSequenceRouteHintHop{}

func (c FfiConverterSequenceSequenceRouteHintHop) Lift(rb RustBufferI) [][]RouteHintHop {
	return LiftFromRustBuffer[[][]RouteHintHop](c, rb)
}

func (c FfiConverterSequenceSequenceRouteHintHop) Read(reader io.Reader) [][]RouteHintHop {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([][]RouteHintHop, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterSequenceRouteHintHopINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceSequenceRouteHintHop) Lower(value [][]RouteHintHop) C.RustBuffer {
	return LowerIntoRustBuffer[[][]RouteHintHop](c, value)
}

func (c FfiConverterSequenceSequenceRouteHintHop) LowerExternal(value [][]RouteHintHop) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[][]RouteHintHop](c, value))
}

func (c FfiConverterSequenceSequenceRouteHintHop) Write(writer io.Writer, value [][]RouteHintHop) {
	if len(value) > math.MaxInt32 {
		panic("[][]RouteHintHop is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterSequenceRouteHintHopINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceSequenceRouteHintHop struct{}

func (FfiDestroyerSequenceSequenceRouteHintHop) Destroy(sequence [][]RouteHintHop) {
	for _, value := range sequence {
		FfiDestroyerSequenceRouteHintHop{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeAddress struct{}

var FfiConverterSequenceTypeAddressINSTANCE = FfiConverterSequenceTypeAddress{}

func (c FfiConverterSequenceTypeAddress) Lift(rb RustBufferI) []Address {
	return LiftFromRustBuffer[[]Address](c, rb)
}

func (c FfiConverterSequenceTypeAddress) Read(reader io.Reader) []Address {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]Address, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeAddressINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeAddress) Lower(value []Address) C.RustBuffer {
	return LowerIntoRustBuffer[[]Address](c, value)
}

func (c FfiConverterSequenceTypeAddress) LowerExternal(value []Address) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]Address](c, value))
}

func (c FfiConverterSequenceTypeAddress) Write(writer io.Writer, value []Address) {
	if len(value) > math.MaxInt32 {
		panic("[]Address is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeAddressINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeAddress struct{}

func (FfiDestroyerSequenceTypeAddress) Destroy(sequence []Address) {
	for _, value := range sequence {
		FfiDestroyerTypeAddress{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeNodeId struct{}

var FfiConverterSequenceTypeNodeIdINSTANCE = FfiConverterSequenceTypeNodeId{}

func (c FfiConverterSequenceTypeNodeId) Lift(rb RustBufferI) []NodeId {
	return LiftFromRustBuffer[[]NodeId](c, rb)
}

func (c FfiConverterSequenceTypeNodeId) Read(reader io.Reader) []NodeId {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]NodeId, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeNodeIdINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeNodeId) Lower(value []NodeId) C.RustBuffer {
	return LowerIntoRustBuffer[[]NodeId](c, value)
}

func (c FfiConverterSequenceTypeNodeId) LowerExternal(value []NodeId) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]NodeId](c, value))
}

func (c FfiConverterSequenceTypeNodeId) Write(writer io.Writer, value []NodeId) {
	if len(value) > math.MaxInt32 {
		panic("[]NodeId is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeNodeIdINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeNodeId struct{}

func (FfiDestroyerSequenceTypeNodeId) Destroy(sequence []NodeId) {
	for _, value := range sequence {
		FfiDestroyerTypeNodeId{}.Destroy(value)
	}
}

type FfiConverterSequenceTypePublicKey struct{}

var FfiConverterSequenceTypePublicKeyINSTANCE = FfiConverterSequenceTypePublicKey{}

func (c FfiConverterSequenceTypePublicKey) Lift(rb RustBufferI) []PublicKey {
	return LiftFromRustBuffer[[]PublicKey](c, rb)
}

func (c FfiConverterSequenceTypePublicKey) Read(reader io.Reader) []PublicKey {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]PublicKey, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypePublicKeyINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypePublicKey) Lower(value []PublicKey) C.RustBuffer {
	return LowerIntoRustBuffer[[]PublicKey](c, value)
}

func (c FfiConverterSequenceTypePublicKey) LowerExternal(value []PublicKey) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]PublicKey](c, value))
}

func (c FfiConverterSequenceTypePublicKey) Write(writer io.Writer, value []PublicKey) {
	if len(value) > math.MaxInt32 {
		panic("[]PublicKey is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypePublicKeyINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypePublicKey struct{}

func (FfiDestroyerSequenceTypePublicKey) Destroy(sequence []PublicKey) {
	for _, value := range sequence {
		FfiDestroyerTypePublicKey{}.Destroy(value)
	}
}

type FfiConverterSequenceTypeSocketAddress struct{}

var FfiConverterSequenceTypeSocketAddressINSTANCE = FfiConverterSequenceTypeSocketAddress{}

func (c FfiConverterSequenceTypeSocketAddress) Lift(rb RustBufferI) []SocketAddress {
	return LiftFromRustBuffer[[]SocketAddress](c, rb)
}

func (c FfiConverterSequenceTypeSocketAddress) Read(reader io.Reader) []SocketAddress {
	length := readInt32(reader)
	if length == 0 {
		return nil
	}
	result := make([]SocketAddress, 0, length)
	for i := int32(0); i < length; i++ {
		result = append(result, FfiConverterTypeSocketAddressINSTANCE.Read(reader))
	}
	return result
}

func (c FfiConverterSequenceTypeSocketAddress) Lower(value []SocketAddress) C.RustBuffer {
	return LowerIntoRustBuffer[[]SocketAddress](c, value)
}

func (c FfiConverterSequenceTypeSocketAddress) LowerExternal(value []SocketAddress) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[[]SocketAddress](c, value))
}

func (c FfiConverterSequenceTypeSocketAddress) Write(writer io.Writer, value []SocketAddress) {
	if len(value) > math.MaxInt32 {
		panic("[]SocketAddress is too large to fit into Int32")
	}

	writeInt32(writer, int32(len(value)))
	for _, item := range value {
		FfiConverterTypeSocketAddressINSTANCE.Write(writer, item)
	}
}

type FfiDestroyerSequenceTypeSocketAddress struct{}

func (FfiDestroyerSequenceTypeSocketAddress) Destroy(sequence []SocketAddress) {
	for _, value := range sequence {
		FfiDestroyerTypeSocketAddress{}.Destroy(value)
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

func (c FfiConverterMapStringString) LowerExternal(value map[string]string) ExternalCRustBuffer {
	return RustBufferFromC(LowerIntoRustBuffer[map[string]string](c, value))
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

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type Address = string
type FfiConverterTypeAddress = FfiConverterString
type FfiDestroyerTypeAddress = FfiDestroyerString

var FfiConverterTypeAddressINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type BlockHash = string
type FfiConverterTypeBlockHash = FfiConverterString
type FfiDestroyerTypeBlockHash = FfiDestroyerString

var FfiConverterTypeBlockHashINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type ChannelId = string
type FfiConverterTypeChannelId = FfiConverterString
type FfiDestroyerTypeChannelId = FfiDestroyerString

var FfiConverterTypeChannelIdINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type LSPS1OrderId = string
type FfiConverterTypeLSPS1OrderId = FfiConverterString
type FfiDestroyerTypeLSPS1OrderId = FfiDestroyerString

var FfiConverterTypeLSPS1OrderIdINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type LSPSDateTime = string
type FfiConverterTypeLSPSDateTime = FfiConverterString
type FfiDestroyerTypeLSPSDateTime = FfiDestroyerString

var FfiConverterTypeLSPSDateTimeINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type Mnemonic = string
type FfiConverterTypeMnemonic = FfiConverterString
type FfiDestroyerTypeMnemonic = FfiDestroyerString

var FfiConverterTypeMnemonicINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type NodeAlias = string
type FfiConverterTypeNodeAlias = FfiConverterString
type FfiDestroyerTypeNodeAlias = FfiDestroyerString

var FfiConverterTypeNodeAliasINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type NodeId = string
type FfiConverterTypeNodeId = FfiConverterString
type FfiDestroyerTypeNodeId = FfiDestroyerString

var FfiConverterTypeNodeIdINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type OfferId = string
type FfiConverterTypeOfferId = FfiConverterString
type FfiDestroyerTypeOfferId = FfiDestroyerString

var FfiConverterTypeOfferIdINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type PaymentHash = string
type FfiConverterTypePaymentHash = FfiConverterString
type FfiDestroyerTypePaymentHash = FfiDestroyerString

var FfiConverterTypePaymentHashINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type PaymentId = string
type FfiConverterTypePaymentId = FfiConverterString
type FfiDestroyerTypePaymentId = FfiDestroyerString

var FfiConverterTypePaymentIdINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type PaymentPreimage = string
type FfiConverterTypePaymentPreimage = FfiConverterString
type FfiDestroyerTypePaymentPreimage = FfiDestroyerString

var FfiConverterTypePaymentPreimageINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type PaymentSecret = string
type FfiConverterTypePaymentSecret = FfiConverterString
type FfiDestroyerTypePaymentSecret = FfiDestroyerString

var FfiConverterTypePaymentSecretINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type PublicKey = string
type FfiConverterTypePublicKey = FfiConverterString
type FfiDestroyerTypePublicKey = FfiDestroyerString

var FfiConverterTypePublicKeyINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type ScriptBuf = string
type FfiConverterTypeScriptBuf = FfiConverterString
type FfiDestroyerTypeScriptBuf = FfiDestroyerString

var FfiConverterTypeScriptBufINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type SocketAddress = string
type FfiConverterTypeSocketAddress = FfiConverterString
type FfiDestroyerTypeSocketAddress = FfiDestroyerString

var FfiConverterTypeSocketAddressINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type Txid = string
type FfiConverterTypeTxid = FfiConverterString
type FfiDestroyerTypeTxid = FfiDestroyerString

var FfiConverterTypeTxidINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type UntrustedString = string
type FfiConverterTypeUntrustedString = FfiConverterString
type FfiDestroyerTypeUntrustedString = FfiDestroyerString

var FfiConverterTypeUntrustedStringINSTANCE = FfiConverterString{}

/**
 * Typealias from the type name used in the UDL file to the builtin type.  This
 * is needed because the UDL type name is used in function/method signatures.
 * It's also what we have an external type that references a custom type.
 */
type UserChannelId = string
type FfiConverterTypeUserChannelId = FfiConverterString
type FfiDestroyerTypeUserChannelId = FfiDestroyerString

var FfiConverterTypeUserChannelIdINSTANCE = FfiConverterString{}

const (
	uniffiRustFuturePollReady      int8 = 0
	uniffiRustFuturePollMaybeReady int8 = 1
)

type rustFuturePollFunc func(C.uint64_t, C.UniffiRustFutureContinuationCallback, C.uint64_t)
type rustFutureCompleteFunc[T any] func(C.uint64_t, *C.RustCallStatus) T
type rustFutureFreeFunc func(C.uint64_t)

//export ldk_node_uniffiFutureContinuationCallback
func ldk_node_uniffiFutureContinuationCallback(data C.uint64_t, pollResult C.int8_t) {
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
			(C.UniffiRustFutureContinuationCallback)(C.ldk_node_uniffiFutureContinuationCallback),
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

//export ldk_node_uniffiFreeGorutine
func ldk_node_uniffiFreeGorutine(data C.uint64_t) {
	handle := cgo.Handle(uintptr(data))
	defer handle.Delete()

	guard := handle.Value().(chan struct{})
	guard <- struct{}{}
}

func DefaultConfig() Config {
	return FfiConverterConfigINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_func_default_config(_uniffiStatus),
		}
	}))
}

func GenerateEntropyMnemonic(wordCount *WordCount) Mnemonic {
	return FfiConverterTypeMnemonicINSTANCE.Lift(rustCall(func(_uniffiStatus *C.RustCallStatus) RustBufferI {
		return GoRustBuffer{
			inner: C.uniffi_ldk_node_fn_func_generate_entropy_mnemonic(FfiConverterOptionalWordCountINSTANCE.Lower(wordCount), _uniffiStatus),
		}
	}))
}
