// Package jxl provides lossless JPEG <-> JPEG XL transcoding through the
// system-installed libjxl shared library. JPEG XL is the only widely
// available codec that rewrites a JPEG codestream and guarantees bit-exact
// reconstruction of the original file, including every marker and metadata
// segment. Fotoro uses it to cold-store originals at a fraction of their
// size while preserving byte-identical restore.
package jxl

/*
#cgo LDFLAGS: -l:libjxl.so.0.10
#include <stdlib.h>
#include <stdint.h>
#include <string.h>
#include <stdio.h>

// Minimal declarations for the stable libjxl C API (ABI versioned 0.x). The
// -devel headers are not required since we link the versioned soname.

// Encoder
extern void*  JxlEncoderCreate(const void* memory_manager);
extern void   JxlEncoderDestroy(void* enc);
extern int    JxlEncoderUseContainer(void* enc, int use_container);
extern int    JxlEncoderStoreJPEGMetadata(void* enc, int store_jpeg_metadata);
extern void*  JxlEncoderFrameSettingsCreate(void* enc, const void* source);
extern int    JxlEncoderFrameSettingsSetOption(void* frame_settings, int option, int64_t value);
extern int    JxlEncoderAddJPEGFrame(const void* frame_settings, const uint8_t* buffer, size_t size);
extern void   JxlEncoderCloseInput(void* enc);
extern int    JxlEncoderProcessOutput(void* enc, uint8_t** next_out, size_t* avail_out);

// Decoder
extern void*  JxlDecoderCreate(const void* memory_manager);
extern void   JxlDecoderDestroy(void* dec);
extern int    JxlDecoderSubscribeEvents(void* dec, int events);
extern int    JxlDecoderSetInput(void* dec, const uint8_t* data, size_t size);
extern size_t JxlDecoderReleaseInput(void* dec);
extern void   JxlDecoderCloseInput(void* dec);
extern int    JxlDecoderProcessInput(void* dec);
extern int    JxlDecoderSetJPEGBuffer(void* dec, uint8_t* data, size_t size);
extern size_t JxlDecoderReleaseJPEGBuffer(void* dec);

enum {
    JXL_ENC_SUCCESS = 0,
    JXL_ENC_ERROR = 1,
    JXL_ENC_NEED_MORE_OUTPUT = 2,

    JXL_ENC_FRAME_SETTING_EFFORT = 0,

    JXL_DEC_SUCCESS = 0,
    JXL_DEC_ERROR = 1,
    JXL_DEC_NEED_MORE_INPUT = 2,
    JXL_DEC_JPEG_NEED_MORE_OUTPUT = 6,
    JXL_DEC_FULL_IMAGE = 0x1000,
    JXL_DEC_JPEG_RECONSTRUCTION = 0x2000
};

typedef struct {
    int status;      // 0 ok, -1 api/config error, -2 encode error, -3 decode error
    char err[160];
} jxl_op_result;

// jxl_encode_jpeg losslessly transcodes one JPEG file into a JXL codestream.
static jxl_op_result jxl_encode_jpeg(const uint8_t* jpeg, size_t jpeg_size,
                                     int effort, uint8_t** out, size_t* out_size) {
    jxl_op_result r; r.status = 0; r.err[0] = 0;
    void* enc = JxlEncoderCreate(NULL);
    if (!enc) { r.status = -1; snprintf(r.err, sizeof(r.err), "JxlEncoderCreate failed"); return r; }
    if (JxlEncoderUseContainer(enc, 1) != JXL_ENC_SUCCESS) {
        r.status = -2; snprintf(r.err, sizeof(r.err), "UseContainer failed");
        JxlEncoderDestroy(enc); return r;
    }
    if (JxlEncoderStoreJPEGMetadata(enc, 1) != JXL_ENC_SUCCESS) {
        r.status = -2; snprintf(r.err, sizeof(r.err), "StoreJPEGMetadata failed");
        JxlEncoderDestroy(enc); return r;
    }
    void* fs = JxlEncoderFrameSettingsCreate(enc, NULL);
    if (!fs) {
        r.status = -1; snprintf(r.err, sizeof(r.err), "FrameSettingsCreate failed");
        JxlEncoderDestroy(enc); return r;
    }
    if (effort < 1) effort = 4;
    if (effort > 9) effort = 9;
    JxlEncoderFrameSettingsSetOption(fs, JXL_ENC_FRAME_SETTING_EFFORT, effort);
    if (JxlEncoderAddJPEGFrame(fs, jpeg, jpeg_size) != JXL_ENC_SUCCESS) {
        r.status = -2; snprintf(r.err, sizeof(r.err), "AddJPEGFrame failed (not a supported JPEG?)");
        JxlEncoderDestroy(enc); return r;
    }
    JxlEncoderCloseInput(enc);

    size_t cap = jpeg_size + 65536;
    uint8_t* buf = (uint8_t*)malloc(cap);
    if (!buf) { r.status = -1; snprintf(r.err, sizeof(r.err), "out of memory"); JxlEncoderDestroy(enc); return r; }
    uint8_t* next = buf;
    size_t avail = cap;
    for (;;) {
        int st = JxlEncoderProcessOutput(enc, &next, &avail);
        if (st == JXL_ENC_SUCCESS) break;
        if (st == JXL_ENC_NEED_MORE_OUTPUT) {
            size_t used = (size_t)(next - buf);
            cap = cap * 2;
            uint8_t* nb = (uint8_t*)realloc(buf, cap);
            if (!nb) { free(buf); r.status = -1; snprintf(r.err, sizeof(r.err), "out of memory"); JxlEncoderDestroy(enc); return r; }
            buf = nb;
            next = buf + used;
            avail = cap - used;
            continue;
        }
        free(buf);
        r.status = -2; snprintf(r.err, sizeof(r.err), "ProcessOutput failed");
        JxlEncoderDestroy(enc); return r;
    }
    JxlEncoderDestroy(enc);
    *out = buf;
    *out_size = (size_t)(next - buf);
    return r;
}

// jxl_decode_jpeg reconstructs the exact original JPEG bytes from a JXL file
// produced by jxl_encode_jpeg (or cjxl --lossless_jpeg=1).
static jxl_op_result jxl_decode_jpeg(const uint8_t* jx, size_t jx_size,
                                     uint8_t** out, size_t* out_size) {
    jxl_op_result r; r.status = 0; r.err[0] = 0;
    void* dec = JxlDecoderCreate(NULL);
    if (!dec) { r.status = -1; snprintf(r.err, sizeof(r.err), "JxlDecoderCreate failed"); return r; }
    if (JxlDecoderSubscribeEvents(dec, JXL_DEC_JPEG_RECONSTRUCTION | JXL_DEC_FULL_IMAGE) != JXL_DEC_SUCCESS) {
        r.status = -3; snprintf(r.err, sizeof(r.err), "SubscribeEvents failed");
        JxlDecoderDestroy(dec); return r;
    }
    JxlDecoderSetInput(dec, jx, jx_size);
    JxlDecoderCloseInput(dec);

    size_t cap = jx_size * 4 + 65536;
    uint8_t* buf = (uint8_t*)malloc(cap);
    if (!buf) { r.status = -1; snprintf(r.err, sizeof(r.err), "out of memory"); JxlDecoderDestroy(dec); return r; }
    size_t written = 0;
    int buf_set = 0;
    int guard = 0;
    for (;;) {
        int st = JxlDecoderProcessInput(dec);
        if (st == JXL_DEC_SUCCESS) break;
        if (st == JXL_DEC_ERROR) {
            free(buf); r.status = -3; snprintf(r.err, sizeof(r.err), "decoder error (corrupt JXL?)");
            JxlDecoderDestroy(dec); return r;
        }
        if (st == JXL_DEC_NEED_MORE_INPUT) {
            free(buf); r.status = -3; snprintf(r.err, sizeof(r.err), "truncated JXL codestream");
            JxlDecoderDestroy(dec); return r;
        }
        if (st == JXL_DEC_JPEG_RECONSTRUCTION) {
            if (JxlDecoderSetJPEGBuffer(dec, buf + written, cap - written) != JXL_DEC_SUCCESS) {
                free(buf); r.status = -3; snprintf(r.err, sizeof(r.err), "SetJPEGBuffer failed");
                JxlDecoderDestroy(dec); return r;
            }
            buf_set = 1;
            continue;
        }
        if (st == JXL_DEC_FULL_IMAGE) {
            continue;
        }
        if (st == JXL_DEC_JPEG_NEED_MORE_OUTPUT) {
            size_t unwritten = JxlDecoderReleaseJPEGBuffer(dec);
            written = cap - unwritten;
            cap = cap * 2;
            uint8_t* nb = (uint8_t*)realloc(buf, cap);
            if (!nb) { free(buf); r.status = -1; snprintf(r.err, sizeof(r.err), "out of memory"); JxlDecoderDestroy(dec); return r; }
            buf = nb;
            if (JxlDecoderSetJPEGBuffer(dec, buf + written, cap - written) != JXL_DEC_SUCCESS) {
                free(buf); r.status = -3; snprintf(r.err, sizeof(r.err), "SetJPEGBuffer failed");
                JxlDecoderDestroy(dec); return r;
            }
            continue;
        }
        // Any other event: surface it instead of spinning.
        if (++guard > 1000) {
            free(buf); r.status = -3; snprintf(r.err, sizeof(r.err), "unexpected decoder status %d", st);
            JxlDecoderDestroy(dec); return r;
        }
    }
    if (buf_set) {
        size_t unwritten = JxlDecoderReleaseJPEGBuffer(dec);
        written = cap - unwritten;
    }
    JxlDecoderDestroy(dec);
    if (written == 0) {
        free(buf); r.status = -3; snprintf(r.err, sizeof(r.err), "no JPEG reconstruction data");
        return r;
    }
    *out = buf;
    *out_size = written;
    return r;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// Available reports whether the libjxl binding is usable. It is true when the
// package compiled — the soname is resolved at link time.
func Available() bool { return true }

// EncodeJPEG losslessly transcodes JPEG file bytes into a JPEG XL codestream
// (effort 1-9, default 4). The result decodes back to identical bytes.
func EncodeJPEG(jpeg []byte, effort int) ([]byte, error) {
	if len(jpeg) < 4 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 {
		return nil, errors.New("not a JPEG file")
	}
	var out *C.uint8_t
	var outSize C.size_t
	res := C.jxl_encode_jpeg((*C.uint8_t)(unsafe.Pointer(&jpeg[0])), C.size_t(len(jpeg)),
		C.int(effort), &out, &outSize)
	if res.status != 0 {
		return nil, fmt.Errorf("jxl encode: %s", C.GoString(&res.err[0]))
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoBytes(unsafe.Pointer(out), C.int(outSize)), nil
}

// DecodeJPEG reconstructs the exact original JPEG bytes from a JPEG XL file.
// Returns an error when the input has no JPEG reconstruction data.
func DecodeJPEG(jxlData []byte) ([]byte, error) {
	if len(jxlData) < 2 {
		return nil, errors.New("empty JXL input")
	}
	var out *C.uint8_t
	var outSize C.size_t
	res := C.jxl_decode_jpeg((*C.uint8_t)(unsafe.Pointer(&jxlData[0])), C.size_t(len(jxlData)),
		&out, &outSize)
	if res.status != 0 {
		return nil, fmt.Errorf("jxl decode: %s", C.GoString(&res.err[0]))
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoBytes(unsafe.Pointer(out), C.int(outSize)), nil
}
