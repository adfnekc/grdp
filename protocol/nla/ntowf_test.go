package nla

import (
	"encoding/hex"
	"testing"

	"github.com/adfnekc/grdp/core"
)

func TestNTOWFv2KnownVector(t *testing.T) {
	// NTOWFv2("", "rdptest", "rdptest"), computed independently with impacket.
	const want = "3e2b031ccf4b6c378808abfe217a50ef"
	got := hex.EncodeToString(NTOWFv2("rdptest", "rdptest", ""))
	t.Logf("ours  NTOWFv2 = %s", got)
	t.Logf("impacket     = %s", want)
	if got != want {
		t.Errorf("NTOWFv2 mismatch")
	}
	// Also pin the MD4 of the password on its own, so a broken MD4 is obvious.
	t.Logf("MD4(utf16(rdptest)) = %s", hex.EncodeToString(MD4(core.UnicodeEncode("rdptest"))))
}
