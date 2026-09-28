/*
 * gen-zgfx-vectors.c - decode ZGFX streams with libfreerdp and store the result.
 *
 * The Go side writes the streams to decode, together with the bytes it expects
 * them to produce. This program runs those streams through FreeRDP's own ZGFX
 * decoder and writes what the reference implementation produced, so the Go
 * tests can compare against it byte for byte.
 *
 * One decoder is used for every stream and in the order given, because the
 * decompression history is shared between messages on a real channel: a match
 * may refer to bytes produced by an earlier message. Passing the streams
 * through a fresh decoder would silently change their meaning.
 *
 * It also checks the reference output against the expected plaintext, which is
 * what catches a stream that our own encoder emitted wrongly: a stream the
 * reference decodes to something else is not a usable vector.
 *
 * The FreeRDP headers are not installed, so the declarations needed are
 * repeated here against the exported symbols.
 *
 * Build and run (paths are for Debian/Ubuntu libfreerdp3):
 *
 *   cc -O2 -o /tmp/genzgfx scripts/gen-zgfx-vectors.c \
 *      /usr/lib/x86_64-linux-gnu/libfreerdp3.so.3 \
 *      /usr/lib/x86_64-linux-gnu/libwinpr3.so.3
 *   /tmp/genzgfx <dir-with-streams.bin-and-plain.bin> <output-file>
 *
 * Usually run through scripts/gen-zgfx-vectors.sh.
 */

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef int BOOL;
typedef uint8_t BYTE;
typedef uint16_t UINT16;
typedef uint32_t UINT32;

#define TRUE 1
#define FALSE 0

/* freerdp/codec/zgfx.h 3.5.1 */
typedef struct S_ZGFX_CONTEXT ZGFX_CONTEXT;
extern ZGFX_CONTEXT* zgfx_context_new(BOOL Compressor);
extern void zgfx_context_free(ZGFX_CONTEXT* zgfx);
extern int zgfx_decompress(ZGFX_CONTEXT* zgfx, const BYTE* pSrcData, UINT32 SrcSize,
                           BYTE** ppDstData, UINT32* pDstSize, UINT32 flags);

typedef struct
{
	BYTE* data;
	UINT32 size;
} BLOB;

static void die(const char* what, const char* detail)
{
	fprintf(stderr, "gen-zgfx-vectors: %s%s%s\n", what, detail ? ": " : "", detail ? detail : "");
	exit(1);
}

static uint32_t read_u32(FILE* f)
{
	BYTE b[4];
	if (fread(b, 1, 4, f) != 4)
		die("truncated input", NULL);
	return (uint32_t)b[0] | ((uint32_t)b[1] << 8) | ((uint32_t)b[2] << 16) | ((uint32_t)b[3] << 24);
}

static void write_u32(FILE* f, uint32_t v)
{
	BYTE b[4] = { (BYTE)v, (BYTE)(v >> 8), (BYTE)(v >> 16), (BYTE)(v >> 24) };
	if (fwrite(b, 1, 4, f) != 4)
		die("write failed", NULL);
}

/* A file of records: a count, then a length and that many bytes for each. */
static BLOB* read_records(const char* path, uint32_t* count)
{
	FILE* f = fopen(path, "rb");
	if (!f)
		die("cannot open", path);

	*count = read_u32(f);
	BLOB* out = calloc(*count ? *count : 1, sizeof(BLOB));
	if (!out)
		die("out of memory", NULL);

	for (uint32_t i = 0; i < *count; i++)
	{
		out[i].size = read_u32(f);
		out[i].data = malloc(out[i].size ? out[i].size : 1);
		if (!out[i].data)
			die("out of memory", NULL);
		if (fread(out[i].data, 1, out[i].size, f) != out[i].size)
			die("truncated record", path);
	}

	fclose(f);
	return out;
}

int main(int argc, char** argv)
{
	if (argc != 3)
	{
		fprintf(stderr, "usage: %s <input-dir> <output-file>\n", argv[0]);
		return 2;
	}

	char path[4096];
	uint32_t streamCount = 0;
	snprintf(path, sizeof(path), "%s/streams.bin", argv[1]);
	BLOB* streams = read_records(path, &streamCount);

	uint32_t plainCount = 0;
	snprintf(path, sizeof(path), "%s/plain.bin", argv[1]);
	BLOB* plains = read_records(path, &plainCount);

	if (streamCount != plainCount)
		die("stream and plaintext counts differ", NULL);

	/* One decoder for the whole run, fed in order. */
	ZGFX_CONTEXT* zgfx = zgfx_context_new(FALSE);
	if (!zgfx)
		die("zgfx_context_new failed", NULL);

	FILE* out = fopen(argv[2], "wb");
	if (!out)
		die("cannot write", argv[2]);

	uint32_t mismatches = 0;

	fwrite("ZGFX", 1, 4, out);
	write_u32(out, streamCount);

	for (uint32_t i = 0; i < streamCount; i++)
	{
		BYTE* decoded = NULL;
		UINT32 decodedSize = 0;

		int status = zgfx_decompress(zgfx, streams[i].data, streams[i].size, &decoded, &decodedSize, 0);
		if (status < 0)
		{
			fprintf(stderr, "gen-zgfx-vectors: stream %u (%u bytes) was rejected by the reference decoder\n", i,
			        streams[i].size);
			return 1;
		}

		if (decodedSize != plains[i].size || (decodedSize && memcmp(decoded, plains[i].data, decodedSize) != 0))
		{
			fprintf(stderr, "gen-zgfx-vectors: stream %u decodes to %u bytes, expected %u, and they differ\n", i, decodedSize,
			        plains[i].size);
			mismatches++;
		}

		write_u32(out, streams[i].size);
		fwrite(streams[i].data, 1, streams[i].size, out);
		write_u32(out, decodedSize);
		if (decodedSize)
			fwrite(decoded, 1, decodedSize, out);

		free(decoded);
	}

	fclose(out);
	zgfx_context_free(zgfx);

	if (mismatches)
	{
		fprintf(stderr, "gen-zgfx-vectors: %u of %u streams disagree with the expected plaintext\n", mismatches, streamCount);
		return 1;
	}

	printf("gen-zgfx-vectors: %u streams decoded by the reference, written to %s\n", streamCount, argv[2]);
	return 0;
}
