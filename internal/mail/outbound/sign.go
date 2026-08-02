package outbound

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// hmacSHA256 returns the lowercase-hex HMAC-SHA256 of msg keyed by key.
func hmacSHA256Hex(key, msg string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// hmacSHA256B64 returns the base64 HMAC-SHA256 of msg keyed by key.
func hmacSHA256B64(key, msg string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	return b64(mac.Sum(nil))
}

func hmacSHA256Bytes(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// signAWSV4 produces the Authorization header value (and the x-amz-date to use)
// for an AWS Signature Version 4 request. It follows the Query/REST-POST path
// where parameters live in the body and the query string is empty.
func signAWSV4(method, host, region, service, body, accessKey, secretKey string, now time.Time) (authorization, amzDate, contentSHA string) {
	dateStamp := now.UTC().Format("20060102")
	amzDate = now.UTC().Format("20060102T150405Z")
	contentSHA = sha256Hex([]byte(body))

	canonicalHeaders := strings.Join([]string{
		"content-type:application/x-www-form-urlencoded\n",
		"host:" + host + "\n",
		"x-amz-content-sha256:" + contentSHA + "\n",
		"x-amz-date:" + amzDate + "\n",
	}, "")
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"

	canonicalRequest := strings.Join([]string{
		method, "/", "", canonicalHeaders, signedHeaders, contentSHA,
	}, "\n")

	scope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256Bytes([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256Bytes(kDate, []byte(region))
	kService := hmacSHA256Bytes(kRegion, []byte(service))
	kSigning := hmacSHA256Bytes(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256Bytes(kSigning, []byte(stringToSign)))

	credential := accessKey + "/" + scope
	authorization = strings.Join([]string{
		"AWS4-HMAC-SHA256 Credential=" + credential,
		"SignedHeaders=" + signedHeaders,
		"Signature=" + signature,
	}, ", ")
	return authorization, amzDate, contentSHA
}
