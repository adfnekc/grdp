/*
 * gen-codec-vectors.c - generate reference codec test vectors.
 *
 * Encodes a deterministic image with the NSCodec and RemoteFX encoders from
 * libfreerdp and writes the raw encoder output next to the source image. The
 * Go tests then decode those bytes with our own implementation and compare
 * against the original pixels, which is the only way to check an image codec
 * without a live server that speaks it.
 *
 * The FreeRDP headers are not installed, so the handful of declarations needed
 * are repeated here against the exported symbols.
 *
 * Build and run (paths are for Debian/Ubuntu libfreerdp3):
 *
 *   cc -O2 -o /tmp/genvec scripts/gen-codec-vectors.c \
 *      /usr/lib/x86_64-linux-gnu/libfreerdp3.so.3 \
 *      /usr/lib/x86_64-linux-gnu/libwinpr3.so.3
 *   /tmp/genvec codec/testdata
 */

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef int BOOL;
typedef uint8_t BYTE;
typedef uint16_t UINT16;
typedef uint32_t UINT32;
typedef uint32_t DWORD;

#define TRUE 1
#define FALSE 0

/* winpr/stream.h 3.5.1 (public struct, so it can be read after encoding). */
typedef struct s_wStreamPool wStreamPool;
typedef struct
{
	BYTE* buffer;
	BYTE* pointer;
	size_t length;
	size_t capacity;
	DWORD count;
	wStreamPool* pool;
	BOOL isAllocatedStream;
	BOOL isOwner;
} wStream;

extern wStream* Stream_New(BYTE* buffer, size_t size);
extern void Stream_Free(wStream* s, BOOL bFreeBuffer);

/* include/freerdp/codec/color.h */
#define FREERDP_PIXEL_FORMAT(_bpp, _type, _a, _r, _g, _b) \
	(((_bpp) << 24) | ((_type) << 16) | ((_a) << 12) | ((_r) << 8) | ((_g) << 4) | (_b))
#define PIXEL_FORMAT_BGRA32 FREERDP_PIXEL_FORMAT(32, 4, 8, 8, 8, 8)

/* include/freerdp/codec/rfx.h */
typedef enum
{
	RLGR1,
	RLGR3
} RLGR_MODE;

typedef struct
{
	UINT16 x;
	UINT16 y;
	UINT16 width;
	UINT16 height;
} RFX_RECT;

typedef struct S_RFX_CONTEXT RFX_CONTEXT;

extern RFX_CONTEXT* rfx_context_new(BOOL encoder);
extern void rfx_context_free(RFX_CONTEXT* context);
extern BOOL rfx_context_reset(RFX_CONTEXT* context, UINT32 width, UINT32 height);
extern BOOL rfx_context_set_mode(RFX_CONTEXT* context, RLGR_MODE mode);
extern void rfx_context_set_pixel_format(RFX_CONTEXT* context, UINT32 pixel_format);
extern BOOL rfx_compose_message(RFX_CONTEXT* context, wStream* s, const RFX_RECT* rects,
                                size_t num_rects, const BYTE* image_data, UINT32 width,
                                UINT32 height, UINT32 rowstride);

/* include/freerdp/codec/nsc.h */
typedef enum
{
	NSC_COLOR_LOSS_LEVEL,
	NSC_ALLOW_SUBSAMPLING,
	NSC_DYNAMIC_COLOR_FIDELITY,
	NSC_COLOR_FORMAT
} NSC_PARAMETER;

typedef struct S_NSC_CONTEXT NSC_CONTEXT;

extern NSC_CONTEXT* nsc_context_new(void);
extern void nsc_context_free(NSC_CONTEXT* context);
extern BOOL nsc_context_set_parameters(NSC_CONTEXT* context, NSC_PARAMETER what, UINT32 value);
extern BOOL nsc_compose_message(NSC_CONTEXT* context, wStream* s, const BYTE* bmpdata, UINT32 width,
                                UINT32 height, UINT32 rowstride);
extern BOOL nsc_process_message(NSC_CONTEXT* context, UINT16 bpp, UINT32 width, UINT32 height,
                                const BYTE* data, UINT32 length, BYTE* pDstData, UINT32 DstFormat,
                                UINT32 nDstStride, UINT32 nXDst, UINT32 nYDst, UINT32 nWidth,
                                UINT32 nHeight, UINT32 flip);

static void write_file(const char* dir, const char* name, const BYTE* data, size_t n)
{
	char path[1024];
	FILE* f;

	snprintf(path, sizeof(path), "%s/%s", dir, name);
	f = fopen(path, "wb");
	if (!f)
	{
		fprintf(stderr, "cannot open %s\n", path);
		exit(1);
	}
	if (fwrite(data, 1, n, f) != n)
	{
		fprintf(stderr, "short write to %s\n", path);
		exit(1);
	}
	fclose(f);
	printf("wrote %-28s %6zu bytes\n", name, n);
}

/* A deterministic but structured image: gradients plus a hard edge, so a
   decoding mistake shows up as visible garbage rather than a small delta. */
