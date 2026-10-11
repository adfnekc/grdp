package nla

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rc4"
	"strings"

	"github.com/adfnekc/grdp/core"
	"golang.org/x/crypto/md4"
)

// MD4 returns the MD4 digest of data, which NTLMv2 needs and which the standard
// library does not provide, since MD4 is long broken. It is used only where the
// protocol requires it, to derive a key from a password, and not as a hash in its
// own right.
func MD4(data []byte) []byte {
	h := md4.New()
	h.Write(data)
	return h.Sum(nil)
}

// MD5 returns the MD5 digest of data.
func MD5(data []byte) []byte {
	h := md5.New()
	h.Write(data)
	return h.Sum(nil)
}

// HMAC_MD5 returns the HMAC of data under key, using MD5, which is the
// construction NTLMv2 derives its responses and keys with.
func HMAC_MD5(key, data []byte) []byte {
	h := hmac.New(md5.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// Version 2 of NTLM hash function
func NTOWFv2(password, user, domain string) []byte {
	return HMAC_MD5(MD4(core.UnicodeEncode(password)), core.UnicodeEncode(strings.ToUpper(user)+domain))
}

// Same as NTOWFv2
func LMOWFv2(password, user, domain string) []byte {
	return NTOWFv2(password, user, domain)
}

// RC4K returns src encrypted under key with RC4, which NTLM uses to wrap a session
// key. It returns a new slice and leaves src alone.
func RC4K(key, src []byte) []byte {
	result := make([]byte, len(src))
	rc4obj, _ := rc4.NewCipher(key)
	rc4obj.XORKeyStream(result, src)
	return result
}
