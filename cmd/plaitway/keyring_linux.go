package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"

	pb "github.com/KoukeNeko/Plaitway/internal/gen/plaitway/v1"
)

// The items the Linux app saves (linux/src/plaitway/app/secret_service.py):
// one per profile and kind under this libsecret schema, holding
// {"username": ..., "password": ...}.
const (
	secretSchema  = "io.github.koukeneko.plaitway.credentials"
	secretService = "org.freedesktop.secrets"
)

var savedCredentials = secretServiceCredentials

// secretServiceCredentials reads what the app saved for the profile from the
// Secret Service (GNOME Keyring, KWallet). It only reads, and found is false
// when nothing is saved.
func secretServiceCredentials(ctx context.Context, profileID string, kind pb.CredentialKind) (username, password string, found bool, err error) {
	conn, err := dbus.SessionBusPrivate()
	if err != nil {
		return "", "", false, err
	}
	defer conn.Close()
	if err := conn.Auth(nil); err != nil {
		return "", "", false, err
	}
	if err := conn.Hello(); err != nil {
		return "", "", false, err
	}

	service := conn.Object(secretService, "/org/freedesktop/secrets")
	attributes := map[string]string{"xdg:schema": secretSchema, "profile_id": profileID, "kind": secretKindName(kind)}
	var unlocked, locked []dbus.ObjectPath
	if err := service.CallWithContext(ctx, "org.freedesktop.Secret.Service.SearchItems", 0, attributes).Store(&unlocked, &locked); err != nil {
		return "", "", false, err
	}
	if len(unlocked) == 0 {
		if len(locked) > 0 {
			// shortcut: no unlock prompt; the password is asked for instead. Run the unlock prompt
			// (Service.Unlock, then Prompt.Prompt) if a locked keyring turns out to be common.
			return "", "", false, errors.New("the keyring is locked")
		}
		return "", "", false, nil
	}

	// A plain session: the secret crosses the session bus of this user unencrypted,
	// where only this user's processes can read it. It ends with the connection.
	var ignored dbus.Variant
	var session dbus.ObjectPath
	if err := service.CallWithContext(ctx, "org.freedesktop.Secret.Service.OpenSession", 0, "plain", dbus.MakeVariant("")).Store(&ignored, &session); err != nil {
		return "", "", false, err
	}

	var secret struct {
		Session     dbus.ObjectPath
		Parameters  []byte
		Value       []byte
		ContentType string
	}
	if err := conn.Object(secretService, unlocked[0]).CallWithContext(ctx, "org.freedesktop.Secret.Item.GetSecret", 0, session).Store(&secret); err != nil {
		return "", "", false, err
	}
	var stored struct {
		Username string  `json:"username"`
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(secret.Value, &stored); err != nil || stored.Password == nil {
		return "", "", false, fmt.Errorf("the saved credentials of %s are unreadable", profileID)
	}
	return stored.Username, *stored.Password, true, nil
}

// secretKindName is the kind attribute the app gives an item.
func secretKindName(kind pb.CredentialKind) string {
	if kind == pb.CredentialKind_CREDENTIAL_KIND_KEY_PASSPHRASE {
		return "key-passphrase"
	}
	return "user-password"
}