static BYTE* make_image(int w, int h)
{
	BYTE* img = malloc((size_t)w * h * 4);
	int x, y;

	for (y = 0; y < h; y++)
	{
		for (x = 0; x < w; x++)
		{
			int i = (y * w + x) * 4;
			img[i + 0] = (BYTE)((x * 4 + y) & 0xFF);          /* blue  */
			img[i + 1] = (BYTE)((y * 5 + 9) & 0xFF);          /* green */
			img[i + 2] = (BYTE)((x * 7 + y * 2) & 0xFF);      /* red   */
			img[i + 3] = 0xFF;                                /* alpha */
			if (x < w / 2 && y < h / 2)
			{
				img[i + 0] = 0x10;
				img[i + 1] = 0x20;
				img[i + 2] = 0x30;
			}
		}
	}
	return img;
}

static void gen_nsc(const char* dir, int w, int h, BYTE* img, UINT32 colorLoss,
                    UINT32 subsampling)
{
	NSC_CONTEXT* ctx = nsc_context_new();
	wStream* s = Stream_New(NULL, (size_t)w * h * 4 + 4096);
	size_t n;
	char name[128];

	if (!ctx || !s)
	{
		fprintf(stderr, "nsc: out of memory\n");
		exit(1);
	}
	nsc_context_set_parameters(ctx, NSC_COLOR_LOSS_LEVEL, colorLoss);
	nsc_context_set_parameters(ctx, NSC_ALLOW_SUBSAMPLING, subsampling);
	nsc_context_set_parameters(ctx, NSC_COLOR_FORMAT, PIXEL_FORMAT_BGRA32);

	if (!nsc_compose_message(ctx, s, img, (UINT32)w, (UINT32)h, (UINT32)w * 4))
	{
		fprintf(stderr, "nsc: compose failed\n");
		exit(1);
	}
	n = (size_t)(s->pointer - s->buffer);
	snprintf(name, sizeof(name), "nsc_%dx%d_loss%u_sub%u.bin", w, h, colorLoss, subsampling);
	write_file(dir, name, s->buffer, n);

	/* Also decode the very same payload with FreeRDP. Comparing against the
	   reference decoder output isolates "is my decoder equivalent" from
	   "how lossy is this codec", which are very different questions. */
	{
		NSC_CONTEXT* dctx = nsc_context_new();
		BYTE* dec = malloc((size_t)w * h * 4);
		char dname[128];

		if (!dctx || !dec)
		{
			fprintf(stderr, "nsc: out of memory\n");
			exit(1);
		}
		nsc_context_set_parameters(dctx, NSC_COLOR_LOSS_LEVEL, colorLoss);
		nsc_context_set_parameters(dctx, NSC_ALLOW_SUBSAMPLING, subsampling);
		nsc_context_set_parameters(dctx, NSC_COLOR_FORMAT, PIXEL_FORMAT_BGRA32);
		if (!nsc_process_message(dctx, 32, (UINT32)w, (UINT32)h, s->buffer, (UINT32)n, dec,
		                         PIXEL_FORMAT_BGRA32, (UINT32)w * 4, 0, 0, (UINT32)w, (UINT32)h, 1))
		{
			fprintf(stderr, "nsc: process failed\n");
			exit(1);
		}
		snprintf(dname, sizeof(dname), "nsc_dec_%dx%d_loss%u_sub%u.bin", w, h, colorLoss,
		         subsampling);
		write_file(dir, dname, dec, (size_t)w * h * 4);
		free(dec);
		nsc_context_free(dctx);
	}

	Stream_Free(s, TRUE);
	nsc_context_free(ctx);
}

static void gen_rfx(const char* dir, int w, int h, BYTE* img, RLGR_MODE mode)
{
	RFX_CONTEXT* ctx = rfx_context_new(TRUE);
	RFX_RECT rect = { 0, 0, (UINT16)w, (UINT16)h };
	wStream* s = Stream_New(NULL, (size_t)w * h * 4 + 65536);
	size_t n;
	char name[128];

	if (!ctx || !s)
	{
		fprintf(stderr, "rfx: out of memory\n");
		exit(1);
	}
	rfx_context_set_pixel_format(ctx, PIXEL_FORMAT_BGRA32);
	rfx_context_set_mode(ctx, mode);
	if (!rfx_context_reset(ctx, (UINT32)w, (UINT32)h))
	{
		fprintf(stderr, "rfx: reset failed\n");
		exit(1);
	}
	if (!rfx_compose_message(ctx, s, &rect, 1, img, (UINT32)w, (UINT32)h, (UINT32)w * 4))
	{
		fprintf(stderr, "rfx: compose failed\n");
		exit(1);
	}
	n = (size_t)(s->pointer - s->buffer);
	snprintf(name, sizeof(name), "rfx_%dx%d_rlgr%d.bin", w, h, mode == RLGR3 ? 3 : 1);
	write_file(dir, name, s->buffer, n);

	Stream_Free(s, TRUE);
	rfx_context_free(ctx);
}

int main(int argc, char** argv)
{
	const char* dir = argc > 1 ? argv[1] : ".";
	BYTE* img;

	img = make_image(64, 64);
	write_file(dir, "raw_64x64.bin", img, 64 * 64 * 4);
	gen_nsc(dir, 64, 64, img, 1, 0);
	gen_nsc(dir, 64, 64, img, 3, 0);
	gen_nsc(dir, 64, 64, img, 1, 1);
	gen_rfx(dir, 64, 64, img, RLGR1);
	gen_rfx(dir, 64, 64, img, RLGR3);
	free(img);

	img = make_image(128, 64);
	write_file(dir, "raw_128x64.bin", img, 128 * 64 * 4);
	gen_rfx(dir, 128, 64, img, RLGR3);
	free(img);

	return 0;
}
