//go:build !linux

package main

import (
	"context"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

// savedCredentials is nil where the client does not read the app's saved
// credentials yet (the Keychain, the Windows Credential Manager).
var savedCredentials func(ctx context.Context, profileID string, kind pb.CredentialKind) (username, password string, found bool, err error)
