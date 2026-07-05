package providers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// sigV4Service is the AWS service name used for signing. AWS Bedrock's SigV4
// namespace is "bedrock".
const sigV4Service = "bedrock"

// sigV4Algorithm is the AWS signature algorithm identifier embedded in the
// Authorization header and the string-to-sign.
const sigV4Algorithm = "AWS4-HMAC-SHA256"

// signBedrockSigV4 builds the AWS SigV4 Authorization header for a Bedrock
// invocation request and returns the full header set the caller must attach to
// the upstream request: Authorization, X-Amz-Date, and X-Amz-Content-SHA256.
//
// Inputs:
//   - method, parsedURL, body: the request line and payload (body is hashed
//     into both the canonical request and the X-Amz-Content-SHA256 header).
//   - accessKey, secretKey:    AWS credentials.
//   - region:                  AWS region (e.g. "us-east-1").
//   - t:                       signing time (injectable for deterministic tests).
//
// The returned headers are everything SigV4 requires beyond what the gateway
// already sets; the caller merges them into the outgoing request.
func signBedrockSigV4(method string, parsedURL *url.URL, body []byte, accessKey, secretKey, region string, t time.Time) (http.Header, error) {
	// SigV4 needs a fixed "Z" suffix; time.Format already appends one with the
	// RFC3339 layout, but we use the compact AWS forms (no separators) below.
	dateStamp := t.UTC().Format("20060102")
	amzDate := t.UTC().Format("20060102T150405Z")

	bodyHash := sha256Hex(body)

	// Step 1: canonical request.
	canonicalURI := canonicalPath(parsedURL.EscapedPath())
	canonicalQuery := canonicalQuery(parsedURL.Query())

	// Bedrock signs host, x-amz-content-sha256, and x-amz-date. The header
	// VALUES we feed into the canonical form must be the exact ones we send.
	host := parsedURL.Host
	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + bodyHash + "\n" +
		"x-amz-date:" + amzDate + "\n"

	canonicalRequest := method + "\n" +
		canonicalURI + "\n" +
		canonicalQuery + "\n" +
		canonicalHeaders + "\n" +
		strings.Join(signedHeaders, ";") + "\n" +
		bodyHash

	// Step 2: string-to-sign.
	credentialScope := dateStamp + "/" + region + "/" + sigV4Service + "/aws4_request"
	stringToSign := sigV4Algorithm + "\n" +
		amzDate + "\n" +
		credentialScope + "\n" +
		sha256Hex([]byte(canonicalRequest))

	// Step 3: derived signing key (HMAC chain).
	kDate := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, sigV4Service)
	kSigning := hmacSHA256(kService, "aws4_request")

	// Step 4: signature.
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	// Step 5: Authorization header.
	authz := fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		sigV4Algorithm,
		accessKey,
		credentialScope,
		strings.Join(signedHeaders, ";"),
		signature,
	)

	h := http.Header{}
	h.Set("Authorization", authz)
	h.Set("X-Amz-Date", amzDate)
	h.Set("X-Amz-Content-SHA256", bodyHash)
	return h, nil
}

// canonicalPath URI-encodes the request path per SigV4 rules: each segment
// double-encoded; for the common Bedrock path ("/model/.../invoke" or
// "/converse") there are no reserved chars to encode, so the path is returned
// as-is when it is already in canonical form. Empty path becomes "/".
func canonicalPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// canonicalQuery builds the canonical query string per SigV4: name=value pairs
// sorted by name (then value), each name and value percent-encoded per RFC 3986
// (with SPACE→%20, not "+"). SigV4 forbids the "+" form.
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for j, v := range vals {
			if i > 0 || j > 0 {
				b.WriteByte('&')
			}
			b.WriteString(sigV4Encode(k))
			b.WriteByte('=')
			b.WriteString(sigV4Encode(v))
		}
	}
	return b.String()
}

// sigV4Encode applies the SigV4 URI-encoding rules: unreserved A-Za-z0-9-._~
// pass through; everything else (including the space) is %-encoded. The tilde
// is unreserved per RFC 3986 and AWS keeps it unencoded.
func sigV4Encode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0F])
		}
	}
	return b.String()
}

// sha256Hex returns the lowercase hex SHA-256 digest of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hmacSHA256 returns HMAC-SHA256(key, []byte(data)).
func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
