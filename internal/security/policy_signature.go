package security

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

const policySignatureDomain = "IVP-POLICY-V1"

// CanonicalJSONHash normalizes JSON object key order and whitespace before
// hashing. It rejects trailing values so the signed hash has one meaning.
func CanonicalJSONHash(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("multiple JSON values")
		}
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// SignPolicyEnvelope signs the repository, version, and content hash. The
// signing key is an Ed25519 private key encoded with standard base64.
func SignPolicyEnvelope(encodedPrivateKey string, repositoryID uuid.UUID, version int64, policyHash string) (keyID, signature string, err error) {
	privateBytes, err := decodeKey(encodedPrivateKey)
	if err != nil || len(privateBytes) != ed25519.PrivateKeySize {
		return "", "", errors.New("IVP policy signing key must be a base64 Ed25519 private key")
	}
	privateKey := ed25519.PrivateKey(privateBytes)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID = policyKeyID(publicKey)
	signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, policyMessage(repositoryID, version, policyHash)))
	return keyID, signature, nil
}

// VerifyPolicyEnvelope verifies a pinned Ed25519 public key and key identifier.
func VerifyPolicyEnvelope(encodedPublicKey string, repositoryID uuid.UUID, version int64, policyHash, keyID, signature string) error {
	publicBytes, err := decodeKey(encodedPublicKey)
	if err != nil || len(publicBytes) != ed25519.PublicKeySize {
		return errors.New("IVP_POLICY_VERIFY_PUBLIC_KEY must be a base64 Ed25519 public key")
	}
	publicKey := ed25519.PublicKey(publicBytes)
	if keyID == "" || keyID != policyKeyID(publicKey) {
		return errors.New("policy signing key is not the pinned verification key")
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil || !ed25519.Verify(publicKey, policyMessage(repositoryID, version, policyHash), signatureBytes) {
		return errors.New("policy signature verification failed")
	}
	return nil
}

func decodeKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("empty key")
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(encoded); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64 key")
}

func policyKeyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:8])
}

func policyMessage(repositoryID uuid.UUID, version int64, policyHash string) []byte {
	return []byte(fmt.Sprintf("%s\n%s\n%d\n%s", policySignatureDomain, repositoryID.String(), version, strings.ToLower(policyHash)))
}
