package wg

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"golang.org/x/sys/windows"

	"github.com/KoukeNeko/Plaitway/internal/tunnel"
)

const (
	// adapterIDBytes is how much of a profile's hash goes into the adapter
	// name: short enough to read in the network settings, long enough that two
	// profiles never share a name.
	adapterIDBytes = 4

	adapterGUIDDomain = "plaitway wintun adapter\x00"

	// The version and variant bits RFC 4122 gives a name-based UUID.
	guidVersionMask    = 0x0f
	guidVersionNamed   = 0x50
	guidVariantMask    = 0x3f
	guidVariantRFC4122 = 0x80
	guidVersionByte    = 6
	guidVariantByte    = 8
	guidSize           = 16
)

// adapterName is the name of the adapter of one profile: the same profile gets
// the same name on every run, and a leftover adapter can be told by its prefix.
func adapterName(prefix string, owner tunnel.OwnerID) string {
	sum := sha256.Sum256([]byte(owner))
	return prefix + hex.EncodeToString(sum[:adapterIDBytes])
}

// adapterGUID is the GUID requested for the adapter of one profile. Windows
// derives the network's identity (and with it the firewall profile the user
// chose for it) from this GUID, so a profile that gets a new one on every
// connection would be asked about, and treated as, a new network each time.
func adapterGUID(owner tunnel.OwnerID) windows.GUID {
	sum := sha256.Sum256([]byte(adapterGUIDDomain + string(owner)))
	id := sum[:guidSize]
	id[guidVersionByte] = id[guidVersionByte]&guidVersionMask | guidVersionNamed
	id[guidVariantByte] = id[guidVariantByte]&guidVariantMask | guidVariantRFC4122

	guid := windows.GUID{
		Data1: binary.BigEndian.Uint32(id[0:4]),
		Data2: binary.BigEndian.Uint16(id[4:6]),
		Data3: binary.BigEndian.Uint16(id[6:8]),
	}
	copy(guid.Data4[:], id[8:])
	return guid
}
