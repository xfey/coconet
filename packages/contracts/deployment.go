package contracts

import (
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
)

func DeploymentSigningPayload(info DeploymentInfo) []byte {
	return []byte(strings.Join([]string{
		"coconet-deployment-v1",
		strconv.Itoa(info.SchemaVersion),
		strconv.Itoa(info.ProtocolVersion),
		info.DeploymentID,
		info.PublicKey,
		info.PublicKeySHA256,
		info.Challenge,
		info.ServerVersion,
	}, "\n"))
}

// DeploymentCapabilitiesSigningPayload binds additive feature discovery to the
// legacy-compatible Deployment handshake without changing its original
// signature. Capabilities must be sorted and unique before this is called.
func DeploymentCapabilitiesSigningPayload(info DeploymentInfo) []byte {
	legacyDigest := sha256.Sum256(DeploymentSigningPayload(info))
	return []byte(strings.Join([]string{
		"coconet-deployment-capabilities-v1",
		base64.RawURLEncoding.EncodeToString(legacyDigest[:]),
		strconv.Itoa(len(info.Capabilities)),
		strings.Join(info.Capabilities, "\n"),
	}, "\n"))
}
