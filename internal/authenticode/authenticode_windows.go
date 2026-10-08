package authenticode

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cmsgSignerCertInfoParam asks CryptMsgGetParam for the CERT_INFO (issuer and
// serial number) of a message's signer.
const cmsgSignerCertInfoParam = 7

const certEncoding = windows.X509_ASN_ENCODING | windows.PKCS_7_ASN_ENCODING

var (
	crypt32              = windows.NewLazySystemDLL("crypt32.dll")
	procCryptMsgGetParam = crypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose    = crypt32.NewProc("CryptMsgClose")
)

// VerifySignedBy checks that the file open as handle has a valid embedded
// Authenticode signature and that the certificate of the signer is issued to
// signer. path is the same file, for the query of the signer; the caller keeps
// handle open without write sharing so that the file cannot change in between.
//
// The error completes "<file> ...": it says what is wrong with the file.
func VerifySignedBy(handle windows.Handle, path, signer string) error {
	if err := verifyTrust(handle, path); err != nil {
		return err
	}
	name, err := signerName(path)
	if err != nil {
		return fmt.Errorf("has a signature whose signer cannot be read: %w", err)
	}
	if name != signer {
		return fmt.Errorf("is signed by %q, not by %q", name, signer)
	}
	return nil
}

// trustDataFor describes the check: the embedded signature of the open file, no
// user interface, and no revocation lookup of any kind (see the package
// comment). file must stay alive while the returned data is used.
func trustDataFor(file *windows.WinTrustFileInfo) *windows.WinTrustData {
	return &windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(file),
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		ProvFlags:                       windows.WTD_REVOCATION_CHECK_NONE | windows.WTD_CACHE_ONLY_URL_RETRIEVAL,
	}
}

func verifyTrust(handle windows.Handle, path string) error {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	file := &windows.WinTrustFileInfo{
		Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
		FilePath: path16,
		File:     handle,
	}
	data := trustDataFor(file)
	verifyErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	data.StateAction = windows.WTD_STATEACTION_CLOSE
	closeErr := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, data)
	runtime.KeepAlive(file)
	if verifyErr != nil {
		return describeTrustError(verifyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("release the signature check: %w", closeErr)
	}
	return nil
}

// describeTrustError says in words what the common answers of WinVerifyTrust
// mean for a file; the rest keep the system's message.
func describeTrustError(err error) error {
	switch err {
	case windows.Errno(windows.TRUST_E_NOSIGNATURE), windows.Errno(windows.TRUST_E_SUBJECT_FORM_UNKNOWN):
		return errors.New("has no Authenticode signature")
	case windows.Errno(windows.TRUST_E_BAD_DIGEST):
		return errors.New("was changed after it was signed")
	case windows.Errno(windows.CERT_E_UNTRUSTEDROOT), windows.Errno(windows.CERT_E_CHAINING), windows.Errno(windows.TRUST_E_EXPLICIT_DISTRUST):
		return fmt.Errorf("is signed by a publisher this machine does not trust: %w", err)
	case windows.Errno(windows.CERT_E_REVOKED):
		return fmt.Errorf("is signed with a revoked certificate: %w", err)
	}
	return fmt.Errorf("has no valid Authenticode signature: %w", err)
}

// signerName returns the common name of the certificate that signed the file's
// embedded signature, the one the signature check validated.
func signerName(path string) (string, error) {
	var name string
	err := withSignerCertificate(path, func(cert *windows.CertContext) error {
		var nameErr error
		name, nameErr = certificateName(cert)
		return nameErr
	})
	return name, err
}

// withSignerCertificate calls use with the certificate that signed the file's
// embedded signature. The certificate is valid only during the call.
func withSignerCertificate(path string, use func(*windows.CertContext) error) error {
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var store, message windows.Handle
	err = windows.CryptQueryObject(windows.CERT_QUERY_OBJECT_FILE, unsafe.Pointer(path16),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED, windows.CERT_QUERY_FORMAT_FLAG_BINARY,
		0, nil, nil, nil, &store, &message, nil)
	if err != nil {
		return fmt.Errorf("read the signature: %w", err)
	}
	defer windows.CertCloseStore(store, 0)
	defer closeCryptMessage(message)

	signer, err := signerCertInfo(message)
	if err != nil {
		return err
	}
	cert, err := windows.CertFindCertificateInStore(store, certEncoding, 0, windows.CERT_FIND_SUBJECT_CERT, unsafe.Pointer(&signer[0]), nil)
	if err != nil {
		return fmt.Errorf("find the signer's certificate: %w", err)
	}
	defer windows.CertFreeCertificateContext(cert)
	return use(cert)
}

// signerCertInfo returns the CERT_INFO of the message's first signer. The
// result is a slice of words so that the structure inside it is aligned.
func signerCertInfo(message windows.Handle) ([]uint64, error) {
	var size uint32
	if err := cryptMsgGetParam(message, cmsgSignerCertInfoParam, nil, &size); err != nil {
		return nil, fmt.Errorf("size the signer's data: %w", err)
	}
	const wordSize = 8
	words := make([]uint64, (size+wordSize-1)/wordSize)
	if err := cryptMsgGetParam(message, cmsgSignerCertInfoParam, unsafe.Pointer(&words[0]), &size); err != nil {
		return nil, fmt.Errorf("read the signer's data: %w", err)
	}
	return words, nil
}

func cryptMsgGetParam(message windows.Handle, param uint32, data unsafe.Pointer, size *uint32) error {
	const firstSigner = 0
	if ok, _, err := procCryptMsgGetParam.Call(uintptr(message), uintptr(param), firstSigner, uintptr(data), uintptr(unsafe.Pointer(size))); ok == 0 {
		return err
	}
	return nil
}

func closeCryptMessage(message windows.Handle) {
	if message != 0 {
		procCryptMsgClose.Call(uintptr(message))
	}
}

func certificateName(cert *windows.CertContext) (string, error) {
	size := windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, nil, 0)
	if size <= 1 {
		return "", errors.New("the certificate has no name")
	}
	name := make([]uint16, size)
	windows.CertGetNameString(cert, windows.CERT_NAME_SIMPLE_DISPLAY_TYPE, 0, nil, &name[0], size)
	return windows.UTF16ToString(name), nil
}
