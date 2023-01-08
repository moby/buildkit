package openssl

// #include "shim.h"
import "C"

import (
	"unsafe"

	"github.com/mattn/go-pointer"
)

//export go_ssl_crypto_ex_free
func go_ssl_crypto_ex_free(
	parent *C.void, ptr unsafe.Pointer,
	cryptoData *C.CRYPTO_EX_DATA, idx C.int,
	argl C.long, argp *C.void,
) {
	pointer.Unref(ptr)
}
